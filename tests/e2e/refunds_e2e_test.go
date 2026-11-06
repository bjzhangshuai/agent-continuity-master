package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/agent-continuity/agent-continuity-master/pkg/client"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
	"os"
	"testing"
	"time"
)

func TestRefundPaymentJourney(t *testing.T) {
	base, bootstrap, merchantID := os.Getenv("CONTINUITY_E2E_URL"), os.Getenv("CONTINUITY_E2E_BOOTSTRAP_TOKEN"), os.Getenv("CONTINUITY_E2E_MERCHANT_ACCOUNT")
	if base == "" || bootstrap == "" || merchantID == "" {
		t.Skip("local URL, bootstrap and configured merchant account required")
	}
	if base != "http://127.0.0.1:18081" && base != "http://127.0.0.1:18080" {
		t.Fatal("refund acceptance is local simulation only")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, token := createAccount(t, base, bootstrap, fmt.Sprintf("Refund acceptance %d", time.Now().UnixNano()))
	api := client.New(base, token)
	call := func(method, path, credential string, input any, key string, want int) []byte {
		t.Helper()
		var b []byte
		if input != nil {
			b, _ = json.Marshal(input)
		}
		return request(t, method, base+path, credential, b, map[string]string{"Content-Type": "application/json", "Idempotency-Key": key}, want)
	}
	b, _ := json.Marshal(map[string]any{"account_id": merchantID, "subject": "refund-acceptance-operator", "ttl_seconds": 300})
	raw := request(t, "POST", base+"/v1/auth/token", "", b, map[string]string{"Content-Type": "application/json", "X-Bootstrap-Token": bootstrap}, 200)
	var auth struct {
		AccessToken string `json:"access_token"`
	}
	if json.Unmarshal(raw, &auth) != nil || auth.AccessToken == "" {
		t.Fatal("merchant login")
	}
	for i := 0; i < 2; i++ {
		if _, err := api.CreateTask(ctx, "Refund acceptance", "local simulation", ""); err != nil {
			t.Fatal(err)
		}
	}
	var ba protocol.BillingAccount
	_ = json.Unmarshal(call("POST", "/v1/billing/accounts", token, map[string]string{"plan": "pro", "currency": "CNY"}, "", 201), &ba)
	// Use server Date for the billing window, not the laptop clock.
	response, err := api.HTTP.Get(base + "/v1/health/ready")
	if err != nil {
		t.Fatal(err)
	}
	serverDate, err := time.Parse(time.RFC1123, response.Header.Get("Date"))
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	// Date is second-resolution; next request after one second captures all usage.
	time.Sleep(1100 * time.Millisecond)
	response, err = api.HTTP.Get(base + "/v1/health/ready")
	if err != nil {
		t.Fatal(err)
	}
	end, err := time.Parse(time.RFC1123, response.Header.Get("Date"))
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	d, err := api.IssueInvoice(ctx, client.InvoiceRequest{BillingAccountID: ba.ID, PeriodStart: serverDate.Add(-time.Hour), PeriodEnd: end}, "refund-journey-invoice")
	if err != nil {
		t.Fatal(err)
	}
	if !d.Invoice.TestMode || d.Invoice.AmountMinor < 2 {
		t.Fatal("test pricing required")
	}
	checkout, err := api.CreatePayment(ctx, d.Invoice.ID, "demo", "refund-journey-payment")
	if err != nil {
		t.Fatal(err)
	}
	path := "/v1/billing/invoices/" + d.Invoice.ID
	call("POST", path+"/payments/"+checkout.Payment.ID+"/simulate", token, map[string]string{"outcome": "succeeded"}, "", 200)
	for i, amount := range []int64{1, d.Invoice.AmountMinor - 1} {
		d, err = api.RequestRefund(ctx, d.Invoice.ID, checkout.Payment.ID, amount, "本地验收原路退款", fmt.Sprintf("refund-journey-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		r := d.Refunds[len(d.Refunds)-1]
		op := "/v1/treasury/receivables/" + d.Invoice.ID + "/refunds/" + r.ID
		call("POST", op+"/decision", auth.AccessToken, map[string]string{"decision": "approved", "reason": "本地演示批准"}, "", 200)
		call("POST", op+"/submit", auth.AccessToken, nil, "", 200)
		for j := 0; j < 2; j++ {
			call("POST", op+"/simulate", auth.AccessToken, map[string]string{"outcome": "succeeded"}, "", 200)
		}
	}
	d, err = api.GetInvoice(ctx, d.Invoice.ID)
	if err != nil || d.Invoice.Status != "refunded" || len(d.CreditNotes) != 2 || len(d.Receipts) != 1 {
		t.Fatal("persisted refund evidence", err)
	}
	statement := call("GET", path+"/statement", token, nil, "", 200)
	if !bytes.Contains(statement, []byte(d.CreditNotes[0].ID)) {
		t.Fatal("statement missing refund credit")
	}
	t.Logf("PASS: immutable bill %s, payment %s, 2 partial refunds -> full refund, 2 credit notes and original receipt retained; no funds moved", d.Invoice.ID, checkout.Payment.ID)
}
