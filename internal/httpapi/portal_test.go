package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/agent-continuity/agent-continuity-master/internal/auth"
	"github.com/agent-continuity/agent-continuity-master/internal/billing"
	"github.com/agent-continuity/agent-continuity-master/internal/payments"
	"github.com/agent-continuity/agent-continuity-master/internal/tenancy"
	"github.com/agent-continuity/agent-continuity-master/internal/treasury"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func portalWant(t *testing.T, f *apiFixture, method, path, token string, body any, status int) []byte {
	t.Helper()
	code, b := f.request(method, path, token, body, map[string]string{"Idempotency-Key": "portal-test-key-0001"})
	if code != status {
		t.Fatalf("%s %s: want %d got %d %s", method, path, status, code, b)
	}
	return b
}
func portalOrg(t *testing.T, f *apiFixture, token, name, parent, owner string) protocol.Organization {
	t.Helper()
	return decode[protocol.Organization](t, portalWant(t, f, "POST", "/v1/portal/tenants", token, map[string]string{"name": name, "slug": name, "parent_id": parent, "owner_account_id": owner}, 201))
}
func portalMember(t *testing.T, f *apiFixture, token, org, account, role string) {
	t.Helper()
	portalWant(t, f, "PUT", "/v1/organizations/"+org+"/members/"+account, token, map[string]string{"role": role}, 200)
}

