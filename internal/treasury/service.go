package treasury

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

var (
	ErrNotFound     = errors.New("treasury record not found")
	ErrConflict     = errors.New("treasury conflict; refresh and retry")
	ErrInvalid      = errors.New("invalid treasury operation")
	ErrInsufficient = errors.New("insufficient settled available balance")
	ErrUnavailable  = errors.New("this payout route is not configured or supported")
)

const MaxAmountMinor int64 = 100_000_000

type Repository interface {
	ListMerchantBills(context.Context, int) ([]protocol.BillingDocument, error)
	LoadTreasury(context.Context, string) (protocol.TreasuryDocument, error)
	SaveTreasury(context.Context, protocol.TreasuryDocument, int64) error
	GetBill(context.Context, string, string) (protocol.BillingDocument, error)
	GetBillByID(context.Context, string) (protocol.BillingDocument, error)
}

type Service struct {
	Repo   Repository
	Owner  string
	Demo   bool
	Stripe *Stripe
	Now    func() time.Time
}

func New(repo Repository, owner string, demo bool, stripe *Stripe) *Service {
	return &Service{Repo: repo, Owner: owner, Demo: demo, Stripe: stripe, Now: time.Now}
}

func id(prefix, value string) string {
	sum := sha256.Sum256([]byte(value))
	return prefix + hex.EncodeToString(sum[:12])
}

func (s *Service) Load(ctx context.Context) (protocol.TreasuryDocument, error) {
	if s.Owner == "" {
		return protocol.TreasuryDocument{}, ErrUnavailable
	}
	d, err := s.Repo.LoadTreasury(ctx, s.Owner)
	if errors.Is(err, ErrNotFound) {
		return protocol.TreasuryDocument{OwnerAccountID: s.Owner, Destinations: []protocol.TreasuryDestination{}, Collections: []protocol.TreasuryCollection{}, Payouts: []protocol.TreasuryPayout{}, Events: []protocol.TreasuryEvent{}}, nil
	}
	if err == nil {
		err = s.projectRefunds(ctx, &d)
	}
	return d, err
}

// Refund evidence remains authoritative in the invoice aggregate. A version
// guard at treasury commit makes concurrent payout reservations retry.
func (s *Service) projectRefunds(ctx context.Context, d *protocol.TreasuryDocument) error {
	for i := range d.Collections {
		c := &d.Collections[i]
		bill, err := s.Repo.GetBillByID(ctx, c.InvoiceID)
		if err != nil {
			return err
		}
		c.BillingVersion = bill.Version
		c.RefundedMinor = 0
		c.RefundHeldMinor = 0
		for _, r := range bill.Refunds {
			if r.PaymentID != c.PaymentID {
				continue
			}
			switch r.Status {
			case "succeeded":
				c.RefundedMinor += r.AmountMinor
			case "rejected", "failed":
			default:
				c.RefundHeldMinor += r.AmountMinor
			}
		}
	}
	return nil
}

func (s *Service) mutate(ctx context.Context, fn func(*protocol.TreasuryDocument) error) (protocol.TreasuryDocument, error) {
	for i := 0; i < 12; i++ {
		if err := ctx.Err(); err != nil {
			return protocol.TreasuryDocument{}, err
		}
		d, err := s.Load(ctx)
		if err != nil {
			return d, err
		}
		billVersions := map[string]int64{}
		for _, c := range d.Collections {
			billVersions[c.ID] = c.BillingVersion
		}
		if err = fn(&d); err != nil {
			return d, err
		}
		if err = s.projectRefunds(ctx, &d); err != nil {
			return d, err
		}
		changed := false
		for _, c := range d.Collections {
			if v, ok := billVersions[c.ID]; ok && v != c.BillingVersion {
				changed = true
			}
		}
		if changed {
			continue
		}
		if len(d.Collections) > 5000 || len(d.Payouts) > 5000 || len(d.Events) > 50000 {
			return d, fmt.Errorf("%w: operator ledger capacity reached; archive before further writes", ErrInvalid)
		}
		err = s.Repo.SaveTreasury(ctx, d, d.Version)
		if errors.Is(err, ErrConflict) {
			continue
		}
		if err == nil {
			d.Version++
		}
		return d, err
	}
	return protocol.TreasuryDocument{}, ErrConflict
}

