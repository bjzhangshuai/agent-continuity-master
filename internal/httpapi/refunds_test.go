package httpapi

import (
	"bytes"
	"github.com/agent-continuity/agent-continuity-master/internal/billing"
	"github.com/agent-continuity/agent-continuity-master/internal/payments"
	"github.com/agent-continuity/agent-continuity-master/internal/treasury"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
	"testing"
	"time"
)

func TestTenantRefundJourneyAndMerchantSeparation(t *testing.T) {
	f := newFixtureWithOptions(t, WithPayments(payments.NewRegistry(payments.Demo{}), billing.DemoPriceBook(), "http://127.0.0.1:18080"))
	if status, body := f.request("GET", "/refunds.js", "", nil, nil); status != 200 || !bytes.Contains(body, []byte("function renderRefunds")) {
		t.Fatal("refund UI not served", status)
	}
	_, payer := f.account("payer")
	m, merchant := f.account("merchant")
	_, other := f.account("other")
	f.api.treasury = treasury.New(f.api.store, m.ID, true, nil)
	call := func(method, path, token string, body any, key string, want int) []byte {
		t.Helper()
		h := map[string]string{}
		if key != "" {
			h["Idempotency-Key"] = key
		}
		s, b := f.request(method, path, token, body, h)
		if s != want {
			t.Fatalf("%s %s: %d %s", method, path, s, b)
		}
		return b
	}
	ba := decode[protocol.BillingAccount](t, call("POST", "/v1/billing/accounts", payer, map[string]string{"plan": "pro", "currency": "CNY"}, "", 201))
	call("POST", "/v1/tasks", payer, map[string]string{"title": "Refund E2E", "objective": "exercise original payment refund"}, "", 201)
	d := decode[protocol.BillingDocument](t, call("POST", "/v1/billing/invoices", payer, map[string]any{"billing_account_id": ba.ID, "period_start": time.Now().Add(-time.Hour), "period_end": time.Now()}, "rf-invoice-key", 201))
	base := "/v1/billing/invoices/" + d.Invoice.ID
	ops := "/v1/treasury/receivables/" + d.Invoice.ID
	checkout := decode[struct {
		Payment protocol.BillingPayment `json:"payment"`
	}](t, call("POST", base+"/payments", payer, map[string]string{"provider": "demo"}, "rf-checkout-key", 201))
	call("POST", base+"/payments/"+checkout.Payment.ID+"/simulate", payer, map[string]string{"outcome": "succeeded"}, "", 200)
	in := map[string]any{"payment_id": checkout.Payment.ID, "amount_minor": d.Invoice.AmountMinor, "reason": "验收原路退款"}
	call("POST", base+"/refunds", other, in, "rf-request-key", 404)
	d = decode[protocol.BillingDocument](t, call("POST", base+"/refunds", payer, in, "rf-request-key", 200))
	rid := d.Refunds[0].ID
	op := ops + "/refunds/" + rid
	call("POST", op+"/decision", payer, map[string]string{"decision": "approved", "reason": "tenant cannot approve"}, "", 403)
	call("POST", op+"/submit", merchant, nil, "", 409)
	call("POST", op+"/decision", merchant, map[string]string{"decision": "approved", "reason": "验收通过"}, "", 200)
	call("POST", op+"/submit", merchant, nil, "", 200)
	for i := 0; i < 2; i++ {
		d = decode[protocol.BillingDocument](t, call("POST", op+"/simulate", merchant, map[string]string{"outcome": "succeeded"}, "", 200))
	}
	if d.Invoice.Status != "refunded" || len(d.CreditNotes) != 1 || len(d.Receipts) != 1 {
		t.Fatal("refund evidence", d)
	}
	b := call("GET", base+"/statement", payer, nil, "", 200)
	if !bytes.Contains(b, []byte("不是税务发票")) || !bytes.Contains(b, []byte(rid)) {
		t.Fatal("statement missing refund")
	}
	call("GET", base+"/statement", other, nil, "", 404)
	call("GET", ops+"/statement", merchant, nil, "", 200)
	b = call("GET", "/v1/treasury/receivables", merchant, nil, "", 200)
	if !bytes.Contains(b, []byte(rid)) {
		t.Fatal("refunded bill disappeared from merchant list")
	}
}
