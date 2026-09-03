package client

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
	"net/http"
	"net/url"
	"time"
)

type InvoiceRequest struct {
	BillingAccountID string    `json:"billing_account_id"`
	PeriodStart      time.Time `json:"period_start"`
	PeriodEnd        time.Time `json:"period_end"`
}
type PaymentCheckout struct {
	Payment     protocol.BillingPayment `json:"payment"`
	PaymentLink string                  `json:"payment_link"`
	Invoice     protocol.BillingInvoice `json:"invoice"`
}

// BillingMutation never generates an idempotency key for the caller. Persist and
// reuse the same key across network retries to prevent accidental double payment.
func (c *Client) BillingMutation(ctx context.Context, path, key string, input, result any) error {
	b, err := json.Marshal(input)
	if err != nil {
		return err
	}
	r, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	r.Header.Set("Authorization", "Bearer "+c.Token)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Idempotency-Key", key)
	return c.doRequest(r, result)
}
func (c *Client) QuoteInvoice(ctx context.Context, input InvoiceRequest) (protocol.BillingInvoice, error) {
	var result protocol.BillingInvoice
	err := c.Do(ctx, "POST", "/v1/billing/quote", input, &result)
	return result, err
}
func (c *Client) IssueInvoice(ctx context.Context, input InvoiceRequest, key string) (protocol.BillingDocument, error) {
	var result protocol.BillingDocument
	err := c.BillingMutation(ctx, "/v1/billing/invoices", key, input, &result)
	return result, err
}
func (c *Client) ListInvoices(ctx context.Context) ([]protocol.BillingDocument, error) {
	var result struct {
		Invoices []protocol.BillingDocument `json:"invoices"`
	}
	err := c.Do(ctx, "GET", "/v1/billing/invoices?limit=200", nil, &result)
	return result.Invoices, err
}
func (c *Client) GetInvoice(ctx context.Context, id string) (protocol.BillingDocument, error) {
	var result protocol.BillingDocument
	err := c.Do(ctx, "GET", "/v1/billing/invoices/"+url.PathEscape(id), nil, &result)
	return result, err
}
func (c *Client) CreatePayment(ctx context.Context, invoice, provider, key string) (PaymentCheckout, error) {
	var result PaymentCheckout
	err := c.BillingMutation(ctx, "/v1/billing/invoices/"+url.PathEscape(invoice)+"/payments", key, map[string]string{"provider": provider}, &result)
	return result, err
}
func (c *Client) ReconcilePayment(ctx context.Context, invoice, payment string) (protocol.BillingDocument, error) {
	var result protocol.BillingDocument
	err := c.Do(ctx, "POST", "/v1/billing/invoices/"+url.PathEscape(invoice)+"/payments/"+url.PathEscape(payment)+"/reconcile", nil, &result)
	return result, err
}