func (s *Service) event(d *protocol.TreasuryDocument, resource, kind, actor, detail string) {
	t := s.Now().UTC()
	d.Events = append(d.Events, protocol.TreasuryEvent{ID: id("tev_", fmt.Sprintf("%s/%d/%d/%s", s.Owner, d.Version, len(d.Events), resource)), ResourceID: resource, Kind: kind, Actor: actor, Detail: detail, CreatedAt: t})
}

func Balances(d protocol.TreasuryDocument) []protocol.TreasuryBalance {
	by := map[string]*protocol.TreasuryBalance{}
	get := func(c string) *protocol.TreasuryBalance {
		if by[c] == nil {
			by[c] = &protocol.TreasuryBalance{Currency: c, Environment: "simulation"}
		}
		return by[c]
	}
	for _, c := range d.Collections {
		if c.Environment != "simulation" {
			continue
		}
		b := get(c.Currency)
		b.GrossMinor += c.GrossMinor
		b.FeeMinor += c.FeeMinor
		b.RefundedMinor += c.RefundedMinor
		b.RefundHeldMinor += c.RefundHeldMinor
		if c.Status == "settled" {
			b.AvailableMinor += c.NetMinor - c.RefundedMinor - c.RefundHeldMinor
		} else {
			b.PendingMinor += c.NetMinor - c.RefundedMinor - c.RefundHeldMinor
		}
	}
	for _, p := range d.Payouts {
		if p.Environment != "simulation" {
			continue
		}
		b := get(p.Currency)
		switch p.Status {
		case "requested", "approved", "submitting", "processing":
			b.AvailableMinor -= p.AmountMinor
			b.ReservedMinor += p.AmountMinor
		case "paid":
			b.AvailableMinor -= p.AmountMinor
			b.PaidMinor += p.AmountMinor
		case "returned":
			b.ReturnedMinor += p.AmountMinor
		}
	}
	out := []protocol.TreasuryBalance{}
	for _, c := range []string{"CNY", "USD", "EUR"} {
		if b := by[c]; b != nil {
			out = append(out, *b)
		}
	}
	return out
}

func available(d protocol.TreasuryDocument, currency string) int64 {
	for _, b := range Balances(d) {
		if b.Currency == currency {
			return b.AvailableMinor
		}
	}
	return 0
}