func TestPortalHierarchyGrantsAndNoImplicitTenantAccess(t *testing.T) {
	f := newFixture(t)
	super, st := f.account("super")
	owner, ot := f.account("owner")
	admin, at := f.account("scoped-admin")
	support, spt := f.account("support")
	f.api.platformAdmins = map[string]bool{super.ID: true}
	group := portalOrg(t, f, st, "group", "", owner.ID)
	child := portalOrg(t, f, st, "child", group.ID, owner.ID)
	other := portalOrg(t, f, st, "other", "", owner.ID)
	grant := decode[tenancy.Grant](t, portalWant(t, f, "POST", "/v1/portal/grants", st, map[string]string{"account_id": admin.ID, "role": "admin", "tenant_id": group.ID}, 201))
	sub := decode[tenancy.Grant](t, portalWant(t, f, "POST", "/v1/portal/grants", at, map[string]string{"account_id": support.ID, "role": "support", "tenant_id": child.ID, "parent_id": grant.ID}, 201))
	for _, scope := range []string{"*", other.ID} {
		portalWant(t, f, "POST", "/v1/portal/grants", at, map[string]string{"account_id": support.ID, "role": "admin", "tenant_id": scope, "parent_id": grant.ID}, 403)
	}
	portalWant(t, f, "POST", "/v1/portal/grants", spt, map[string]string{"account_id": support.ID, "role": "admin", "tenant_id": child.ID, "parent_id": sub.ID}, 403)
	for _, token := range []string{st, at, spt} {
		portalWant(t, f, "GET", "/v1/organizations/"+child.ID, token, nil, 403)
		portalWant(t, f, "GET", "/v1/portal/tenants/"+child.ID+"/billing", token, nil, 403)
	}
	// Parent membership alone does not grant child membership.
	grand := portalOrg(t, f, st, "grandchild", child.ID, super.ID)
	portalWant(t, f, "GET", "/v1/organizations/"+grand.ID, ot, nil, 403)
	portalWant(t, f, "POST", "/v1/portal/grants/"+grant.ID+"/revoke", st, nil, 200)
	ctrl, err := f.api.store.LoadTenancyControl(context.Background())
	if err != nil || ctrl.ActiveGrant(sub.ID) {
		t.Fatal("descendant stayed effective", err)
	}
	portalWant(t, f, "POST", "/v1/portal/grants", at, map[string]string{"account_id": support.ID, "role": "support", "tenant_id": child.ID, "parent_id": grant.ID}, 403)
	portalWant(t, f, "DELETE", "/v1/organizations/"+group.ID, ot, nil, 409)
	parent := child.ID
	for depth := 3; depth <= 8; depth++ {
		o := portalOrg(t, f, st, "level-"+strings.Repeat("x", depth), parent, owner.ID)
		parent = o.ID
	}
	portalWant(t, f, "POST", "/v1/portal/tenants", st, map[string]string{"name": "too-deep", "slug": "too-deep", "parent_id": parent}, 400)
}
func TestPortalTenantBillingConsentAndRevocation(t *testing.T) {
	f := newFixtureWithOptions(t, WithPayments(payments.NewRegistry(payments.Demo{}), billing.DemoPriceBook(), "http://127.0.0.1:18080"))
	groupOwner, gt := f.account("group-owner")
	childOwner, ct := f.account("child-owner")
	payer, pt := f.account("payer")
	viewer, vt := f.account("viewer")
	admin, at := f.account("tenant-admin")
	f.api.platformAdmins = map[string]bool{groupOwner.ID: true}
	group := portalOrg(t, f, gt, "billing-group", "", groupOwner.ID)
	child := portalOrg(t, f, gt, "billing-child", group.ID, childOwner.ID)
	portalMember(t, f, gt, group.ID, payer.ID, "billing")
	portalMember(t, f, ct, child.ID, viewer.ID, "viewer")
	portalMember(t, f, ct, child.ID, admin.ID, "admin")
	portalWant(t, f, "PUT", "/v1/organizations/"+child.ID+"/members/"+admin.ID, at, map[string]string{"role": "billing"}, 403)
	portalWant(t, f, "PUT", "/v1/organizations/"+child.ID+"/members/"+childOwner.ID, at, map[string]string{"role": "admin"}, 403)
	portalWant(t, f, "POST", "/v1/portal/tenants/"+child.ID+"/billing-account", ct, map[string]string{"currency": "CNY"}, 201)
	mandate := decode[tenancy.Mandate](t, portalWant(t, f, "POST", "/v1/portal/mandates", ct, map[string]string{"tenant_id": child.ID, "payer_tenant_id": group.ID}, 201))
	portalWant(t, f, "POST", "/v1/portal/mandates/"+mandate.ID+"/accept", ct, nil, 403)
	portalWant(t, f, "POST", "/v1/portal/mandates/"+mandate.ID+"/accept", gt, nil, 200)
	portalWant(t, f, "POST", "/v1/portal/mandates/"+mandate.ID+"/accept", gt, nil, 409)
	now := time.Now().UTC()
	for n, account := range []string{childOwner.ID, viewer.ID} {
		err := f.api.store.RecordUsage(context.Background(), protocol.UsageRecord{ID: []string{"u1", "u2"}[n], AccountID: account, OrganizationID: child.ID, Metric: "api.mutation", Unit: "operation", Quantity: 10, RecordedAt: now.Add(-time.Minute)})
		if err != nil {
			t.Fatal(err)
		}
	}
	input := map[string]any{"period_start": now.Add(-time.Hour), "period_end": now}
	invoice := decode[protocol.BillingDocument](t, portalWant(t, f, "POST", "/v1/portal/tenants/"+child.ID+"/invoices", ct, input, 201))
	if invoice.Invoice.AmountMinor != 20 || invoice.Invoice.PayerOrganizationID != group.ID || invoice.Invoice.IssuedBy != childOwner.ID {
		t.Fatalf("wrong tenant aggregate/liability %+v", invoice.Invoice)
	}
	id := invoice.Invoice.ID
	path := "/v1/billing/invoices/" + id
	portalWant(t, f, "GET", path, vt, nil, 404)
	portalWant(t, f, "POST", path+"/payments", at, map[string]string{"provider": "demo"}, 404)
	portalWant(t, f, "POST", "/v1/portal/mandates/"+mandate.ID+"/revoke", gt, nil, 200)
	// Existing liability remains frozen even after future delegation is revoked.
	p := decode[struct {
		Payment protocol.BillingPayment `json:"payment"`
	}](t, portalWant(t, f, "POST", path+"/payments", pt, map[string]string{"provider": "demo"}, 201))
	if p.Payment.InitiatedBy != payer.ID {
		t.Fatal("payer actor was replaced by invoice creator")
	}
	read := decode[struct {
		Links    map[string]string         `json:"payment_links"`
		CanPay   bool                      `json:"can_pay"`
		Payments []protocol.BillingPayment `json:"payments"`
	}](t, portalWant(t, f, "GET", path, at, nil, 200))
	if read.CanPay || len(read.Links) != 0 || read.Payments[0].QRCode != "" || read.Payments[0].CheckoutURL != "" {
		t.Fatal("read-only administrator received payment capabilities")
	}
	for n := 0; n < 2; n++ {
		d := decode[protocol.BillingDocument](t, portalWant(t, f, "POST", path+"/payments/"+p.Payment.ID+"/simulate", pt, map[string]string{"outcome": "succeeded"}, 200))
		if len(d.Receipts) != 1 || d.Invoice.Status != "paid" {
			t.Fatal("non-idempotent payment confirmation")
		}
	}
	portalWant(t, f, "DELETE", "/v1/organizations/"+group.ID+"/members/"+payer.ID, gt, nil, 204)
	portalWant(t, f, "GET", path, pt, nil, 404)
	portalWant(t, f, "GET", "/v1/treasury", ct, nil, 503) // no operator configured in this fixture
	b, _ := f.api.store.GetTenantBillingAccount(context.Background(), child.ID)
	b.PayerOrganizationID = group.ID
	b.PaymentMandateID = mandate.ID
	_, err := f.api.billing.Issue(context.Background(), b, now.Add(-2*time.Hour), now.Add(-time.Hour), "stale-consent-key")
	if err != billing.ErrConflict {
		t.Fatal("revoked mandate accepted at commit", err)
	}
}
func TestPortalRemovedCreatorLosesLegacyTaskACL(t *testing.T) {
	f := newFixture(t)
	owner, ot := f.account("owner")
	member, mt := f.account("member")
	org := portalOrg(t, f, ot, "acl-tenant", "", owner.ID)
	portalMember(t, f, ot, org.ID, member.ID, "member")
	task := decode[protocol.Task](t, portalWant(t, f, "POST", "/v1/tasks", mt, map[string]string{"title": "tenant task", "objective": "isolation", "organization_id": org.ID}, 201))
	portalWant(t, f, "GET", "/v1/tasks/"+task.ID, mt, nil, 200)
	r, _ := http.NewRequest("GET", f.server.URL+"/v1/tasks/"+task.ID+"/realtime", nil)
	r.Header.Set("Authorization", "Bearer "+mt)
	claims, _ := f.api.tokens.Parse(mt)
	if !f.api.realtimeAccessValid(r, claims, task.ID) {
		t.Fatal("active member socket rejected")
	}
	portalWant(t, f, "DELETE", "/v1/organizations/"+org.ID+"/members/"+member.ID, ot, nil, 204)
	portalWant(t, f, "GET", "/v1/tasks/"+task.ID, mt, nil, 403)
	if f.api.realtimeAccessValid(r, claims, task.ID) {
		t.Fatal("revoked member socket retained task access")
	}
	if err := f.api.store.CheckPermission(context.Background(), task.ID, member.ID, protocol.PermissionRead); err == nil {
		t.Fatal("storage ACL bypass after removal")
	}
}

