package client

import (
	"context"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
	"net/url"
)

type TreasuryView struct {
	Document         protocol.TreasuryDocument  `json:"document"`
	Balances         []protocol.TreasuryBalance `json:"balances"`
	DemoEnabled      bool                       `json:"demo_enabled"`
	StripeAPIEnabled bool                       `json:"stripe_api_enabled"`
}
type PayoutRequest struct {
	DestinationID string `json:"destination_id"`
	AmountMinor   int64  `json:"amount_minor"`
	Currency      string `json:"currency"`
	Provider      string `json:"provider"`
}

func (c *Client) GetTreasury(ctx context.Context) (TreasuryView, error) {
	var v TreasuryView
	err := c.Do(ctx, "GET", "/v1/treasury", nil, &v)
	return v, err
}
func (c *Client) ImportDemoCollection(ctx context.Context, invoice string) (protocol.TreasuryDocument, error) {
	var d protocol.TreasuryDocument
	err := c.Do(ctx, "POST", "/v1/treasury/collections/import", map[string]string{"invoice_id": invoice}, &d)
	return d, err
}
func (c *Client) RequestPayout(ctx context.Context, in PayoutRequest, key string) (protocol.TreasuryDocument, error) {
	var d protocol.TreasuryDocument
	err := c.BillingMutation(ctx, "/v1/treasury/payouts", key, in, &d)
	return d, err
}
func (c *Client) DecidePayout(ctx context.Context, id, decision string) (protocol.TreasuryDocument, error) {
	var d protocol.TreasuryDocument
	err := c.Do(ctx, "POST", "/v1/treasury/payouts/"+url.PathEscape(id)+"/decision", map[string]string{"decision": decision}, &d)
	return d, err
}
func (c *Client) SubmitPayout(ctx context.Context, id string) (protocol.TreasuryDocument, error) {
	var d protocol.TreasuryDocument
	err := c.Do(ctx, "POST", "/v1/treasury/payouts/"+url.PathEscape(id)+"/submit", nil, &d)
	return d, err
}
func (c *Client) ReconcilePayout(ctx context.Context, id string) (protocol.TreasuryDocument, error) {
	var d protocol.TreasuryDocument
	err := c.Do(ctx, "POST", "/v1/treasury/payouts/"+url.PathEscape(id)+"/reconcile", nil, &d)
	return d, err
}
