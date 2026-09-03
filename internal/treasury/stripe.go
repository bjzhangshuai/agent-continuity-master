package treasury

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

type Config struct {
	OwnerAccountID string        `json:"owner_account_id"`
	DemoEnabled    bool          `json:"demo_enabled"`
	AllowLive      bool          `json:"allow_live"`
	Stripe         *StripeConfig `json:"stripe,omitempty"`
}
type StripeConfig struct {
	AccountID     string `json:"account_id"`
	SecretKeyFile string `json:"secret_key_file"`
	Sandbox       bool   `json:"sandbox"`
}
type Stripe struct {
	account, key, base string
	sandbox            bool
	http               *http.Client
}
type Snapshot struct {
	Provider       string                       `json:"provider"`
	AccountID      string                       `json:"merchant_account_id"`
	Currency       string                       `json:"currency"`
	Environment    string                       `json:"environment"`
	AvailableMinor int64                        `json:"available_minor"`
	PendingMinor   int64                        `json:"pending_minor"`
	Destination    protocol.TreasuryDestination `json:"destination"`
	CheckedAt      time.Time                    `json:"checked_at"`
}
type Result struct {
	ID, Status, Currency, DestinationID string
	AmountMinor                         int64
}

func ParseConfig(raw string) (Config, error) {
	var c Config
	if raw == "" {
		return c, nil
	}
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return c, err
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return c, ErrInvalid
	}
	if (c.DemoEnabled || c.Stripe != nil) && strings.TrimSpace(c.OwnerAccountID) == "" {
		return c, fmt.Errorf("%w: explicit treasury owner required", ErrInvalid)
	}
	if c.Stripe != nil && (!c.Stripe.Sandbox && !c.AllowLive || !strings.HasPrefix(c.Stripe.AccountID, "acct_") || c.Stripe.SecretKeyFile == "") {
		return c, fmt.Errorf("%w: Stripe owner, key file and explicit live authorization required", ErrInvalid)
	}
	return c, nil
}
func Build(c Config) (*Stripe, error) {
	if c.Stripe == nil {
		return nil, nil
	}
	if !c.Stripe.Sandbox && !c.AllowLive {
		return nil, ErrUnavailable
	}
	b, err := os.ReadFile(c.Stripe.SecretKeyFile)
	if err != nil {
		return nil, err
	}
	key := strings.TrimSpace(string(b))
	prefix := "sk_live_"
	if c.Stripe.Sandbox {
		prefix = "sk_test_"
	}
	if !strings.HasPrefix(key, prefix) {
		return nil, fmt.Errorf("%w: Stripe key/environment mismatch", ErrInvalid)
	}
	return &Stripe{account: c.Stripe.AccountID, key: key, sandbox: c.Stripe.Sandbox, base: "https://api.stripe.com", http: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (p *Stripe) Environment() string {
	if p.sandbox {
		return "sandbox"
	}
	return "live"
}
func (p *Stripe) call(ctx context.Context, method, path, key string, v url.Values, out any) error {
	r, err := http.NewRequestWithContext(ctx, method, p.base+path, strings.NewReader(v.Encode()))
	if err != nil {
		return err
	}
	r.SetBasicAuth(p.key, "")
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	resp, err := p.http.Do(r)
	if err != nil {
		return fmt.Errorf("%w: Stripe network result uncertain", ErrUnavailable)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%w: Stripe returned HTTP %d; inspect original payout", ErrUnavailable, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20+1))
	if err != nil || len(b) > 1<<20 {
		return ErrInvalid
	}
	if err = json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("%w: invalid Stripe response", ErrInvalid)
	}
	return nil
}

func (p *Stripe) Snapshot(ctx context.Context, currency string) (Snapshot, error) {
	s := Snapshot{Provider: "stripe", AccountID: p.account, Currency: currency, Environment: p.Environment(), CheckedAt: time.Now().UTC()}
	if !slices.Contains([]string{"CNY", "USD", "EUR"}, currency) {
		return s, ErrInvalid
	}
	var account struct {
		ID       string `json:"id"`
		Payouts  bool   `json:"payouts_enabled"`
		External struct {
			Data []struct {
				ID       string `json:"id"`
				Object   string `json:"object"`
				Currency string `json:"currency"`
				Last4    string `json:"last4"`
				Bank     string `json:"bank_name"`
				Status   string `json:"status"`
				Default  bool   `json:"default_for_currency"`
			} `json:"data"`
		} `json:"external_accounts"`
		Settings struct {
			Payouts struct {
				Schedule struct {
					Interval string `json:"interval"`
				} `json:"schedule"`
			} `json:"payouts"`
		} `json:"settings"`
	}
	// /v1/account is resolved by the credential, not by a user-provided account ID.
	if err := p.call(ctx, "GET", "/v1/account", "", nil, &account); err != nil {
		return s, err
	}
	if account.ID != p.account || !account.Payouts {
		return s, fmt.Errorf("%w: merchant identity/payout capability mismatch", ErrInvalid)
	}
	if account.Settings.Payouts.Schedule.Interval != "manual" {
		return s, fmt.Errorf("%w: use the Stripe dashboard for automatic payouts; manual schedule required for API withdrawals", ErrUnavailable)
	}
	for _, a := range account.External.Data {
		if a.Object == "bank_account" && a.Default && strings.EqualFold(a.Currency, currency) && a.Status != "errored" && a.Status != "verification_failed" {
			s.Destination = protocol.TreasuryDestination{ID: a.ID, Label: a.Bank, Kind: "bank", Currency: currency, MaskedAccount: "**** " + a.Last4, Environment: p.Environment(), Verified: true}
			break
		}
	}
	if s.Destination.ID == "" {
		return s, fmt.Errorf("%w: no eligible default own bank account returned by Stripe", ErrUnavailable)
	}
	var balance struct {
		Live      bool `json:"livemode"`
		Available []struct {
			Amount   int64  `json:"amount"`
			Currency string `json:"currency"`
		} `json:"available"`
		Pending []struct {
			Amount   int64  `json:"amount"`
			Currency string `json:"currency"`
		} `json:"pending"`
	}
	if err := p.call(ctx, "GET", "/v1/balance", "", nil, &balance); err != nil {
		return s, err
	}
	if balance.Live == p.sandbox {
		return s, ErrInvalid
	}
	for _, b := range balance.Available {
		if strings.EqualFold(b.Currency, currency) {
			s.AvailableMinor = b.Amount
		}
	}
	for _, b := range balance.Pending {
		if strings.EqualFold(b.Currency, currency) {
			s.PendingMinor = b.Amount
		}
	}
	return s, nil
}

type stripePayout struct {
	ID          string            `json:"id"`
	Status      string            `json:"status"`
	Amount      int64             `json:"amount"`
	Currency    string            `json:"currency"`
	Destination string            `json:"destination"`
	Live        bool              `json:"livemode"`
	Type        string            `json:"type"`
	Metadata    map[string]string `json:"metadata"`
}

func (p *Stripe) result(v stripePayout, o protocol.TreasuryPayout) (Result, error) {
	if v.ID == "" || v.Live == p.sandbox || v.Type != "bank_account" || v.Amount != o.AmountMinor || !strings.EqualFold(v.Currency, o.Currency) || v.Destination != o.Destination.ID || v.Metadata["treasury_payout_id"] != o.ID {
		return Result{}, fmt.Errorf("%w: Stripe payout identity, destination or amount mismatch", ErrInvalid)
	}
	status := map[string]string{"pending": "processing", "in_transit": "processing", "paid": "paid", "failed": "failed", "canceled": "canceled"}[v.Status]
	if status == "" {
		return Result{}, ErrInvalid
	}
	return Result{ID: v.ID, Status: status, AmountMinor: v.Amount, Currency: strings.ToUpper(v.Currency), DestinationID: v.Destination}, nil
}
func (p *Stripe) Create(ctx context.Context, o protocol.TreasuryPayout) (Result, error) {
	if o.Environment != p.Environment() {
		return Result{}, ErrInvalid
	}
	snap, err := p.Snapshot(ctx, o.Currency)
	if err != nil {
		return Result{}, err
	}
	if snap.Destination.ID != o.Destination.ID {
		return Result{}, fmt.Errorf("%w: default bank changed; do not redirect an approved payout", ErrInvalid)
	}
	// Stripe is authoritative for balance/fees. An uncertain retry must reach the
	// same idempotency key even if the first attempt already reduced the balance.
	v := url.Values{"amount": {strconv.FormatInt(o.AmountMinor, 10)}, "currency": {strings.ToLower(o.Currency)}, "destination": {o.Destination.ID}, "method": {"standard"}, "metadata[treasury_payout_id]": {o.ID}}
	var result stripePayout
	if err = p.call(ctx, "POST", "/v1/payouts", o.ID, v, &result); err != nil {
		return Result{}, err
	}
	return p.result(result, o)
}
func (p *Stripe) Query(ctx context.Context, o protocol.TreasuryPayout) (Result, error) {
	if o.Environment != p.Environment() || !strings.HasPrefix(o.ProviderPayoutID, "po_") {
		return Result{}, ErrInvalid
	}
	// A bank may reject a previously submitted payout or stop being the default.
	// Historical reconciliation must not depend on current payout eligibility.
	var account struct {
		ID string `json:"id"`
	}
	if err := p.call(ctx, "GET", "/v1/account", "", nil, &account); err != nil {
		return Result{}, err
	}
	if account.ID != p.account {
		return Result{}, ErrInvalid
	}
	var v stripePayout
	if err := p.call(ctx, "GET", "/v1/payouts/"+url.PathEscape(o.ProviderPayoutID), "", nil, &v); err != nil {
		return Result{}, err
	}
	return p.result(v, o)
}
