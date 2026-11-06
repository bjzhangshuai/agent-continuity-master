package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/agent-continuity/agent-continuity-master/internal/treasury"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
	"github.com/jackc/pgx/v5"
)

func (s *Store) LoadTreasury(ctx context.Context, owner string) (protocol.TreasuryDocument, error) {
	var raw []byte
	var d protocol.TreasuryDocument
	err := s.pool.QueryRow(ctx, `SELECT document FROM treasury_documents WHERE owner_account_id=$1`, owner).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, treasury.ErrNotFound
	}
	if err != nil {
		return d, err
	}
	err = json.Unmarshal(raw, &d)
	return d, err
}
func (s *Store) SaveTreasury(ctx context.Context, d protocol.TreasuryDocument, version int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, c := range d.Collections {
		var current int64
		if err = tx.QueryRow(ctx, `SELECT version FROM billing_documents WHERE invoice_id=$1 FOR SHARE`, c.InvoiceID).Scan(&current); err != nil {
			return err
		}
		if current != c.BillingVersion {
			return treasury.ErrConflict
		}
	}
	d.Version = version + 1
	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	var changed int64
	if version == 0 {
		tag, e := tx.Exec(ctx, `INSERT INTO treasury_documents(owner_account_id,version,document) VALUES($1,1,$2) ON CONFLICT DO NOTHING`, d.OwnerAccountID, raw)
		err = e
		changed = tag.RowsAffected()
	} else {
		tag, e := tx.Exec(ctx, `UPDATE treasury_documents SET version=$1,document=$2,updated_at=now() WHERE owner_account_id=$3 AND version=$4`, d.Version, raw, d.OwnerAccountID, version)
		err = e
		changed = tag.RowsAffected()
	}
	if err != nil {
		return err
	}
	if changed != 1 {
		return treasury.ErrConflict
	}
	return tx.Commit(ctx)
}
