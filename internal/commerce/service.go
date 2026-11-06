package commerce

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/agent-continuity/agent-continuity-master/internal/billing"
	"github.com/agent-continuity/agent-continuity-master/internal/operations"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
	"strings"
	"time"
)

type Repository interface {
	operations.Repository
	billing.Repository
}
type Service struct {
	Repo       Repository
	Gateway    Gateway
	Simulation bool
}
type Payout struct {
	Effect Effect `json:"request"`
	Result Result `json:"result"`
}
type Settlement struct {
	TransactionID       string   `json:"transaction_id"`
	InvoiceID           string   `json:"invoice_id"`
	Buyer               string   `json:"buyer"`
	Seller              string   `json:"seller"`
	Currency            string   `json:"currency"`
	Gross               int64    `json:"gross_minor"`
	Commission          int64    `json:"commission_minor"`
	Refunded            int64    `json:"refunded_minor"`
	Held                int64    `json:"held_minor"`
	SellerEntitlement   int64    `json:"seller_entitlement_minor"`
	PlatformEntitlement int64    `json:"platform_entitlement_minor"`
	Available           int64    `json:"available_minor"`
	Debt                int64    `json:"debt_minor"`
	Collection          Effect   `json:"collection"`
	CollectionResult    Result   `json:"collection_result"`
	Payouts             []Payout `json:"payouts"`
	Reversals           []Payout `json:"reversals"`
	History             []string `json:"history"`
}

func (s *Service) CreateSettlement(ctx context.Context, t protocol.MarketplaceTransaction, d protocol.BillingDocument) (Settlement, error) {
	if s.Gateway == nil || t.ID == "" || t.Status != "authorized" && t.Status != "completed" || d.Invoice.SourceID != t.ID || d.Invoice.SourceKind != "marketplace" || d.Invoice.AccountID != t.BuyerAccountID || d.Invoice.AmountMinor != t.AmountMinor || d.Invoice.Currency != t.Currency || d.Invoice.TestMode != s.Simulation || t.CommissionMinor < 0 || t.CommissionMinor > t.AmountMinor {
		return Settlement{}, operations.ErrInvalid
	}
	if !paid(d) {
		return Settlement{}, fmt.Errorf("%w: verified successful payment required", operations.ErrInvalid)
	}
	old, e := s.Repo.GetOperation(ctx, "settlement", t.ID)
	if e == nil {
		var v Settlement
		e = json.Unmarshal(old.Data, &v)
		return v, e
	}
	if !errors.Is(e, operations.ErrNotFound) {
		return Settlement{}, e
	}
	v := Settlement{TransactionID: t.ID, InvoiceID: d.Invoice.ID, Buyer: t.BuyerAccountID, Seller: t.SellerAccountID, Currency: t.Currency, Gross: t.AmountMinor, Commission: t.CommissionMinor, Payouts: []Payout{}, History: []string{"verified_payment"}}
	v.Collection = Effect{ID: "split:" + t.ID, Kind: "split", Account: t.SellerAccountID, Amount: t.AmountMinor - t.CommissionMinor, Currency: t.Currency, Reference: d.Receipts[0].ProviderTransactionID, Fields: map[string]string{"invoice_id": d.Invoice.ID, "commission_minor": fmt.Sprint(t.CommissionMinor)}}
	project(&v, d)
	o := settlementRecord(v, "pending")
	e = s.Repo.SaveOperation(ctx, o, 0, []operations.BillGuard{{ID: d.Invoice.ID, Version: d.Version}})
	if errors.Is(e, operations.ErrConflict) {
		return Settlement{}, operations.ErrConflict
	}
	return v, e
}
func paid(d protocol.BillingDocument) bool {
	for _, r := range d.Receipts {
		if r.AmountMinor == d.Invoice.AmountMinor && r.Currency == d.Invoice.Currency && r.ProviderTransactionID != "" {
			for _, p := range d.Payments {
				if p.ID == r.PaymentID && p.Status == "succeeded" {
					return true
				}
			}
		}
	}
	return false
}
func project(v *Settlement, d protocol.BillingDocument) {
	v.Refunded = 0
	v.Held = 0
	for _, r := range d.Refunds {
		switch r.Status {
		case "succeeded":
			v.Refunded += r.AmountMinor
		case "rejected", "failed":
		default:
			v.Held += r.AmountMinor
		}
	}
	// Cumulative allocation avoids rounding drift over many partial refunds.
	v.PlatformEntitlement = v.Commission - (v.Refunded * v.Commission / v.Gross)
	v.SellerEntitlement = v.Gross - v.Refunded - v.PlatformEntitlement
	available := v.SellerEntitlement - v.Held
	for _, p := range v.Payouts {
		if p.Result.Status != "failed" {
			available -= p.Effect.Amount
		}
	}
	if v.CollectionResult.Status != "succeeded" {
		available = 0
	}
	v.Available = max(0, available)
	v.Debt = max(0, -available)
}
func settlementRecord(v Settlement, state string) operations.Record {
	return operations.Record{Kind: "settlement", ID: v.TransactionID, Owner: v.Buyer, Peer: v.Seller, Status: state, Due: time.Now().Add(time.Minute), Data: operations.Encode(v)}
}
func (s *Service) Load(ctx context.Context, actor, id string) (Settlement, error) {
	o, err := s.Repo.GetOperation(ctx, "settlement", id)
	if err != nil {
		return Settlement{}, err
	}
	if o.Owner != actor && o.Peer != actor {
		return Settlement{}, operations.ErrNotFound
	}
	var v Settlement
	if err = json.Unmarshal(o.Data, &v); err != nil {
		return v, err
	}
	d, err := s.Repo.GetBill(ctx, v.Buyer, v.InvoiceID)
	if err == nil {
		project(&v, d)
	}
	return v, err
}

