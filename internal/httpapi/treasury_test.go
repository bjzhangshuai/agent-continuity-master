package httpapi

import (
	"bytes"
	"context"
	"fmt"
	"github.com/agent-continuity/agent-continuity-master/internal/billing"
	"github.com/agent-continuity/agent-continuity-master/internal/payments"
	"github.com/agent-continuity/agent-continuity-master/internal/treasury"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
	"strings"
	"testing"
	"time"
)

func TestTreasuryFullHTTPJourney(t *testing.T) {
	f := newFixtureWithOptions(t, WithPayments(payments.NewRegistry(payments.Demo{}), billing.DemoPriceBook(), "http://127.0.0.1:18080"))
	owner, token := f.account("merchant operator")
	_, other := f.account("payer without treasury access")
	f.api.treasury = treasury.New(f.api.store, owner.ID, true, nil)
	call := func(method, path string, body any, want int, key string) []byte {
		t.Helper()
		h := map[string]string{}
		if key != "" {
			h["Idempotency-Key"] = key
		}
		status, b := f.request(method, path, token, body, h)
		if status != want {
			t.Fatalf("%s %s: %d want %d: %s", method, path, status, want, b)
		}
		return b
	}
	if status, _ := f.request("GET", "/v1/treasury", other, nil, nil); status != 403 {
		t.Fatal("payer accessed merchant money", status)
	}
	readOnly, err := f.api.tokens.IssueScoped(owner.ID, "viewer", []string{"treasury:read"}, "", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if status, _ := f.request("POST", "/v1/treasury/destinations", readOnly, map[string]string{"kind": "bank", "label": "forbidden", "currency": "CNY"}, nil); status != 403 {
		t.Fatal("read-only wrote treasury")
	}
	b := call("POST", "/v1/billing/accounts", map[string]string{"plan": "pro", "currency": "CNY"}, 201, "")
	ba := decode[protocol.BillingAccount](t, b)
	now := time.Now().UTC()
	if err = f.api.store.RecordUsage(context.Background(), protocol.UsageRecord{ID: "treasury-usage", AccountID: owner.ID, Metric: "api.mutation", Quantity: 10000, Unit: "operation", IdempotencyKey: "treasury-usage", RecordedAt: now.Add(-time.Second)}); err != nil {
		t.Fatal(err)
	}
	b = call("POST", "/v1/billing/invoices", map[string]any{"billing_account_id": ba.ID, "period_start": now.Add(-time.Hour), "period_end": now}, 201, "treasury-invoice-1")
	invoice := decode[protocol.BillingDocument](t, b)
	invoicePath := "/v1/billing/invoices/" + invoice.Invoice.ID
	b = call("POST", invoicePath+"/payments", map[string]string{"provider": "demo"}, 201, "treasury-payment-1")
	pay := decode[struct {
		Payment protocol.BillingPayment `json:"payment"`
	}](t, b)
	call("POST", "/v1/treasury/collections/import", map[string]string{"invoice_id": invoice.Invoice.ID}, 422, "")
	call("POST", invoicePath+"/payments/"+pay.Payment.ID+"/simulate", map[string]string{"outcome": "succeeded"}, 200, "")
	b = call("POST", "/v1/treasury/collections/import", map[string]string{"invoice_id": invoice.Invoice.ID}, 200, "")
	d := decode[protocol.TreasuryDocument](t, b)
	call("POST", "/v1/treasury/collections/"+d.Collections[0].ID+"/settle-demo", nil, 200, "")
	b = call("POST", "/v1/treasury/destinations", map[string]string{"kind": "bank", "label": "=test virtual bank", "currency": "CNY"}, 200, "")
	d = decode[protocol.TreasuryDocument](t, b)
	dest := d.Destinations[0].ID
	input := map[string]any{"provider": "demo", "destination_id": dest, "amount_minor": 5000, "currency": "CNY"}
	call("POST", "/v1/treasury/payouts", input, 422, "treasury-payout-1")
	call("POST", "/v1/treasury/destinations/"+dest+"/verify-demo", nil, 200, "")
	b = call("POST", "/v1/treasury/payouts", input, 200, "treasury-payout-1")
	d = decode[protocol.TreasuryDocument](t, b)
	pid := d.Payouts[0].ID
	path := "/v1/treasury/payouts/" + pid
	call("POST", path+"/submit", nil, 409, "")
	call("POST", path+"/decision", map[string]string{"decision": "approve"}, 200, "")
	call("POST", path+"/submit", nil, 200, "")
	call("GET", path+"/receipt", nil, 404, "")
	b = call("POST", path+"/simulate", map[string]string{"outcome": "paid"}, 200, "")
	d = decode[protocol.TreasuryDocument](t, b)
	if d.Payouts[0].ReceiptID == "" {
		t.Fatal("missing payout record")
	}
	b = call("GET", path+"/receipt", nil, 200, "")
	if !bytes.Contains(b, []byte("未发生真实转账")) {
		t.Fatal("missing simulation warning")
	}
	if bytes.Contains(b, []byte("nil")) || !bytes.Contains(b, []byte("渠道确认到账")) {
		t.Fatal("receipt contains unformatted status/time")
	}
	b = call("GET", "/v1/treasury/export", nil, 200, "")
	if !bytes.Contains(b, []byte("'=test")) {
		t.Fatal("CSV injection protection missing")
	}
	call("POST", path+"/simulate", map[string]string{"outcome": "returned"}, 200, "")
	b = call("GET", path+"/receipt", nil, 200, "")
	if !bytes.Contains(b, []byte("returned")) {
		t.Fatal("return hidden from receipt")
	}
	b = call("GET", "/treasury.js", nil, 200, "")
	for _, needle := range []string{"parseTreasuryAmount", "treasury-panel", "收款与提现"} {
		if needle == "收款与提现" {
			continue
		}
		if !strings.Contains(string(b), needle) {
			t.Fatal("UI incomplete", needle)
		}
	}
	for _, op := range []string{"decision", "submit", "simulate", "reconcile"} {
		status, _ := f.request("POST", fmt.Sprintf("%s/%s", path, op), other, map[string]string{}, nil)
		if status != 403 {
			t.Fatal("foreign payout mutation", op, status)
		}
	}
}
func TestTreasuryDisabledByDefault(t *testing.T) {
	f := newFixture(t)
	_, token := f.account("ordinary payer")
	status, _ := f.request("GET", "/v1/treasury", token, nil, nil)
	if status != 503 {
		t.Fatal("treasury enabled without operator configuration", status)
	}
}
