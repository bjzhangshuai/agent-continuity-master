package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/agent-continuity/agent-continuity-master/internal/archive"
	"github.com/jackc/pgx/v5"
)

type ArchiveQueue struct{ store *Store }

func (s *Store) ArchiveQueue() *ArchiveQueue { return &ArchiveQueue{s} }

func (q *ArchiveQueue) Configure(ctx context.Context, c archive.Config) error {
	tx, err := q.store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if c.Enabled {
		for _, target := range c.Targets {
			_, err = tx.Exec(ctx, `INSERT INTO archive_targets(target_id,identity_hash) VALUES($1,$2) ON CONFLICT DO NOTHING`, target.ID, target.Identity())
			if err != nil {
				return err
			}
			var identity string
			if err = tx.QueryRow(ctx, `SELECT identity_hash FROM archive_targets WHERE target_id=$1`, target.ID).Scan(&identity); err != nil {
				return err
			}
			if identity != target.Identity() {
				return fmt.Errorf("archive target %s changed physical destination: use a new target ID", target.ID)
			}
		}
	}
	routes, _ := json.Marshal(c.OrganizationTargets)
	if string(routes) == "null" {
		routes = []byte(`{}`)
	}
	_, err = tx.Exec(ctx, `UPDATE archive_control SET enabled=$1,default_target=$2,organization_targets=$3,max_pending=$4 WHERE singleton`, c.Enabled, c.DefaultTarget, routes, c.MaxPendingJobs)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

const archiveColumns = `job_id,task_id,organization_id,account_id,source_kind,resource_id,version_id,target_id,state,attempts,lease_version,source_ref,content_type,content_hash,size,object_key,error_code,created_at,updated_at,next_attempt`

func scanArchive(row pgx.Row) (archive.Job, error) {
	var j archive.Job
	err := row.Scan(&j.ID, &j.TaskID, &j.OrganizationID, &j.AccountID, &j.Kind, &j.ResourceID, &j.VersionID, &j.TargetID, &j.State, &j.Attempts, &j.LeaseVersion, &j.SourceRef, &j.ContentType, &j.ContentHash, &j.Size, &j.ObjectKey, &j.ErrorCode, &j.CreatedAt, &j.UpdatedAt, &j.NextAttempt)
	if errors.Is(err, pgx.ErrNoRows) {
		err = archive.ErrNotFound
	}
	return j, err
}
func (q *ArchiveQueue) ClaimJob(ctx context.Context, id int64, maxAttempts int, lease time.Duration) (*archive.Job, error) {
	// Exhausted leases become dead letters even if the process crashed on its last attempt.
	_, err := q.store.pool.Exec(ctx, `UPDATE archive_jobs SET state='dead',error_code='lease_expired',updated_at=now(),lease_until=NULL WHERE state='processing' AND lease_until<now() AND attempts >= $1`, maxAttempts)
	if err != nil {
		return nil, err
	}
	j, err := scanArchive(q.store.pool.QueryRow(ctx, `UPDATE archive_jobs SET state='processing',attempts=attempts+1,lease_version=lease_version+1,lease_until=now()+($3 * interval '1 second'),updated_at=now() WHERE job_id=$1 AND ((state IN ('queued','retry') AND next_attempt<=now()) OR (state='processing' AND lease_until<now())) AND attempts<$2 RETURNING `+archiveColumns, id, maxAttempts, int(lease.Seconds())))
	if errors.Is(err, archive.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &j, nil
}
func (q *ArchiveQueue) PendingDispatch(ctx context.Context, limit int) ([]archive.Job, error) {
	if limit < 1 || limit > 100 {
		limit = 100
	}
	rows, err := q.store.pool.Query(ctx, `SELECT `+archiveColumns+` FROM archive_jobs WHERE ((state IN ('queued','retry') AND next_attempt<=now()) OR (state='processing' AND lease_until<now())) AND (dispatched_at IS NULL OR dispatched_at<now()-interval '30 seconds') ORDER BY next_attempt,job_id LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := []archive.Job{}
	for rows.Next() {
		j, err := scanArchive(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}
func (q *ArchiveQueue) MarkDispatched(ctx context.Context, j archive.Job) error {
	_, err := q.store.pool.Exec(ctx, `UPDATE archive_jobs SET dispatched_at=now() WHERE job_id=$1 AND lease_version=$2`, j.ID, j.LeaseVersion)
	return err
}
func (q *ArchiveQueue) Complete(ctx context.Context, j archive.Job, r archive.Receipt) error {
	tx, err := q.store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	res, err := tx.Exec(ctx, `UPDATE archive_jobs SET state='stored',object_key=$3,content_hash=$4,size=$5,error_code='',lease_until=NULL,updated_at=now() WHERE job_id=$1 AND lease_version=$2 AND state='processing' AND lease_until>now()`, j.ID, j.LeaseVersion, r.Key, r.Hash, r.Size)
	if err != nil {
		return err
	}
	if res.RowsAffected() != 1 {
		return archive.ErrLeaseLost
	}
	// Ledger insertion and acknowledgement are atomic; broker retries cannot double count.
	metadata, _ := json.Marshal(map[string]any{"task_id": j.TaskID, "archive_job_id": j.ID, "target_id": j.TargetID, "source_kind": j.Kind, "content_hash": r.Hash})
	_, err = tx.Exec(ctx, `INSERT INTO usage_records(usage_id,account_id,organization_id,metric,quantity,unit,resource_id,idempotency_key,metadata,recorded_at) VALUES($1,$2,NULLIF($3,''),'storage.archive.bytes',$4,'byte',$5,$1,$6,now()) ON CONFLICT(account_id,metric,idempotency_key) DO NOTHING`, fmt.Sprintf("archive_%d", j.ID), j.AccountID, j.OrganizationID, r.Size, j.ResourceID, metadata)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (q *ArchiveQueue) Fail(ctx context.Context, j archive.Job, code string, maxAttempts int, delay time.Duration) error {
	state := "retry"
	if j.Attempts >= maxAttempts {
		state = "dead"
	}
	r, err := q.store.pool.Exec(ctx, `UPDATE archive_jobs SET state=$3,error_code=$4,next_attempt=now()+($5 * interval '1 second'),lease_until=NULL,dispatched_at=NULL,updated_at=now() WHERE job_id=$1 AND lease_version=$2 AND state='processing' AND lease_until>now()`, j.ID, j.LeaseVersion, state, code, int(delay.Seconds()))
	if err != nil {
		return err
	}
	if r.RowsAffected() != 1 {
		return archive.ErrLeaseLost
	}
	return nil
}
func (q *ArchiveQueue) Get(ctx context.Context, id int64) (archive.Job, error) {
	return scanArchive(q.store.pool.QueryRow(ctx, `SELECT `+archiveColumns+` FROM archive_jobs WHERE job_id=$1`, id))
}
func (q *ArchiveQueue) List(ctx context.Context, task string, after int64, limit int) ([]archive.Job, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	rows, err := q.store.pool.Query(ctx, `SELECT `+archiveColumns+` FROM archive_jobs WHERE task_id=$1 AND job_id>$2 ORDER BY job_id LIMIT $3`, task, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []archive.Job{}
	for rows.Next() {
		j, err := scanArchive(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, j)
	}
	return result, rows.Err()
}
func (q *ArchiveQueue) Retry(ctx context.Context, id int64) error {
	r, err := q.store.pool.Exec(ctx, `UPDATE archive_jobs SET state='queued',attempts=0,lease_version=lease_version+1,error_code='',next_attempt=now(),dispatched_at=NULL,updated_at=now() WHERE job_id=$1 AND state='dead'`, id)
	if err != nil {
		return err
	}
	if r.RowsAffected() != 1 {
		return archive.ErrLeaseLost
	}
	return nil
}
func (q *ArchiveQueue) FindArtifact(ctx context.Context, task, id, version string) (archive.Job, error) {
	return scanArchive(q.store.pool.QueryRow(ctx, `SELECT `+archiveColumns+` FROM archive_jobs WHERE task_id=$1 AND source_kind='artifact' AND resource_id=$2 AND version_id=$3`, task, id, version))
}
func (q *ArchiveQueue) Backfill(ctx context.Context, task string, limit int) (int64, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	// Historical uploads are opt-in and task-scoped, never silently exported on startup.
	var count int64
	err := q.store.pool.QueryRow(ctx, `SELECT count(continuity_archive_enqueue(kind,task_id,resource_id,version_id,ref,mime,hash,bytes)) FROM (SELECT 'event' kind,e.task_id,e.event_id resource_id,'' version_id,'' ref,'application/json' mime,'' hash,0::bigint bytes FROM events e WHERE e.task_id=$1 AND NOT EXISTS(SELECT 1 FROM archive_jobs j WHERE j.task_id=e.task_id AND j.source_kind='event' AND j.resource_id=e.event_id) UNION ALL SELECT 'artifact',a.task_id,a.artifact_id,a.version_id,a.storage_reference,a.content_type,a.content_hash,a.size FROM artifact_versions a WHERE a.task_id=$1 AND NOT EXISTS(SELECT 1 FROM archive_jobs j WHERE j.task_id=a.task_id AND j.source_kind='artifact' AND j.resource_id=a.artifact_id AND j.version_id=a.version_id) LIMIT $2) sources`, task, limit).Scan(&count)
	return count, mapError(err)
}
