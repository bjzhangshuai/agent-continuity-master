package payments

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type PayPal struct {
	config     PayPalConfig
	base       string
	http       *http.Client
	mu         sync.Mutex
	token      string
	tokenUntil time.Time
}

func (p *PayPal) Info() Info {
	return Info{ID: "paypal", Name: "PayPal Checkout", Environment: environment(p.config.Sandbox), Enabled: true, Currencies: []string{"USD", "EUR"}, Capabilities: []string{"hosted_checkout", "query", "capture", "verified_webhook"}}
}
func (p *PayPal) accessToken(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if time.Now().Before(p.tokenUntil) {
		return p.token, nil
	}
	r, err := http.NewRequestWithContext(ctx, "POST", p.base+"/v1/oauth2/token", strings.NewReader("grant_type=client_credentials"))
	if err != nil {
		return "", err
	}
	r.SetBasicAuth(p.config.ClientID, p.config.ClientSecret)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := p.http.Do(r)
	if err != nil {
		return "", err
	}
	b, err := readResponse(resp)
	if err != nil {
		return "", err
	}
	var v struct {
		Token   string `json:"access_token"`
		Expires int    `json:"expires_in"`
	}
	if json.Unmarshal(b, &v) != nil || v.Token == "" {
		return "", errors.New("PayPal token response invalid")
	}
	p.token = v.Token
	p.tokenUntil = time.Now().Add(time.Duration(max(1, v.Expires-60)) * time.Second)
	return p.token, nil
}
func (p *PayPal) call(ctx context.Context, method, path, key string, input any) ([]byte, error) {
	token, err := p.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	var b []byte
	if input != nil {
		b, err = json.Marshal(input)
		if err != nil {
			return nil, err
		}
	}
	r, err := http.NewRequestWithContext(ctx, method, p.base+path, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Prefer", "return=representation")
	if key != "" {
		r.Header.Set("PayPal-Request-Id", key)
	}
	resp, err := p.http.Do(r)
	if err != nil {
		return nil, err
	}
	return readResponse(resp)
}

type paypalMoney struct {
	Currency string `json:"currency_code"`
	Value    string `json:"value"`
}
type paypalCapture struct {
	ID         string      `json:"id"`
	Status     string      `json:"status"`
	Amount     paypalMoney `json:"amount"`
	CreateTime time.Time   `json:"create_time"`
	Payee      struct {
		MerchantID string `json:"merchant_id"`
	} `json:"payee"`
}
type paypalOrder struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Links  []struct {
		Href string `json:"href"`
		Rel  string `json:"rel"`
	} `json:"links"`
	Units []struct {
		CustomID string      `json:"custom_id"`
		Amount   paypalMoney `json:"amount"`
		Payee    struct {
			MerchantID string `json:"merchant_id"`
		} `json:"payee"`
		Payments struct {
			Captures []paypalCapture `json:"captures"`
		} `json:"payments"`
	} `json:"purchase_units"`
}

