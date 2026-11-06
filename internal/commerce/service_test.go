package commerce_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/agent-continuity/agent-continuity-master/internal/billing"
	"github.com/agent-continuity/agent-continuity-master/internal/commerce"
	"github.com/agent-continuity/agent-continuity-master/internal/operations"
	"github.com/agent-continuity/agent-continuity-master/internal/payments"
	"github.com/agent-continuity/agent-continuity-master/internal/storage/memory"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
	"sync"
	"testing"
	"time"
)

func fixture(t *testing.T) (*commerce.Service, *billing.Service, protocol.MarketplaceTransaction, protocol.BillingDocument) {
	t.Helper()
	repo := memory.New()
	b := billing.New(repo, payments.NewRegistry(payments.Demo{}), billing.DemoPriceBook(), "", make([]byte, 32))
	tx := protocol.MarketplaceTransaction{ID: "tx", BuyerAccountID: "buyer", SellerAccountID: "seller", Status: "authorized", AmountMinor: 10000, CommissionMinor: 1000, Currency: "CNY", CreatedAt: time.Now()}
	account := protocol.BillingAccount{ID: "billing", AccountID: "buyer", Status: "active", Currency: "CNY"}
	d, e := b.IssuePurchase(context.Background(), tx, account)
	if e != nil {
		t.Fatal(e)
	}
	d, pid, e := b.CreatePayment(context.Background(), "buyer", d.Invoice.ID, "demo", "pay-key-000")
	if e != nil {
		t.Fatal(e)
	}
	d, e = b.Simulate(context.Background(), "buyer", d.Invoice.ID, pid, "succeeded")
	if e != nil {
		t.Fatal(e)
	}
	s := &commerce.Service{Repo: repo, Gateway: commerce.DemoGateway{}, Simulation: true}
	return s, b, tx, d
}
func TestCollectionSplitWithdrawalRefundAndFiscalClosure(t *testing.T) {
	s, b, tx, d := fixture(t)
	ctx := billing.WithActor(context.Background(), "buyer")
	v, e := s.CreateSettlement(ctx, tx, d)
	if e != nil {
		t.Fatal(e)
	}
	if v.Available != 0 {
		t.Fatal("unsettled funds available")
	}
	v, e = s.Reconcile(ctx, "seller", tx.ID)
	if e != nil || v.Available != 9000 || v.PlatformEntitlement != 1000 {
		t.Fatalf("split %+v %v", v, e)
	}
	_, e = s.Recipient(ctx, "seller", "sim-bank-token", "bank")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Recipient(ctx, "seller", "sim-bank-token", "paypal"); !errors.Is(e, operations.ErrConflict) {
		t.Fatal("recipient token repurposed")
	}
	if _, e = s.Withdraw(ctx, "buyer", tx.ID, "payout-key", "sim-bank-token", 1); !errors.Is(e, operations.ErrNotFound) {
		t.Fatal("buyer withdrew seller funds")
	}
	fiscal, e := s.IssueFiscal(ctx, "buyer", d.Invoice.ID, commerce.FiscalRequest{BuyerName: "测试公司", TaxID: "91310000TEST"})
	if e != nil {
		t.Fatal(e)
	}
	fiscal, e = s.ReconcileFiscal(ctx, "buyer", fiscal.Effect.ID)
	if e != nil || fiscal.Result.Status != "succeeded" || !fiscal.Result.Simulated {
		t.Fatal("fiscal issue", e)
	}
	var wg sync.WaitGroup
	success := 0
	var mu sync.Mutex
	for _, key := range []string{"payout-one", "payout-two"} {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			_, err := s.Withdraw(ctx, "seller", tx.ID, key, "sim-bank-token", 6000)
			if err == nil {
				mu.Lock()
				success++
				mu.Unlock()
			}
		}(key)
	}
	wg.Wait()
	if success != 1 {
		t.Fatalf("concurrent overspend success=%d", success)
	}
	v, e = s.Reconcile(ctx, "seller", tx.ID)
	if e != nil || v.Available != 3000 {
		t.Fatal("withdrawal", v, e)
	}
	d, e = b.RequestRefund(ctx, "buyer", d.Invoice.ID, d.Payments[0].ID, 5000, "partial", "refund-key")
	if e != nil {
		t.Fatal(e)
	}
	v, e = s.Load(ctx, "seller", tx.ID)
	if e != nil || v.Available != 0 || v.Held != 5000 {
		t.Fatal("refund reservation", v, e)
	}
	merchant := billing.WithActor(ctx, "merchant")
	d, e = b.DecideRefund(merchant, "buyer", d.Invoice.ID, d.Refunds[0].ID, "approved", "verified")
	if e != nil {
		t.Fatal(e)
	}
	d, e = b.SubmitRefund(merchant, "buyer", d.Invoice.ID, d.Refunds[0].ID)
	if e != nil {
		t.Fatal(e)
	}
	d, e = b.SimulateRefund(merchant, "buyer", d.Invoice.ID, d.Refunds[0].ID, "succeeded")
	if e != nil {
		t.Fatal(e)
	}
	v, e = s.Reconcile(ctx, "seller", tx.ID)
	if e != nil || v.Refunded != 5000 || v.SellerEntitlement != 4500 || v.PlatformEntitlement != 500 || v.Debt != 1500 || len(v.Reversals) != 1 || v.Reversals[0].Result.Status != "succeeded" {
		t.Fatalf("reversal %+v %v", v, e)
	}
	_, e = s.ReconcileFiscal(ctx, "buyer", fiscal.Effect.ID)
	if e != nil {
		t.Fatal(e)
	}
	rows, e := s.Repo.ListOperations(ctx, "fiscal", "buyer", "", 100)
	if e != nil || len(rows) != 2 {
		t.Fatal("missing credit invoice", e)
	}
	for _, o := range rows {
		var f commerce.Fiscal
		_ = json.Unmarshal(o.Data, &f)
		if f.RefundID != "" {
			f, e = s.ReconcileFiscal(ctx, "buyer", o.ID)
			if e != nil || f.Result.Amount != 5000 || f.Effect.Reference != fiscal.Result.ProviderID {
				t.Fatal("credit issue", e)
			}
		}
	}
	if billing.PurchasePaid(d, tx.ID) {
		t.Fatal("refunded license still payable")
	}
}

