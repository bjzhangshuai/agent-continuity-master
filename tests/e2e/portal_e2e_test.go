package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

// Uses the named local demo fixture only. No new orders or real financial actions.
func TestPortalLocalRolesAndGroupPayment(t *testing.T) {
	stateFile, base := os.Getenv("CONTINUITY_E2E_PORTAL_STATE"), os.Getenv("CONTINUITY_E2E_URL")
	if stateFile == "" || base == "" {
		t.Skip("explicit local portal demo state and URL required")
	}
	u, e := url.Parse(base)
	if e != nil || u.Scheme != "http" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") {
		t.Fatal("loopback required")
	}
	raw, e := os.ReadFile(stateFile)
	if e != nil {
		t.Fatal(e)
	}
	var state struct {
		Users map[string]struct {
			Username  string `json:"username"`
			Password  string `json:"password"`
			AccountID string `json:"account_id"`
		} `json:"users"`
		Tenants  map[string]string `json:"tenants"`
		Invoices map[string]string `json:"invoices"`
	}
	if e = json.Unmarshal(raw, &state); e != nil {
		t.Fatal(e)
	}
	call := func(c *http.Client, method, path string, input any, want int, csrf bool) []byte {
		t.Helper()
		b, _ := json.Marshal(input)
		req, _ := http.NewRequest(method, base+path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		if csrf {
			req.Header.Set("X-Portal-Request", "1")
		}
		r, e := c.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer r.Body.Close()
		data, e := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		if e != nil || r.StatusCode != want {
			t.Fatalf("%s %s got %d expected %d: %s (%v)", method, path, r.StatusCode, want, data, e)
		}
		return data
	}
	login := func(key string) *http.Client {
		t.Helper()
		jar, _ := cookiejar.New(nil)
		c := &http.Client{Jar: jar, Timeout: 15 * time.Second}
		user := state.Users[key]
		call(c, "POST", "/v1/auth/login", map[string]string{"username": user.Username, "password": user.Password}, 200, true)
		return c
	}
	anonymous := &http.Client{Timeout: 10 * time.Second}
	call(anonymous, "GET", "/v1/portal/session", nil, 401, true)
	root := login("platform")
	finance := login("finance")
	viewer := login("viewer")
	region := login("region")
	var identity struct {
		Super   bool `json:"super_admin"`
		Managed []struct {
			ID string `json:"id"`
		} `json:"managed_tenants"`
	}
	if e = json.Unmarshal(call(root, "GET", "/v1/portal/session", nil, 200, true), &identity); e != nil || !identity.Super || len(identity.Managed) < 5 {
		t.Fatal("super admin graph missing", e)
	}
	identity.Super = false
	identity.Managed = nil
	_ = json.Unmarshal(call(region, "GET", "/v1/portal/session", nil, 200, true), &identity)
	if identity.Super || len(identity.Managed) != 2 {
		t.Fatal("regional scope leaked", len(identity.Managed))
	}
	for _, o := range identity.Managed {
		if o.ID != state.Tenants["east"] && o.ID != state.Tenants["lab"] {
			t.Fatal("out-of-scope tenant visible")
		}
	}
	call(region, "GET", "/v1/portal/tenants/"+state.Tenants["east"]+"/billing", nil, 403, true)
	call(region, "GET", "/v1/portal/tenants/"+state.Tenants["west"]+"/finance-summary", nil, 403, true)
	call(viewer, "GET", "/v1/portal/tenants/"+state.Tenants["east"]+"/billing", nil, 403, true)
	call(finance, "GET", "/v1/portal/tenants/"+state.Tenants["west"]+"/billing", nil, 403, true)
	call(finance, "GET", "/v1/treasury", nil, 403, true)
	call(finance, "POST", "/v1/treasury/payouts", map[string]any{}, 403, true)
	paidID := state.Invoices["east-paid"]
	path := "/v1/billing/invoices/" + paidID
	call(root, "GET", path, nil, 404, true)
	call(viewer, "GET", path, nil, 404, true)
	var bill protocol.BillingDocument
	if e = json.Unmarshal(call(finance, "GET", path, nil, 200, true), &bill); e != nil {
		t.Fatal(e)
	}
	if bill.Invoice.Status != "paid" || bill.Invoice.OrganizationID != state.Tenants["east"] || bill.Invoice.PayerOrganizationID != state.Tenants["group"] || len(bill.Receipts) != 1 || len(bill.Payments) != 1 || bill.Payments[0].InitiatedBy != state.Users["finance"].AccountID {
		t.Fatal("payer/tenant attribution or receipt mismatch")
	}
	var group struct {
		Invoices []protocol.BillingDocument `json:"invoices"`
	}
	_ = json.Unmarshal(call(finance, "GET", "/v1/portal/tenants/"+state.Tenants["group"]+"/billing", nil, 200, true), &group)
	found := false
	for _, d := range group.Invoices {
		if d.Invoice.ID == paidID {
			found = true
		}
		if d.Invoice.OrganizationID != state.Tenants["group"] && d.Invoice.PayerOrganizationID != state.Tenants["group"] {
			t.Fatal("unrelated invoice in group view")
		}
	}
	if !found {
		t.Fatal("delegated invoice absent")
	}
	receipt := call(finance, "GET", path+"/receipts/"+bill.Receipts[0].ID+"?format=html", nil, 200, true)
	for _, value := range []string{bill.Receipts[0].ID, state.Tenants["east"], state.Tenants["group"], state.Users["finance"].AccountID} {
		if !strings.Contains(string(receipt), value) {
			t.Fatal("printable receipt attribution missing")
		}
	}
	merchantPath := "/v1/treasury/receivables/" + paidID
	call(finance, "GET", merchantPath, nil, 403, true)
	var receivable struct {
		protocol.BillingDocument
		CanPay bool `json:"can_pay"`
	}
	if e = json.Unmarshal(call(root, "GET", merchantPath, nil, 200, true), &receivable); e != nil || receivable.CanPay || receivable.Invoice.ID != paidID {
		t.Fatal("merchant read-only financial evidence missing", e)
	}
	for _, p := range receivable.Payments {
		if p.CheckoutURL != "" || p.QRCode != "" {
			t.Fatal("merchant received tenant payment authority")
		}
	}
	call(root, "GET", merchantPath+"/receipts/"+bill.Receipts[0].ID+"?format=html", nil, 200, true)
	csv := call(root, "GET", merchantPath+"/export", nil, 200, true)
	for _, value := range []string{state.Tenants["east"], state.Tenants["group"], bill.Invoice.PaymentMandateID} {
		if value == "" || !strings.Contains(string(csv), value) {
			t.Fatal("exported invoice liability missing")
		}
	}
	// Repeating an already confirmed demo callback must not credit money twice.
	for n := 0; n < 2; n++ {
		var repeated protocol.BillingDocument
		_ = json.Unmarshal(call(finance, "POST", path+"/payments/"+bill.Payments[0].ID+"/simulate", map[string]string{"outcome": "succeeded"}, 200, true), &repeated)
		if len(repeated.Receipts) != 1 || repeated.Invoice.AmountMinor != bill.Invoice.AmountMinor {
			t.Fatal("duplicate credit")
		}
	}
	call(finance, "POST", "/v1/auth/session/logout", nil, 403, false)
	call(finance, "POST", "/v1/auth/session/logout", nil, 204, true)
	call(finance, "GET", "/v1/portal/session", nil, 401, true)
	t.Logf("Verified PostgreSQL-backed login, four roles, scoped administration, group liability, isolated treasury, receipt download, idempotent confirmation, and logout")
}
