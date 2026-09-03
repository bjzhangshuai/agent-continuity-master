package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBillingClientPreservesIdempotency(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Idempotency-Key") != "persisted-key" || r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing billing auth/idempotency")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"payment":{"payment_id":"pay_test"},"payment_link":"https://billing.example/pay/test"}`))
	}))
	defer server.Close()
	c := New(server.URL, "test-token")
	p, err := c.CreatePayment(context.Background(), "inv_test", "demo", "persisted-key")
	if err != nil || p.Payment.ID != "pay_test" {
		t.Fatal(p, err)
	}
}
