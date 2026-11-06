package client

import (
	"context"
	"fmt"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
	"io"
	"net/http"
	"net/url"
)

// InvoiceStatement downloads the authenticated, printable non-tax statement.
func (c *Client) InvoiceStatement(ctx context.Context, invoice string) (string, error) {
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/v1/billing/invoices/"+url.PathEscape(invoice)+"/statement", nil)
	if err != nil {
		return "", err
	}
	r.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := c.HTTP.Do(r)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("statement download: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return string(data), err
}

func (c *Client) RequestRefund(ctx context.Context, invoice, payment string, amount int64, reason, key string) (protocol.BillingDocument, error) {
	var d protocol.BillingDocument
	err := c.BillingMutation(ctx, "/v1/billing/invoices/"+url.PathEscape(invoice)+"/refunds", key, map[string]any{"payment_id": payment, "amount_minor": amount, "reason": reason}, &d)
	return d, err
}
func (c *Client) ReconcileRefund(ctx context.Context, invoice, refund string) (protocol.BillingDocument, error) {
	var d protocol.BillingDocument
	err := c.Do(ctx, "POST", "/v1/billing/invoices/"+url.PathEscape(invoice)+"/refunds/"+url.PathEscape(refund)+"/reconcile", nil, &d)
	return d, err
}