// ImportDemo uses a verified local payment receipt, never an amount supplied by the browser.
// Real merchant revenue cannot be inferred from a payer's invoice or demo ledger.
func (s *Service) ImportDemo(ctx context.Context, invoice, actor string) (protocol.TreasuryDocument, error) {
	if !s.Demo {
		return protocol.TreasuryDocument{}, ErrUnavailable
	}
	// Only treasuryAuth can reach this operator action. Payer identity is not
	// the merchant identity; tenant payments belong to the same configured merchant.
	bill, err := s.Repo.GetBillByID(ctx, invoice)
	if err != nil {
		return protocol.TreasuryDocument{}, ErrNotFound
	}
	if !bill.Invoice.TestMode || bill.Invoice.Status != "paid" || len(bill.Receipts) != 1 {
		return protocol.TreasuryDocument{}, fmt.Errorf("%w: one verified paid demo receipt is required", ErrInvalid)
	}
	r := bill.Receipts[0]
	if r.Provider != "demo" || r.Environment != "simulation" || r.AmountMinor <= 0 || r.AmountMinor > MaxAmountMinor || r.AmountMinor != bill.Invoice.AmountMinor || r.Currency != bill.Invoice.Currency {
		return protocol.TreasuryDocument{}, ErrInvalid
	}
	if !slices.Contains([]string{"CNY", "USD", "EUR"}, r.Currency) {
		return protocol.TreasuryDocument{}, ErrInvalid
	}
	confirmed := false
	for _, p := range bill.Payments {
		if p.ID == r.PaymentID && p.Status == "succeeded" && p.Provider == "demo" && p.ProviderTransactionID == r.ProviderTransactionID {
			confirmed = true
		}
	}
	if !confirmed {
		return protocol.TreasuryDocument{}, ErrInvalid
	}
	return s.mutate(ctx, func(d *protocol.TreasuryDocument) error {
		for _, c := range d.Collections {
			if c.ReceiptID == r.ID {
				return nil
			}
		}
		fee := r.AmountMinor * 60 / 10000 // Explicit simulation-only 0.6% fee, rounded down to minor units.
		c := protocol.TreasuryCollection{ID: id("col_", r.ID), InvoiceID: invoice, InvoiceNumber: bill.Invoice.Number, PaymentID: r.PaymentID, ReceiptID: r.ID, ProviderTransactionID: r.ProviderTransactionID, Currency: r.Currency, GrossMinor: r.AmountMinor, FeeMinor: fee, NetMinor: r.AmountMinor - fee, Status: "pending", Environment: "simulation", ReceivedAt: r.PaidAt}
		d.Collections = append(d.Collections, c)
		s.event(d, c.ID, "collection.received", actor, "Verified demo payment imported; settlement is still pending.")
		return nil
	})
}

func (s *Service) SettleDemo(ctx context.Context, collection, actor string) (protocol.TreasuryDocument, error) {
	if !s.Demo {
		return protocol.TreasuryDocument{}, ErrUnavailable
	}
	return s.mutate(ctx, func(d *protocol.TreasuryDocument) error {
		for i := range d.Collections {
			c := &d.Collections[i]
			if c.ID != collection {
				continue
			}
			if c.Environment != "simulation" {
				return ErrInvalid
			}
			if c.Status == "settled" {
				return nil
			}
			if c.Status != "pending" {
				return ErrConflict
			}
			t := s.Now().UTC()
			c.Status = "settled"
			c.SettledAt = &t
			s.event(d, c.ID, "collection.settled", actor, "Simulated channel settlement made net funds available.")
			return nil
		}
		return ErrNotFound
	})
}

func (s *Service) AddDemoDestination(ctx context.Context, kind, label, currency, actor string) (protocol.TreasuryDocument, error) {
	if !s.Demo {
		return protocol.TreasuryDocument{}, ErrUnavailable
	}
	label = strings.TrimSpace(label)
	if !slices.Contains([]string{"bank", "alipay", "paypal"}, kind) || !slices.Contains([]string{"CNY", "USD", "EUR"}, currency) || label == "" || len(label) > 120 {
		return protocol.TreasuryDocument{}, ErrInvalid
	}
	// These are virtual fixtures, not a claim that a bank/KYC verification took place.
	return s.mutate(ctx, func(d *protocol.TreasuryDocument) error {
		key := id("dst_", kind+"/"+currency+"/"+label)
		for _, x := range d.Destinations {
			if x.ID == key {
				return nil
			}
		}
		if len(d.Destinations) >= 20 {
			return ErrInvalid
		}
		d.Destinations = append(d.Destinations, protocol.TreasuryDestination{ID: key, Label: label, Kind: kind, Currency: currency, MaskedAccount: "DEMO · **** " + key[len(key)-4:], Environment: "simulation", Verified: false})
		s.event(d, key, "destination.registered", actor, "Virtual own-account destination; no real account details collected.")
		return nil
	})
}

