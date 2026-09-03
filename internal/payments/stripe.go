package payments

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Stripe struct {
	config StripeConfig
	base   string
	http   *http.Client
}

func (p *Stripe) Info() Info {
	return Info{ID: "stripe", Name: "Stripe 托管收银台", Environment: environment(p.config.Sandbox), Enabled: true, Currencies: []string{"CNY", "USD", "EUR"}, Capabilities: []string{"hosted_checkout", "query", "signed_webhook"}}
}
func (p *Stripe) call(ctx context.Context, method, path, key string, v url.Values) ([]byte, error) {
	r, err := http.NewRequestWithContext(ctx, method, p.base+path, strings.NewReader(v.Encode()))
	if err != nil {
		return nil, err
	}
	r.Header.Set("Authorization", "Bearer "+p.config.SecretKey)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	resp, err := p.http.Do(r)
	if err != nil {
		return nil, err
	}
	return readResponse(resp)
}
func (p *Stripe) parse(b []byte) (Result, error) {
	var v struct {
		ID            string `json:"id"`
		URL           string `json:"url"`
		Status        string `json:"status"`
		PaymentStatus string `json:"payment_status"`
		Currency      string `json:"currency"`
		Amount        int64  `json:"amount_total"`
		Live          bool   `json:"livemode"`
		Reference     string `json:"client_reference_id"`
		PaymentIntent string `json:"payment_intent"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return Result{}, err
	}
	if v.ID == "" || v.Live == p.config.Sandbox {
		return Result{}, errors.New("Stripe session/environment mismatch")
	}
	s := "pending"
	if v.Status == "expired" {
		s = "expired"
	}
	if v.PaymentStatus == "paid" {
		s = "succeeded"
	}
	return Result{PaymentID: v.Reference, OrderID: v.ID, TransactionID: v.PaymentIntent, Status: s, AmountMinor: v.Amount, Currency: strings.ToUpper(v.Currency), CheckoutURL: v.URL}, nil
}
func (p *Stripe) Create(ctx context.Context, o Order) (Result, error) {
	v := url.Values{"mode": {"payment"}, "client_reference_id": {o.PaymentID}, "metadata[payment_id]": {o.PaymentID}, "success_url": {o.ReturnURL}, "cancel_url": {o.ReturnURL}, "expires_at": {strconv.FormatInt(o.ExpiresAt.Unix(), 10)}, "line_items[0][price_data][currency]": {strings.ToLower(o.Currency)}, "line_items[0][price_data][unit_amount]": {strconv.FormatInt(o.AmountMinor, 10)}, "line_items[0][price_data][product_data][name]": {o.Description}, "line_items[0][quantity]": {"1"}}
	b, err := p.call(ctx, "POST", "/v1/checkout/sessions", o.PaymentID, v)
	if err != nil {
		return Result{}, err
	}
	return p.parse(b)
}
func (p *Stripe) Query(ctx context.Context, o Order) (Result, error) {
	if o.ProviderOrderID == "" {
		return Result{}, ErrNotFound
	}
	b, err := p.call(ctx, "GET", "/v1/checkout/sessions/"+url.PathEscape(o.ProviderOrderID), "", nil)
	if err != nil {
		return Result{}, err
	}
	return p.parse(b)
}
func (p *Stripe) Capture(context.Context, Order) (Result, error) { return Result{}, ErrUnsupported }
func (p *Stripe) VerifyNotification(_ context.Context, h http.Header, b []byte) (Result, error) {
	parts := strings.Split(h.Get("Stripe-Signature"), ",")
	timestamp := ""
	signatures := []string{}
	for _, part := range parts {
		k, v, ok := strings.Cut(part, "=")
		if ok {
			if k == "t" {
				timestamp = v
			}
			if k == "v1" {
				signatures = append(signatures, v)
			}
		}
	}
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || time.Since(time.Unix(ts, 0)).Abs() > 5*time.Minute {
		return Result{}, ErrSignature
	}
	mac := hmac.New(sha256.New, []byte(p.config.WebhookSecret))
	mac.Write([]byte(timestamp + "."))
	mac.Write(b)
	valid := false
	for _, sig := range signatures {
		d, err := hex.DecodeString(sig)
		if err == nil && hmac.Equal(d, mac.Sum(nil)) {
			valid = true
		}
	}
	if !valid {
		return Result{}, ErrSignature
	}
	var v struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		Live bool   `json:"livemode"`
		Data struct {
			Object json.RawMessage `json:"object"`
		} `json:"data"`
	}
	if err = json.Unmarshal(b, &v); err != nil {
		return Result{}, err
	}
	if v.Live == p.config.Sandbox {
		return Result{}, ErrSignature
	}
	switch v.Type {
	case "checkout.session.completed", "checkout.session.async_payment_succeeded", "checkout.session.expired":
	default:
		return Result{}, ErrUnsupported
	}
	r, err := p.parse(v.Data.Object)
	r.EventID = v.ID
	return r, err
}
