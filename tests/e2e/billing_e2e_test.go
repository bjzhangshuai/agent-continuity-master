package e2e

import (
	"encoding/json"
	"fmt"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBillingPaymentJourney(t *testing.T) {
	base, bootstrap := os.Getenv("CONTINUITY_E2E_URL"), os.Getenv("CONTINUITY_E2E_BOOTSTRAP_TOKEN")
	if base == "" || bootstrap == "" {
		t.Skip("local E2E server required")
	}
	_, token := createAccount(t, base, bootstrap, "billing-e2e-"+time.Now().Format("150405"))
	headers := map[string]string{"Content-Type": "application/json"}
	call := func(method, path string, body any, status int, key string) []byte {
		var b []byte
		if body != nil {
			b, _ = json.Marshal(body)
		}
		h := map[string]string{"Content-Type": "application/json"}
		if key != "" {
			h["Idempotency-Key"] = key
		}
		return request(t, method, base+path, token, b, h, status)
	}
	var config struct {
		Providers []struct {
			ID      string `json:"id"`
			Enabled bool   `json:"enabled"`
		} `json:"providers"`
	}
	_ = json.Unmarshal(call("GET", "/v1/billing/providers", nil, 200, ""), &config)
	enabled := false
	for _, p := range config.Providers {
		if p.ID == "demo" && p.Enabled {
			enabled = true
		}
	}
	if !enabled {
		t.Skip("simulation provider disabled")
	}
	var account protocol.BillingAccount
	_ = json.Unmarshal(call("POST", "/v1/billing/accounts", map[string]string{"plan": "pro", "currency": "CNY"}, 201, ""), &account)
	call("POST", "/v1/tasks", map[string]string{"title": "Billing E2E billable task", "objective": "Verify local payment accounting without real money"}, 201, "")
	input := map[string]any{"billing_account_id": account.ID, "period_start": time.Now().Add(-time.Hour), "period_end": time.Now()}
	var doc protocol.BillingDocument
	_ = json.Unmarshal(call("POST", "/v1/billing/invoices", input, 201, "e2e-invoice-key"), &doc)
	if doc.Invoice.AmountMinor <= 0 {
		t.Fatal("bill has no consumption")
	}
	id := doc.Invoice.ID
	var again protocol.BillingDocument
	_ = json.Unmarshal(call("POST", "/v1/billing/invoices", input, 201, "e2e-invoice-key"), &again)
	if again.Invoice.ID != id {
		t.Fatal("idempotent issue created a duplicate invoice")
	}
	var checkout struct {
		Payment protocol.BillingPayment `json:"payment"`
		Link    string                  `json:"payment_link"`
	}
	_ = json.Unmarshal(call("POST", "/v1/billing/invoices/"+id+"/payments", map[string]string{"provider": "demo"}, 201, "e2e-payment-key"), &checkout)
	u, _ := url.Parse(checkout.Link)
	page := request(t, "GET", base+u.RequestURI(), "", nil, nil, 200)
	if !strings.Contains(string(page), "本地模拟") {
		t.Fatal("simulation safety warning missing")
	}
	// Exercise PostgreSQL optimistic concurrency: exactly one receipt under callback retries.
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest("POST", base+"/v1/billing/invoices/"+id+"/payments/"+checkout.Payment.ID+"/simulate", strings.NewReader(`{"outcome":"succeeded"}`))
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				errs <- err
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != 200 {
				errs <- fmt.Errorf("simulate returned %d", resp.StatusCode)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	_ = json.Unmarshal(call("GET", "/v1/billing/invoices/"+id, nil, 200, ""), &doc)
	if doc.Invoice.Status != "paid" || len(doc.Receipts) != 1 {
		t.Fatalf("invalid settlement %+v", doc)
	}
	call("GET", "/v1/billing/invoices/"+id+"/export", nil, 200, "")
	call("GET", "/v1/billing/invoices/"+id+"/receipts/"+doc.Receipts[0].ID+"?format=html", nil, 200, "")
	_, other := createAccount(t, base, bootstrap, "billing-other-"+time.Now().Format("150405"))
	request(t, "GET", base+"/v1/billing/invoices/"+id, other, nil, headers, 404)
	call("POST", "/v1/billing/invoices/"+id+"/payments/"+checkout.Payment.ID+"/revoke-link", nil, 200, "")
	request(t, "GET", base+u.RequestURI(), "", nil, nil, 404)
}
