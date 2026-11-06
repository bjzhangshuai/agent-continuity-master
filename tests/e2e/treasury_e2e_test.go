package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-continuity/agent-continuity-master/internal/treasury"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

// This optional operator test only mutates the isolated, loopback demo ledger.
// It leaves its invoice/receipt/audit as evidence and returns the demo payout.
func TestTreasuryLocalCompleteJourney(t *testing.T) {
	base, token := os.Getenv("CONTINUITY_E2E_URL"), os.Getenv("CONTINUITY_E2E_TREASURY_TOKEN")
	if base == "" || token == "" {
		t.Skip("explicit local treasury operator token required")
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "http" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") {
		t.Fatal("treasury E2E is loopback only")
	}
	httpClient := &http.Client{Timeout: 10 * time.Second}
	invoke := func(method, path string, body any, key string) (int, []byte, error) {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		r, err := http.NewRequest(method, base+path, bytes.NewReader(b))
		if err != nil {
			return 0, nil, err
		}
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+token)
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		resp, err := httpClient.Do(r)
		if err != nil {
			return 0, nil, err
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		return resp.StatusCode, data, err
	}
	call := func(method, path string, body any, want int, key string) []byte {
		t.Helper()
		status, data, err := invoke(method, path, body, key)
		if err != nil || status != want {
			t.Fatalf("%s %s: status=%d want=%d err=%v body=%s", method, path, status, want, err, data)
		}
		return data
	}
	var view struct {
		Document protocol.TreasuryDocument `json:"document"`
		Demo     bool                      `json:"demo_enabled"`
	}
	if err = json.Unmarshal(call("GET", "/v1/treasury", nil, 200, ""), &view); err != nil || !view.Demo {
		t.Fatal("simulation must be enabled")
	}
	var old struct {
		Invoices []protocol.BillingDocument `json:"invoices"`
	}
	_ = json.Unmarshal(call("GET", "/v1/billing/invoices?limit=200", nil, 200, ""), &old)
	key := fmt.Sprintf("treasury-e2e-%d", time.Now().UnixNano())
	var ba protocol.BillingAccount
	var accounts struct {
		Items []protocol.BillingAccount `json:"billing_accounts"`
	}
	_ = json.Unmarshal(call("GET", "/v1/billing/accounts", nil, 200, ""), &accounts)
	for _, a := range accounts.Items {
		if a.Currency == "CNY" && a.OrganizationID == "" {
			ba = a
			break
		}
	}
	if ba.ID == "" {
		_ = json.Unmarshal(call("POST", "/v1/billing/accounts", map[string]string{"plan": "pro", "currency": "CNY"}, 201, ""), &ba)
	}
	var task protocol.Task
	_ = json.Unmarshal(call("POST", "/v1/tasks", map[string]string{"title": "资金闭环 E2E 验收", "objective": "测试付款、收款、本人提现及退回；无真实资金"}, 201, ""), &task)
	// Rancher VM and host clocks may differ. Bill the server's persisted meter
	// timestamp, not a millisecond-sized window based on the test runner clock.
	var usage struct {
		Items []protocol.UsageRecord `json:"usage"`
	}
	_ = json.Unmarshal(call("GET", "/v1/usage?limit=100", nil, 200, ""), &usage)
	var recorded time.Time
	for _, record := range usage.Items {
		if record.ResourceID == task.ID {
			recorded = record.RecordedAt
			break
		}
	}
	if recorded.IsZero() {
		t.Fatal("task meter was not persisted")
	}
	var bill protocol.BillingDocument
	_ = json.Unmarshal(call("POST", "/v1/billing/invoices", map[string]any{"billing_account_id": ba.ID, "period_start": recorded, "period_end": recorded.Add(time.Microsecond)}, 201, key+"-invoice"), &bill)
	if bill.Invoice.AmountMinor <= 0 || !bill.Invoice.TestMode {
		t.Fatal("expected nonzero demo bill")
	}
	var checkout struct {
		Payment protocol.BillingPayment `json:"payment"`
		Link    string                  `json:"payment_link"`
	}
	ip := "/v1/billing/invoices/" + bill.Invoice.ID
	_ = json.Unmarshal(call("POST", ip+"/payments", map[string]string{"provider": "demo"}, 201, key+"-payment"), &checkout)
	if checkout.Payment.Environment != "simulation" {
		t.Fatal("not a simulation")
	}
	link, _ := url.Parse(checkout.Link)
	if !bytes.Contains(call("GET", link.RequestURI(), nil, 200, ""), []byte("本地模拟")) {
		t.Fatal("checkout missing simulation warning")
	}
	call("POST", ip+"/payments/"+checkout.Payment.ID+"/simulate", map[string]string{"outcome": "succeeded"}, 200, "")
	var d protocol.TreasuryDocument
	_ = json.Unmarshal(call("POST", "/v1/treasury/collections/import", map[string]string{"invoice_id": bill.Invoice.ID}, 200, ""), &d)
	collection := ""
	for _, c := range d.Collections {
		if c.InvoiceID == bill.Invoice.ID {
			collection = c.ID
		}
	}
	if collection == "" {
		t.Fatal("payment receipt not linked to collection")
	}
	call("POST", "/v1/treasury/collections/"+collection+"/settle-demo", nil, 200, "")
	_ = json.Unmarshal(call("POST", "/v1/treasury/destinations", map[string]string{"kind": "bank", "label": "E2E 本人银行卡 · 虚拟", "currency": "CNY"}, 200, ""), &d)
	destination := ""
	for _, x := range d.Destinations {
		if x.Label == "E2E 本人银行卡 · 虚拟" {
			destination = x.ID
		}
	}
	call("POST", "/v1/treasury/destinations/"+destination+"/verify-demo", nil, 200, "")
	input := map[string]any{"provider": "demo", "destination_id": destination, "currency": "CNY", "amount_minor": 1}
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, _, err := invoke("POST", "/v1/treasury/payouts", input, key+"-payout")
			if err != nil || status != 200 {
				errs <- fmt.Errorf("parallel request status=%d err=%v", status, err)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	_ = json.Unmarshal(call("GET", "/v1/treasury", nil, 200, ""), &view)
	var payout protocol.TreasuryPayout
	count := 0
	for _, p := range view.Document.Payouts {
		if p.IdempotencyKey == key+"-payout" {
			payout = p
			count++
		}
	}
	if count != 1 {
		t.Fatalf("duplicate reservations: %d", count)
	}
	path := "/v1/treasury/payouts/" + payout.ID
	call("POST", path+"/submit", nil, 409, "")
	call("POST", path+"/decision", map[string]string{"decision": "approve"}, 200, "")
	call("POST", path+"/submit", nil, 200, "")
	call("POST", path+"/simulate", map[string]string{"outcome": "paid"}, 200, "")
	if !bytes.Contains(call("GET", path+"/receipt", nil, 200, ""), []byte("未发生真实转账")) {
		t.Fatal("missing receipt warning")
	}
	call("POST", path+"/simulate", map[string]string{"outcome": "returned"}, 200, "")
	if !strings.Contains(string(call("GET", path+"/receipt", nil, 200, "")), "returned") {
		t.Fatal("return not disclosed")
	}
	if !bytes.Contains(call("GET", "/v1/treasury/export", nil, 200, ""), []byte(payout.ID)) {
		t.Fatal("export missing payout")
	}
	_ = json.Unmarshal(call("GET", "/v1/treasury", nil, 200, ""), &view)
	for _, b := range treasury.Balances(view.Document) {
		if b.AvailableMinor < 0 || b.GrossMinor-b.FeeMinor-b.RefundedMinor-b.RefundHeldMinor != b.PendingMinor+b.AvailableMinor+b.ReservedMinor+b.PaidMinor {
			t.Fatal("money conservation failed", b)
		}
	}
	for _, previous := range old.Invoices {
		var current protocol.BillingDocument
		_ = json.Unmarshal(call("GET", "/v1/billing/invoices/"+previous.Invoice.ID, nil, 200, ""), &current)
		if current.Invoice.Status != previous.Invoice.Status || len(current.Receipts) != len(previous.Receipts) {
			t.Fatal("previous payment changed")
		}
	}
	t.Logf("verified payment=%s collection=%s payout=%s; previous %d invoices preserved", checkout.Payment.ID, collection, payout.ID, len(old.Invoices))
}
