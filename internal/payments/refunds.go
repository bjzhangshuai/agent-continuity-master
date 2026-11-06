package payments

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// RefundProvider is optional so new payment integrations can advertise support
// honestly without a breaking change to existing checkout adapters.
type RefundProvider interface {
	Refund(context.Context, RefundOrder) (RefundResult, error)
	QueryRefund(context.Context, RefundOrder) (RefundResult, error)
}
type RefundOrder struct {
	RefundID, PaymentID, TransactionID, ProviderRefundID, Currency, Reason string
	AmountMinor, TotalMinor                                                int64
}
type RefundResult struct {
	RefundID, ProviderRefundID, Status, Currency string
	AmountMinor                                  int64
}

func refundMismatch() error {
	return fmt.Errorf("refund response identity, currency or amount mismatch")
}
func (Demo) Refund(_ context.Context, o RefundOrder) (RefundResult, error) {
	return RefundResult{o.RefundID, "demo_" + o.RefundID, "pending", o.Currency, o.AmountMinor}, nil
}
func (d Demo) QueryRefund(ctx context.Context, o RefundOrder) (RefundResult, error) {
	return d.Refund(ctx, o)
}

func (p *Stripe) refundResult(b []byte, o RefundOrder) (RefundResult, error) {
	var v struct {
		ID, Status, Currency string
		Amount               int64
		PaymentIntent        string `json:"payment_intent"`
		Metadata             map[string]string
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return RefundResult{}, err
	}
	if v.ID == "" || v.PaymentIntent != o.TransactionID || v.Metadata["refund_id"] != o.RefundID || v.Amount != o.AmountMinor || strings.ToUpper(v.Currency) != o.Currency {
		return RefundResult{}, refundMismatch()
	}
	status := "review"
	switch v.Status {
	case "succeeded":
		status = "succeeded"
	case "pending", "requires_action":
		status = "pending"
	case "failed", "canceled":
		status = "failed"
	}
	return RefundResult{o.RefundID, v.ID, status, o.Currency, v.Amount}, nil
}
func (p *Stripe) Refund(ctx context.Context, o RefundOrder) (RefundResult, error) {
	b, err := p.call(ctx, "POST", "/v1/refunds", o.RefundID, url.Values{"payment_intent": {o.TransactionID}, "amount": {strconv.FormatInt(o.AmountMinor, 10)}, "metadata[refund_id]": {o.RefundID}})
	if err != nil {
		return RefundResult{}, err
	}
	return p.refundResult(b, o)
}
func (p *Stripe) QueryRefund(ctx context.Context, o RefundOrder) (RefundResult, error) {
	if o.ProviderRefundID == "" {
		return RefundResult{}, ErrNotFound
	}
	b, err := p.call(ctx, "GET", "/v1/refunds/"+url.PathEscape(o.ProviderRefundID), "", nil)
	if err != nil {
		return RefundResult{}, err
	}
	return p.refundResult(b, o)
}
func (p *Wechat) refundResult(b []byte, o RefundOrder) (RefundResult, error) {
	var v struct {
		ID          string `json:"refund_id"`
		Ref         string `json:"out_refund_no"`
		Transaction string `json:"transaction_id"`
		Status      string
		Amount      struct {
			Refund, Total int64
			Currency      string
		}
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return RefundResult{}, err
	}
	if v.ID == "" || v.Ref != o.RefundID || v.Transaction != o.TransactionID || v.Amount.Total != o.TotalMinor || v.Amount.Refund != o.AmountMinor || v.Amount.Currency != o.Currency {
		return RefundResult{}, refundMismatch()
	}
	status := "review"
	switch v.Status {
	case "SUCCESS":
		status = "succeeded"
	case "PROCESSING":
		status = "pending"
	case "CLOSED":
		status = "failed"
	}
	return RefundResult{v.Ref, v.ID, status, v.Amount.Currency, v.Amount.Refund}, nil
}
func (p *Wechat) Refund(ctx context.Context, o RefundOrder) (RefundResult, error) {
	b, err := p.call(ctx, "POST", "/v3/refund/domestic/refunds", map[string]any{"transaction_id": o.TransactionID, "out_refund_no": o.RefundID, "reason": o.Reason, "amount": map[string]any{"refund": o.AmountMinor, "total": o.TotalMinor, "currency": o.Currency}})
	if err != nil {
		return RefundResult{}, err
	}
	return p.refundResult(b, o)
}
func (p *Wechat) QueryRefund(ctx context.Context, o RefundOrder) (RefundResult, error) {
	b, err := p.call(ctx, "GET", "/v3/refund/domestic/refunds/"+url.PathEscape(o.RefundID), nil)
	if err != nil {
		return RefundResult{}, err
	}
	return p.refundResult(b, o)
}
func (p *Alipay) Refund(ctx context.Context, o RefundOrder) (RefundResult, error) {
	_, err := p.call(ctx, "alipay.trade.refund", map[string]string{"trade_no": o.TransactionID, "out_request_no": o.RefundID, "refund_amount": Decimal(o.AmountMinor), "refund_reason": o.Reason}, "")
	if err != nil {
		return RefundResult{}, err
	}
	return p.QueryRefund(ctx, o)
}
func (p *Alipay) QueryRefund(ctx context.Context, o RefundOrder) (RefundResult, error) {
	b, err := p.call(ctx, "alipay.trade.fastpay.refund.query", map[string]string{"trade_no": o.TransactionID, "out_request_no": o.RefundID}, "")
	if err != nil {
		return RefundResult{}, err
	}
	var v struct {
		Trade  string      `json:"trade_no"`
		Ref    string      `json:"out_request_no"`
		Amount json.Number `json:"refund_amount"`
		Status string      `json:"refund_status"`
	}
	if err = json.Unmarshal(b, &v); err != nil {
		return RefundResult{}, err
	}
	n, err := ParseDecimal(v.Amount.String())
	if err != nil || v.Trade != o.TransactionID || v.Ref != o.RefundID || n != o.AmountMinor || o.Currency != "CNY" {
		return RefundResult{}, refundMismatch()
	}
	status := "review"
	if v.Status == "REFUND_SUCCESS" {
		status = "succeeded"
	}
	return RefundResult{o.RefundID, o.RefundID, status, "CNY", n}, nil
}
func (p *PayPal) refundResult(b []byte, o RefundOrder) (RefundResult, error) {
	var v struct {
		ID, Status string
		Amount     paypalMoney
		InvoiceID  string `json:"invoice_id"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return RefundResult{}, err
	}
	n, err := ParseDecimal(v.Amount.Value)
	if err != nil || v.ID == "" || v.InvoiceID != o.RefundID || v.Amount.Currency != o.Currency || n != o.AmountMinor {
		return RefundResult{}, refundMismatch()
	}
	status := "review"
	switch v.Status {
	case "COMPLETED":
		status = "succeeded"
	case "PENDING":
		status = "pending"
	case "FAILED", "CANCELLED":
		status = "failed"
	}
	return RefundResult{o.RefundID, v.ID, status, o.Currency, n}, nil
}
func (p *PayPal) Refund(ctx context.Context, o RefundOrder) (RefundResult, error) {
	b, err := p.call(ctx, "POST", "/v2/payments/captures/"+url.PathEscape(o.TransactionID)+"/refund", o.RefundID, map[string]any{"amount": paypalMoney{Currency: o.Currency, Value: Decimal(o.AmountMinor)}, "invoice_id": o.RefundID})
	if err != nil {
		return RefundResult{}, err
	}
	return p.refundResult(b, o)
}
func (p *PayPal) QueryRefund(ctx context.Context, o RefundOrder) (RefundResult, error) {
	if o.ProviderRefundID == "" {
		return RefundResult{}, ErrNotFound
	}
	b, err := p.call(ctx, "GET", "/v2/payments/refunds/"+url.PathEscape(o.ProviderRefundID), "", nil)
	if err != nil {
		return RefundResult{}, err
	}
	return p.refundResult(b, o)
}
