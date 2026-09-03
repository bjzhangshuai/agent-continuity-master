package memory

import (
	"context"
	"encoding/json"
	"github.com/agent-continuity/agent-continuity-master/internal/billing"
	"github.com/agent-continuity/agent-continuity-master/internal/storage"
	"github.com/agent-continuity/agent-continuity-master/internal/tenancy"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
	"sort"
	"time"
)

func cloneControl(v tenancy.Control) tenancy.Control {
	raw, _ := json.Marshal(v)
	var out tenancy.Control
	_ = json.Unmarshal(raw, &out)
	return out
}
func (s *Store) ProvisionPortalUser(_ context.Context, a *protocol.Account, l tenancy.Login, m *protocol.OrganizationMember) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, old := range s.portalLogins {
		if old.Username == l.Username || old.AccountID == l.AccountID {
			return storage.ErrAlreadyExists
		}
	}
	if a == nil {
		if _, ok := s.accounts[l.AccountID]; !ok {
			return storage.ErrNotFound
		}
	} else if _, ok := s.accounts[a.ID]; ok {
		return storage.ErrAlreadyExists
	}
	if m != nil {
		if _, ok := s.organizations[m.OrganizationID]; !ok {
			return storage.ErrNotFound
		}
	}
	if a != nil {
		s.accounts[a.ID] = *a
	}
	s.portalLogins[l.Username] = l
	if m != nil {
		s.organizationMembers[organizationMemberKey(m.OrganizationID, m.AccountID)] = *m
	}
	return nil
}
func (s *Store) LoadTenancyControl(context.Context) (tenancy.Control, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneControl(s.tenancyControl), nil
}
func (s *Store) SaveTenancyControl(_ context.Context, v tenancy.Control, version int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tenancyControl.Version != version {
		return tenancy.ErrConflict
	}
	v.Version = version + 1
	s.tenancyControl = cloneControl(v)
	return nil
}
func (s *Store) AllOrganizations(context.Context) ([]protocol.Organization, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []protocol.Organization{}
	for _, o := range s.organizations {
		out = append(out, o)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
func (s *Store) CreatePortalLogin(_ context.Context, v tenancy.Login) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, l := range s.portalLogins {
		if l.Username == v.Username || l.AccountID == v.AccountID {
			return storage.ErrAlreadyExists
		}
	}
	s.portalLogins[v.Username] = v
	return nil
}
func (s *Store) GetPortalLogin(_ context.Context, id string) (tenancy.Login, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.portalLogins[id]
	if !ok {
		return v, storage.ErrNotFound
	}
	return v, nil
}
func (s *Store) CreatePortalSession(_ context.Context, v tenancy.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for k, p := range s.portalSessions {
		if now.After(p.ExpiresAt) || now.Sub(p.LastSeen) > 30*time.Minute {
			delete(s.portalSessions, k)
		}
	}
	s.portalSessions[v.Hash] = v
	return nil
}
func (s *Store) GetPortalSession(_ context.Context, id string) (tenancy.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.portalSessions[id]
	now := s.now()
	if !ok || !now.Before(v.ExpiresAt) || now.Sub(v.LastSeen) > 30*time.Minute {
		return tenancy.Session{}, storage.ErrUnauthorized
	}
	v.LastSeen = now
	s.portalSessions[id] = v
	return v, nil
}
func (s *Store) DeletePortalSession(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.portalSessions, id)
	return nil
}
func (s *Store) GetBillByID(_ context.Context, id string) (protocol.BillingDocument, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.billingDocuments[id]
	if !ok {
		return v, billing.ErrNotFound
	}
	return cloneBill(v), nil
}
func (s *Store) ListMerchantBills(_ context.Context, limit int) ([]protocol.BillingDocument, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	out := []protocol.BillingDocument{}
	for _, d := range s.billingDocuments {
		if d.Invoice.Status == "paid" && len(d.Receipts) > 0 {
			out = append(out, cloneBill(d))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Invoice.CreatedAt.After(out[j].Invoice.CreatedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (s *Store) ListTenantBills(_ context.Context, id string, limit int) ([]protocol.BillingDocument, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	out := []protocol.BillingDocument{}
	for _, d := range s.billingDocuments {
		if d.Invoice.OrganizationID == id || d.Invoice.PayerOrganizationID == id {
			raw, _ := json.Marshal(d)
			var v protocol.BillingDocument
			_ = json.Unmarshal(raw, &v)
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Invoice.CreatedAt.After(out[j].Invoice.CreatedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (s *Store) GetTenantBillingAccount(_ context.Context, id string) (protocol.BillingAccount, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var found protocol.BillingAccount
	for _, b := range s.billingAccounts {
		if b.OrganizationID == id && b.Status == "active" && (found.ID == "" || b.CreatedAt.Before(found.CreatedAt)) {
			found = b
		}
	}
	if found.ID == "" {
		return found, storage.ErrNotFound
	}
	return found, nil
}
