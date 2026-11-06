package commerce

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/agent-continuity/agent-continuity-master/internal/operations"
	"log/slog"
	"strings"
	"time"
)

type FiscalRequest struct {
	BuyerName string `json:"buyer_name"`
	TaxID     string `json:"tax_id"`
}
type Fiscal struct {
	InvoiceID string        `json:"invoice_id"`
	RefundID  string        `json:"refund_id,omitempty"`
	Buyer     FiscalRequest `json:"buyer"`
	Effect    Effect        `json:"request"`
	Result    Result        `json:"result"`
	Created   time.Time     `json:"created_at"`
}

func (s *Service) IssueFiscal(ctx context.Context, owner, invoice string, buyer FiscalRequest) (Fiscal, error) {
	if s.Gateway == nil || strings.TrimSpace(buyer.BuyerName) == "" || len(buyer.BuyerName) > 200 || len(buyer.TaxID) < 4 || len(buyer.TaxID) > 40 {
		return Fiscal{}, operations.ErrInvalid
	}
	for _, c := range buyer.TaxID {
		if c != '-' && (c < '0' || c > '9') && (c < 'A' || c > 'Z') {
			return Fiscal{}, operations.ErrInvalid
		}
	}
	id := operations.ID("fiscal", owner, invoice)
	old, err := s.Repo.GetOperation(ctx, "fiscal", id)
	if err == nil {
		var v Fiscal
		err = json.Unmarshal(old.Data, &v)
		if v.Buyer != buyer {
			return v, operations.ErrConflict
		}
		return v, err
	}
	if !errors.Is(err, operations.ErrNotFound) {
		return Fiscal{}, err
	}
	d, err := s.Repo.GetBill(ctx, owner, invoice)
	if err != nil {
		return Fiscal{}, err
	}
	if !paid(d) || d.Invoice.TestMode != s.Simulation {
		return Fiscal{}, fmt.Errorf("%w: fiscal invoice requires verified payment in the same environment", operations.ErrInvalid)
	}
	v := Fiscal{InvoiceID: invoice, Buyer: buyer, Created: time.Now().UTC(), Effect: Effect{ID: id, Kind: "invoice", Account: owner, Currency: d.Invoice.Currency, Amount: d.Invoice.AmountMinor, Reference: invoice, Fields: map[string]string{"buyer_name": buyer.BuyerName, "tax_id": buyer.TaxID}}, Result: Result{Status: "pending", Simulated: s.Simulation}}
	err = s.Repo.SaveOperation(ctx, operations.Record{Kind: "fiscal", ID: id, Owner: owner, Status: "pending", Due: time.Now(), Data: operations.Encode(v)}, 0, []operations.BillGuard{{ID: invoice, Version: d.Version}})
	return v, err
}
func (s *Service) ReconcileFiscal(ctx context.Context, owner, id string) (Fiscal, error) {
	o, err := s.Repo.GetOperation(ctx, "fiscal", id)
	if err != nil || o.Owner != owner {
		return Fiscal{}, operations.ErrNotFound
	}
	var v Fiscal
	if err = json.Unmarshal(o.Data, &v); err != nil {
		return v, err
	}
	if v.Result.Status == "succeeded" || o.Status == "failed" {
		if v.Result.Status == "succeeded" {
			err = s.ensureCredits(ctx, owner, v)
			o.Due = time.Now().Add(time.Minute)
			if err == nil {
				err = s.Repo.SaveOperation(ctx, o, o.Version, nil)
			}
		}
		return v, err
	}
	if s.Gateway == nil {
		return v, operations.ErrInvalid
	}
	r, err := s.Gateway.Apply(ctx, v.Effect)
	if err != nil {
		return v, err
	}
	if err = validate(v.Effect, r, s.Simulation); err != nil {
		return v, err
	}
	if r.Status == "succeeded" && (r.Document == "" || len(r.Document) > 128<<10) {
		return v, errors.New("missing or excessive fiscal document")
	}
	v.Result = r
	o.Data = operations.Encode(v)
	o.Status = r.Status
	if r.Status == "succeeded" {
		o.Status = "pending"
	}
	o.Due = time.Now().Add(time.Minute)
	err = s.Repo.SaveOperation(ctx, o, o.Version, nil)
	if err == nil && r.Status == "succeeded" {
		err = s.ensureCredits(ctx, owner, v)
	}
	return v, err
}
func (s *Service) ensureCredits(ctx context.Context, owner string, blue Fiscal) error {
	if blue.RefundID != "" {
		return nil
	}
	d, err := s.Repo.GetBill(ctx, owner, blue.InvoiceID)
	if err != nil {
		return err
	}
	for _, refund := range d.Refunds {
		if refund.Status != "succeeded" {
			continue
		}
		id := operations.ID("fiscal", owner, "credit:"+refund.ID)
		_, err = s.Repo.GetOperation(ctx, "fiscal", id)
		if err == nil {
			continue
		}
		if !errors.Is(err, operations.ErrNotFound) {
			return err
		}
		red := Fiscal{InvoiceID: blue.InvoiceID, RefundID: refund.ID, Buyer: blue.Buyer, Created: time.Now().UTC(), Effect: Effect{ID: id, Kind: "credit_invoice", Account: owner, Currency: refund.Currency, Amount: refund.AmountMinor, Reference: blue.Result.ProviderID, Fields: map[string]string{"refund_id": refund.ID, "buyer_name": blue.Buyer.BuyerName, "tax_id": blue.Buyer.TaxID}}, Result: Result{Status: "pending", Simulated: s.Simulation}}
		err = s.Repo.SaveOperation(ctx, operations.Record{Kind: "fiscal", ID: id, Owner: owner, Status: "pending", Due: time.Now(), Data: operations.Encode(red)}, 0, []operations.BillGuard{{ID: d.Invoice.ID, Version: d.Version}})
		if err != nil && !errors.Is(err, operations.ErrConflict) {
			return err
		}
	}
	return nil
}
func (s *Service) Run(ctx context.Context, logger *slog.Logger) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for _, kind := range []string{"settlement", "fiscal", "recipient"} {
				rows, err := s.Repo.DueOperations(ctx, kind, time.Now(), 10)
				if err != nil {
					logger.Error("commerce scan", "error", err)
					continue
				}
				for _, o := range rows {
					switch kind {
					case "settlement":
						_, err = s.Reconcile(ctx, o.Owner, o.ID)
					case "fiscal":
						_, err = s.ReconcileFiscal(ctx, o.Owner, o.ID)
					case "recipient":
						_, err = s.VerifyRecipient(ctx, o.Owner, o.ID)
					}
					if err != nil && !errors.Is(err, operations.ErrConflict) {
						logger.Warn("commerce operation remains pending", "kind", kind, "id", o.ID)
					}
				}
			}
		}
	}
}
