package billing

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/agent-continuity/agent-continuity-master/internal/payments"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
	"log/slog"
	"math"
	"net/url"
	"slices"
	"strings"
	"time"
)

const MaxAmountMinor int64 = 100_000_000 // bounded at 1m major units; override via product review, not browser input.
type PriceBook struct {
	Version    string                            `json:"version"`
	TestMode   bool                              `json:"test_mode"`
	Currencies map[string][]protocol.BillingRate `json:"currencies"`
}

func DemoPriceBook() PriceBook {
	return PriceBook{Version: "demo-2026-09-v1", TestMode: true, Currencies: map[string][]protocol.BillingRate{
		"CNY": {{Metric: "api.mutation", Description: "API 变更操作（演示定价）", Unit: "operation", UnitSize: 1, UnitPriceMinor: 1}, {Metric: "artifact.bytes", Description: "产物上传字节（演示定价，非容量月租）", Unit: "byte", UnitSize: 1024, UnitPriceMinor: 1}},
		"USD": {{Metric: "api.mutation", Description: "API mutations (test price)", Unit: "operation", UnitSize: 1, UnitPriceMinor: 1}, {Metric: "artifact.bytes", Description: "Artifact upload bytes (test price)", Unit: "byte", UnitSize: 1024, UnitPriceMinor: 1}},
		"EUR": {{Metric: "api.mutation", Description: "API mutations (test price)", Unit: "operation", UnitSize: 1, UnitPriceMinor: 1}, {Metric: "artifact.bytes", Description: "Artifact upload bytes (test price)", Unit: "byte", UnitSize: 1024, UnitPriceMinor: 1}},
	}}
}
func ParsePriceBook(raw string) (PriceBook, error) {
	if raw == "" {
		return DemoPriceBook(), nil
	}
	var b PriceBook
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&b); err != nil {
		return b, err
	}
	return b, b.Validate()
}
func (b PriceBook) Validate() error {
	if b.Version == "" || len(b.Currencies) == 0 {
		return fmt.Errorf("%w: price book version/currencies required", ErrInvalid)
	}
	for currency, rates := range b.Currencies {
		if !slices.Contains([]string{"CNY", "USD", "EUR"}, currency) || len(rates) == 0 {
			return fmt.Errorf("%w: supported two-decimal currencies are CNY/USD/EUR", ErrInvalid)
		}
		seen := map[string]bool{}
		for _, r := range rates {
			if seen[r.Metric] || r.Metric == "" || r.Unit == "" || r.Description == "" || r.UnitSize <= 0 || r.UnitPriceMinor < 0 || r.UnitPriceMinor > MaxAmountMinor {
				return fmt.Errorf("%w: invalid or duplicate meter rate", ErrInvalid)
			}
			seen[r.Metric] = true
		}
	}
	return nil
}
func Calculate(totals []protocol.UsageTotal, rates []protocol.BillingRate) ([]protocol.BillingLine, int64, error) {
	lines := []protocol.BillingLine{}
	var amount int64
	for _, t := range totals {
		found := false
		for _, rate := range rates {
			if rate.Metric != t.Metric {
				continue
			}
			found = true
			if t.Quantity < 0 || rate.Unit != t.Unit || rate.UnitSize <= 0 || rate.UnitPriceMinor < 0 || t.Quantity > math.MaxInt64/max(1, rate.UnitPriceMinor) {
				return nil, 0, fmt.Errorf("%w: invalid meter quantity/unit or price overflow", ErrInvalid)
			}
			product := t.Quantity * rate.UnitPriceMinor
			subtotal := product / rate.UnitSize
			if product%rate.UnitSize != 0 {
				subtotal++
			}
			if subtotal > MaxAmountMinor-amount {
				return nil, 0, fmt.Errorf("%w: invoice exceeds amount limit", ErrInvalid)
			}
			amount += subtotal
			lines = append(lines, protocol.BillingLine{BillingRate: rate, Quantity: t.Quantity, Records: t.Records, AmountMinor: subtotal})
		}
		if !found {
			return nil, 0, fmt.Errorf("%w: no approved rate for meter %s", ErrInvalid, t.Metric)
		}
	}
	return lines, amount, nil
}

