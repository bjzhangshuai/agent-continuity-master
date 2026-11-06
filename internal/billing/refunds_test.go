package billing_test

import (
	"context"
	"errors"
	"fmt"
	"github.com/agent-continuity/agent-continuity-master/internal/billing"
	"github.com/agent-continuity/agent-continuity-master/internal/payments"
	"sync"
	"testing"
)

type lostRefundReply struct {
	payments.Provider
	creates, queries int
}

func (p *lostRefundReply) Refund(_ context.Context, _ payments.RefundOrder) (payments.RefundResult, error) {
	p.creates++
	return payments.RefundResult{}, context.DeadlineExceeded
}
func (p *lostRefundReply) QueryRefund(_ context.Context, o payments.RefundOrder) (payments.RefundResult, error) {
	p.queries++
	return payments.RefundResult{RefundID: o.RefundID, ProviderRefundID: "recovered_" + o.RefundID, Status: "succeeded", Currency: o.Currency, AmountMinor: o.AmountMinor}, nil
}

func TestRefundUnknownOutcomeKeepsHoldAndRecoversOriginalOrder(t *testing.T) {
	s, a, from, to := fixture(t)
	ctx := billing.WithActor(context.Background(), "payer")
	merchant := billing.WithActor(ctx, "merchant")
	d, err := s.Issue(ctx, a, from, to, "recover-invoice")
	if err != nil {
		t.Fatal(err)
	}
	d, pid, err := s.CreatePayment(ctx, a.AccountID, d.Invoice.ID, "demo", "recover-payment")
	if err != nil {
		t.Fatal(err)
	}
	d, err = s.Simulate(ctx, a.AccountID, d.Invoice.ID, pid, "succeeded")
	if err != nil {
		t.Fatal(err)
	}
	originalReceipt := d.Receipts[0]
	provider, _ := s.Providers.Get("demo")
	lost := &lostRefundReply{Provider: provider}
	s.Providers = payments.NewRegistry(lost)
	d, err = s.RequestRefund(ctx, a.AccountID, d.Invoice.ID, pid, 1105, "all", "recover-refund")
	if err != nil {
		t.Fatal(err)
	}
	rid := d.Refunds[0].ID
	d, err = s.DecideRefund(merchant, a.AccountID, d.Invoice.ID, rid, "rejected", "verify release")
	if err != nil {
		t.Fatal(err)
	}
	if billing.RefundAvailable(d, pid) != 1105 {
		t.Fatal("rejection failed to release hold")
	}
	d, err = s.RequestRefund(ctx, a.AccountID, d.Invoice.ID, pid, 1105, "all", "recover-refund-2")
	if err != nil {
		t.Fatal(err)
	}
	rid = d.Refunds[1].ID
	d, err = s.DecideRefund(merchant, a.AccountID, d.Invoice.ID, rid, "approved", "verified")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.SubmitRefund(merchant, a.AccountID, d.Invoice.ID, rid)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	d, _ = s.Repo.GetBill(ctx, a.AccountID, d.Invoice.ID)
	if d.Refunds[1].Status != "submitting" || billing.RefundAvailable(d, pid) != 0 || len(d.CreditNotes) != 0 {
		t.Fatal("unknown outcome released funds or credited refund")
	}
	d, err = s.SubmitRefund(merchant, a.AccountID, d.Invoice.ID, rid)
	if err != nil || lost.creates != 1 || lost.queries != 1 || len(d.CreditNotes) != 1 || d.Invoice.Status != "refunded" {
		t.Fatal("original refund not recovered", err, lost, d.Refunds)
	}
	_, err = s.ApplyRefund(ctx, a.AccountID, d.Invoice.ID, rid, payments.RefundResult{RefundID: rid, ProviderRefundID: "recovered_" + rid, Status: "pending", Currency: d.Invoice.Currency, AmountMinor: 1105})
	if err != nil {
		t.Fatal(err)
	}
	d, _ = s.Repo.GetBill(ctx, a.AccountID, d.Invoice.ID)
	if d.Refunds[1].Status != "succeeded" || len(d.CreditNotes) != 1 {
		t.Fatal("late state regressed success")
	}
	if len(d.Receipts) != 1 || d.Receipts[0] != originalReceipt {
		t.Fatal("recovery changed original receipt")
	}
}

