package treasury_test

import (
	"context"
	"errors"
	"github.com/agent-continuity/agent-continuity-master/internal/treasury"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
	"testing"
)

func TestRefundReservationReducesMerchantFundsAndGuardsStalePayout(t *testing.T) {
	s, repo := fixture(t)
	ctx := context.Background()
	dest := fund(t, s)
	stale, err := s.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	bill, err := repo.GetBillByID(ctx, "invoice")
	if err != nil {
		t.Fatal(err)
	}
	bill.Refunds = []protocol.BillingRefund{{ID: "rf", PaymentID: "payment", AmountMinor: 5000, Currency: "CNY", Status: "requested"}}
	if err = repo.SaveBill(ctx, bill, bill.Version); err != nil {
		t.Fatal(err)
	}
	if err = repo.SaveTreasury(ctx, stale, stale.Version); !errors.Is(err, treasury.ErrConflict) {
		t.Fatal("stale funds committed", err)
	}
	fresh, err := s.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	balance := treasury.Balances(fresh)[0]
	if balance.AvailableMinor != 4940 || balance.RefundHeldMinor != 5000 {
		t.Fatal(balance)
	}
	if _, err = s.RequestPayout(ctx, treasury.Request{DestinationID: dest, Provider: "demo", Currency: "CNY", AmountMinor: 5000}, "over-refund-balance", "operator"); !errors.Is(err, treasury.ErrInsufficient) {
		t.Fatal("refunded money available", err)
	}
	bill, _ = repo.GetBillByID(ctx, "invoice")
	bill.Refunds[0].Status = "succeeded"
	if err = repo.SaveBill(ctx, bill, bill.Version); err != nil {
		t.Fatal(err)
	}
	fresh, err = s.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	balance = treasury.Balances(fresh)[0]
	if balance.AvailableMinor != 4940 || balance.RefundHeldMinor != 0 || balance.RefundedMinor != 5000 {
		t.Fatal(balance)
	}
}
