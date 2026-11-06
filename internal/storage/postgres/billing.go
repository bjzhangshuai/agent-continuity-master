package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/agent-continuity/agent-continuity-master/internal/billing"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
	"github.com/jackc/pgx/v5"
	"time"
)

func (s *Store) SummarizeUsage(ctx context.Context, account, org string, from, to time.Time) ([]protocol.UsageTotal, error) {
	rows, err := s.pool.Query(ctx, `SELECT metric,unit,SUM(quantity)::bigint,COUNT(*) FROM usage_records WHERE ($2<>'' OR account_id=$1) AND COALESCE(organization_id,'')=$2 AND recorded_at >= $3 AND recorded_at < $4 GROUP BY metric,unit
 UNION ALL
 SELECT 'storage.retained.byte_hours','byte_hour',floor(sum(size::numeric*EXTRACT(EPOCH FROM(LEAST(COALESCE(deleted_at,$4),$4)-GREATEST(stored_at,$3))))/3600)::bigint,count(*) FROM archive_jobs WHERE ($2<>'' OR account_id=$1) AND organization_id=$2 AND stored_at<$4 AND (deleted_at IS NULL OR deleted_at>$3) HAVING count(*)>0
 ORDER BY metric`, account, org, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []protocol.UsageTotal{}
	for rows.Next() {
		var v protocol.UsageTotal
		if err = rows.Scan(&v.Metric, &v.Unit, &v.Quantity, &v.Records); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func decodeBill(raw []byte, err error) (protocol.BillingDocument, error) {
	var d protocol.BillingDocument
	if errors.Is(err, pgx.ErrNoRows) {
		return d, billing.ErrNotFound
	}
	if err != nil {
		return d, err
	}
	err = json.Unmarshal(raw, &d)
	return d, err
}
func (s *Store) CreateBill(ctx context.Context, d protocol.BillingDocument) (protocol.BillingDocument, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return d, err
	}
	defer tx.Rollback(ctx)
	// Serialize issuance by account, not by the whole service. This also protects
	// overlapping periods across replicas without requiring a DB extension.
	lockScope := d.Invoice.AccountID
	if d.Invoice.OrganizationID != "" {
		lockScope = "tenant:" + d.Invoice.OrganizationID
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, lockScope); err != nil {
		return d, err
	}
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT document FROM billing_documents WHERE account_id=$1 AND idempotency_key=$2`, d.Invoice.AccountID, d.Invoice.IdempotencyKey).Scan(&raw)
	if err == nil {
		old, err := decodeBill(raw, nil)
		if err != nil {
			return d, err
		}
		if !billing.SameRequest(old.Invoice, d.Invoice) {
			return d, billing.ErrConflict
		}
		return old, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return d, err
	}
	var overlap bool
	if d.Invoice.PaymentMandateID != "" {
		var control []byte
		if err = tx.QueryRow(ctx, `SELECT document FROM tenancy_control WHERE id=true FOR SHARE`).Scan(&control); err != nil {
			return d, err
		}
		var state struct {
			Mandates []struct {
				ID            string `json:"id"`
				TenantID      string `json:"tenant_id"`
				PayerTenantID string `json:"payer_tenant_id"`
				Status        string `json:"status"`
			} `json:"mandates"`
		}
		if err = json.Unmarshal(control, &state); err != nil {
			return d, err
		}
		valid := false
		for _, m := range state.Mandates {
			if m.ID == d.Invoice.PaymentMandateID && m.Status == "active" && m.TenantID == d.Invoice.OrganizationID && m.PayerTenantID == d.Invoice.PayerOrganizationID {
				valid = true
			}
		}
		if !valid {
			return d, billing.ErrConflict
		}
	}
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM billing_documents WHERE ($2<>'' OR account_id=$1) AND organization_id=$2 AND test_mode=$3 AND status<>'void' AND period_start<$5 AND period_end>$4)`, d.Invoice.AccountID, d.Invoice.OrganizationID, d.Invoice.TestMode, d.Invoice.PeriodStart, d.Invoice.PeriodEnd).Scan(&overlap)
	if err != nil {
		return d, err
	}
	if overlap {
		return d, billing.ErrOverlap
	}
	d.Version = 1
	raw, err = json.Marshal(d)
	if err != nil {
		return d, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO billing_documents(invoice_id,account_id,organization_id,billing_account_id,idempotency_key,period_start,period_end,test_mode,status,version,document,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,1,$10,$11)`, d.Invoice.ID, d.Invoice.AccountID, d.Invoice.OrganizationID, d.Invoice.BillingAccountID, d.Invoice.IdempotencyKey, d.Invoice.PeriodStart, d.Invoice.PeriodEnd, d.Invoice.TestMode, d.Invoice.Status, raw, d.Invoice.CreatedAt)
	if err != nil {
		return d, err
	}
	return d, tx.Commit(ctx)
}
func (s *Store) SaveBill(ctx context.Context, d protocol.BillingDocument, version int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	d.Version = version + 1
	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE billing_documents SET document=$1,version=$2,status=$3 WHERE invoice_id=$4 AND account_id=$5 AND version=$6`, raw, d.Version, d.Invoice.Status, d.Invoice.ID, d.Invoice.AccountID, version)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return billing.ErrConflict
	}
	for _, p := range d.Payments {
		_, err = tx.Exec(ctx, `INSERT INTO billing_payment_routes(payment_id,invoice_id,provider,provider_order_id,provider_transaction_id,status,environment,last_checked_at,reconcile_attempts) VALUES($1,$2,$3,NULLIF($4,''),NULLIF($5,''),$6,$7,$8,$9) ON CONFLICT(payment_id) DO UPDATE SET provider_order_id=EXCLUDED.provider_order_id,provider_transaction_id=EXCLUDED.provider_transaction_id,status=EXCLUDED.status,environment=EXCLUDED.environment,last_checked_at=EXCLUDED.last_checked_at,reconcile_attempts=EXCLUDED.reconcile_attempts WHERE billing_payment_routes.invoice_id=EXCLUDED.invoice_id AND billing_payment_routes.provider=EXCLUDED.provider`, p.ID, d.Invoice.ID, p.Provider, p.ProviderOrderID, p.ProviderTransactionID, p.Status, p.Environment, p.LastCheckedAt, p.ReconcileAttempts)
		if err != nil {
			return err
		}
	}
	for _, r := range d.Refunds {
		provider := ""
		for _, p := range d.Payments {
			if p.ID == r.PaymentID {
				provider = p.Provider
			}
		}
		_, err = tx.Exec(ctx, `INSERT INTO billing_refund_routes(refund_id,invoice_id,provider,provider_refund_id,status,last_checked_at,check_attempts) VALUES($1,$2,$3,NULLIF($4,''),$5,$6,$7) ON CONFLICT(refund_id) DO UPDATE SET provider_refund_id=EXCLUDED.provider_refund_id,status=EXCLUDED.status,last_checked_at=EXCLUDED.last_checked_at,check_attempts=EXCLUDED.check_attempts`, r.ID, d.Invoice.ID, provider, r.ProviderRefundID, r.Status, r.LastCheckedAt, r.CheckAttempts)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// PaymentID carries the refund ID for this common recovery target structure.
func (s *Store) DueRefunds(ctx context.Context, before time.Time, limit int) ([]protocol.PaymentReconcileTarget, error) {
	if limit <= 0 || limit > 20 {
		limit = 10
	}
	rows, err := s.pool.Query(ctx, `SELECT d.account_id,r.invoice_id,r.refund_id FROM billing_refund_routes r JOIN billing_documents d USING(invoice_id) WHERE r.provider<>'demo' AND r.status IN ('submitting','pending','review') AND r.check_attempts<20 AND (r.last_checked_at IS NULL OR r.last_checked_at<$1) ORDER BY r.last_checked_at NULLS FIRST LIMIT $2`, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []protocol.PaymentReconcileTarget{}
	for rows.Next() {
		var v protocol.PaymentReconcileTarget
		if err = rows.Scan(&v.AccountID, &v.InvoiceID, &v.PaymentID); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) DuePayments(ctx context.Context, before time.Time, limit int) ([]protocol.PaymentReconcileTarget, error) {
	if limit <= 0 || limit > 20 {
		limit = 10
	}
	rows, err := s.pool.Query(ctx, `SELECT d.account_id,p.invoice_id,p.payment_id FROM billing_payment_routes p JOIN billing_documents d USING(invoice_id) WHERE p.environment<>'simulation' AND p.status IN ('creating','pending','approved') AND p.reconcile_attempts<20 AND (p.last_checked_at IS NULL OR p.last_checked_at<$1) ORDER BY p.last_checked_at NULLS FIRST LIMIT $2`, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []protocol.PaymentReconcileTarget{}
	for rows.Next() {
		var t protocol.PaymentReconcileTarget
		if err = rows.Scan(&t.AccountID, &t.InvoiceID, &t.PaymentID); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
func (s *Store) GetBill(ctx context.Context, account, id string) (protocol.BillingDocument, error) {
	var b []byte
	err := s.pool.QueryRow(ctx, `SELECT document FROM billing_documents WHERE account_id=$1 AND invoice_id=$2`, account, id).Scan(&b)
	return decodeBill(b, err)
}
func (s *Store) ListBills(ctx context.Context, account string, limit int) ([]protocol.BillingDocument, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `SELECT document FROM billing_documents WHERE account_id=$1 ORDER BY created_at DESC LIMIT $2`, account, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []protocol.BillingDocument{}
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		d, err := decodeBill(raw, nil)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
func (s *Store) FindPayment(ctx context.Context, provider, ref string) (protocol.BillingDocument, error) {
	var b []byte
	err := s.pool.QueryRow(ctx, `SELECT d.document FROM billing_payment_routes p JOIN billing_documents d USING(invoice_id) WHERE p.provider=$1 AND (p.payment_id=$2 OR p.provider_order_id=$2) LIMIT 1`, provider, ref).Scan(&b)
	return decodeBill(b, err)
}
