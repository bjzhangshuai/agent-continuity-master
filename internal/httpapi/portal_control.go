package httpapi

import (
	"errors"
	"github.com/agent-continuity/agent-continuity-master/internal/auth"
	"github.com/agent-continuity/agent-continuity-master/internal/storage"
	"github.com/agent-continuity/agent-continuity-master/internal/tenancy"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
	"net/http"
	"strings"
	"time"
)

func (s *Server) registerPortalRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/auth/login", s.portalLogin)
	mux.HandleFunc("POST /v1/auth/session/logout", s.portalLogout)
	mux.HandleFunc("GET /v1/portal/session", s.portalIdentity)
	mux.HandleFunc("POST /v1/portal/users", s.portalCreateUser)
	mux.HandleFunc("POST /v1/portal/grants", s.portalGrant)
	mux.HandleFunc("POST /v1/portal/grants/{grantID}/revoke", s.portalRevokeGrant)
	mux.HandleFunc("POST /v1/portal/mandates", s.portalMandate)
	mux.HandleFunc("POST /v1/portal/mandates/{mandateID}/{action}", s.portalMandateAction)
	mux.HandleFunc("POST /v1/portal/tenants", s.createOrganization)
	mux.HandleFunc("PATCH /v1/portal/tenants/{tenantID}/metadata", s.portalTenantMetadata)
	mux.HandleFunc("GET /v1/portal/tenants/{tenantID}/finance-summary", s.portalFinanceSummary)
	mux.HandleFunc("GET /v1/portal/tenants/{tenantID}/billing", s.portalTenantBilling)
	mux.HandleFunc("POST /v1/portal/tenants/{tenantID}/billing-account", s.portalBillingAccount)
	mux.HandleFunc("POST /v1/portal/tenants/{tenantID}/quote", s.portalQuote)
	mux.HandleFunc("POST /v1/portal/tenants/{tenantID}/invoices", s.portalIssue)
	mux.HandleFunc("GET /portal.js", s.staticAsset)
}
func (s *Server) tenantRole(r *http.Request, c auth.Claims, id string) string {
	m, e := s.store.GetOrganizationMember(r.Context(), id, c.AccountID)
	if e != nil || m.Status != "active" {
		return ""
	}
	return m.Role
}
func tenantFinanceRole(role string, write bool) bool {
	if write {
		return role == "owner" || role == "billing"
	}
	return role == "owner" || role == "admin" || role == "billing"
}
func (s *Server) platformAllows(r *http.Request, c auth.Claims, tenant, action string) bool {
	if c.APIKeyID != "" || !auth.HasScope(c, "*") {
		return false
	}
	if s.superAdmin(c) {
		return true
	}
	control, e := s.store.LoadTenancyControl(r.Context())
	if e != nil {
		return false
	}
	orgs, e := s.store.AllOrganizations(r.Context())
	return e == nil && control.Allows(orgs, c.AccountID, tenant, action)
}
func (s *Server) portalIdentity(w http.ResponseWriter, r *http.Request) {
	c, ok := s.authorize(w, r)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	control, err := s.store.LoadTenancyControl(r.Context())
	if err != nil {
		writeStorageError(w, err)
		return
	}
	orgs, err := s.store.AllOrganizations(r.Context())
	if err != nil {
		writeStorageError(w, err)
		return
	}
	tenants := []map[string]any{}
	managed := []map[string]any{}
	mine := map[string]bool{}
	interactive := c.APIKeyID == "" && auth.HasScope(c, "*")
	for _, o := range orgs {
		role := s.tenantRole(r, c, o.ID)
		if role != "" && auth.HasScope(c, "organizations:read") {
			mine[o.ID] = true
			tenants = append(tenants, map[string]any{"tenant": o, "role": role, "can_read_billing": tenantFinanceRole(role, false) && auth.HasScope(c, "billing:read"), "can_pay": tenantFinanceRole(role, true) && auth.HasScope(c, "billing:write")})
		}
		if s.superAdmin(c) || (interactive && control.Allows(orgs, c.AccountID, o.ID, "read")) {
			managed = append(managed, map[string]any{"id": o.ID, "name": o.Name, "parent_id": o.ParentID, "owner_account_id": o.OwnerAccountID, "can_manage": s.superAdmin(c) || control.Allows(orgs, c.AccountID, o.ID, "delegate"), "can_operate": s.superAdmin(c) || control.Allows(orgs, c.AccountID, o.ID, "operate"), "can_finance": s.superAdmin(c) || control.Allows(orgs, c.AccountID, o.ID, "finance")})
		}
	}
	grants := []map[string]any{}
	events := []tenancy.Event{}
	mandates := []tenancy.Mandate{}
	if interactive {
		for _, g := range control.Grants {
			if s.superAdmin(c) || g.AccountID == c.AccountID || control.Allows(orgs, c.AccountID, g.TenantID, "delegate") {
				account, _ := s.store.GetAccount(r.Context(), g.AccountID)
				grants = append(grants, map[string]any{"grant": g, "effective": control.ActiveGrant(g.ID), "account_name": account.Name})
			}
		}
	}
	for _, m := range control.Mandates {
		if auth.HasScope(c, "billing:read") && ((mine[m.TenantID] && tenantFinanceRole(s.tenantRole(r, c, m.TenantID), false)) || (mine[m.PayerTenantID] && tenantFinanceRole(s.tenantRole(r, c, m.PayerTenantID), false))) {
			mandates = append(mandates, m)
		}
	}
	if interactive {
		for i := len(control.Events) - 1; i >= 0 && len(events) < 100; i-- {
			e := control.Events[i]
			if s.superAdmin(c) || (e.TenantID != "" && control.Allows(orgs, c.AccountID, e.TenantID, "read")) {
				events = append(events, e)
			}
		}
	}
	account, err := s.store.GetAccount(r.Context(), c.AccountID)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	names := map[string]string{}
	visibleIDs := map[string]bool{}
	for _, o := range orgs {
		if mine[o.ID] {
			visibleIDs[o.ID] = true
			visibleIDs[o.ParentID] = true
		}
	}
	for _, m := range mandates {
		visibleIDs[m.TenantID] = true
		visibleIDs[m.PayerTenantID] = true
	}
	for _, o := range orgs {
		if visibleIDs[o.ID] {
			names[o.ID] = o.Name
		}
	}
	writeJSON(w, 200, map[string]any{"account": account, "tenant_names": names, "subject": c.Subject, "super_admin": s.superAdmin(c), "treasury_operator": s.treasury != nil && s.treasury.Owner == c.AccountID, "tenants": tenants, "managed_tenants": managed, "grants": grants, "mandates": mandates, "events": events, "max_depth": tenancy.MaxDepth, "session_idle_minutes": 30, "session_max_hours": 8})
}
func (s *Server) mutateControl(w http.ResponseWriter, r *http.Request, c auth.Claims, action, tenant, id string, fn func(*tenancy.Control) error) bool {
	for attempt := 0; attempt < 8; attempt++ {
		v, err := s.store.LoadTenancyControl(r.Context())
		if err != nil {
			writeStorageError(w, err)
			return false
		}
		if (len(v.Grants) >= 5000 || len(v.Mandates) >= 5000 || len(v.Events) >= 50000) && !strings.Contains(action, "revoke") {
			writeError(w, 409, "control_capacity", "Control-plane capacity reached; archive/migrate before adding records")
			return false
		}
		if err = fn(&v); err != nil {
			writeStorageError(w, err)
			return false
		}
		// Resolve event scope from immutable relationships, including revocations.
		if tenant == "" {
			for _, g := range v.Grants {
				if g.ID == id {
					tenant = g.TenantID
				}
			}
			for _, m := range v.Mandates {
				if m.ID == id {
					tenant = m.TenantID
				}
			}
		}
		eventID, _ := protocol.NewID("ctl")
		v.Events = append(v.Events, tenancy.Event{ID: eventID, ActorID: c.AccountID, Action: action, TenantID: tenant, ResourceID: id, CreatedAt: time.Now().UTC()})
		err = s.store.SaveTenancyControl(r.Context(), v, v.Version)
		if errors.Is(err, tenancy.ErrConflict) {
			continue
		}
		if err != nil {
			writeStorageError(w, err)
			return false
		}
		return true
	}
	writeError(w, 409, "control_conflict", "Concurrent change; reload and retry")
	return false
}

