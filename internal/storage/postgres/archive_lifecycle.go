package postgres

import (
	"context"
	"encoding/json"
	"time"

	"github.com/agent-continuity/agent-continuity-master/internal/archive"
)

func (q *ArchiveQueue) Ping(ctx context.Context) error {
	// Check connectivity AND the lifecycle migration without scanning the queue.
	_, err := q.store.pool.Exec(ctx, `SELECT cache_evicted_at FROM archive_jobs LIMIT 0`)
	return err
}

func (q *ArchiveQueue) RequestDelete(ctx context.Context, id int64, hash, actor, reason string) error {
	tx, err := q.store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	result, err := tx.Exec(ctx, `UPDATE archive_jobs SET operation='delete',state='queued',attempts=0,lease_version=lease_version+1,dispatched_at=NULL,next_attempt=now(),updated_at=now() WHERE job_id=$1 AND content_hash=$2 AND state='stored' AND NOT legal_hold AND cache_released_at IS NULL AND COALESCE(retain_until,now())<=now()`, id, hash)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM archive_jobs WHERE job_id=$1 AND content_hash=$2 AND operation='delete')`, id, hash).Scan(&exists); err == nil && exists {
			return tx.Commit(ctx) // same immutable deletion request is idempotent
		}
		return archive.ErrLeaseLost
	}
	if _, err = tx.Exec(ctx, `INSERT INTO archive_action_audit(job_id,actor,action,reason) VALUES($1,$2,'delete.requested',$3)`, id, actor, reason); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (q *ArchiveQueue) CacheCandidates(ctx context.Context, age time.Duration, limit int, targets []string) ([]archive.Job, error) {
	rows, err := q.store.pool.Query(ctx, `SELECT `+archiveColumns+` FROM archive_jobs WHERE source_kind='artifact' AND state='stored' AND cache_evicted_at IS NULL AND stored_at<now()-$1::interval AND target_id=ANY($3) ORDER BY job_id LIMIT $2`, age.String(), min(limit, 100), targets)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []archive.Job
	for rows.Next() {
		j, err := scanArchive(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

// Caller holds the shared filesystem content lock across this transaction and
// unlink. Commit relinquishing local durability BEFORE unlink, so a crash cannot
// race an archive-only deletion and destroy the last copy. All references matter,
// including other tenants and versions that have not yet obtained an outbox row.
func (q *ArchiveQueue) PrepareCacheEviction(ctx context.Context, ref string) (bool, error) {
	tx, err := q.store.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT job_id FROM archive_jobs WHERE source_ref=$1 ORDER BY job_id FOR UPDATE`, ref)
	if err != nil {
		return false, err
	}
	for rows.Next() {
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return false, err
	}
	var safe bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM artifact_versions WHERE storage_reference=$1) AND NOT EXISTS(SELECT 1 FROM artifact_versions a LEFT JOIN archive_jobs j ON j.task_id=a.task_id AND j.source_kind='artifact' AND j.resource_id=a.artifact_id AND j.version_id=a.version_id WHERE a.storage_reference=$1 AND (j.job_id IS NULL OR j.state<>'stored' OR j.operation<>'archive'))`, ref).Scan(&safe)
	if err != nil || !safe {
		return false, err
	}
	if _, err = tx.Exec(ctx, `UPDATE archive_jobs SET cache_released_at=COALESCE(cache_released_at,now()) WHERE source_ref=$1`, ref); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}
func (q *ArchiveQueue) CompleteCacheEviction(ctx context.Context, ref string) error {
	_, err := q.store.pool.Exec(ctx, `UPDATE archive_jobs SET cache_evicted_at=now() WHERE source_ref=$1 AND cache_released_at IS NOT NULL`, ref)
	return err
}

func (q *ArchiveQueue) CompleteDelete(ctx context.Context, j archive.Job) error {
	tx, err := q.store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	r, err := tx.Exec(ctx, `UPDATE archive_jobs SET state='deleted',deleted_at=now(),updated_at=now(),lease_until=NULL,error_code='' WHERE job_id=$1 AND lease_version=$2 AND operation='delete' AND state='processing' AND lease_until>now()`, j.ID, j.LeaseVersion)
	if err != nil {
		return err
	}
	if r.RowsAffected() != 1 {
		return archive.ErrLeaseLost
	}
	if _, err = tx.Exec(ctx, `INSERT INTO archive_action_audit(job_id,actor,action,reason) VALUES($1,'archive-worker','delete.completed','Current object key removed; bucket versions follow provider lifecycle')`, j.ID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (q *ArchiveQueue) SetHold(ctx context.Context, id int64, hold bool, actor, reason string) error {
	tx, err := q.store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	r, err := tx.Exec(ctx, `UPDATE archive_jobs SET legal_hold=$2,updated_at=now() WHERE job_id=$1 AND operation='archive' AND state<>'deleted'`, id, hold)
	if err != nil {
		return err
	}
	if r.RowsAffected() != 1 {
		return archive.ErrLeaseLost
	}
	action := "hold.released"
	if hold {
		action = "hold.placed"
	}
	if _, err = tx.Exec(ctx, `INSERT INTO archive_action_audit(job_id,actor,action,reason)VALUES($1,$2,$3,$4)`, id, actor, action, reason); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (q *ArchiveQueue) RecordDownload(ctx context.Context, j archive.Job, id string) error {
	metadata, _ := json.Marshal(map[string]any{"task_id": j.TaskID, "archive_job_id": j.ID, "target_id": j.TargetID, "scope": "successful-full-object-readback"})
	_, err := q.store.pool.Exec(ctx, `INSERT INTO usage_records(usage_id,account_id,organization_id,metric,quantity,unit,resource_id,idempotency_key,metadata,recorded_at)VALUES($1,$2,NULLIF($3,''),'storage.download.bytes',$4,'byte',$5,$1,$6,now())ON CONFLICT(account_id,metric,idempotency_key)DO NOTHING`, id, j.AccountID, j.OrganizationID, j.Size, j.ResourceID, metadata)
	return err
}

func (q *ArchiveQueue) Stats(ctx context.Context) (archive.Stats, error) {
	result := archive.Stats{States: map[string]int64{}}
	rows, err := q.store.pool.Query(ctx, `SELECT state,count(*) FROM archive_jobs GROUP BY state`)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var state string
		var n int64
		if err = rows.Scan(&state, &n); err != nil {
			rows.Close()
			return result, err
		}
		result.States[state] = n
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return result, err
	}
	err = q.store.pool.QueryRow(ctx, `SELECT COALESCE(sum(size) FILTER(WHERE stored_at IS NOT NULL AND deleted_at IS NULL),0),COALESCE(EXTRACT(EPOCH FROM(now()-min(created_at)FILTER(WHERE state IN('queued','retry','processing')))),0) FROM archive_jobs`).Scan(&result.RetainedBytes, &result.OldestPendingSeconds)
	return result, err
}