func TestRefundPartialFullConcurrentAndImmutableEvidence(t *testing.T) {
	s, a, from, to := fixture(t)
	ctx := billing.WithActor(context.Background(), "customer")
	merchant := billing.WithActor(ctx, "merchant")
	d, err := s.Issue(ctx, a, from, to, "refund-invoice")
	if err != nil {
		t.Fatal(err)
	}
	d, pid, err := s.CreatePayment(ctx, a.AccountID, d.Invoice.ID, "demo", "refund-payment")
	if err != nil {
		t.Fatal(err)
	}
	d, err = s.Simulate(ctx, a.AccountID, d.Invoice.ID, pid, "succeeded")
	if err != nil {
		t.Fatal(err)
	}
	invoice := d.Invoice.ID
	receipt := d.Receipts[0]
	d, err = s.RequestRefund(ctx, a.AccountID, invoice, pid, 400, "partial refund", "refund-key-1")
	if err != nil {
		t.Fatal(err)
	}
	rid := d.Refunds[0].ID
	d, err = s.RequestRefund(ctx, a.AccountID, invoice, pid, 400, "partial refund", "refund-key-1")
	if err != nil || len(d.Refunds) != 1 {
		t.Fatal("idempotency", err)
	}
	if _, err = s.RequestRefund(ctx, a.AccountID, invoice, pid, 401, "partial refund", "refund-key-1"); !errors.Is(err, billing.ErrConflict) {
		t.Fatal("changed key accepted", err)
	}
	if _, err = s.SubmitRefund(ctx, a.AccountID, invoice, rid); !errors.Is(err, billing.ErrConflict) {
		t.Fatal("unapproved submitted", err)
	}
	if _, err = s.DecideRefund(merchant, a.AccountID, invoice, rid, "approved", "approved test"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SubmitRefund(merchant, a.AccountID, invoice, rid); err != nil {
		t.Fatal(err)
	}
	wrong := payments.RefundResult{RefundID: rid, ProviderRefundID: "demo_" + rid, Status: "succeeded", Currency: "CNY", AmountMinor: 399}
	if _, err = s.ApplyRefund(ctx, a.AccountID, invoice, rid, wrong); err == nil {
		t.Fatal("wrong amount accepted")
	}
	var wg sync.WaitGroup
	fail := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := s.SimulateRefund(merchant, a.AccountID, invoice, rid, "succeeded")
			fail <- e
		}()
	}
	wg.Wait()
	close(fail)
	for e := range fail {
		if e != nil {
			t.Fatal(e)
		}
	}
	d, _ = s.Repo.GetBill(ctx, a.AccountID, invoice)
	if len(d.CreditNotes) != 1 || d.Invoice.Status != "paid" || d.Receipts[0] != receipt {
		t.Fatal("partial refund evidence incorrect", d)
	}
	// Only one competing request can reserve the remaining 705 minor units.
	successes := make(chan string, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			v, e := s.RequestRefund(ctx, a.AccountID, invoice, pid, 705, "remaining", fmt.Sprintf("remaining-key-%d", i))
			if e == nil {
				successes <- v.Refunds[len(v.Refunds)-1].ID
			}
		}(i)
	}
	wg.Wait()
	close(successes)
	if len(successes) != 1 {
		t.Fatalf("concurrent over-refund: %d", len(successes))
	}
	last := <-successes
	if _, err = s.DecideRefund(merchant, a.AccountID, invoice, last, "approved", "complete refund"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SubmitRefund(merchant, a.AccountID, invoice, last); err != nil {
		t.Fatal(err)
	}
	d, err = s.SimulateRefund(merchant, a.AccountID, invoice, last, "succeeded")
	if err != nil || d.Invoice.Status != "refunded" || len(d.CreditNotes) != 2 || d.Invoice.AmountMinor != 1105 || d.Receipts[0] != receipt {
		t.Fatal("full refund", err, d)
	}
	if _, err = s.RequestRefund(ctx, a.AccountID, invoice, pid, 1, "over refund", "over-refund-key"); err == nil {
		t.Fatal("over-refund")
	}
	if _, err = s.ReconcileRefund(ctx, "intruder", invoice, rid); !errors.Is(err, billing.ErrNotFound) {
		t.Fatal("cross-account read", err)
	}
}
