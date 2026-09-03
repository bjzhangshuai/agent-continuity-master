package payments

import (
	"context"
	"net/http"
)

// Demo never contacts a network or masquerades as a real provider.
type Demo struct{}

func (Demo) Info() Info {
	return Info{ID: "demo", Name: "本地模拟支付（不扣款）", Environment: "simulation", Enabled: true, Currencies: []string{"CNY", "USD", "EUR"}, Capabilities: []string{"hosted_checkout", "query", "simulation"}}
}
func (Demo) Create(_ context.Context, o Order) (Result, error) {
	return Result{PaymentID: o.PaymentID, OrderID: "demo_" + o.PaymentID, Status: "pending", AmountMinor: o.AmountMinor, Currency: o.Currency}, nil
}
func (d Demo) Query(ctx context.Context, o Order) (Result, error) { return d.Create(ctx, o) }
func (Demo) Capture(context.Context, Order) (Result, error)       { return Result{}, ErrUnsupported }
func (Demo) VerifyNotification(context.Context, http.Header, []byte) (Result, error) {
	return Result{}, ErrUnsupported
}
