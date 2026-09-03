package httpapi

import (
	"context"
	"github.com/agent-continuity/agent-continuity-master/internal/auth"
	"github.com/agent-continuity/agent-continuity-master/internal/billing"
	"github.com/agent-continuity/agent-continuity-master/internal/storage"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
	"net/http"
	"strings"
	"time"
)

type invoiceActorKey struct{}

func (s *Server) canAccessInvoice(r *http.Request, c auth.Claims, i protocol.BillingInvoice, write bool) bool {
	if i.OrganizationID == "" {
		return i.AccountID == c.AccountID
	}
	if tenantFinanceRole(s.tenantRole(r, c, i.OrganizationID), write) {
		return true
	}
	return i.PayerOrganizationID != "" && tenantFinanceRole(s.tenantRole(r, c, i.PayerOrganizationID), write)
}

// These claims are ONLY the repository's historical invoice owner key. The
// original actor is carried separately; they must never authorize other APIs.
func (s *Server) authorizeInvoiceScope(w http.ResponseWriter, r *http.Request, scope string) (auth.Claims, bool) {
	merchantRead := r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v1/treasury/receivables/")
	if merchantRead {
		scope = "treasury:read"
	}
	c, ok := s.authorizeScope(w, r, scope)
	if !ok {
		return c, false
	}
	if merchantRead && (s.treasury == nil || s.treasury.Owner == "" || s.treasury.Owner != c.AccountID) {
		writeStorageError(w, storage.ErrUnauthorized)
		return c, false
	}
	d, err := s.store.GetBillByID(r.Context(), r.PathValue("invoiceID"))
	if err != nil {
		billingError(w, billing.ErrNotFound)
		return c, false
	}
	if !merchantRead && !s.canAccessInvoice(r, c, d.Invoice, scope == "billing:write") {
		billingError(w, billing.ErrNotFound)
		return c, false
	}
	*r = *r.WithContext(context.WithValue(billing.WithActor(r.Context(), c.AccountID), invoiceActorKey{}, c))
	c.AccountID = d.Invoice.AccountID
	return c, true
}
func hideCheckout(d *protocol.BillingDocument) {
	for i := range d.Payments {
		d.Payments[i].CheckoutURL = ""
		d.Payments[i].QRCode = ""
	}
}
func (s *Server) invoiceCanPay(r *http.Request, i protocol.BillingInvoice) bool {
	if strings.HasPrefix(r.URL.Path, "/v1/treasury/receivables/") {
		return false
	}
	c, ok := s.authorizeForDisplay(r)
	return ok && auth.HasScope(c, "billing:write") && s.canAccessInvoice(r, c, i, true)
}

