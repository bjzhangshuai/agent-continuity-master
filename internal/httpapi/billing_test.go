package httpapi

import (
	"bytes"
	"github.com/agent-continuity/agent-continuity-master/internal/billing"
	"github.com/agent-continuity/agent-continuity-master/internal/payments"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestBillingAPIEndToEnd(t *testing.T) {
	f := newFixtureWithOptions(t, WithPayments(payments.NewRegistry(payments.Demo{}), billing.DemoPriceBook(), "http://127.0.0.1:18080"))
	_, token := f.account("payer")
	_, other := f.account("not-payer")
	status, b := f.request("POST", "/v1/billing/accounts", token, map[string]string{"plan": "pro", "currency": "CNY"}, nil)
	if status != 201 {
		t.Fatal(status, string(b))
	}
	account := decode[protocol.BillingAccount](t, b)
	status, b = f.request("POST", "/v1/tasks", token, map[string]string{"title": "Billable mutation", "objective": "payment test"}, nil)
	if status != 201 {
		t.Fatal(status, string(b))
	}
	input := map[string]any{"billing_account_id": account.ID, "period_start": time.Now().Add(-24 * time.Hour), "period_end": time.Now()}
	headers := map[string]string{"Idempotency-Key": "invoice-request-1"}
	status, b = f.request("POST", "/v1/billing/invoices", token, input, headers)
	if status != 201 {
		t.Fatal(status, string(b))
	}
	doc := decode[protocol.BillingDocument](t, b)
	id := doc.Invoice.ID
	if doc.Invoice.AmountMinor <= 0 {
		t.Fatal("usage not priced")
	}
	status, _ = f.request("GET", "/v1/billing/invoices/"+id, other, nil, nil)
	if status != 404 {
		t.Fatal("cross-account invoice leaked", status)
	}
	status, _ = f.request("POST", "/v1/billing/invoices/"+id+"/payments", other, map[string]string{"provider": "demo"}, headers)
	if status != 404 {
		t.Fatal("cross-account write allowed", status)
	}
	status, b = f.request("POST", "/v1/billing/invoices/"+id+"/payments", token, map[string]string{"provider": "demo"}, map[string]string{"Idempotency-Key": "payment-request-1"})
	if status != 201 {
		t.Fatal(status, string(b))
	}
	pay := decode[struct {
		Payment protocol.BillingPayment `json:"payment"`
		Link    string                  `json:"payment_link"`
	}](t, b)
	u, _ := url.Parse(pay.Link)
	path := u.RequestURI()
	status, b = f.request("GET", path, "", nil, nil)
	if status != 200 || !bytes.Contains(b, []byte("本地模拟")) || bytes.Contains(b, []byte(token)) {
		t.Fatal("public checkout invalid", status, string(b))
	}
	status, _ = f.request("GET", "/pay/"+pay.Payment.ID+"?token=invalid", "", nil, nil)
	if status != 404 {
		t.Fatal("invalid token accepted")
	}
	status, b = f.request("POST", "/v1/billing/invoices/"+id+"/payments/"+pay.Payment.ID+"/simulate", token, map[string]string{"outcome": "succeeded"}, nil)
	if status != 200 {
		t.Fatal(status, string(b))
	}
	doc = decode[protocol.BillingDocument](t, b)
	if doc.Invoice.Status != "paid" || len(doc.Receipts) != 1 {
		t.Fatal("receipt missing")
	}
	status, b = f.request("GET", "/v1/billing/invoices/"+id+"/export", token, nil, nil)
	if status != 200 || !bytes.Contains(b, []byte("Amount (minor)")) {
		t.Fatal("CSV missing")
	}
	receiptPath := "/v1/billing/invoices/" + id + "/receipts/" + doc.Receipts[0].ID + "?format=html"
	status, b = f.request("GET", receiptPath, token, nil, nil)
	if status != 200 || !bytes.Contains(b, []byte("不是税务发票")) {
		t.Fatal("receipt invalid", status, string(b))
	}
	status, _ = f.request("GET", receiptPath, other, nil, nil)
	if status != 404 {
		t.Fatal("receipt leaked")
	}
	status, _ = f.request("POST", "/v1/billing/invoices/"+id+"/payments/"+pay.Payment.ID+"/revoke-link", token, nil, nil)
	if status != 200 {
		t.Fatal("revoke failed")
	}
	status, _ = f.request("GET", path, "", nil, nil)
	if status != 404 {
		t.Fatal("revoked link accepted")
	}
	status, _ = f.request("POST", "/v1/payments/webhooks/demo", "", map[string]string{"status": "succeeded"}, nil)
	if status != 204 {
		t.Fatal("unsupported demo callbacks should not mutate", status)
	}
	status, b = f.request("GET", "/billing.js", "", nil, nil)
	if status != http.StatusOK || !strings.Contains(string(b), "loadBillingFinance") {
		t.Fatal("billing UI missing")
	}
}
func TestBillingDisabledProvidersAndCSVInjection(t *testing.T) {
	f := newFixture(t)
	_, token := f.account("payer")
	status, b := f.request("GET", "/v1/billing/providers", token, nil, nil)
	if status != 200 || bytes.Contains(b, []byte(`"enabled":true`)) {
		t.Fatal("real providers default enabled")
	}
	for _, v := range []string{"=cmd()", "+1", "@sum(A1)", "-formula", "\t=1"} {
		if !strings.HasPrefix(csvSafe(v), "'") {
			t.Fatal("CSV formula not neutralized")
		}
	}
}