func (s *Service) VerifyDemoDestination(ctx context.Context, destination, actor string) (protocol.TreasuryDocument, error) {
	if !s.Demo {
		return protocol.TreasuryDocument{}, ErrUnavailable
	}
	return s.mutate(ctx, func(d *protocol.TreasuryDocument) error {
		for i := range d.Destinations {
			x := &d.Destinations[i]
			if x.ID != destination {
				continue
			}
			if x.Environment != "simulation" {
				return ErrInvalid
			}
			if x.Verified {
				return nil
			}
			x.Verified = true
			s.event(d, x.ID, "destination.demo_verified", actor, "Simulated ownership verification only, not bank/KYC verification.")
			return nil
		}
		return ErrNotFound
	})
}

type Request struct {
	DestinationID string `json:"destination_id"`
	AmountMinor   int64  `json:"amount_minor"`
	Currency      string `json:"currency"`
	Provider      string `json:"provider"`
}

func (s *Service) RequestPayout(ctx context.Context, input Request, key, actor string) (protocol.TreasuryDocument, error) {
	if input.AmountMinor <= 0 || input.AmountMinor > MaxAmountMinor || len(key) < 8 || len(key) > 120 || actor == "" {
		return protocol.TreasuryDocument{}, ErrInvalid
	}
	// Read an existing logical request before consulting the channel: retries must
	// still work when its bank changes or the provider is temporarily unavailable.
	existing, err := s.Load(ctx)
	if err != nil {
		return existing, err
	}
	for _, p := range existing.Payouts {
		if p.IdempotencyKey == key {
			if p.AmountMinor != input.AmountMinor || p.Currency != input.Currency || p.Destination.ID != input.DestinationID || p.Provider != input.Provider {
				return existing, ErrConflict
			}
			return existing, nil
		}
	}
	var destination protocol.TreasuryDestination
	var funds int64
	var snapshotVersion int64
	if input.Provider == "stripe" {
		if s.Stripe == nil {
			return protocol.TreasuryDocument{}, ErrUnavailable
		}
		before, err := s.Load(ctx)
		if err != nil {
			return before, err
		}
		snapshotVersion = before.Version
		snap, err := s.Stripe.Snapshot(ctx, input.Currency)
		if err != nil {
			return protocol.TreasuryDocument{}, err
		}
		destination = snap.Destination
		funds = snap.AvailableMinor
		if destination.ID != input.DestinationID {
			return protocol.TreasuryDocument{}, ErrInvalid
		}
	} else if input.Provider != "demo" || !s.Demo {
		return protocol.TreasuryDocument{}, ErrUnavailable
	}
	return s.mutate(ctx, func(d *protocol.TreasuryDocument) error {
		remaining := funds
		for _, p := range d.Payouts {
			if p.IdempotencyKey == key {
				if p.AmountMinor != input.AmountMinor || p.Currency != input.Currency || p.Destination.ID != input.DestinationID || p.Provider != input.Provider {
					return ErrConflict
				}
				return nil
			}
		}
		if input.Provider == "stripe" && d.Version != snapshotVersion {
			return ErrConflict
		}
		if input.Provider == "demo" {
			for _, x := range d.Destinations {
				if x.ID == input.DestinationID {
					destination = x
				}
			}
			remaining = available(*d, input.Currency)
		} else {
			for _, p := range d.Payouts {
				if p.Provider == "stripe" && p.Environment == destination.Environment && p.Currency == input.Currency && slices.Contains([]string{"requested", "approved", "submitting", "processing"}, p.Status) {
					remaining -= p.AmountMinor
				}
			}
		}
		if destination.ID == "" || !destination.Verified || destination.Currency != input.Currency {
			return fmt.Errorf("%w: verified own-account destination and matching currency required", ErrInvalid)
		}
		if input.AmountMinor > remaining {
			return ErrInsufficient
		}
		t := s.Now().UTC()
		p := protocol.TreasuryPayout{ID: id("po_", s.Owner+"/"+key), Destination: destination, AmountMinor: input.AmountMinor, Currency: input.Currency, Provider: input.Provider, Environment: destination.Environment, Status: "requested", IdempotencyKey: key, RequestedBy: actor, CreatedAt: t, UpdatedAt: t}
		d.Payouts = append(d.Payouts, p)
		s.event(d, p.ID, "payout.requested", actor, "Funds reserved; destination and amount frozen for approval.")
		return nil
	})
}

