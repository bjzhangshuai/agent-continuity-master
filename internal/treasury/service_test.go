package treasury_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-continuity/agent-continuity-master/internal/storage/memory"
	"github.com/agent-continuity/agent-continuity-master/internal/treasury"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

func fixture(t *testing.T) (*treasury.Service, *memory.Store) {
	t.Helper()
	ctx := context.Background()
	repo := memory.New()
	_ = repo.CreateAccount(ctx, protocol.Account{ID: "operator", Name: "operator"})
	s := treasury.New(repo, "operator", true, nil)
	now := time.Now()
	_, err := repo.CreateBill(ctx, protocol.BillingDocument{Invoice: protocol.BillingInvoice{ID: "invoice", AccountID: "operator", Number: "DEMO-100", Status: "paid", TestMode: true, AmountMinor: 10000, Currency: "CNY", IdempotencyKey: "fixture", PeriodStart: now.Add(-time.Hour), PeriodEnd: now}, Payments: []protocol.BillingPayment{{ID: "payment", Provider: "demo", Environment: "simulation", Status: "succeeded", ProviderTransactionID: "demo_transaction"}}, Receipts: []protocol.PaymentReceipt{{ID: "receipt", PaymentID: "payment", InvoiceID: "invoice", Provider: "demo", Environment: "simulation", AmountMinor: 10000, Currency: "CNY", ProviderTransactionID: "demo_transaction", PaidAt: now}}})
	if err != nil {
		t.Fatal(err)
	}
	return s, repo
}
func must(t *testing.T, d protocol.TreasuryDocument, err error) protocol.TreasuryDocument {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	return d
}
func fund(t *testing.T, s *treasury.Service) string {
	t.Helper()
	ctx := context.Background()
	d, err := s.ImportDemo(ctx, "invoice", "operator")
	d = must(t, d, err)
	d, err = s.SettleDemo(ctx, d.Collections[0].ID, "operator")
	must(t, d, err)
	d, err = s.AddDemoDestination(ctx, "bank", "My bank (virtual)", "CNY", "operator")
	d = must(t, d, err)
	dest := d.Destinations[0].ID
	d, err = s.VerifyDemoDestination(ctx, dest, "operator")
	must(t, d, err)
	return dest
}
func TestCollectionSettlementPayoutReturn(t *testing.T) {
	s, _ := fixture(t)
	ctx := context.Background()
	d, err := s.ImportDemo(ctx, "invoice", "op")
	d = must(t, d, err)
	if b := treasury.Balances(d)[0]; b.PendingMinor != 9940 || b.AvailableMinor != 0 || b.FeeMinor != 60 {
		t.Fatalf("bad collection: %+v", b)
	}
	d, err = s.ImportDemo(ctx, "invoice", "op")
	d = must(t, d, err)
	if len(d.Collections) != 1 {
		t.Fatal("duplicate import")
	}
	dest := fund(t, s)
	input := treasury.Request{DestinationID: dest, Provider: "demo", Currency: "CNY", AmountMinor: 4000}
	d, err = s.RequestPayout(ctx, input, "request-0001", "requester")
	d = must(t, d, err)
	pid := d.Payouts[0].ID
	d, err = s.RequestPayout(ctx, input, "request-0001", "requester")
	d = must(t, d, err)
	if len(d.Payouts) != 1 || treasury.Balances(d)[0].ReservedMinor != 4000 {
		t.Fatal("duplicate reservation")
	}
	input.AmountMinor++
	if _, err = s.RequestPayout(ctx, input, "request-0001", "requester"); !errors.Is(err, treasury.ErrConflict) {
		t.Fatal("idempotency mismatch accepted")
	}
	if _, err = s.Submit(ctx, pid, "requester"); !errors.Is(err, treasury.ErrConflict) {
		t.Fatal("unapproved payout submitted")
	}
	d, err = s.Decide(ctx, pid, "approve", "approver")
	must(t, d, err)
	d, err = s.Submit(ctx, pid, "approver")
	must(t, d, err)
	d, err = s.Simulate(ctx, pid, "paid", "operator")
	d = must(t, d, err)
	if b := treasury.Balances(d)[0]; b.PaidMinor != 4000 || b.ReservedMinor != 0 || b.AvailableMinor != 5940 {
		t.Fatalf("bad paid balance %+v", b)
	}
	rid := d.Payouts[0].ReceiptID
	d, err = s.Simulate(ctx, pid, "paid", "operator")
	d = must(t, d, err)
	if d.Payouts[0].ReceiptID != rid {
		t.Fatal("duplicate receipt")
	}
	d, err = s.Simulate(ctx, pid, "returned", "operator")
	d = must(t, d, err)
	if b := treasury.Balances(d)[0]; b.AvailableMinor != 9940 || b.PaidMinor != 0 || b.ReturnedMinor != 4000 {
		t.Fatalf("bad return %+v", b)
	}
	if d.Payouts[0].ReceiptID != rid {
		t.Fatal("history destroyed")
	}
	if _, err = s.Simulate(ctx, pid, "paid", "op"); !errors.Is(err, treasury.ErrConflict) {
		t.Fatal("returned overwritten")
	}
}

