package postgres

import (
	"context"
	"errors"
	"github.com/agent-continuity/agent-continuity-master/internal/operations"
	"github.com/jackc/pgx/v5"
	"time"
)

func scanOperation(row pgx.Row) (r operations.Record, err error) {
	err = row.Scan(&r.Kind, &r.ID, &r.Owner, &r.Peer, &r.Status, &r.Version, &r.Due, &r.Data, &r.Updated, &r.Resource)
	if errors.Is(err, pgx.ErrNoRows) {
		err = operations.ErrNotFound
	}
	return
}

const operationColumns = `kind,operation_id,owner_id,peer_id,status,version,due_at,document,updated_at,resource_id`

func (s *Store) GetOperation(ctx context.Context, kind, id string) (operations.Record, error) {
	return scanOperation(s.pool.QueryRow(ctx, `SELECT `+operationColumns+` FROM workflow_operations WHERE kind=$1 AND operation_id=$2`, kind, id))
}
func (s *Store) ListOperations(ctx context.Context, kind, owner, after string, limit int) ([]operations.Record, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+operationColumns+` FROM workflow_operations WHERE kind=$1 AND (owner_id=$2 OR peer_id=$2) AND operation_id>$3 ORDER BY operation_id LIMIT $4`, kind, owner, after, min(max(limit, 1), 1000))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []operations.Record{}
	for rows.Next() {
		r, e := scanOperation(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *Store) DueOperations(ctx context.Context, kind string, now time.Time, limit int) ([]operations.Record, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+operationColumns+` FROM workflow_operations WHERE kind=$1 AND status IN ('queued','running','pending') AND due_at<=$2 ORDER BY due_at,operation_id LIMIT $3`, kind, now, min(max(limit, 1), 100))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []operations.Record{}
	for rows.Next() {
		r, e := scanOperation(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *Store) SaveOperation(ctx context.Context, r operations.Record, version int64, guards []operations.BillGuard) error {
	if r.Updated.IsZero() {
		r.Updated = time.Now().UTC()
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, g := range guards {
		var v int64
		if err = tx.QueryRow(ctx, `SELECT version FROM billing_documents WHERE invoice_id=$1 FOR SHARE`, g.ID).Scan(&v); err != nil {
			return err
		}
		if v != g.Version {
			return operations.ErrConflict
		}
	}
	var n int64
	if version == 0 {
		tag, e := tx.Exec(ctx, `INSERT INTO workflow_operations (`+operationColumns+`) VALUES($1,$2,$3,$4,$5,1,$6,$7,$8,$9) ON CONFLICT DO NOTHING`, r.Kind, r.ID, r.Owner, r.Peer, r.Status, r.Due, r.Data, r.Updated, r.Resource)
		err = e
		n = tag.RowsAffected()
	} else {
		tag, e := tx.Exec(ctx, `UPDATE workflow_operations SET status=$1,version=version+1,due_at=$2,document=$3,updated_at=$9,resource_id=$10 WHERE kind=$4 AND operation_id=$5 AND owner_id=$6 AND peer_id=$7 AND version=$8`, r.Status, r.Due, r.Data, r.Kind, r.ID, r.Owner, r.Peer, version, r.Updated, r.Resource)
		err = e
		n = tag.RowsAffected()
	}
	if err != nil {
		return err
	}
	if n != 1 {
		return operations.ErrConflict
	}
	for _, u := range r.Usage {
		if _, err = tx.Exec(ctx, `INSERT INTO usage_records(usage_id,account_id,organization_id,metric,quantity,unit,resource_id,idempotency_key,metadata,recorded_at) VALUES($1,$2,NULLIF($3,''),$4,$5,$6,NULLIF($7,''),$8,$9,$10) ON CONFLICT(account_id,metric,idempotency_key) DO NOTHING`, u.ID, u.AccountID, u.OrganizationID, u.Metric, u.Quantity, u.Unit, u.ResourceID, u.IdempotencyKey, []byte(u.Metadata), u.RecordedAt); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) QueryRuns(ctx context.Context, owner string, q operations.RunQuery) ([]operations.Record, int, error) {
	where := `kind='run' AND owner_id=$1 AND ($2='' OR COALESCE(NULLIF(document->'request'->>'context_id',''),'ctx_'||operation_id)=$2) AND ($3='' OR status=$3) AND ($4='' OR resource_id=$4) AND ($5::timestamptz IS NULL OR updated_at>=$5)`
	var after, before any
	if !q.After.IsZero() {
		after = q.After
	}
	if !q.Before.IsZero() {
		before = q.Before
	}
	var total int
	if e := s.pool.QueryRow(ctx, `SELECT count(*) FROM workflow_operations WHERE `+where, owner, q.ContextID, q.Status, q.TaskID, after).Scan(&total); e != nil {
		return nil, 0, e
	}
	rows, e := s.pool.Query(ctx, `SELECT `+operationColumns+` FROM workflow_operations WHERE `+where+` AND ($6::timestamptz IS NULL OR (updated_at,operation_id)<($6,$7)) ORDER BY updated_at DESC,operation_id DESC LIMIT $8`, owner, q.ContextID, q.Status, q.TaskID, after, before, q.BeforeID, min(max(q.Limit, 1), 501))
	if e != nil {
		return nil, 0, e
	}
	defer rows.Close()
	out := []operations.Record{}
	for rows.Next() {
		r, e := scanOperation(rows)
		if e != nil {
			return nil, 0, e
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}