type badGateway struct {
	commerce.Gateway
	Mismatch bool
	Fail     bool
}

func (g badGateway) Apply(ctx context.Context, e commerce.Effect) (commerce.Result, error) {
	if g.Fail {
		return commerce.Result{}, context.DeadlineExceeded
	}
	r, err := g.Gateway.Apply(ctx, e)
	if g.Mismatch {
		r.Amount++
	}
	return r, err
}
func TestUnknownOutcomeHeldAcrossRestartAndMismatchRejected(t *testing.T) {
	s, _, tx, d := fixture(t)
	ctx := context.Background()
	_, e := s.CreateSettlement(ctx, tx, d)
	if e != nil {
		t.Fatal(e)
	}
	s.Gateway = badGateway{Gateway: commerce.DemoGateway{}, Fail: true}
	if _, e = s.Reconcile(ctx, "buyer", tx.ID); e == nil {
		t.Fatal("lost response accepted")
	}
	s.Gateway = badGateway{Gateway: commerce.DemoGateway{}, Mismatch: true}
	if _, e = s.Reconcile(ctx, "buyer", tx.ID); e == nil {
		t.Fatal("mismatch accepted")
	}
	other := &commerce.Service{Repo: s.Repo, Gateway: commerce.DemoGateway{}, Simulation: true}
	v, e := other.Reconcile(ctx, "seller", tx.ID)
	if e != nil || v.Available != 9000 {
		t.Fatal("recovery failed", e)
	}
	if _, e = other.Load(ctx, "stranger", tx.ID); !errors.Is(e, operations.ErrNotFound) {
		t.Fatal("tenant leak")
	}
}

func TestPurchaseRetryCannotMoveLiabilityToDifferentBillingAccount(t *testing.T) {
	_, b, tx, original := fixture(t)
	_, err := b.IssuePurchase(context.Background(), tx, protocol.BillingAccount{ID: "another-billing-account", AccountID: tx.BuyerAccountID, Status: "active", Currency: tx.Currency})
	if !errors.Is(err, billing.ErrConflict) {
		t.Fatal("purchase billing account silently changed", err)
	}
	saved, err := b.Repo.GetBill(context.Background(), tx.BuyerAccountID, original.Invoice.ID)
	if err != nil || saved.Invoice.BillingAccountID != original.Invoice.BillingAccountID {
		t.Fatal("immutable bill changed", err)
	}
}