// Withdraw reserves funds before making an external call. Concurrent refunds are
// protected by BillGuard; a later refund produces explicit seller debt.
func (s *Service) Withdraw(ctx context.Context, seller, id, key, destination string, amount int64) (Settlement, error) {
	if len(key) < 8 || len(key) > 128 || amount <= 0 || destination == "" || len(destination) > 200 {
		return Settlement{}, operations.ErrInvalid
	}
	for attempt := 0; attempt < 8; attempt++ {
		o, e := s.Repo.GetOperation(ctx, "settlement", id)
		if e != nil || o.Peer != seller {
			return Settlement{}, operations.ErrNotFound
		}
		var v Settlement
		if e = json.Unmarshal(o.Data, &v); e != nil {
			return v, e
		}
		for _, p := range v.Payouts {
			if p.Effect.ID == operations.ID("payout", seller, id+":"+key) {
				if p.Effect.Amount != amount || p.Effect.Destination != destination {
					return v, operations.ErrConflict
				}
				return v, nil
			}
		}
		// Destination belongs to this seller and was verified by the configured gateway.
		recipient, e := s.Repo.GetOperation(ctx, "recipient", operations.ID("recipient", seller, destination))
		if e != nil || recipient.Owner != seller || recipient.Status != "succeeded" {
			return v, operations.ErrInvalid
		}
		d, e := s.Repo.GetBill(ctx, v.Buyer, v.InvoiceID)
		if e != nil {
			return v, e
		}
		project(&v, d)
		if amount > v.Available || len(v.Payouts) >= 100 {
			return v, operations.ErrInvalid
		}
		v.Payouts = append(v.Payouts, Payout{Effect: Effect{ID: operations.ID("payout", seller, id+":"+key), Kind: "payout", Account: seller, Currency: v.Currency, Amount: amount, Reference: id, Destination: destination}, Result: Result{Status: "pending"}})
		v.History = append(v.History, "payout_reserved")
		project(&v, d)
		e = s.Repo.SaveOperation(ctx, settlementRecord(v, "pending"), o.Version, []operations.BillGuard{{ID: d.Invoice.ID, Version: d.Version}})
		if !errors.Is(e, operations.ErrConflict) {
			return v, e
		}
	}
	return Settlement{}, operations.ErrConflict
}
func (s *Service) Reconcile(ctx context.Context, actor, id string) (Settlement, error) {
	o, e := s.Repo.GetOperation(ctx, "settlement", id)
	if e != nil || o.Owner != actor && o.Peer != actor {
		return Settlement{}, operations.ErrNotFound
	}
	var v Settlement
	if e = json.Unmarshal(o.Data, &v); e != nil {
		return v, e
	}
	if s.Gateway == nil {
		return v, operations.ErrInvalid
	}
	// Side effects are independently idempotent; pending/uncertain operations remain held.
	if v.CollectionResult.Status != "succeeded" && v.CollectionResult.Status != "failed" {
		r, err := s.Gateway.Apply(ctx, v.Collection)
		if err != nil {
			return v, err
		}
		if err = validate(v.Collection, r, s.Simulation); err != nil {
			return v, err
		}
		v.CollectionResult = r
	}
	for i := range v.Payouts {
		p := &v.Payouts[i]
		if p.Result.Status == "succeeded" || p.Result.Status == "failed" {
			continue
		}
		r, err := s.Gateway.Apply(ctx, p.Effect)
		if err != nil {
			return v, err
		}
		if err = validate(p.Effect, r, s.Simulation); err != nil {
			return v, err
		}
		p.Result = r
	}
	d, e := s.Repo.GetBill(ctx, v.Buyer, v.InvoiceID)
	if e != nil {
		return v, e
	}
	project(&v, d)
	// Refund reversals are independent durable effects, identified by the refund.
	for _, rf := range d.Refunds {
		if rf.Status != "succeeded" {
			continue
		}
		found := false
		for _, rv := range v.Reversals {
			if rv.Effect.ID == "reverse:"+rf.ID {
				found = true
			}
		}
		if !found {
			v.Reversals = append(v.Reversals, Payout{Effect: Effect{ID: "reverse:" + rf.ID, Kind: "reverse_split", Account: v.Seller, Currency: v.Currency, Amount: rf.AmountMinor, Reference: v.CollectionResult.ProviderID, Fields: map[string]string{"refund_id": rf.ID, "gross_minor": fmt.Sprint(v.Gross), "commission_minor": fmt.Sprint(v.Commission)}}, Result: Result{Status: "pending"}})
		}
	}
	// Persist reversal intent before calling its provider. Reconcile resumes it later.
	newReversal := len(v.Reversals) > 0
	if newReversal {
		saved := settlementRecord(v, "pending")
		if e = s.Repo.SaveOperation(ctx, saved, o.Version, []operations.BillGuard{{ID: d.Invoice.ID, Version: d.Version}}); e != nil {
			return v, e
		}
		o.Version++
	}
	for i := range v.Reversals {
		rv := &v.Reversals[i]
		if rv.Result.Status == "succeeded" || rv.Result.Status == "failed" {
			continue
		}
		result, err := s.Gateway.Apply(ctx, rv.Effect)
		if err != nil {
			return v, err
		}
		if err = validate(rv.Effect, result, s.Simulation); err != nil {
			return v, err
		}
		rv.Result = result
	}
	state := "pending"
	if v.CollectionResult.Status == "pending" {
		state = "pending"
	}
	for _, p := range v.Payouts {
		if p.Result.Status == "pending" {
			state = "pending"
		}
	}
	v.History = append(v.History, "reconciled")
	if len(v.History) > 200 {
		v.History = v.History[len(v.History)-200:]
	}
	e = s.Repo.SaveOperation(ctx, settlementRecord(v, state), o.Version, []operations.BillGuard{{ID: d.Invoice.ID, Version: d.Version}})
	return v, e
}

