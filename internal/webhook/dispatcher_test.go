package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/agent-continuity/agent-continuity-master/internal/secrets"
	"github.com/agent-continuity/agent-continuity-master/internal/storage/memory"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

func TestDispatcherSignsAndCompletesDelivery(t *testing.T) {
	payload := json.RawMessage(`{"task_id":"task_1"}`)
	secret := "whsec_test_secret"
	received := make(chan *http.Request, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		received <- r.Clone(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()
	store := memory.New()
	now := time.Now().UTC()
	account := protocol.Account{ID: "acct_webhook", Name: "webhook", CreatedAt: now}
	if err := store.CreateAccount(context.Background(), account); err != nil {
		t.Fatal(err)
	}
	cipher, err := secrets.New([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := cipher.Encrypt([]byte(secret), "webhook_test")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := protocol.WebhookEndpoint{ID: "webhook_test", AccountID: account.ID, URL: target.URL, Events: []string{"task.created"}, CreatedAt: now}
	if err := store.CreateWebhookEndpoint(context.Background(), endpoint, encrypted); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateWebhookDeliveries(context.Background(), account.ID, "task.created", payload, now); err != nil {
		t.Fatal(err)
	}
	items, err := store.ListReadyWebhookDeliveries(context.Background(), now, 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("ready deliveries: %+v %v", items, err)
	}
	dispatcher := New(store, cipher, slog.New(slog.NewTextHandler(io.Discard, nil)))
	dispatcher.SetClientForTest(target.Client())
	if err := dispatcher.Deliver(context.Background(), items[0]); err != nil {
		t.Fatal(err)
	}
	request := <-received
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(payload)
	expected := "v1=" + hex.EncodeToString(mac.Sum(nil))
	if request.Header.Get("X-Agent-Continuity-Signature") != expected || request.Header.Get("X-Agent-Continuity-Event") != "task.created" {
		t.Fatalf("invalid webhook headers: %+v", request.Header)
	}
	items, err = store.ListReadyWebhookDeliveries(context.Background(), now.Add(24*time.Hour), 10)
	if err != nil || len(items) != 0 {
		t.Fatalf("delivered webhook was retried: %+v %v", items, err)
	}
}

func TestEndpointURLRejectsLocalTargets(t *testing.T) {
	for _, value := range []string{"http://example.com/hook", "https://127.0.0.1/hook", "https://user@example.com/hook"} {
		if err := ValidateEndpointURL(value); err == nil {
			t.Fatalf("unsafe URL accepted: %s", value)
		}
	}
	if err := ValidateEndpointURL("https://hooks.example.com/continuity"); err != nil {
		t.Fatalf("public HTTPS URL rejected: %v", err)
	}
}
