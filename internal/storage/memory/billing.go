package memory

import (
	"context"
	"encoding/json"
	"github.com/agent-continuity/agent-continuity-master/internal/billing"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
	"sort"
	"time"
)

func cloneBill(d protocol.BillingDocument) protocol.BillingDocument {
	b, _ := json.Marshal(d)
	var out protocol.BillingDocument
	_ = json.Unmarshal(b, &out)
	return out
}
func (s *Store) SummarizeUsage(_ context.Context, account, org string, from, to time.Time) ([]protocol.UsageTotal, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	totals := map[string]protocol.UsageTotal{}
	for _, v := range s.usageRecords {
		if (org != "" || v.AccountID == account) && v.OrganizationID == org && !v.RecordedAt.Before(from) && v.RecordedAt.Before(to) {
			k := v.Metric + "\x00" + v.Unit
			t := totals[k]
			t.Metric = v.Metric
			t.Unit = v.Unit
			t.Quantity += v.Quantity
			t.Records++
			totals[k] = t
		}
	}
	out := []protocol.UsageTotal{}
	for _, v := range totals {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Metric < out[j].Metric })
	return out, nil
}
func (s *Store) CreateBill(_ context.Context, d protocol.BillingDocument) (protocol.BillingDocument, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, old := range s.billingDocuments {
		if old.Invoice.AccountID == d.Invoice.AccountID && old.Invoice.IdempotencyKey == d.Invoice.IdempotencyKey {
			if !billing.SameRequest(old.Invoice, d.Invoice) {
				return d, billing.ErrConflict
			}
			return cloneBill(old), nil
		}
	}
	for _, old := range s.billingDocuments {
		if billing.Overlaps(old.Invoice, d.Invoice) {
			return d, billing.ErrOverlap
		}
	}
	if d.Invoice.PaymentMandateID != "" {
		valid := false
		for _, m := range s.tenancyControl.Mandates {
			if m.ID == d.Invoice.PaymentMandateID && m.Status == "active" && m.TenantID == d.Invoice.OrganizationID && m.PayerTenantID == d.Invoice.PayerOrganizationID {
				valid = true
			}
		}
		if !valid {
			return d, billing.ErrConflict
		}
	}
	d.Version = 1
	s.billingDocuments[d.Invoice.ID] = cloneBill(d)
	return cloneBill(d), nil
}
func (s *Store) SaveBill(_ context.Context, d protocol.BillingDocument, version int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.billingDocuments[d.Invoice.ID]
	if !ok {
		return billing.ErrNotFound
	}
	if old.Version != version {
		return billing.ErrConflict
	}
	for id, other := range s.billingDocuments {
		if id == d.Invoice.ID {
			continue
		}
		for _, p := range d.Payments {
			for _, q := range other.Payments {
				if p.Provider == q.Provider && (p.ProviderTransactionID != "" && p.ProviderTransactionID == q.ProviderTransactionID || p.ProviderOrderID != "" && p.ProviderOrderID == q.ProviderOrderID) {
					return billing.ErrConflict
				}
			}
		}
	}
	d.Version = version + 1
	s.billingDocuments[d.Invoice.ID] = cloneBill(d)
	return nil
}
func (s *Store) GetBill(_ context.Context, account, id string) (protocol.BillingDocument, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.billingDocuments[id]
	if !ok || d.Invoice.AccountID != account {
		return d, billing.ErrNotFound
	}
	return cloneBill(d), nil
}
func (s *Store) ListBills(_ context.Context, account string, limit int) ([]protocol.BillingDocument, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []protocol.BillingDocument{}
	for _, d := range s.billingDocuments {
		if d.Invoice.AccountID == account {
			out = append(out, cloneBill(d))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Invoice.CreatedAt.After(out[j].Invoice.CreatedAt) })
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (s *Store) FindPayment(_ context.Context, provider, ref string) (protocol.BillingDocument, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, d := range s.billingDocuments {
		for _, p := range d.Payments {
			if p.Provider == provider && (p.ID == ref || p.ProviderOrderID == ref) {
				return cloneBill(d), nil
			}
		}
	}
	return protocol.BillingDocument{}, billing.ErrNotFound
}

func (s *Store) DuePayments(_ context.Context, before time.Time, limit int) ([]protocol.PaymentReconcileTarget, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > 20 {
		limit = 10
	}
	out := []protocol.PaymentReconcileTarget{}
	for _, d := range s.billingDocuments {
		for _, p := range d.Payments {
			if p.Environment != "simulation" && (p.Status == "creating" || p.Status == "pending" || p.Status == "approved") && p.ReconcileAttempts < 20 && (p.LastCheckedAt == nil || p.LastCheckedAt.Before(before)) {
				out = append(out, protocol.PaymentReconcileTarget{AccountID: d.Invoice.AccountID, InvoiceID: d.Invoice.ID, PaymentID: p.ID})
				if len(out) >= limit {
					return out, nil
				}
			}
		}
	}
	return out, nil
}

func (s *Store) DueRefunds(_ context.Context, before time.Time, limit int) ([]protocol.PaymentReconcileTarget, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > 20 {
		limit = 10
	}
	out := []protocol.PaymentReconcileTarget{}
	for _, d := range s.billingDocuments {
		for _, r := range d.Refunds {
			demo := false
			for _, p := range d.Payments {
				if p.ID == r.PaymentID {
					demo = p.Provider == "demo"
				}
			}
			if !demo && (r.Status == "pending" || r.Status == "submitting" || r.Status == "review") && r.CheckAttempts < 20 && (r.LastCheckedAt == nil || r.LastCheckedAt.Before(before)) {
				out = append(out, protocol.PaymentReconcileTarget{AccountID: d.Invoice.AccountID, InvoiceID: d.Invoice.ID, PaymentID: r.ID})
				if len(out) >= limit {
					return out, nil
				}
			}
		}
	}
	return out, nil
}