func (s *Server) portalTenantMetadata(w http.ResponseWriter, r *http.Request) {
	c, ok := s.authorizeScope(w, r, "*")
	if !ok {
		return
	}
	id := r.PathValue("tenantID")
	if !s.platformAllows(r, c, id, "operate") {
		writeStorageError(w, storage.ErrUnauthorized)
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if !s.decodeJSON(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 120 {
		writeError(w, 400, "invalid_name", "Name must be 1..120 bytes")
		return
	}
	o, err := s.store.GetOrganization(r.Context(), id)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	o.Name = in.Name
	o.UpdatedAt = time.Now().UTC()
	if err = s.store.UpdateOrganization(r.Context(), o); err != nil {
		writeStorageError(w, err)
		return
	}
	s.audit(r, c, "platform.tenant.renamed", "organization", id, nil)
	writeJSON(w, 200, map[string]string{"id": o.ID, "name": o.Name})
}
func (s *Server) portalFinanceSummary(w http.ResponseWriter, r *http.Request) {
	c, ok := s.authorizeScope(w, r, "*")
	if !ok {
		return
	}
	id := r.PathValue("tenantID")
	if !s.platformAllows(r, c, id, "finance") {
		writeStorageError(w, storage.ErrUnauthorized)
		return
	}
	docs, err := s.store.ListTenantBills(r.Context(), id, 500)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	totals := map[string]int64{}
	for _, d := range docs {
		env := "live"
		if d.Invoice.TestMode {
			env = "test"
		}
		totals[d.Invoice.Currency+"/"+env+"/"+d.Invoice.Status] += d.Invoice.AmountMinor
	}
	writeJSON(w, 200, map[string]any{"tenant_id": id, "count": len(docs), "limit": 500, "summary_minor": totals, "payment_access": false, "withdrawal_access": false})
}
func (s *Server) portalGrant(w http.ResponseWriter, r *http.Request) {
	c, ok := s.authorizeScope(w, r, "*")
	if !ok {
		return
	}
	if c.APIKeyID != "" {
		writeStorageError(w, storage.ErrUnauthorized)
		return
	}
	var in struct {
		AccountID string `json:"account_id"`
		Role      string `json:"role"`
		TenantID  string `json:"tenant_id"`
		ParentID  string `json:"parent_id"`
	}
	if !s.decodeJSON(w, r, &in) {
		return
	}
	if !tenancy.ValidRole(in.Role) || in.TenantID == "" {
		writeError(w, 400, "invalid_grant", "Valid role and explicit tenant scope required")
		return
	}
	if _, err := s.store.GetAccount(r.Context(), in.AccountID); err != nil {
		writeStorageError(w, err)
		return
	}
	orgs, err := s.store.AllOrganizations(r.Context())
	if err != nil {
		writeStorageError(w, err)
		return
	}
	if in.TenantID != "*" {
		if _, err = s.store.GetOrganization(r.Context(), in.TenantID); err != nil {
			writeStorageError(w, err)
			return
		}
	}
	id, _ := protocol.NewID("grant")
	v := tenancy.Grant{ID: id, AccountID: in.AccountID, Role: in.Role, TenantID: in.TenantID, ParentID: in.ParentID, GrantedBy: c.AccountID, CreatedAt: time.Now().UTC()}
	if !s.mutateControl(w, r, c, "platform.grant.created", in.TenantID, id, func(ctrl *tenancy.Control) error {
		if s.superAdmin(c) {
			v.ParentID = ""
		} else {
			valid := false
			for _, p := range ctrl.Grants {
				if p.ID == in.ParentID && p.AccountID == c.AccountID && p.Role == "admin" && ctrl.ActiveGrant(p.ID) && tenancy.Contains(orgs, p.TenantID, in.TenantID) {
					valid = true
				}
			}
			if !valid {
				return storage.ErrUnauthorized
			}
		}
		trial := *ctrl
		trial.Grants = append(append([]tenancy.Grant{}, ctrl.Grants...), v)
		if !trial.ActiveGrant(v.ID) {
			return storage.ErrUnauthorized
		}
		ctrl.Grants = trial.Grants
		return nil
	}) {
		return
	}
	writeJSON(w, 201, v)
}
func (s *Server) portalRevokeGrant(w http.ResponseWriter, r *http.Request) {
	c, ok := s.authorizeScope(w, r, "*")
	if !ok {
		return
	}
	if c.APIKeyID != "" {
		writeStorageError(w, storage.ErrUnauthorized)
		return
	}
	orgs, err := s.store.AllOrganizations(r.Context())
	if err != nil {
		writeStorageError(w, err)
		return
	}
	id := r.PathValue("grantID")
	if !s.mutateControl(w, r, c, "platform.grant.revoked", "", id, func(ctrl *tenancy.Control) error {
		for i, g := range ctrl.Grants {
			if g.ID != id {
				continue
			}
			if !s.superAdmin(c) && (g.GrantedBy != c.AccountID || !ctrl.Allows(orgs, c.AccountID, g.TenantID, "delegate")) {
				return storage.ErrUnauthorized
			}
			ctrl.Grants[i].Revoked = true
			return nil
		}
		return storage.ErrNotFound
	}) {
		return
	}
	writeJSON(w, 200, map[string]string{"status": "revoked", "descendants": "immediately ineffective"})
}
func (s *Server) portalMandate(w http.ResponseWriter, r *http.Request) {
	c, ok := s.authorizeScope(w, r, "billing:write")
	if !ok {
		return
	}
	var in struct {
		TenantID      string `json:"tenant_id"`
		PayerTenantID string `json:"payer_tenant_id"`
	}
	if !s.decodeJSON(w, r, &in) {
		return
	}
	if s.tenantRole(r, c, in.TenantID) != "owner" {
		writeStorageError(w, storage.ErrUnauthorized)
		return
	}
	orgs, err := s.store.AllOrganizations(r.Context())
	if err != nil {
		writeStorageError(w, err)
		return
	}
	if in.TenantID == in.PayerTenantID || !tenancy.Contains(orgs, in.PayerTenantID, in.TenantID) {
		writeError(w, 400, "invalid_payer", "集团代付方必须是上级租户 / Payer must be an ancestor tenant")
		return
	}
	id, _ := protocol.NewID("mandate")
	m := tenancy.Mandate{ID: id, TenantID: in.TenantID, PayerTenantID: in.PayerTenantID, Status: "pending", RequestedBy: c.AccountID, CreatedAt: time.Now().UTC()}
	if !s.mutateControl(w, r, c, "mandate.requested", in.TenantID, id, func(v *tenancy.Control) error {
		for _, old := range v.Mandates {
			if old.TenantID == in.TenantID && (old.Status == "active" || old.Status == "pending") {
				return storage.ErrAlreadyExists
			}
		}
		v.Mandates = append(v.Mandates, m)
		return nil
	}) {
		return
	}
	writeJSON(w, 201, m)
}
func (s *Server) portalMandateAction(w http.ResponseWriter, r *http.Request) {
	c, ok := s.authorizeScope(w, r, "billing:write")
	if !ok {
		return
	}
	id := r.PathValue("mandateID")
	action := r.PathValue("action")
	if action != "accept" && action != "revoke" {
		writeError(w, 400, "invalid_action", "accept or revoke required")
		return
	}
	if !s.mutateControl(w, r, c, "mandate."+action, "", id, func(v *tenancy.Control) error {
		for i, m := range v.Mandates {
			if m.ID != id {
				continue
			}
			payerOwner := s.tenantRole(r, c, m.PayerTenantID) == "owner"
			tenantOwner := s.tenantRole(r, c, m.TenantID) == "owner"
			if action == "accept" {
				if !payerOwner {
					return storage.ErrUnauthorized
				}
				if m.Status != "pending" {
					return storage.ErrAlreadyExists
				}
				v.Mandates[i].Status = "active"
				v.Mandates[i].AcceptedBy = c.AccountID
			} else {
				if !payerOwner && !tenantOwner {
					return storage.ErrUnauthorized
				}
				v.Mandates[i].Status = "revoked"
			}
			return nil
		}
		return storage.ErrNotFound
	}) {
		return
	}
	writeJSON(w, 200, map[string]string{"status": action, "historical_invoices": "unchanged"})
}
