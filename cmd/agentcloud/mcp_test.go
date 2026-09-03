package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agent-continuity/agent-continuity-master/pkg/client"
)

func TestMCPStdioBridgeForwardsAndSuppressesNotificationReplies(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var value map[string]any
		if err := json.NewDecoder(r.Body).Decode(&value); err != nil {
			t.Fatal(err)
		}
		if value["method"] == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": value["id"], "result": map[string]any{"ok": true}})
	}))
	defer server.Close()
	input := strings.NewReader("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\"}\n{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}\n")
	var output bytes.Buffer
	if err := serveMCPStdio(context.Background(), client.New(server.URL, "token"), input, &output); err != nil {
		t.Fatal(err)
	}
	if strings.Count(strings.TrimSpace(output.String()), "\n") != 0 || !strings.Contains(output.String(), `"ok":true`) {
		t.Fatalf("unexpected output %q", output.String())
	}
}
