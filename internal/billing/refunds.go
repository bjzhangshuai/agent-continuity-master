package billing

import (
	"context"
	"fmt"
	"github.com/agent-continuity/agent-continuity-master/internal/payments"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
	"strings"
	"time"
)

func refund(d *protocol.BillingDocument, id string) *protocol.BillingRefund {
	for i := range d.Refunds {
		if d.Refunds[i].ID == id {
			return &d.Refunds[i]
		}
	}
	return nil
}
func RefundAvailable(d protocol.BillingDocument, paymentID string) int64 {
	p := payment(&d, paymentID)
	if p == nil {
		return 0
	}
	available := p.AmountMinor
	for _, r := range d.Refunds {
		if r.PaymentID == paymentID && r.Status != "rejected" && r.Status != "failed" {
			available -= r.AmountMinor
		}
	}
	return available
}
func (s *Service) RequestRefund(ctx context.Context, account, invoice, paymentID string, amount int64, reason, key string) (protocol.BillingDocument, error) {
	reason = strings.TrimSpace(reason)
	if !validKey(key) || amount <= 0 || len(reason) == 0 || len(reason) > 240 || Actor(ctx) == "" {
		return protocol.BillingDocument{}, ErrInvalid
	}
	return s.mutate(ctx, account, invoice, func(d *protocol.BillingDocument) error {
		for _, r := range d.Refunds {
			if r.IdempotencyKey == key {
				if r.PaymentID != paymentID || r.AmountMinor != amount || r.Reason != reason || r.RequestedBy != Actor(ctx) {
					return ErrConflict
				}
				return nil
			}
		}
		p := payment(d, paymentID)
		if p == nil || (p.Status != "succeeded" && p.Status != "review") || p.ProviderTransactionID == "" {
			return ErrInvalid
		}
		confirmed := false
		for _, receipt := range d.Receipts {
			if receipt.PaymentID == paymentID {
				confirmed = true
			}
		}
		if !confirmed || len(d.Refunds) >= 100 || len(d.Events) >= 500 || amount > RefundAvailable(*d, paymentID) {
			return fmt.Errorf("%w: amount exceeds refundable balance or record limit", ErrInvalid)
		}
		provider, err := s.Providers.Get(p.Provider)
		if err != nil {
			return err
		}
		if _, ok := provider.(payments.RefundProvider); !ok {
			return fmt.Errorf("%w: refunds unsupported", ErrInvalid)
		}
		now := s.now().UTC()
		d.Refunds = append(d.Refunds, protocol.BillingRefund{ID: payments.NewID("rf_"), PaymentID: paymentID, AmountMinor: amount, Currency: p.Currency, Reason: reason, Status: "requested", RequestedBy: Actor(ctx), IdempotencyKey: key, CreatedAt: now, UpdatedAt: now})
		addEvent(d, paymentID, "refund.requested", d.Refunds[len(d.Refunds)-1].ID+" by "+Actor(ctx), now)
		return nil
	})
}

// Approval authority is enforced at the HTTP boundary; real requests also
// require a distinct approver here. Rejection releases only an unsubmitted hold.
func (s *Service) DecideRefund(ctx context.Context, account, invoice, id, decision, reason string) (protocol.BillingDocument, error) {
	if Actor(ctx) == "" || (decision != "approved" && decision != "rejected") || len(strings.TrimSpace(reason)) == 0 || len(reason) > 240 {
		return protocol.BillingDocument{}, ErrInvalid
	}
	return s.mutate(ctx, account, invoice, func(d *protocol.BillingDocument) error {
		r := refund(d, id)
		if r == nil {
			return ErrNotFound
		}
		if r.Status == decision && r.DecidedBy == Actor(ctx) {
			return nil
		}
		if r.Status != "requested" {
			return ErrConflict
		}
		if !d.Invoice.TestMode && r.RequestedBy == Actor(ctx) {
			return fmt.Errorf("%w: separate refund approver required", ErrInvalid)
		}
		r.Status = decision
		r.DecidedBy = Actor(ctx)
		r.DecisionReason = reason
		r.UpdatedAt = s.now().UTC()
		addEvent(d, r.PaymentID, "refund."+decision, id+" by "+Actor(ctx), r.UpdatedAt)
		return nil
	})
}
func refundOrder(d *protocol.BillingDocument, r *protocol.BillingRefund) payments.RefundOrder {
	p := payment(d, r.PaymentID)
	return payments.RefundOrder{RefundID: r.ID, PaymentID: p.ID, TransactionID: p.ProviderTransactionID, ProviderRefundID: r.ProviderRefundID, Currency: r.Currency, Reason: r.Reason, AmountMinor: r.AmountMinor, TotalMinor: p.AmountMinor}
}