func (s *Service) Decide(ctx context.Context, payout, decision, actor string) (protocol.TreasuryDocument, error) {
	if !slices.Contains([]string{"approve", "reject", "cancel"}, decision) {
		return protocol.TreasuryDocument{}, ErrInvalid
	}
	return s.mutate(ctx, func(d *protocol.TreasuryDocument) error {
		for i := range d.Payouts {
			p := &d.Payouts[i]
			if p.ID != payout {
				continue
			}
			target := map[string]string{"approve": "approved", "reject": "rejected", "cancel": "canceled"}[decision]
			if p.Status == target {
				return nil
			}
			if p.Status != "requested" && !(decision == "cancel" && p.Status == "approved") {
				return ErrConflict
			}
			if decision == "approve" {
				if p.Environment == "live" && actor == p.RequestedBy {
					return fmt.Errorf("%w: live payouts require a different approving subject", ErrInvalid)
				}
				p.ApprovedBy = actor
			}
			p.Status = target
			p.UpdatedAt = s.Now().UTC()
			s.event(d, p.ID, "payout."+target, actor, "Approval decision; rejected/canceled requests release their reservation.")
			return nil
		}
		return ErrNotFound
	})
}

func (s *Service) Submit(ctx context.Context, payout, actor string) (protocol.TreasuryDocument, error) {
	var claimed protocol.TreasuryPayout
	d, err := s.mutate(ctx, func(d *protocol.TreasuryDocument) error {
		for i := range d.Payouts {
			p := &d.Payouts[i]
			if p.ID != payout {
				continue
			}
			if p.Status != "approved" {
				return ErrConflict
			}
			if p.Provider == "demo" && available(*d, p.Currency) < 0 {
				return ErrInsufficient
			}
			if (p.Provider == "stripe" && s.Stripe == nil) || (p.Provider == "demo" && !s.Demo) || (p.Provider != "demo" && p.Provider != "stripe") {
				return ErrUnavailable
			}
			t := s.Now().UTC()
			p.Status = "submitting"
			p.SubmittedAt = &t
			p.UpdatedAt = t
			claimed = *p
			s.event(d, p.ID, "payout.submitting", actor, "Submitting the approved immutable payout with its original idempotency key.")
			return nil
		}
		return ErrNotFound
	})
	if err != nil {
		return d, err
	}
	if claimed.Provider == "demo" {
		return s.Apply(ctx, payout, Result{ID: "demo_" + payout, Status: "processing", AmountMinor: claimed.AmountMinor, Currency: claimed.Currency, DestinationID: claimed.Destination.ID}, actor)
	}
	r, err := s.Stripe.Create(ctx, claimed)
	if err != nil {
		s.recordError(ctx, payout, actor)
		return d, err
	}
	return s.Apply(ctx, payout, r, actor)
}

func (s *Service) recordError(ctx context.Context, payout, actor string) {
	_, _ = s.mutate(ctx, func(d *protocol.TreasuryDocument) error {
		for i := range d.Payouts {
			p := &d.Payouts[i]
			if p.ID == payout && slices.Contains([]string{"submitting", "processing"}, p.Status) {
				p.LastError = "Channel result unknown. Funds remain reserved; query/retry the original payout only."
				s.event(d, p.ID, "payout.uncertain", actor, p.LastError)
				return nil
			}
		}
		return nil
	})
}