// Recipient uses a provider-issued account token, never a raw bank account.
func (s *Service) Recipient(ctx context.Context, owner, token, kind string) (operations.Record, error) {
	if s.Gateway == nil || token == "" || len(token) > 200 || !strings.Contains("|bank|wechat|alipay|paypal|", "|"+kind+"|") {
		return operations.Record{}, operations.ErrInvalid
	}
	id := operations.ID("recipient", owner, token)
	o, err := s.Repo.GetOperation(ctx, "recipient", id)
	if err == nil {
		var original Effect
		if json.Unmarshal(o.Data, &original) != nil || original.Fields["kind"] != kind {
			return o, operations.ErrConflict
		}
		return o, nil
	}
	if !errors.Is(err, operations.ErrNotFound) {
		return o, err
	}
	e := Effect{ID: id, Kind: "verify_recipient", Account: owner, Destination: token, Fields: map[string]string{"kind": kind}}
	o = operations.Record{Kind: "recipient", ID: id, Owner: owner, Status: "pending", Due: time.Now(), Data: operations.Encode(e)}
	err = s.Repo.SaveOperation(ctx, o, 0, nil)
	if err != nil {
		return o, err
	}
	return s.VerifyRecipient(ctx, owner, id)
}
func (s *Service) VerifyRecipient(ctx context.Context, owner, id string) (operations.Record, error) {
	o, err := s.Repo.GetOperation(ctx, "recipient", id)
	if err != nil || o.Owner != owner {
		return o, operations.ErrNotFound
	}
	if o.Status == "succeeded" || o.Status == "failed" {
		return o, nil
	}
	var e Effect
	if err = json.Unmarshal(o.Data, &e); err != nil {
		return o, err
	}
	r, err := s.Gateway.Apply(ctx, e)
	if err != nil {
		return o, err
	}
	if err = validate(e, r, s.Simulation); err != nil {
		return o, err
	}
	o.Status = r.Status
	o.Due = time.Now().Add(time.Minute)
	err = s.Repo.SaveOperation(ctx, o, o.Version, nil)
	return o, err
}
