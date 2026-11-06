// Package commerce keeps settlement and fiscal documents separate from payments.
// Gateway is an acquirer/tax-platform adapter contract, not a claim that one wire
// protocol can be sent directly to every financial provider.
package commerce

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/agent-continuity/agent-continuity-master/internal/operations"
	"io"
	"net/http"
	"strings"
	"time"
)

type Effect struct {
	ID          string            `json:"idempotency_key"`
	Kind        string            `json:"kind"`
	Account     string            `json:"account_id"`
	Currency    string            `json:"currency"`
	Amount      int64             `json:"amount_minor"`
	Reference   string            `json:"reference"`
	Destination string            `json:"destination,omitempty"`
	Fields      map[string]string `json:"fields,omitempty"`
}
type Result struct {
	ID         string `json:"idempotency_key"`
	Status     string `json:"status"`
	ProviderID string `json:"provider_id"`
	Amount     int64  `json:"amount_minor"`
	Currency   string `json:"currency"`
	Document   string `json:"document,omitempty"`
	Simulated  bool   `json:"simulated"`
}
type Gateway interface {
	Apply(context.Context, Effect) (Result, error)
}
type DemoGateway struct{}

func (DemoGateway) Apply(_ context.Context, e Effect) (Result, error) {
	r := Result{ID: e.ID, Status: "succeeded", ProviderID: "SIM-" + operations.ID(e.Kind, e.Account, e.ID), Amount: e.Amount, Currency: e.Currency, Simulated: true}
	if e.Kind == "invoice" || e.Kind == "credit_invoice" {
		r.Document = string(operations.Encode(map[string]any{"simulation": true, "legally_valid_tax_invoice": false, "number": r.ProviderID, "request": e, "notice": "模拟票据，不可作为税务报销凭证"}))
	}
	return r, nil
}

// HTTPGateway expects a provider-specific gateway to authenticate the caller,
// deduplicate effect IDs durably and return the same result on unknown-outcome retry.
// Credentials and endpoint are server configuration, never request parameters.
type HTTPGateway struct {
	URL, Token string
	Client     *http.Client
	Simulated  bool
}

func (g HTTPGateway) Apply(ctx context.Context, e Effect) (Result, error) {
	data := operations.Encode(e)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(g.URL, "/")+"/effects", bytes.NewReader(data))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Authorization", "Bearer "+g.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", e.ID)
	c := g.Client
	if c == nil {
		c = &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	resp, err := c.Do(req)
	if err != nil {
		return Result{}, errors.New("commerce gateway unavailable; reconcile original operation")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return Result{}, fmt.Errorf("commerce gateway HTTP %d", resp.StatusCode)
	}
	var r Result
	if err = json.NewDecoder(io.LimitReader(resp.Body, 256<<10)).Decode(&r); err != nil {
		return r, errors.New("invalid commerce gateway response")
	}
	return r, nil
}
func validate(e Effect, r Result, simulation bool) error {
	if r.ID != e.ID || r.Amount != e.Amount || r.Currency != e.Currency || r.ProviderID == "" || r.Simulated != simulation || (r.Status != "succeeded" && r.Status != "pending" && r.Status != "failed") {
		return errors.New("provider result mismatch; original operation remains pending")
	}
	return nil
}