func TestPayoutDisableAndLiveApprovalGates(t *testing.T) {
	s, repo := fixture(t)
	ctx := context.Background()
	dest := fund(t, s)
	in := treasury.Request{DestinationID: dest, AmountMinor: 100, Currency: "CNY", Provider: "demo"}
	d, err := s.RequestPayout(ctx, in, "safety-request-1", "requester")
	d = must(t, d, err)
	pid := d.Payouts[0].ID
	// Existing records remain readable/idempotent when an operator disables a route.
	s.Demo = false
	d, err = s.RequestPayout(ctx, in, "safety-request-1", "requester")
	d = must(t, d, err)
	d, err = s.Decide(ctx, pid, "approve", "approver")
	d = must(t, d, err)
	if _, err = s.Submit(ctx, pid, "approver"); !errors.Is(err, treasury.ErrUnavailable) {
		t.Fatal("disabled demo route submitted", err)
	}
	// Fixture a live pending request without calling any financial network.
	d.Payouts[0].Status = "requested"
	d.Payouts[0].Provider = "stripe"
	d.Payouts[0].Environment = "live"
	d.Payouts[0].ApprovedBy = ""
	if err = repo.SaveTreasury(ctx, d, d.Version); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Decide(ctx, pid, "approve", "requester"); !errors.Is(err, treasury.ErrInvalid) {
		t.Fatal("live self approval accepted", err)
	}
	d, err = s.Decide(ctx, pid, "approve", "another-human")
	must(t, d, err)
	if _, err = s.Submit(ctx, pid, "another-human"); !errors.Is(err, treasury.ErrUnavailable) {
		t.Fatal("unconfigured live route submitted", err)
	}
}
func TestPayoutGuardsAndConcurrentReservation(t *testing.T) {
	s, _ := fixture(t)
	ctx := context.Background()
	dest := fund(t, s)
	for _, amount := range []int64{-1, 0, 9941, treasury.MaxAmountMinor + 1} {
		_, err := s.RequestPayout(ctx, treasury.Request{DestinationID: dest, Provider: "demo", Currency: "CNY", AmountMinor: amount}, fmt.Sprintf("bad-amount-%d", amount), "op")
		if err == nil {
			t.Fatalf("invalid amount %d", amount)
		}
	}
	for _, in := range []treasury.Request{{dest, 1, "USD", "demo"}, {"unknown", 1, "CNY", "demo"}, {dest, 1, "CNY", "stripe"}} {
		if _, err := s.RequestPayout(ctx, in, "guard-0000001", "op"); err == nil {
			t.Fatal("destination/currency/provider guard")
		}
	}
	var wg sync.WaitGroup
	var accepted atomic.Int64
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := s.RequestPayout(ctx, treasury.Request{dest, 1000, "CNY", "demo"}, fmt.Sprintf("parallel-%04d", i), "op")
			if err == nil {
				accepted.Add(1)
			} else if !errors.Is(err, treasury.ErrInsufficient) && !errors.Is(err, treasury.ErrConflict) {
				t.Errorf("unexpected: %v", err)
			}
		}(i)
	}
	wg.Wait()
	d, err := s.Load(ctx)
	d = must(t, d, err)
	b := treasury.Balances(d)[0]
	if accepted.Load() > 9 || b.AvailableMinor < 0 || b.AvailableMinor+b.ReservedMinor != 9940 {
		t.Fatalf("overdraw %+v", b)
	}
}
func TestFailureRejectionAndUnverified(t *testing.T) {
	s, _ := fixture(t)
	ctx := context.Background()
	d, err := s.AddDemoDestination(ctx, "alipay", "My wallet", "CNY", "op")
	d = must(t, d, err)
	if _, err = s.RequestPayout(ctx, treasury.Request{d.Destinations[0].ID, 1, "CNY", "demo"}, "unverified-1", "op"); err == nil {
		t.Fatal("unverified destination")
	}
	dest := fund(t, s)
	for i, outcome := range []string{"reject", "cancel", "failed"} {
		d, err = s.RequestPayout(ctx, treasury.Request{dest, 100, "CNY", "demo"}, fmt.Sprintf("failure-%04d", i), "op")
		d = must(t, d, err)
		pid := d.Payouts[len(d.Payouts)-1].ID
		if outcome == "failed" {
			d, err = s.Decide(ctx, pid, "approve", "op")
			must(t, d, err)
			d, err = s.Submit(ctx, pid, "op")
			must(t, d, err)
			d, err = s.Simulate(ctx, pid, "failed", "op")
		} else {
			d, err = s.Decide(ctx, pid, outcome, "op")
		}
		d = must(t, d, err)
		if treasury.Balances(d)[0].AvailableMinor != 9940 {
			t.Fatal("reservation leaked")
		}
	}
}
func TestImportRejectsFakeOrLiveAndCAS(t *testing.T) {
	s, repo := fixture(t)
	ctx := context.Background()
	s.Demo = false
	if _, err := s.ImportDemo(ctx, "invoice", "op"); !errors.Is(err, treasury.ErrUnavailable) {
		t.Fatal("disabled simulation")
	}
	s.Demo = true
	d, err := repo.GetBill(ctx, "operator", "invoice")
	d = mustBill(t, d, err)
	d.Receipts[0].Environment = "live"
	if err = repo.SaveBill(ctx, d, d.Version); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ImportDemo(ctx, "invoice", "op"); !errors.Is(err, treasury.ErrInvalid) {
		t.Fatal("live receipt imported as demo")
	}
	doc, err := s.Load(ctx)
	doc = must(t, doc, err)
	if err = repo.SaveTreasury(ctx, doc, 0); err != nil {
		t.Fatal(err)
	}
	if err = repo.SaveTreasury(ctx, doc, 0); !errors.Is(err, treasury.ErrConflict) {
		t.Fatal("CAS missing")
	}
	if _, err = repo.LoadTreasury(ctx, "other"); !errors.Is(err, treasury.ErrNotFound) {
		t.Fatal("cross-owner leak")
	}
}
func mustBill(t *testing.T, d protocol.BillingDocument, e error) protocol.BillingDocument {
	t.Helper()
	if e != nil {
		t.Fatal(e)
	}
	return d
}
