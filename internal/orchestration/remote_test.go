package orchestration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-continuity/agent-continuity-master/internal/orchestration"
)

func TestRemoteA2AResumesOriginalTaskAfterUncertainResponse(t *testing.T) {
	var mu sync.Mutex
	sends, polls := 0, 0
	messages := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer synthetic-agent-token" || r.Header.Get("A2A-Version") != "1.0" {
			t.Error("missing protocol authentication")
		}
		var q struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.NewDecoder(r.Body).Decode(&q) != nil {
			t.Error("invalid JSON")
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if q.Method == "SendMessage" {
			sends++
			var p struct {
				Message struct {
					ID    string `json:"messageId"`
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"message"`
			}
			_ = json.Unmarshal(q.Params, &p)
			if p.Message.ID != "stable-run-step" || !strings.Contains(p.Message.Parts[0].Text, "reviewed upstream") {
				t.Error("lost idempotency or dependencies")
			}
			messages[p.Message.ID] = true
			if sends == 1 {
				w.WriteHeader(503)
				return
			} // remote already accepted; caller has no response
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"stable-run-step","result":{"task":{"id":"remote-original","status":{"state":"TASK_STATE_WORKING"}}}}`))
		} else if q.Method == "GetTask" {
			polls++
			if !strings.Contains(string(q.Params), "remote-original") {
				t.Error("wrong task polled")
			}
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"stable-run-step","result":{"id":"remote-original","status":{"state":"TASK_STATE_COMPLETED"},"artifacts":[{"parts":[{"text":"final shared output"}]}]}}`))
		} else {
			t.Error("unexpected method")
			w.WriteHeader(400)
		}
	}))
	defer server.Close()
	token := filepath.Join(t.TempDir(), "agent-token")
	if err := os.WriteFile(token, []byte("synthetic-agent-token"), 0600); err != nil {
		t.Fatal(err)
	}
	remote := orchestration.RemoteA2A{URL: server.URL, TokenFile: token}
	in := orchestration.Input{ID: "stable-run-step", Prompt: "finish summary", Dependencies: map[string]string{"review": "reviewed upstream"}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := remote.Execute(ctx, in); err == nil {
		t.Fatal("uncertain response accepted as completion")
	}
	output, err := remote.Execute(ctx, in)
	if err != nil || output != "final shared output" {
		t.Fatal(output, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(messages) != 1 || sends != 2 || polls != 1 {
		t.Fatal("duplicate work or missing recovery", sends, polls, len(messages))
	}
}