func (p *PayPal) parse(b []byte) (Result, error) {
	var v paypalOrder
	if err := json.Unmarshal(b, &v); err != nil {
		return Result{}, err
	}
	if v.ID == "" || len(v.Units) != 1 {
		return Result{}, errors.New("PayPal requires exactly one purchase unit")
	}
	u := v.Units[0]
	if u.Payee.MerchantID != p.config.MerchantID {
		return Result{}, errors.New("PayPal payee mismatch")
	}
	n, err := ParseDecimal(u.Amount.Value)
	if err != nil {
		return Result{}, err
	}
	r := Result{PaymentID: u.CustomID, OrderID: v.ID, Status: "pending", AmountMinor: n, Currency: u.Amount.Currency}
	for _, l := range v.Links {
		if l.Rel == "approve" || l.Rel == "payer-action" {
			r.CheckoutURL = l.Href
		}
	}
	if v.Status == "APPROVED" {
		r.Status = "approved"
	}
	if v.Status == "VOIDED" {
		r.Status = "canceled"
	}
	if v.Status == "COMPLETED" {
		if len(u.Payments.Captures) != 1 {
			return Result{}, errors.New("PayPal unexpected capture count")
		}
		c := u.Payments.Captures[0]
		if c.Status == "COMPLETED" {
			amount, err := ParseDecimal(c.Amount.Value)
			if err != nil || amount != n || c.Amount.Currency != u.Amount.Currency {
				return Result{}, errors.New("PayPal capture amount mismatch")
			}
			r.Status = "succeeded"
			r.TransactionID = c.ID
			r.PaidAt = c.CreateTime
		}
	}
	return r, nil
}
func (p *PayPal) Create(ctx context.Context, o Order) (Result, error) {
	b, err := p.call(ctx, "POST", "/v2/checkout/orders", o.PaymentID, map[string]any{"intent": "CAPTURE", "purchase_units": []any{map[string]any{"reference_id": o.PaymentID, "custom_id": o.PaymentID, "description": o.Description, "payee": map[string]string{"merchant_id": p.config.MerchantID}, "amount": paypalMoney{Currency: o.Currency, Value: Decimal(o.AmountMinor)}}}, "payment_source": map[string]any{"paypal": map[string]any{"experience_context": map[string]string{"return_url": o.ReturnURL, "cancel_url": o.ReturnURL, "user_action": "PAY_NOW", "shipping_preference": "NO_SHIPPING"}}}})
	if err != nil {
		return Result{}, err
	}
	return p.parse(b)
}
func (p *PayPal) Query(ctx context.Context, o Order) (Result, error) {
	if o.ProviderOrderID == "" {
		return Result{}, ErrNotFound
	}
	b, err := p.call(ctx, "GET", "/v2/checkout/orders/"+url.PathEscape(o.ProviderOrderID), "", nil)
	if err != nil {
		return Result{}, err
	}
	return p.parse(b)
}
func (p *PayPal) Capture(ctx context.Context, o Order) (Result, error) {
	if o.ProviderOrderID == "" {
		return Result{}, ErrNotFound
	}
	b, err := p.call(ctx, "POST", "/v2/checkout/orders/"+url.PathEscape(o.ProviderOrderID)+"/capture", o.PaymentID+"_cap", map[string]any{})
	if err != nil {
		return Result{}, err
	}
	return p.parse(b)
}
func (p *PayPal) VerifyNotification(ctx context.Context, h http.Header, b []byte) (Result, error) {
	for _, key := range []string{"Paypal-Auth-Algo", "Paypal-Cert-Url", "Paypal-Transmission-Id", "Paypal-Transmission-Sig", "Paypal-Transmission-Time"} {
		if h.Get(key) == "" {
			return Result{}, ErrSignature
		}
	}
	var event json.RawMessage
	if json.Unmarshal(b, &event) != nil {
		return Result{}, ErrSignature
	}
	verification, err := p.call(ctx, "POST", "/v1/notifications/verify-webhook-signature", "", map[string]any{"auth_algo": h.Get("Paypal-Auth-Algo"), "cert_url": h.Get("Paypal-Cert-Url"), "transmission_id": h.Get("Paypal-Transmission-Id"), "transmission_sig": h.Get("Paypal-Transmission-Sig"), "transmission_time": h.Get("Paypal-Transmission-Time"), "webhook_id": p.config.WebhookID, "webhook_event": event})
	if err != nil {
		return Result{}, err
	}
	var verified struct {
		Status string `json:"verification_status"`
	}
	if json.Unmarshal(verification, &verified) != nil || verified.Status != "SUCCESS" {
		return Result{}, ErrSignature
	}
	var v struct {
		ID       string          `json:"id"`
		Type     string          `json:"event_type"`
		Resource json.RawMessage `json:"resource"`
	}
	if err = json.Unmarshal(b, &v); err != nil {
		return Result{}, err
	}
	if v.Type == "CHECKOUT.ORDER.APPROVED" {
		r, err := p.parse(v.Resource)
		r.EventID = v.ID
		return r, err
	}
	if v.Type != "PAYMENT.CAPTURE.COMPLETED" {
		return Result{}, ErrUnsupported
	}
	var c struct {
		paypalCapture
		Supplementary struct {
			Related struct {
				OrderID string `json:"order_id"`
			} `json:"related_ids"`
		} `json:"supplementary_data"`
	}
	if err = json.Unmarshal(v.Resource, &c); err != nil {
		return Result{}, err
	}
	if c.Status != "COMPLETED" || c.Payee.MerchantID != p.config.MerchantID {
		return Result{}, errors.New("PayPal capture payee/status mismatch")
	}
	n, err := ParseDecimal(c.Amount.Value)
	if err != nil {
		return Result{}, err
	}
	return Result{OrderID: c.Supplementary.Related.OrderID, TransactionID: c.ID, Status: "succeeded", AmountMinor: n, Currency: c.Amount.Currency, PaidAt: c.CreateTime, EventID: v.ID}, nil
}