func (s *Service) Reconcile(ctx context.Context, payout, actor string) (protocol.TreasuryDocument, error) {
	d, err := s.Load(ctx)
	if err != nil {
		return d, err
	}
	var p protocol.TreasuryPayout
	for _, x := range d.Payouts {
		if x.ID == payout {
			p = x
		}
	}
	if p.ID == "" {
		return d, ErrNotFound
	}
	if p.Provider != "stripe" || s.Stripe == nil {
		return d, ErrUnavailable
	}
	if !slices.Contains([]string{"submitting", "processing", "paid"}, p.Status) {
		return d, ErrConflict
	}
	var r Result
	if p.ProviderPayoutID == "" {
		if p.SubmittedAt == nil || s.Now().Sub(*p.SubmittedAt) > 23*time.Hour {
			return d, fmt.Errorf("%w: idempotency safety window elapsed; inspect channel dashboard, never recreate", ErrInvalid)
		}
		r, err = s.Stripe.Create(ctx, p)
	} else {
		r, err = s.Stripe.Query(ctx, p)
	}
	if err != nil {
		return d, err
	}
	return s.Apply(ctx, payout, r, actor)
}

func (s *Service) Simulate(ctx context.Context, payout, outcome, actor string) (protocol.TreasuryDocument, error) {
	if !s.Demo || !slices.Contains([]string{"paid", "failed", "returned"}, outcome) {
		return protocol.TreasuryDocument{}, ErrInvalid
	}
	d, err := s.Load(ctx)
	if err != nil {
		return d, err
	}
	for _, p := range d.Payouts {
		if p.ID == payout {
			if p.Provider != "demo" || p.Environment != "simulation" {
				return d, ErrUnavailable
			}
			return s.Apply(ctx, payout, Result{ID: "demo_" + payout, Status: outcome, AmountMinor: p.AmountMinor, Currency: p.Currency, DestinationID: p.Destination.ID}, actor)
		}
	}
	return d, ErrNotFound
}

// Apply accepts only provider-validated results or the isolated simulator, never public JSON.
func (s *Service) Apply(ctx context.Context, payout string, r Result, actor string) (protocol.TreasuryDocument, error) {
	return s.mutate(ctx, func(d *protocol.TreasuryDocument) error {
		for i := range d.Payouts {
			p := &d.Payouts[i]
			if p.ID != payout {
				continue
			}
			if r.ID == "" || r.AmountMinor != p.AmountMinor || r.Currency != p.Currency || r.DestinationID != p.Destination.ID || (p.ProviderPayoutID != "" && p.ProviderPayoutID != r.ID) {
				return fmt.Errorf("%w: payout result mismatch", ErrInvalid)
			}
			if !slices.Contains([]string{"processing", "paid", "failed", "returned", "canceled"}, r.Status) {
				return ErrInvalid
			}
			if p.Status == r.Status {
				return nil
			}
			if p.Status == "paid" {
				if r.Status == "processing" {
					return nil
				}
				if r.Status == "failed" || r.Status == "returned" {
					r.Status = "returned"
				} else {
					return ErrConflict
				}
			} else if !slices.Contains([]string{"submitting", "processing"}, p.Status) {
				return ErrConflict
			} else if r.Status == "returned" {
				return ErrConflict
			}
			for _, other := range d.Payouts {
				if other.ID != p.ID && other.Provider == p.Provider && other.Environment == p.Environment && other.ProviderPayoutID == r.ID {
					return ErrConflict
				}
			}
			t := s.Now().UTC()
			p.Status = r.Status
			p.ProviderPayoutID = r.ID
			p.UpdatedAt = t
			p.LastCheckedAt = &t
			p.LastError = ""
			if r.Status == "paid" {
				p.PaidAt = &t
				p.ReceiptID = id("por_", p.ID)
			}
			if r.Status == "returned" {
				p.ReturnedAt = &t
			}
			if r.Status == "failed" || r.Status == "returned" {
				p.LastError = "Channel reported failure/return; reservation released. Prior receipts remain as history."
			}
			s.event(d, p.ID, "payout."+r.Status, actor, "Provider result applied; payment success and bank arrival are separate facts.")
			return nil
		}
		return ErrNotFound
	})
}