type Service struct {
	Repo       Repository
	Providers  *payments.Registry
	PriceBook  PriceBook
	BaseURL    string
	signingKey []byte
	now        func() time.Time
}

func New(repo Repository, providers *payments.Registry, book PriceBook, baseURL string, key []byte) *Service {
	return &Service{Repo: repo, Providers: providers, PriceBook: book, BaseURL: strings.TrimRight(baseURL, "/"), signingKey: append([]byte(nil), key...), now: time.Now}
}
func (s *Service) Quote(ctx context.Context, b protocol.BillingAccount, from, to time.Time) (protocol.BillingInvoice, error) {
	if !from.Before(to) || from.IsZero() || to.After(s.now().Add(time.Second)) || to.Sub(from) > 366*24*time.Hour {
		return protocol.BillingInvoice{}, fmt.Errorf("%w: use a past usage window of at most 366 days", ErrInvalid)
	}
	rates, ok := s.PriceBook.Currencies[b.Currency]
	if !ok {
		return protocol.BillingInvoice{}, fmt.Errorf("%w: currency has no configured price book", ErrInvalid)
	}
	totals, err := s.Repo.SummarizeUsage(ctx, b.AccountID, b.OrganizationID, from, to)
	if err != nil {
		return protocol.BillingInvoice{}, err
	}
	lines, total, err := Calculate(totals, rates)
	if err != nil {
		return protocol.BillingInvoice{}, err
	}
	return protocol.BillingInvoice{AccountID: b.AccountID, BillingAccountID: b.ID, OrganizationID: b.OrganizationID, PayerOrganizationID: b.PayerOrganizationID, PaymentMandateID: b.PaymentMandateID, Status: "open", Currency: b.Currency, AmountMinor: total, Lines: lines, PriceBook: s.PriceBook.Version, TestMode: s.PriceBook.TestMode, PeriodStart: from, PeriodEnd: to}, nil
}
func (s *Service) Issue(ctx context.Context, b protocol.BillingAccount, from, to time.Time, key string) (protocol.BillingDocument, error) {
	if !validKey(key) {
		return protocol.BillingDocument{}, fmt.Errorf("%w: Idempotency-Key (8..128 characters) required", ErrInvalid)
	}
	i, err := s.Quote(ctx, b, from, to)
	if err != nil {
		return protocol.BillingDocument{}, err
	}
	i.ID = payments.NewID("inv_")
	i.IssuedBy = Actor(ctx)
	i.Number = "AC-" + s.now().UTC().Format("20060102") + "-" + strings.ToUpper(strings.TrimPrefix(i.ID, "inv_"))
	i.CreatedAt = s.now().UTC()
	i.DueAt = i.CreatedAt.Add(7 * 24 * time.Hour)
	i.IdempotencyKey = key
	if i.AmountMinor == 0 {
		i.Status = "paid"
	} // zero-charge bill requires no gateway or fabricated receipt.
	d := protocol.BillingDocument{Invoice: i, Payments: []protocol.BillingPayment{}, Receipts: []protocol.PaymentReceipt{}, Events: []protocol.BillingEvent{}}
	addEvent(&d, "", "invoice.issued", "immutable usage and rate snapshot", s.now())
	return s.Repo.CreateBill(ctx, d)
}
func validKey(k string) bool {
	return len(k) >= 8 && len(k) <= 128 && !strings.ContainsAny(k, "\r\n\x00")
}
func addEvent(d *protocol.BillingDocument, paymentID, kind, detail string, now time.Time) {
	d.Events = append(d.Events, protocol.BillingEvent{ID: payments.NewID("bev_"), PaymentID: paymentID, Kind: kind, Detail: detail, CreatedAt: now.UTC()})
}
func payment(d *protocol.BillingDocument, id string) *protocol.BillingPayment {
	for i := range d.Payments {
		if d.Payments[i].ID == id {
			return &d.Payments[i]
		}
	}
	return nil
}
func (s *Service) mutate(ctx context.Context, account, id string, f func(*protocol.BillingDocument) error) (protocol.BillingDocument, error) {
	for attempt := 0; attempt < 8; attempt++ {
		d, err := s.Repo.GetBill(ctx, account, id)
		if err != nil {
			return d, err
		}
		if err = f(&d); err != nil {
			return d, err
		}
		err = s.Repo.SaveBill(ctx, d, d.Version)
		if errors.Is(err, ErrConflict) {
			continue
		}
		if err == nil {
			d.Version++
		}
		return d, err
	}
	return protocol.BillingDocument{}, ErrConflict
}
func (s *Service) CreatePayment(ctx context.Context, account, invoiceID, providerID, key string) (protocol.BillingDocument, string, error) {
	if !validKey(key) {
		return protocol.BillingDocument{}, "", fmt.Errorf("%w: Idempotency-Key required", ErrInvalid)
	}
	provider, err := s.Providers.Get(providerID)
	if err != nil {
		return protocol.BillingDocument{}, "", fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	info := provider.Info()
	paymentID := ""
	d, err := s.mutate(ctx, account, invoiceID, func(d *protocol.BillingDocument) error {
		for _, p := range d.Payments {
			if p.IdempotencyKey == key {
				if p.Provider != info.ID {
					return ErrConflict
				}
				paymentID = p.ID
				return nil
			}
		}
		if d.Invoice.Status != "open" || d.Invoice.AmountMinor <= 0 {
			return fmt.Errorf("%w: invoice is not payable", ErrInvalid)
		}
		if !slices.Contains(info.Currencies, d.Invoice.Currency) {
			return fmt.Errorf("%w: provider does not support invoice currency (no implicit FX)", ErrInvalid)
		}
		if d.Invoice.TestMode != (info.Environment != "live") {
			return fmt.Errorf("%w: invoice and provider environments do not match", ErrInvalid)
		}
		for _, p := range d.Payments {
			if p.Status == "creating" || p.Status == "pending" || p.Status == "approved" || p.Status == "succeeded" {
				return fmt.Errorf("%w: reconcile the existing payment before opening another channel", ErrConflict)
			}
		}
		if len(d.Payments) >= 10 {
			return fmt.Errorf("%w: maximum payment attempts reached", ErrInvalid)
		}
		paymentID = payments.NewID("pay_")
		now := s.now().UTC()
		d.Payments = append(d.Payments, protocol.BillingPayment{ID: paymentID, InitiatedBy: Actor(ctx), Provider: info.ID, Environment: info.Environment, Status: "creating", AmountMinor: d.Invoice.AmountMinor, Currency: d.Invoice.Currency, IdempotencyKey: key, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(35 * time.Minute)})
		addEvent(d, paymentID, "payment.created", "provider order creation reserved", now)
		return nil
	})
	if err != nil {
		return d, "", err
	}
	p := payment(&d, paymentID)
	if p.Status != "creating" {
		return d, paymentID, nil
	}
	if !s.now().Before(p.ExpiresAt) {
		return d, paymentID, fmt.Errorf("%w: expired uncertain payment requires provider reconciliation, not a new order", ErrInvalid)
	}
	// Provider requests use the durable payment ID, including retries after a crash.
	result, err := provider.Create(ctx, s.order(d, *p))
	if err != nil {
		if payments.DefinitiveRejection(err) {
			_, _ = s.mutate(ctx, account, invoiceID, func(d *protocol.BillingDocument) error {
				p := payment(d, paymentID)
				if p == nil {
					return ErrNotFound
				}
				if p.Status == "creating" {
					p.Status = "failed"
					p.LastError = "Provider rejected checkout; check merchant configuration and amount limits"
					addEvent(d, paymentID, "payment.rejected", p.LastError, s.now())
				}
				return nil
			})
			return d, paymentID, err
		}
		s.noteError(ctx, account, invoiceID, paymentID, "Gateway result uncertain; reconcile before retrying")
		return d, paymentID, err
	}
	d, err = s.Apply(ctx, d.Invoice.AccountID, d.Invoice.ID, paymentID, result, "checkout")
	return d, paymentID, err
}
func (s *Service) order(d protocol.BillingDocument, p protocol.BillingPayment) payments.Order {
	return payments.Order{PaymentID: p.ID, ProviderOrderID: p.ProviderOrderID, Description: "Agent Continuity " + d.Invoice.Number, Currency: p.Currency, AmountMinor: p.AmountMinor, ReturnURL: s.Link(p), NotifyURL: s.BaseURL + "/v1/payments/webhooks/" + p.Provider, ExpiresAt: p.ExpiresAt}
}
func (s *Service) Link(p protocol.BillingPayment) string {
	return s.BaseURL + "/pay/" + p.ID + "?token=" + url.QueryEscape(s.linkToken(p))
}
func (s *Service) linkToken(p protocol.BillingPayment) string {
	mac := hmac.New(sha256.New, s.signingKey)
	fmt.Fprintf(mac, "payment-link:v1:%s:%s:%d", p.Provider, p.ID, p.ExpiresAt.Unix())
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func (s *Service) PublicPayment(ctx context.Context, id, token string) (protocol.BillingDocument, protocol.BillingPayment, error) {
	for _, info := range s.Providers.List() {
		if !info.Enabled {
			continue
		}
		d, err := s.Repo.FindPayment(ctx, info.ID, id)
		if err != nil {
			continue
		}
		p := payment(&d, id)
		if p != nil && !p.LinkRevoked && s.now().Before(p.ExpiresAt) && hmac.Equal([]byte(token), []byte(s.linkToken(*p))) {
			return d, *p, nil
		}
	}
	return protocol.BillingDocument{}, protocol.BillingPayment{}, ErrNotFound
}
func (s *Service) noteError(ctx context.Context, account, invoice, id, message string) {
	_, _ = s.mutate(ctx, account, invoice, func(d *protocol.BillingDocument) error {
		p := payment(d, id)
		if p == nil {
			return ErrNotFound
		}
		p.LastError = message
		p.ReconcileAttempts++
		if p.ReconcileAttempts >= 20 {
			p.LastError = "Automatic reconciliation paused after 20 attempts; operator query required"
		}
		now := s.now().UTC()
		p.LastCheckedAt = &now
		return nil
	})
}
func (s *Service) Reconcile(ctx context.Context, account, invoice, id string, capture bool) (protocol.BillingDocument, error) {
	d, err := s.Repo.GetBill(ctx, account, invoice)
	if err != nil {
		return d, err
	}
	p := payment(&d, id)
	if p == nil {
		return d, ErrNotFound
	}
	if p.Provider == "demo" {
		return d, nil
	}
	provider, err := s.Providers.Get(p.Provider)
	if err != nil {
		return d, err
	}
	r, err := provider.Query(ctx, s.order(d, *p))
	if err != nil {
		s.noteError(ctx, account, invoice, id, "Provider query failed; payment remains unchanged")
		return d, err
	}
	if r.Status == "approved" && capture {
		r, err = provider.Capture(ctx, s.order(d, *p))
		if err != nil {
			s.noteError(ctx, account, invoice, id, "Capture outcome uncertain; query again with the same payment ID")
			return d, err
		}
	}
	return s.Apply(ctx, account, invoice, id, r, "reconciliation")
}
func (s *Service) Apply(ctx context.Context, account, invoice, id string, r payments.Result, source string) (protocol.BillingDocument, error) {
	return s.mutate(ctx, account, invoice, func(d *protocol.BillingDocument) error {
		p := payment(d, id)
		if p == nil {
			return ErrNotFound
		}
		if r.PaymentID != "" && r.PaymentID != p.ID || r.OrderID == "" || p.ProviderOrderID != "" && r.OrderID != p.ProviderOrderID || r.AmountMinor != p.AmountMinor || r.Currency != p.Currency {
			return fmt.Errorf("%w: payment order, amount or currency mismatch", ErrInvalid)
		}
		if !slices.Contains([]string{"pending", "approved", "succeeded", "failed", "canceled", "expired", "review"}, r.Status) {
			return fmt.Errorf("%w: unknown provider status", ErrInvalid)
		}
		if r.Status == "succeeded" && r.TransactionID == "" {
			return fmt.Errorf("%w: successful capture transaction ID required", ErrInvalid)
		}
		if r.EventID != "" {
			for _, e := range d.Events {
				if e.Kind == "webhook" && e.Detail == r.EventID && e.PaymentID == id {
					return nil
				}
			}
		}
		if len(d.Events) > 500 {
			return fmt.Errorf("%w: payment audit history limit requires operator review", ErrInvalid)
		}
		now := s.now().UTC()
		p.LastCheckedAt = &now
		p.LastError = ""
		p.ReconcileAttempts++
		p.UpdatedAt = now
		p.ProviderOrderID = r.OrderID
		if r.CheckoutURL != "" {
			u, err := url.Parse(r.CheckoutURL)
			if err != nil || u.Scheme != "https" || u.User != nil {
				return fmt.Errorf("%w: unsafe provider checkout URL", ErrInvalid)
			}
			p.CheckoutURL = r.CheckoutURL
		}
		if r.QRCode != "" {
			p.QRCode = r.QRCode
		}
		previous := p.Status
		if slices.Contains([]string{"failed", "canceled", "expired"}, previous) && (r.Status == "pending" || r.Status == "approved") {
			return nil
		}
		// Never regress a final success on stale pending/expired notifications.
		if p.Status == "succeeded" && r.Status != "review" {
			if r.Status == "succeeded" && p.ProviderTransactionID != r.TransactionID {
				return ErrConflict
			}
			return nil
		}
		if p.Status == "review" {
			return nil
		}
		if r.Status == "succeeded" {
			if d.Invoice.Status != "open" && d.Invoice.Status != "paid" && d.Invoice.Status != "disputed" {
				return ErrConflict
			}
			p.ProviderTransactionID = r.TransactionID
			if len(d.Receipts) > 0 {
				d.Invoice.Status = "disputed"
				addEvent(d, id, "payment.duplicate_capture", "Additional capture detected; operator refund review required", now)
			} else {
				d.Invoice.Status = "paid"
			}
			paidAt := r.PaidAt
			if paidAt.IsZero() {
				paidAt = now
			}
			d.Receipts = append(d.Receipts, protocol.PaymentReceipt{ID: payments.NewID("rcpt_"), InvoiceID: invoice, PaymentID: id, Provider: p.Provider, ProviderTransactionID: r.TransactionID, AmountMinor: p.AmountMinor, Currency: p.Currency, PaidAt: paidAt, Environment: p.Environment, Kind: "payment_receipt_not_tax_invoice"})
		}
		if r.Status == "review" {
			if previous != "succeeded" {
				return ErrConflict
			}
			d.Invoice.Status = "disputed"
		}
		if previous == "approved" && r.Status == "pending" {
			return nil
		}
		p.Status = r.Status
		if previous != p.Status {
			addEvent(d, id, "payment."+p.Status, source, now)
		}
		if r.EventID != "" {
			addEvent(d, id, "webhook", r.EventID, now)
		}
		return nil
	})
}
func (s *Service) Notify(ctx context.Context, provider string, h map[string][]string, body []byte) (protocol.BillingDocument, error) {
	p, err := s.Providers.Get(provider)
	if err != nil {
		return protocol.BillingDocument{}, err
	}
	r, err := p.VerifyNotification(ctx, h, body)
	if err != nil {
		return protocol.BillingDocument{}, err
	}
	ref := r.PaymentID
	if ref == "" {
		ref = r.OrderID
	}
	if ref == "" {
		return protocol.BillingDocument{}, ErrInvalid
	}
	d, err := s.Repo.FindPayment(ctx, p.Info().ID, ref)
	if err != nil {
		return d, err
	}
	id := ""
	for _, v := range d.Payments {
		if v.Provider == p.Info().ID && (v.ID == ref || v.ProviderOrderID == ref) {
			id = v.ID
		}
	}
	if id == "" {
		return d, ErrNotFound
	}
	if r.Status == "approved" {
		return s.Reconcile(ctx, d.Invoice.AccountID, d.Invoice.ID, id, true)
	}
	return s.Apply(ctx, d.Invoice.AccountID, d.Invoice.ID, id, r, "verified callback")
}
func (s *Service) Simulate(ctx context.Context, account, invoice, id, outcome string) (protocol.BillingDocument, error) {
	d, err := s.Repo.GetBill(ctx, account, invoice)
	if err != nil {
		return d, err
	}
	p := payment(&d, id)
	if p == nil {
		return d, ErrNotFound
	}
	if p.Provider != "demo" || p.Environment != "simulation" || !d.Invoice.TestMode {
		return d, ErrInvalid
	}
	if !slices.Contains([]string{"succeeded", "failed", "canceled"}, outcome) {
		return d, ErrInvalid
	}
	return s.Apply(ctx, account, invoice, id, payments.Result{PaymentID: id, OrderID: p.ProviderOrderID, TransactionID: "demo_tx_" + id, Status: outcome, AmountMinor: p.AmountMinor, Currency: p.Currency, EventID: "demo_" + id + "_" + outcome}, "simulation (no funds moved)")
}
func (s *Service) RevokeLink(ctx context.Context, account, invoice, id string) (protocol.BillingDocument, error) {
	return s.mutate(ctx, account, invoice, func(d *protocol.BillingDocument) error {
		p := payment(d, id)
		if p == nil {
			return ErrNotFound
		}
		if !p.LinkRevoked {
			p.LinkRevoked = true
			addEvent(d, id, "payment.link_revoked", "Link disabled; gateway order is NOT canceled", s.now())
		}
		return nil
	})
}
func (s *Service) Void(ctx context.Context, account, invoice string) (protocol.BillingDocument, error) {
	return s.mutate(ctx, account, invoice, func(d *protocol.BillingDocument) error {
		if d.Invoice.Status != "open" || len(d.Payments) > 0 {
			return fmt.Errorf("%w: only unpaid invoices without payment attempts can be voided", ErrInvalid)
		}
		d.Invoice.Status = "void"
		addEvent(d, "", "invoice.void", "No payment attempt existed", s.now())
		return nil
	})
}

// Run performs bounded recovery of missed notifications. No real network is used
// by the simulator. Provider idempotency and invoice CAS make replica overlap safe.
func (s *Service) Run(ctx context.Context, logger *slog.Logger) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			targets, err := s.Repo.DuePayments(ctx, s.now().Add(-2*time.Minute), 10)
			if err != nil {
				logger.Warn("payment reconciliation scan failed")
				continue
			}
			for _, target := range targets {
				if ctx.Err() != nil {
					return
				}
				d, err := s.mutate(ctx, target.AccountID, target.InvoiceID, func(d *protocol.BillingDocument) error {
					p := payment(d, target.PaymentID)
					if p == nil || p.ReconcileAttempts >= 20 || p.LastCheckedAt != nil && p.LastCheckedAt.After(s.now().Add(-2*time.Minute)) {
						return ErrInvalid
					}
					now := s.now().UTC()
					p.LastCheckedAt = &now
					return nil
				})
				if err != nil {
					continue
				}
				p := payment(&d, target.PaymentID)
				callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
				if p.Status == "creating" && p.ProviderOrderID == "" && s.now().Before(p.ExpiresAt) {
					_, _, err = s.CreatePayment(callCtx, target.AccountID, target.InvoiceID, p.Provider, p.IdempotencyKey)
				} else {
					_, err = s.Reconcile(callCtx, target.AccountID, target.InvoiceID, p.ID, true)
				}
				cancel()
				if err != nil {
					logger.Warn("payment reconciliation requires retry or review", "payment_id", p.ID, "provider", p.Provider)
				}
			}
		}
	}
}