// Submit reserves submitting before network I/O. Repeated submissions query,
// never create a new refund identity, including after a process crash.
func (s *Service) SubmitRefund(ctx context.Context, account, invoice, id string) (protocol.BillingDocument, error) {
	claimed := false
	d, err := s.mutate(ctx, account, invoice, func(d *protocol.BillingDocument) error {
		claimed = false
		r := refund(d, id)
		if r == nil {
			return ErrNotFound
		}
		switch r.Status {
		case "succeeded", "pending", "submitting", "review", "failed":
			return nil
		case "approved":
		default:
			return ErrConflict
		}
		now := s.now().UTC()
		r.Status = "submitting"
		r.SubmittedAt = &now
		r.UpdatedAt = now
		claimed = true
		addEvent(d, r.PaymentID, "refund.submitting", id, now)
		return nil
	})
	if err != nil {
		return d, err
	}
	r := refund(&d, id)
	if !claimed {
		if r.Status == "succeeded" || r.Status == "failed" {
			return d, nil
		}
		return s.ReconcileRefund(ctx, account, invoice, id)
	}
	p, err := s.Providers.Get(payment(&d, r.PaymentID).Provider)
	if err != nil {
		return d, err
	}
	rp, ok := p.(payments.RefundProvider)
	if !ok {
		return d, payments.ErrUnsupported
	}
	result, err := rp.Refund(ctx, refundOrder(&d, r))
	if err != nil {
		s.refundError(ctx, account, invoice, id)
		return d, err
	}
	return s.ApplyRefund(ctx, account, invoice, id, result)
}
func (s *Service) refundError(ctx context.Context, account, invoice, id string) {
	_, _ = s.mutate(ctx, account, invoice, func(d *protocol.BillingDocument) error {
		r := refund(d, id)
		if r == nil {
			return ErrNotFound
		}
		if r.Status == "succeeded" || r.Status == "failed" {
			return nil
		}
		r.LastError = "渠道结果未确认，请使用原退款单查单；额度继续占用"
		r.UpdatedAt = s.now().UTC()
		return nil
	})
}
func (s *Service) ReconcileRefund(ctx context.Context, account, invoice, id string) (protocol.BillingDocument, error) {
	d, err := s.Repo.GetBill(ctx, account, invoice)
	if err != nil {
		return d, err
	}
	r := refund(&d, id)
	if r == nil {
		return d, ErrNotFound
	}
	if r.Status == "succeeded" || r.Status == "failed" {
		return d, nil
	}
	if r.Status != "pending" && r.Status != "submitting" && r.Status != "review" {
		return d, ErrConflict
	}
	p, err := s.Providers.Get(payment(&d, r.PaymentID).Provider)
	if err != nil {
		return d, err
	}
	rp, ok := p.(payments.RefundProvider)
	if !ok {
		return d, payments.ErrUnsupported
	}
	o := refundOrder(&d, r)
	result, err := rp.QueryRefund(ctx, o)
	// Stripe/PayPal require the gateway ID for GET. Retry the identical POST only
	// within a conservative idempotency window after a lost create response.
	if err == payments.ErrNotFound && o.ProviderRefundID == "" && r.SubmittedAt != nil && s.now().Sub(*r.SubmittedAt) < time.Hour {
		result, err = rp.Refund(ctx, o)
	}
	if err != nil {
		s.refundError(ctx, account, invoice, id)
		return d, err
	}
	return s.ApplyRefund(ctx, account, invoice, id, result)
}
func (s *Service) ApplyRefund(ctx context.Context, account, invoice, id string, result payments.RefundResult) (protocol.BillingDocument, error) {
	return s.mutate(ctx, account, invoice, func(d *protocol.BillingDocument) error {
		r := refund(d, id)
		if r == nil {
			return ErrNotFound
		}
		if result.RefundID != id || result.ProviderRefundID == "" || result.AmountMinor != r.AmountMinor || result.Currency != r.Currency || (r.ProviderRefundID != "" && r.ProviderRefundID != result.ProviderRefundID) {
			return fmt.Errorf("%w: refund identity or amount mismatch", ErrInvalid)
		}
		switch result.Status {
		case "succeeded", "pending", "failed", "review":
		default:
			return ErrInvalid
		}
		if r.Status == "succeeded" {
			return nil
		}
		if r.Status == "failed" {
			if result.Status != "failed" {
				return ErrConflict
			}
			return nil
		}
		if r.Status != "pending" && r.Status != "submitting" && r.Status != "review" {
			return ErrConflict
		}
		for _, other := range d.Refunds {
			if other.ID != id && other.ProviderRefundID == result.ProviderRefundID && payment(d, other.PaymentID).Provider == payment(d, r.PaymentID).Provider {
				return ErrConflict
			}
		}
		previous := r.Status
		r.Status = result.Status
		r.ProviderRefundID = result.ProviderRefundID
		r.UpdatedAt = s.now().UTC()
		r.LastCheckedAt = &r.UpdatedAt
		r.CheckAttempts++
		r.LastError = ""
		if previous != r.Status {
			addEvent(d, r.PaymentID, "refund."+r.Status, id, r.UpdatedAt)
		}
		if r.Status == "succeeded" {
			d.CreditNotes = append(d.CreditNotes, protocol.CreditNote{ID: "cn_" + id, RefundID: id, PaymentID: r.PaymentID, AmountMinor: r.AmountMinor, Currency: r.Currency, CreatedAt: r.UpdatedAt, Kind: "refund_credit_note_not_tax_invoice"})
			var captured, returned int64
			for _, receipt := range d.Receipts {
				captured += receipt.AmountMinor
			}
			for _, note := range d.CreditNotes {
				returned += note.AmountMinor
			}
			if captured > 0 && returned == captured {
				d.Invoice.Status = "refunded"
			}
		}
		return nil
	})
}
func (s *Service) SimulateRefund(ctx context.Context, account, invoice, id, outcome string) (protocol.BillingDocument, error) {
	d, err := s.Repo.GetBill(ctx, account, invoice)
	if err != nil {
		return d, err
	}
	r := refund(&d, id)
	if r == nil {
		return d, ErrNotFound
	}
	p := payment(&d, r.PaymentID)
	if !d.Invoice.TestMode || p.Provider != "demo" || p.Environment != "simulation" || (outcome != "succeeded" && outcome != "failed") {
		return d, ErrInvalid
	}
	return s.ApplyRefund(ctx, account, invoice, id, payments.RefundResult{RefundID: id, ProviderRefundID: "demo_" + id, Status: outcome, Currency: r.Currency, AmountMinor: r.AmountMinor})
}

func (s *Service) recoverRefunds(ctx context.Context) {
	targets, err := s.Repo.DueRefunds(ctx, s.now().Add(-2*time.Minute), 10)
	if err != nil {
		return
	}
	for _, t := range targets {
		_, err = s.mutate(ctx, t.AccountID, t.InvoiceID, func(d *protocol.BillingDocument) error {
			r := refund(d, t.PaymentID)
			if r == nil || r.CheckAttempts >= 20 || (r.LastCheckedAt != nil && r.LastCheckedAt.After(s.now().Add(-2*time.Minute))) {
				return ErrConflict
			}
			now := s.now().UTC()
			r.LastCheckedAt = &now
			r.CheckAttempts++
			return nil
		})
		if err != nil {
			continue
		}
		call, cancel := context.WithTimeout(ctx, 20*time.Second)
		_, _ = s.ReconcileRefund(call, t.AccountID, t.InvoiceID, t.PaymentID)
		cancel()
	}
}
