package memory

import (
	"context"
	"encoding/json"
	"github.com/agent-continuity/agent-continuity-master/internal/treasury"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

func cloneTreasury(d protocol.TreasuryDocument) protocol.TreasuryDocument {
	b, _ := json.Marshal(d)
	var out protocol.TreasuryDocument
	_ = json.Unmarshal(b, &out)
	return out
}
func (s *Store) LoadTreasury(_ context.Context, owner string) (protocol.TreasuryDocument, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.treasuryDocuments[owner]
	if !ok {
		return d, treasury.ErrNotFound
	}
	return cloneTreasury(d), nil
}
func (s *Store) SaveTreasury(_ context.Context, d protocol.TreasuryDocument, version int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.accounts[d.OwnerAccountID]; !ok {
		return treasury.ErrNotFound
	}
	old, ok := s.treasuryDocuments[d.OwnerAccountID]
	if ok && old.Version != version || !ok && version != 0 {
		return treasury.ErrConflict
	}
	d.Version = version + 1
	for _, c := range d.Collections {
		bill, ok := s.billingDocuments[c.InvoiceID]
		if !ok || bill.Version != c.BillingVersion {
			return treasury.ErrConflict
		}
	}
	s.treasuryDocuments[d.OwnerAccountID] = cloneTreasury(d)
	return nil
}
