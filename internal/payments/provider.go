// Package payments isolates payment-network protocols from billing/accounting.
package payments

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

var ErrNotFound = errors.New("provider order not found")
var ErrUnsupported = errors.New("operation not supported by this provider")
var ErrSignature = errors.New("invalid payment notification signature")

type GatewayError struct{ Status int }

func (e *GatewayError) Error() string { return fmt.Sprintf("payment gateway HTTP %d", e.Status) }
func DefinitiveRejection(err error) bool {
	var e *GatewayError
	if !errors.As(err, &e) {
		return false
	}
	return e.Status == 400 || e.Status == 401 || e.Status == 403 || e.Status == 422
}

type Info struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Environment  string   `json:"environment"`
	Enabled      bool     `json:"enabled"`
	Currencies   []string `json:"currencies"`
	Capabilities []string `json:"capabilities"`
	Reason       string   `json:"reason,omitempty"`
}

type Order struct {
	PaymentID       string
	ProviderOrderID string
	Description     string
	Currency        string
	AmountMinor     int64
	ReturnURL       string
	NotifyURL       string
	ExpiresAt       time.Time
}

type Result struct {
	PaymentID     string
	OrderID       string
	TransactionID string
	Status        string // pending, approved, succeeded, failed, canceled, expired
	AmountMinor   int64
	Currency      string
	CheckoutURL   string
	QRCode        string
	PaidAt        time.Time
	EventID       string
}

type Provider interface {
	Info() Info
	Create(context.Context, Order) (Result, error)
	Query(context.Context, Order) (Result, error)
	Capture(context.Context, Order) (Result, error)
	VerifyNotification(context.Context, http.Header, []byte) (Result, error)
}

type Registry struct{ providers map[string]Provider }

func NewRegistry(providers ...Provider) *Registry {
	r := &Registry{providers: map[string]Provider{}}
	for _, p := range providers {
		if err := r.Register(p); err != nil {
			panic(err)
		}
	}
	return r
}

// Register is a startup-only extension point; the registry is immutable once the server starts.
func (r *Registry) Register(p Provider) error {
	if p == nil || p.Info().ID == "" {
		return errors.New("provider ID required")
	}
	if _, exists := r.providers[p.Info().ID]; exists {
		return errors.New("duplicate provider ID")
	}
	r.providers[p.Info().ID] = p
	return nil
}
func (r *Registry) Get(id string) (Provider, error) {
	if id == "alipay_cn" || id == "支付宝" {
		id = "alipay"
	}
	if p, ok := r.providers[id]; ok {
		return p, nil
	}
	return nil, fmt.Errorf("payment provider %q is not configured", id)
}
func (r *Registry) List() []Info {
	names := map[string]string{"wechat": "微信支付", "alipay": "支付宝 / Alipay", "paypal": "PayPal", "stripe": "Stripe", "demo": "本地模拟支付（不扣款）"}
	result := []Info{}
	for _, id := range []string{"wechat", "alipay", "paypal", "stripe", "demo"} {
		if p, ok := r.providers[id]; ok {
			result = append(result, p.Info())
		} else {
			result = append(result, Info{ID: id, Name: names[id], Enabled: false, Environment: "unconfigured", Reason: "未配置商户凭据 / Merchant credentials not configured", Currencies: []string{}, Capabilities: []string{}})
		}
	}
	custom := []string{}
	for id := range r.providers {
		if _, standard := names[id]; !standard {
			custom = append(custom, id)
		}
	}
	sort.Strings(custom)
	for _, id := range custom {
		result = append(result, r.providers[id].Info())
	}
	return result
}

func NewID(prefix string) string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + hex.EncodeToString(b) // <=32 bytes for WeChat out_trade_no.
}
func Decimal(minor int64) string { return fmt.Sprintf("%d.%02d", minor/100, minor%100) }
func ParseDecimal(value string) (int64, error) {
	parts := strings.Split(value, ".")
	if len(parts) > 2 || len(parts[0]) == 0 || len(parts[0]) > 12 {
		return 0, errors.New("invalid money")
	}
	fraction := "00"
	if len(parts) == 2 {
		if len(parts[1]) < 1 || len(parts[1]) > 2 {
			return 0, errors.New("invalid money precision")
		}
		fraction = (parts[1] + "0")[:2]
	}
	for _, c := range parts[0] + fraction {
		if c < '0' || c > '9' {
			return 0, errors.New("invalid money digits")
		}
	}
	w, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, err
	}
	f, _ := strconv.ParseInt(fraction, 10, 64)
	return w*100 + f, nil
}

func client() *http.Client {
	return &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func readResponse(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &GatewayError{Status: resp.StatusCode}
	}
	return body, nil
}