// Read-only identity lookup for presentation after authentication has succeeded.
func (s *Server) authorizeForDisplay(r *http.Request) (auth.Claims, bool) {
	c, ok := r.Context().Value(invoiceActorKey{}).(auth.Claims)
	return c, ok
}
func (s *Server) applyPayer(r *http.Request, b *protocol.BillingAccount) error {
	if b.OrganizationID == "" {
		return nil
	}
	b.PayerOrganizationID = b.OrganizationID
	ctrl, err := s.store.LoadTenancyControl(r.Context())
	if err != nil {
		return err
	}
	for _, m := range ctrl.Mandates {
		if m.TenantID == b.OrganizationID && m.Status == "active" {
			b.PayerOrganizationID = m.PayerTenantID
			b.PaymentMandateID = m.ID
			break
		}
	}
	return nil
}
func (s *Server) portalTenantBilling(w http.ResponseWriter, r *http.Request) {
	c, ok := s.authorizeScope(w, r, "billing:read")
	if !ok {
		return
	}
	id := r.PathValue("tenantID")
	role := s.tenantRole(r, c, id)
	if !tenantFinanceRole(role, false) {
		writeStorageError(w, storage.ErrUnauthorized)
		return
	}
	b, err := s.store.GetTenantBillingAccount(r.Context(), id)
	var account any
	if err == nil {
		if err = s.applyPayer(r, &b); err != nil {
			writeStorageError(w, err)
			return
		}
		account = b
	} else if err != storage.ErrNotFound {
		writeStorageError(w, err)
		return
	}
	docs, err := s.store.ListTenantBills(r.Context(), id, 200)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	for i := range docs {
		if !tenantFinanceRole(role, true) || !auth.HasScope(c, "billing:write") {
			hideCheckout(&docs[i])
		}
	}
	totals := map[string]int64{}
	names := map[string]string{}
	for _, d := range docs {
		for _, oid := range []string{d.Invoice.OrganizationID, d.Invoice.PayerOrganizationID} {
			if oid != "" && names[oid] == "" {
				if o, err := s.store.GetOrganization(r.Context(), oid); err == nil {
					names[oid] = o.Name
				}
			}
		}
		if d.Invoice.Status == "open" {
			env := "live"
			if d.Invoice.TestMode {
				env = "test"
			}
			totals[d.Invoice.Currency+"/"+env] += d.Invoice.AmountMinor
		}
	}
	writeJSON(w, 200, map[string]any{"tenant_id": id, "tenant_names": names, "role": role, "billing_account": account, "invoices": docs, "outstanding_minor": totals, "can_pay": tenantFinanceRole(role, true) && auth.HasScope(c, "billing:write"), "limit": 200, "note": "Tenant and explicitly accepted group liabilities only; currencies/environments never combined"})
}
func (s *Server) portalBillingAccount(w http.ResponseWriter, r *http.Request) {
	c, ok := s.authorizeScope(w, r, "billing:write")
	if !ok {
		return
	}
	id := r.PathValue("tenantID")
	if s.tenantRole(r, c, id) != "owner" {
		writeStorageError(w, storage.ErrUnauthorized)
		return
	}
	var in struct {
		Currency string `json:"currency"`
	}
	if !s.decodeJSON(w, r, &in) {
		return
	}
	in.Currency = strings.ToUpper(in.Currency)
	if _, ok = s.billing.PriceBook.Currencies[in.Currency]; !ok {
		writeError(w, 400, "currency_unconfigured", "Configured price-book currency required")
		return
	}
	if b, err := s.store.GetTenantBillingAccount(r.Context(), id); err == nil {
		writeJSON(w, 200, b)
		return
	} else if err != storage.ErrNotFound {
		writeStorageError(w, err)
		return
	}
	org, err := s.store.GetOrganization(r.Context(), id)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	bid, _ := protocol.NewID("billing")
	now := time.Now().UTC()
	b := protocol.BillingAccount{ID: bid, AccountID: org.OwnerAccountID, OrganizationID: id, Plan: "team", Status: "active", Currency: in.Currency, Limits: billingLimits("free"), CurrentPeriodStart: now, CurrentPeriodEnd: now.AddDate(0, 1, 0), CreatedAt: now, UpdatedAt: now}
	if err = s.store.PutBillingAccount(r.Context(), b); err != nil {
		writeStorageError(w, err)
		return
	}
	s.audit(r, c, "tenant.billing_account.created", "billing_account", bid, nil)
	writeJSON(w, 201, b)
}
func (s *Server) portalQuote(w http.ResponseWriter, r *http.Request) {
	s.portalInvoiceRequest(w, r, false)
}
func (s *Server) portalIssue(w http.ResponseWriter, r *http.Request) {
	s.portalInvoiceRequest(w, r, true)
}
func (s *Server) portalInvoiceRequest(w http.ResponseWriter, r *http.Request, issue bool) {
	c, ok := s.authorizeScope(w, r, "billing:write")
	if !ok {
		return
	}
	id := r.PathValue("tenantID")
	if !tenantFinanceRole(s.tenantRole(r, c, id), true) {
		writeStorageError(w, storage.ErrUnauthorized)
		return
	}
	var in struct {
		PeriodStart time.Time `json:"period_start"`
		PeriodEnd   time.Time `json:"period_end"`
	}
	if !s.decodeJSON(w, r, &in) {
		return
	}
	b, err := s.store.GetTenantBillingAccount(r.Context(), id)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	if err = s.applyPayer(r, &b); err != nil {
		writeStorageError(w, err)
		return
	}
	from, to := in.PeriodStart.UTC().Truncate(time.Microsecond), in.PeriodEnd.UTC().Truncate(time.Microsecond)
	if !issue {
		q, e := s.billing.Quote(r.Context(), b, from, to)
		if e != nil {
			billingError(w, e)
			return
		}
		writeJSON(w, 200, q)
		return
	}
	d, err := s.billing.Issue(billing.WithActor(r.Context(), c.AccountID), b, from, to, r.Header.Get("Idempotency-Key"))
	if err != nil {
		billingError(w, err)
		return
	}
	writeJSON(w, 201, d)
}
