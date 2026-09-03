package billing_test

import (
	"context"
	"errors"
	"github.com/agent-continuity/agent-continuity-master/internal/billing"
	"github.com/agent-continuity/agent-continuity-master/internal/payments"
	"github.com/agent-continuity/agent-continuity-master/internal/storage/memory"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
	"math"
	"strings"
	"sync"
	"testing"
	"time"
)

func fixture(t *testing.T) (*billing.Service, protocol.BillingAccount, time.Time, time.Time) {
	t.Helper()
	store := memory.New()
	ctx := context.Background()
	now := time.Now().UTC()
	a := protocol.BillingAccount{ID: "ba1", AccountID: "owner", Currency: "CNY", Status: "active"}
	_ = store.PutBillingAccount(ctx, a)
	for i := 0; i < 1105; i++ {
		err := store.RecordUsage(ctx, protocol.UsageRecord{ID: payments.NewID("use_"), AccountID: "owner", Metric: "api.mutation", Quantity: 1, Unit: "operation", IdempotencyKey: payments.NewID("key_"), RecordedAt: now.Add(-time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
	}
	s := billing.New(store, payments.NewRegistry(payments.Demo{}), billing.DemoPriceBook(), "http://127.0.0.1:18080", []byte(strings.Repeat("x", 32)))
	return s, a, now.Add(-2 * time.Hour), now
}
func TestInvoicePaymentLifecycleConcurrencyAndAccess(t *testing.T) {
	s, a, from, to := fixture(t)
	ctx := context.Background()
	d, err := s.Issue(ctx, a, from, to, "issue-key-1")
	if err != nil {
		t.Fatal(err)
	}
	if d.Invoice.AmountMinor != 1105 || d.Invoice.Lines[0].Records != 1105 {
		t.Fatalf("truncated usage: %+v", d.Invoice)
	}
	again, err := s.Issue(ctx, a, from, to, "issue-key-1")
	if err != nil || again.Invoice.ID != d.Invoice.ID {
		t.Fatal("idempotency failed", err)
	}
	if _, err = s.Issue(ctx, a, from, to, "another-key"); !errors.Is(err, billing.ErrOverlap) {
		t.Fatalf("overlap allowed: %v", err)
	}
	if _, err = s.Repo.GetBill(ctx, "intruder", d.Invoice.ID); !errors.Is(err, billing.ErrNotFound) {
		t.Fatal("cross account read allowed")
	}
	d, id, err := s.CreatePayment(ctx, a.AccountID, d.Invoice.ID, "demo", "checkout-key1")
	if err != nil {
		t.Fatal(err)
	}
	link := s.Link(d.Payments[0])
	if strings.Contains(link, a.AccountID) {
		t.Fatal("account leaked into share token")
	}
	if _, _, err = s.PublicPayment(ctx, id, "wrong"); !errors.Is(err, billing.ErrNotFound) {
		t.Fatal("invalid share accepted")
	}
	if _, _, err = s.CreatePayment(ctx, a.AccountID, d.Invoice.ID, "demo", "other-attempt"); !errors.Is(err, billing.ErrConflict) {
		t.Fatal("second active payment allowed", err)
	}
	bad := payments.Result{PaymentID: id, OrderID: d.Payments[0].ProviderOrderID, TransactionID: "tx", Status: "succeeded", AmountMinor: 1, Currency: "CNY"}
	if _, err = s.Apply(ctx, a.AccountID, d.Invoice.ID, id, bad, "test"); !errors.Is(err, billing.ErrInvalid) {
		t.Fatal("wrong amount accepted")
	}
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Simulate(ctx, a.AccountID, d.Invoice.ID, id, "succeeded")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	d, err = s.Repo.GetBill(ctx, a.AccountID, d.Invoice.ID)
	if err != nil || d.Invoice.Status != "paid" || len(d.Receipts) != 1 {
		t.Fatalf("non-atomic or duplicate receipt: %+v %v", d, err)
	}
	_, err = s.Apply(ctx, a.AccountID, d.Invoice.ID, id, payments.Result{PaymentID: id, OrderID: d.Payments[0].ProviderOrderID, Status: "pending", AmountMinor: 1105, Currency: "CNY"}, "stale")
	if err != nil {
		t.Fatal(err)
	}
	d, _ = s.Repo.GetBill(ctx, a.AccountID, d.Invoice.ID)
	if d.Payments[0].Status != "succeeded" {
		t.Fatal("stale event regressed payment")
	}
	_, err = s.RevokeLink(ctx, a.AccountID, d.Invoice.ID, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.PublicPayment(ctx, id, strings.Split(link, "token=")[1]); !errors.Is(err, billing.ErrNotFound) {
		t.Fatal("revoked link accepted")
	}
}
func TestIntegerRoundingOverflowAndUnknownRates(t *testing.T) {
	rate := []protocol.BillingRate{{Metric: "bytes", Unit: "byte", UnitSize: 1024, UnitPriceMinor: 3}}
	lines, total, err := billing.Calculate([]protocol.UsageTotal{{Metric: "bytes", Unit: "byte", Quantity: 1025}}, rate)
	if err != nil || total != 4 || lines[0].AmountMinor != 4 {
		t.Fatal(lines, total, err)
	}
	for _, v := range []protocol.UsageTotal{{Metric: "unknown", Unit: "byte", Quantity: 1}, {Metric: "bytes", Unit: "other", Quantity: 1}, {Metric: "bytes", Unit: "byte", Quantity: -1}, {Metric: "bytes", Unit: "byte", Quantity: math.MaxInt64}} {
		if _, _, err = billing.Calculate([]protocol.UsageTotal{v}, rate); err == nil {
			t.Fatalf("bad money accepted: %+v", v)
		}
	}
}
func TestVoidsAndZeroInvoices(t *testing.T) {
	s, a, from, to := fixture(t)
	ctx := context.Background()
	d, err := s.Issue(ctx, a, from, to, "void-key-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Void(ctx, a.AccountID, d.Invoice.ID); err != nil {
		t.Fatal(err)
	}
	d, err = s.Issue(ctx, a, from, to, "replacement-1")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.CreatePayment(ctx, a.AccountID, d.Invoice.ID, "demo", "payment-key1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Void(ctx, a.AccountID, d.Invoice.ID); !errors.Is(err, billing.ErrInvalid) {
		t.Fatal("invoice with active gateway order voided")
	}
	a.AccountID = "no-usage"
	zero, err := s.Issue(ctx, a, from, to, "zero-key-1")
	if err != nil || zero.Invoice.Status != "paid" || len(zero.Receipts) != 0 {
		t.Fatal("zero invoice behavior invalid", err)
	}
}

type liveStub struct{ payments.Demo }

func (liveStub) Info() payments.Info {
	return payments.Info{ID: "live-test", Name: "test", Environment: "live", Enabled: true, Currencies: []string{"CNY"}}
}

type rejectedStub struct{ payments.Demo }

func (rejectedStub) Info() payments.Info {
	return payments.Info{ID: "reject-test", Name: "test rejection", Environment: "simulation", Enabled: true, Currencies: []string{"CNY"}}
}
func (rejectedStub) Create(context.Context, payments.Order) (payments.Result, error) {
	return payments.Result{}, &payments.GatewayError{Status: 422}
}
func TestDefinitiveRejectionAllowsFreshAttempt(t *testing.T) {
	s, a, from, to := fixture(t)
	ctx := context.Background()
	_ = s.Providers.Register(rejectedStub{})
	d, err := s.Issue(ctx, a, from, to, "reject-invoice")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.CreatePayment(ctx, a.AccountID, d.Invoice.ID, "reject-test", "reject-first")
	if !payments.DefinitiveRejection(err) {
		t.Fatal("missing definitive rejection", err)
	}
	d, _ = s.Repo.GetBill(ctx, a.AccountID, d.Invoice.ID)
	if d.Payments[0].Status != "failed" {
		t.Fatal("definitive rejection left an uncertain active order")
	}
	_, _, err = s.CreatePayment(ctx, a.AccountID, d.Invoice.ID, "demo", "recover-demo")
	if err != nil {
		t.Fatal("fresh retry blocked", err)
	}
	if payments.DefinitiveRejection(&payments.GatewayError{Status: 429}) || payments.DefinitiveRejection(&payments.GatewayError{Status: 500}) {
		t.Fatal("transient gateway errors were classified as final")
	}
}

func TestFailedSimulationRemainsFailedOnQuery(t *testing.T) {
	s, a, from, to := fixture(t)
	ctx := context.Background()
	d, err := s.Issue(ctx, a, from, to, "failed-invoice")
	if err != nil {
		t.Fatal(err)
	}
	d, id, err := s.CreatePayment(ctx, a.AccountID, d.Invoice.ID, "demo", "first-failure")
	if err != nil {
		t.Fatal(err)
	}
	d, err = s.Simulate(ctx, a.AccountID, d.Invoice.ID, id, "failed")
	if err != nil {
		t.Fatal(err)
	}
	d, err = s.Reconcile(ctx, a.AccountID, d.Invoice.ID, id, true)
	if err != nil || d.Payments[0].Status != "failed" {
		t.Fatal("query regressed failed simulation", err)
	}
	d, next, err := s.CreatePayment(ctx, a.AccountID, d.Invoice.ID, "demo", "new-attempt-key")
	if err != nil || next == id || len(d.Payments) != 2 {
		t.Fatal("fresh attempt not created", err)
	}
}
func TestEnvironmentExpiryAndOrganizationIsolation(t *testing.T) {
	s, a, from, to := fixture(t)
	ctx := context.Background()
	if err := s.Providers.Register(liveStub{}); err != nil {
		t.Fatal(err)
	}
	d, err := s.Issue(ctx, a, from, to, "isolation-issue")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.CreatePayment(ctx, a.AccountID, d.Invoice.ID, "live-test", "live-checkout"); !errors.Is(err, billing.ErrInvalid) {
		t.Fatal("test invoice reached live channel", err)
	}
	d, id, err := s.CreatePayment(ctx, a.AccountID, d.Invoice.ID, "demo", "demo-expiration")
	if err != nil {
		t.Fatal(err)
	}
	link := s.Link(d.Payments[0])
	d.Payments[0].ExpiresAt = time.Now().Add(-time.Second)
	if err = s.Repo.SaveBill(ctx, d, d.Version); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.PublicPayment(ctx, id, strings.Split(link, "token=")[1]); !errors.Is(err, billing.ErrNotFound) {
		t.Fatal("expired link accepted")
	}
	org := a
	org.OrganizationID = "org-different"
	org.ID = "org-billing"
	quote, err := s.Quote(ctx, org, from, to)
	if err != nil || quote.AmountMinor != 0 {
		t.Fatal("personal consumption leaked into organization invoice", err)
	}
	due, err := s.Repo.DuePayments(ctx, time.Now(), 10)
	if err != nil || len(due) != 0 {
		t.Fatal("simulator scheduled network queries")
	}
	found := false
	for _, p := range s.Providers.List() {
		if p.ID == "live-test" {
			found = true
		}
	}
	if !found {
		t.Fatal("custom provider omitted from capability discovery")
	}
}
