package billing

import (
	"context"
	"github.com/agent-continuity/agent-continuity-master/internal/operations"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
	"slices"
	"time"
)

// IssuePurchase accepts only the server's immutable marketplace transaction.
// A purchase is not a usage period, so it never consumes a usage billing window.
func (s *Service) IssuePurchase(ctx context.Context, t protocol.MarketplaceTransaction, b protocol.BillingAccount) (protocol.BillingDocument, error) {
	if b.AccountID != t.BuyerAccountID || b.Status != "active" || b.Currency != t.Currency || t.ID == "" || t.BuyerAccountID == "" || t.AmountMinor <= 0 || t.AmountMinor > MaxAmountMinor || !slices.Contains([]string{"CNY", "USD", "EUR"}, t.Currency) {
		return protocol.BillingDocument{}, ErrInvalid
	}
	now := s.now().UTC()
	id := operations.ID("inv", t.BuyerAccountID, "marketplace:"+t.ID)
	i := protocol.BillingInvoice{ID: id, AccountID: t.BuyerAccountID, BillingAccountID: b.ID, OrganizationID: b.OrganizationID, PayerOrganizationID: b.PayerOrganizationID, PaymentMandateID: b.PaymentMandateID, Number: "AC-MARKET-" + id, Status: "open", Currency: t.Currency, AmountMinor: t.AmountMinor, SourceKind: "marketplace", SourceID: t.ID, TestMode: s.PriceBook.TestMode, PriceBook: "marketplace-fixed-v1", CreatedAt: now, DueAt: now.Add(24 * time.Hour), PeriodStart: t.CreatedAt, PeriodEnd: t.CreatedAt.Add(time.Microsecond), IdempotencyKey: "marketplace:" + t.ID, Lines: []protocol.BillingLine{{BillingRate: protocol.BillingRate{Metric: "marketplace.purchase", Description: t.ListingID, Unit: "order", UnitSize: 1, UnitPriceMinor: t.AmountMinor}, Quantity: 1, Records: 1, AmountMinor: t.AmountMinor}}}
	return s.Repo.CreateBill(ctx, protocol.BillingDocument{Invoice: i, Payments: []protocol.BillingPayment{}, Receipts: []protocol.PaymentReceipt{}, Events: []protocol.BillingEvent{}})
}

// PurchasePaid excludes disputed/refunded purchases from new paid installations.
func PurchasePaid(d protocol.BillingDocument, transaction string) bool {
	if d.Invoice.SourceKind != "marketplace" || d.Invoice.SourceID != transaction {
		return false
	}
	for _, r := range d.Refunds {
		if r.Status != "failed" && r.Status != "rejected" {
			return false
		}
	}
	for _, r := range d.Receipts {
		if r.AmountMinor == d.Invoice.AmountMinor && r.Currency == d.Invoice.Currency && r.ProviderTransactionID != "" {
			for _, p := range d.Payments {
				if p.ID == r.PaymentID && p.Status == "succeeded" {
					return true
				}
			}
		}
	}
	return false
}
