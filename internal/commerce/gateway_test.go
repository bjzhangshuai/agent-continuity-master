package commerce_test

import (
	"context"
	"encoding/json"
	"github.com/agent-continuity/agent-continuity-master/internal/commerce"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPGatewayContractUsesStableEffectIdentity(t *testing.T) {
	saved := map[string]commerce.Result{}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/effects" || r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("invalid gateway authentication/path")
		}
		var e commerce.Effect
		if json.NewDecoder(r.Body).Decode(&e) != nil || r.Header.Get("Idempotency-Key") != e.ID {
			t.Error("lost effect identity")
		}
		requests++
		result, ok := saved[e.ID]
		if !ok {
			result, _ = commerce.DemoGateway{}.Apply(r.Context(), e)
			saved[e.ID] = result
		}
		if requests == 1 {
			w.WriteHeader(503)
			return
		}
		_ = json.NewEncoder(w).Encode(result)
	}))
	defer server.Close()
	g := commerce.HTTPGateway{URL: server.URL, Token: "test-token", Client: server.Client(), Simulated: true}
	effect := commerce.Effect{ID: "durable-payout", Kind: "payout", Account: "seller", Currency: "CNY", Amount: 150, Destination: "provider-owned-recipient"}
	if _, e := g.Apply(context.Background(), effect); e == nil {
		t.Fatal("unknown response not propagated")
	}
	result, e := g.Apply(context.Background(), effect)
	if e != nil || result.ID != effect.ID || result.Amount != 150 || len(saved) != 1 || !result.Simulated {
		t.Fatal("HTTP recovery not idempotent", e)
	}
}
