package client

import (
	"context"
	"encoding/json"
	"net/url"
)

// Workflow operations use explicit caller-owned idempotency keys, as billing does.
func (c *Client) CreateRun(ctx context.Context, plan any, key string) (json.RawMessage, error) {
	var r json.RawMessage
	e := c.BillingMutation(ctx, "/v1/orchestration/runs", key, plan, &r)
	return r, e
}
func (c *Client) GetRun(ctx context.Context, id string) (json.RawMessage, error) {
	var r json.RawMessage
	e := c.Do(ctx, "GET", "/v1/orchestration/runs/"+url.PathEscape(id), nil, &r)
	return r, e
}
func (c *Client) ControlRun(ctx context.Context, id, action string) (json.RawMessage, error) {
	var r json.RawMessage
	e := c.Do(ctx, "POST", "/v1/orchestration/runs/"+url.PathEscape(id)+"/"+url.PathEscape(action), nil, &r)
	return r, e
}
func (c *Client) IssueFiscal(ctx context.Context, invoice, name, taxID string) (json.RawMessage, error) {
	var r json.RawMessage
	e := c.Do(ctx, "POST", "/v1/billing/invoices/"+url.PathEscape(invoice)+"/fiscal", map[string]string{"buyer_name": name, "tax_id": taxID}, &r)
	return r, e
}
func (c *Client) GetSettlement(ctx context.Context, id string) (json.RawMessage, error) {
	var r json.RawMessage
	e := c.Do(ctx, "GET", "/v1/commerce/settlements/"+url.PathEscape(id), nil, &r)
	return r, e
}