func TestMerchantReceivablesDoNotGrantPaymentAuthority(t *testing.T) {
	f := newFixtureWithOptions(t, WithPayments(payments.NewRegistry(payments.Demo{}), billing.DemoPriceBook(), "http://127.0.0.1:18080"))
	operator, ot := f.account("merchant")
	payer, pt := f.account("customer")
	f.api.treasury = treasury.New(f.api.store, operator.ID, true, nil)
	b := decode[protocol.BillingAccount](t, portalWant(t, f, "POST", "/v1/billing/accounts", pt, map[string]string{"plan": "pro", "currency": "CNY"}, 201))
	portalWant(t, f, "POST", "/v1/tasks", pt, map[string]string{"title": "usage", "objective": "merchant payment"}, 201)
	d := decode[protocol.BillingDocument](t, portalWant(t, f, "POST", "/v1/billing/invoices", pt, map[string]any{"billing_account_id": b.ID, "period_start": time.Now().Add(-time.Hour), "period_end": time.Now()}, 201))
	id := d.Invoice.ID
	p := decode[struct {
		Payment protocol.BillingPayment `json:"payment"`
	}](t, portalWant(t, f, "POST", "/v1/billing/invoices/"+id+"/payments", pt, map[string]string{"provider": "demo"}, 201))
	portalWant(t, f, "POST", "/v1/billing/invoices/"+id+"/payments/"+p.Payment.ID+"/simulate", pt, map[string]string{"outcome": "succeeded"}, 200)
	portalWant(t, f, "GET", "/v1/billing/invoices/"+id, ot, nil, 404)
	read := decode[struct {
		CanPay bool              `json:"can_pay"`
		Links  map[string]string `json:"payment_links"`
	}](t, portalWant(t, f, "GET", "/v1/treasury/receivables/"+id, ot, nil, 200))
	if read.CanPay || len(read.Links) > 0 {
		t.Fatal("merchant obtained payment capabilities")
	}
	portalWant(t, f, "GET", "/v1/treasury/receivables", pt, nil, 403)
	portalWant(t, f, "GET", "/v1/treasury/receivables/"+id, pt, nil, 403)
	portalWant(t, f, "POST", "/v1/treasury/collections/import", ot, map[string]string{"invoice_id": id}, 200)
	if payer.ID == operator.ID {
		t.Fatal("fixture must use separate merchant and customer")
	}
}
func TestPortalPasswordCookieAndCSRF(t *testing.T) {
	f := newFixture(t)
	account, token := f.account("portal-user")
	input := map[string]any{"self": true, "username": "test.user", "password": "correct-horse-passphrase", "display_name": "Test User"}
	portalWant(t, f, "POST", "/v1/portal/users", token, input, 201)
	stored, err := f.api.store.GetPortalLogin(context.Background(), "test.user")
	if err != nil || stored.AccountID != account.ID || stored.PasswordHash == input["password"] {
		t.Fatal("password not hashed")
	}
	raw, _ := json.Marshal(map[string]string{"username": "test.user", "password": "correct-horse-passphrase"})
	req, _ := http.NewRequest("POST", f.server.URL+"/v1/auth/login", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Portal-Request", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode, string(body))
	}
	cookies := resp.Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode || bytes.Contains(body, []byte(cookies[0].Value)) {
		t.Fatal("unsafe session cookie")
	}
	headers := map[string]string{"Cookie": cookies[0].Name + "=" + cookies[0].Value}
	portalWantHeaders := func(method, path string, body any, want int) {
		t.Helper()
		code, b := f.request(method, path, "", body, headers)
		if code != want {
			t.Fatalf("cookie %s: %d %s", path, code, b)
		}
	}
	portalWantHeaders("GET", "/v1/portal/session", nil, 200)
	portalWantHeaders("POST", "/v1/tasks", map[string]string{"title": "csrf", "objective": "reject"}, 403)
	headers["X-Portal-Request"] = "1"
	headers["Origin"] = "https://attacker.example"
	portalWantHeaders("POST", "/v1/tasks", map[string]string{"title": "csrf", "objective": "reject"}, 403)
	delete(headers, "Origin")
	portalWantHeaders("POST", "/v1/tasks", map[string]string{"title": "valid", "objective": "session"}, 201)
	portalWantHeaders("POST", "/v1/auth/session/logout", nil, 204)
	portalWantHeaders("GET", "/v1/portal/session", nil, 401)
	// Narrow API credentials never become platform super-admin credentials.
	f.api.platformAdmins = map[string]bool{account.ID: true}
	narrow, _ := f.api.tokens.IssueScoped(account.ID, "narrow", []string{"billing:read"}, "", time.Minute)
	portalWant(t, f, "POST", "/v1/portal/grants", narrow, map[string]string{"account_id": account.ID, "role": "admin", "tenant_id": "*"}, 403)
	old := tenancy.Session{Hash: auth.HashOpaque("pss_expired"), AccountID: account.ID, Username: "test.user", CreatedAt: time.Now().Add(-time.Hour), ExpiresAt: time.Now().Add(time.Hour), LastSeen: time.Now().Add(-31 * time.Minute)}
	_ = f.api.store.CreatePortalSession(context.Background(), old)
	if _, err = f.api.store.GetPortalSession(context.Background(), old.Hash); err == nil {
		t.Fatal("idle timeout ignored")
	}
}
