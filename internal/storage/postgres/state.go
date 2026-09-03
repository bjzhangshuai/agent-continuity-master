package postgres

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/agent-continuity/agent-continuity-master/internal/storage"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

const eventSelect = `SELECT server_sequence,event_id,task_id,branch_id,account_id,COALESCE(participant_id,''),COALESCE(agent_id,''),COALESCE(provider_id,''),device_id,COALESCE(session_ref,''),parent_event_ids,logical_time,device_sequence,event_type,schema_version,content_type,content_digest,payload,artifact_refs,created_at_client,received_at_server,signature,idempotency_key,extensions FROM events`

func (s *Store) GetEvent(ctx context.Context, taskID, eventID string) (protocol.Event, error) {
	return scanEvent(s.pool.QueryRow(ctx, eventSelect+` WHERE task_id=$1 AND event_id=$2`, taskID, eventID), true)
}

func (s *Store) ListBranchEvents(ctx context.Context, taskID, branchID string, after uint64, limit int, includePayload bool) ([]protocol.Event, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.pool.Query(ctx, eventSelect+` WHERE task_id=$1 AND branch_id=$2 AND server_sequence>$3 ORDER BY server_sequence LIMIT $4`, taskID, branchID, int64(after), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]protocol.Event, 0)
	for rows.Next() {
		value, err := scanEvent(rows, includePayload)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (s *Store) CreateBranch(ctx context.Context, value protocol.TaskBranch) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var allowed bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM permissions WHERE task_id=$1 AND account_id=$2)`, value.TaskID, value.CreatedBy).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return storage.ErrUnauthorized
	}
	heads := []string{}
	if value.BaseEventID != "" {
		var exists bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE task_id=$1 AND event_id=$2)`, value.TaskID, value.BaseEventID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return storage.ErrNotFound
		}
		heads = []string{value.BaseEventID}
	}
	headsRaw, _ := json.Marshal(heads)
	_, err = tx.Exec(ctx, `INSERT INTO task_branches(task_id,branch_id,name,base_event_id,head_event_ids,status,created_by,created_at) VALUES($1,$2,$3,NULLIF($4,''),$5,$6,$7,$8)`, value.TaskID, value.ID, value.Name, value.BaseEventID, headsRaw, value.Status, value.CreatedBy, value.CreatedAt)
	if err != nil {
		return mapError(err)
	}
	return tx.Commit(ctx)
}

func (s *Store) GetBranch(ctx context.Context, taskID, branchID string) (protocol.TaskBranch, error) {
	return scanBranch(s.pool.QueryRow(ctx, `SELECT task_id,branch_id,name,COALESCE(base_event_id,''),head_event_ids,status,created_by,created_at,COALESCE(merged_into,''),merged_at FROM task_branches WHERE task_id=$1 AND branch_id=$2`, taskID, branchID))
}

func (s *Store) ListBranches(ctx context.Context, taskID string) ([]protocol.TaskBranch, error) {
	rows, err := s.pool.Query(ctx, `SELECT task_id,branch_id,name,COALESCE(base_event_id,''),head_event_ids,status,created_by,created_at,COALESCE(merged_into,''),merged_at FROM task_branches WHERE task_id=$1 ORDER BY created_at`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]protocol.TaskBranch, 0)
	for rows.Next() {
		value, err := scanBranch(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func scanBranch(row rowScanner) (protocol.TaskBranch, error) {
	var value protocol.TaskBranch
	var heads []byte
	err := row.Scan(&value.TaskID, &value.ID, &value.Name, &value.BaseEventID, &heads, &value.Status, &value.CreatedBy, &value.CreatedAt, &value.MergedInto, &value.MergedAt)
	if err != nil {
		return protocol.TaskBranch{}, mapError(err)
	}
	_ = json.Unmarshal(heads, &value.HeadEventIDs)
	return value, nil
}

func (s *Store) MarkBranchMerged(ctx context.Context, taskID, sourceBranchID, targetBranchID string, mergedAt time.Time) error {
	tag, err := s.pool.Exec(ctx, `UPDATE task_branches source SET status='merged',merged_into=$3,merged_at=$4 WHERE source.task_id=$1 AND source.branch_id=$2 AND source.status='active' AND source.branch_id<>$3 AND EXISTS(SELECT 1 FROM task_branches target WHERE target.task_id=$1 AND target.branch_id=$3 AND target.status='active')`, taskID, sourceBranchID, targetBranchID, mergedAt.UTC())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return storage.ErrNotFound
	}
	return nil
}

func (s *Store) ListConflicts(ctx context.Context, taskID string, includeResolved bool, limit int) ([]protocol.Conflict, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `SELECT conflict_id,task_id,event_id,kind,strategy,details,created_at,resolved FROM conflicts WHERE task_id=$1 AND ($2 OR NOT resolved) ORDER BY created_at LIMIT $3`, taskID, includeResolved, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]protocol.Conflict, 0)
	for rows.Next() {
		var value protocol.Conflict
		var details []byte
		if err := rows.Scan(&value.ID, &value.TaskID, &value.EventID, &value.Kind, &value.Strategy, &details, &value.CreatedAt, &value.Resolved); err != nil {
			return nil, err
		}
		value.Details = details
		result = append(result, value)
	}
	return result, rows.Err()
}

func (s *Store) ResolveConflict(ctx context.Context, value protocol.ConflictResolution) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var resolved bool
	if err = tx.QueryRow(ctx, `SELECT resolved FROM conflicts WHERE conflict_id=$1 AND task_id=$2 FOR UPDATE`, value.ConflictID, value.TaskID).Scan(&resolved); err != nil {
		return mapError(err)
	}
	if resolved {
		return storage.ErrAlreadyExists
	}
	var permission protocol.Permission
	if err = tx.QueryRow(ctx, `SELECT permission FROM permissions WHERE task_id=$1 AND account_id=$2`, value.TaskID, value.AccountID).Scan(&permission); err != nil || permissionRank(permission) < permissionRank(protocol.PermissionWrite) {
		return storage.ErrUnauthorized
	}
	if value.ResolutionEventID != "" {
		var exists bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE task_id=$1 AND event_id=$2)`, value.TaskID, value.ResolutionEventID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return storage.ErrNotFound
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO conflict_resolutions(resolution_id,conflict_id,task_id,account_id,strategy,resolution_event_id,notes,created_at) VALUES($1,$2,$3,$4,$5,NULLIF($6,''),NULLIF($7,''),$8)`, value.ID, value.ConflictID, value.TaskID, value.AccountID, value.Strategy, value.ResolutionEventID, value.Notes, value.CreatedAt); err != nil {
		return mapError(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE conflicts SET resolved=true WHERE conflict_id=$1`, value.ConflictID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) PutSnapshot(ctx context.Context, value protocol.TaskSnapshot) error {
	state, _ := json.Marshal(value.State)
	var existingDigest string
	err := s.pool.QueryRow(ctx, `INSERT INTO task_snapshots(snapshot_id,task_id,branch_id,through_server_sequence,event_count,state,state_digest,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(task_id,branch_id,through_server_sequence) DO UPDATE SET state_digest=task_snapshots.state_digest RETURNING state_digest`, value.ID, value.TaskID, value.BranchID, int64(value.ThroughServerSequence), value.EventCount, state, value.StateDigest, value.CreatedAt).Scan(&existingDigest)
	if err != nil {
		return mapError(err)
	}
	if existingDigest != value.StateDigest {
		return storage.ErrAlreadyExists
	}
	return nil
}

func (s *Store) LatestSnapshot(ctx context.Context, taskID, branchID string) (protocol.TaskSnapshot, error) {
	var value protocol.TaskSnapshot
	var state []byte
	var sequence int64
	err := s.pool.QueryRow(ctx, `SELECT snapshot_id,task_id,branch_id,through_server_sequence,event_count,state,state_digest,created_at FROM task_snapshots WHERE task_id=$1 AND branch_id=$2 ORDER BY through_server_sequence DESC LIMIT 1`, taskID, branchID).Scan(&value.ID, &value.TaskID, &value.BranchID, &sequence, &value.EventCount, &state, &value.StateDigest, &value.CreatedAt)
	if err != nil {
		return protocol.TaskSnapshot{}, mapError(err)
	}
	value.ThroughServerSequence = uint64(sequence)
	if err := json.Unmarshal(state, &value.State); err != nil {
		return protocol.TaskSnapshot{}, err
	}
	return value, nil
}

func classifyConcurrent(incoming protocol.Event, headIDs []string, events map[string]protocol.Event) (string, string, bool) {
	for _, headID := range headIDs {
		head := events[headID]
		if permissionEvent(incoming.Type) && permissionEvent(head.Type) {
			return "permission_conflict", "ask_user", false
		}
		if artifactConflict(incoming, head) {
			if incoming.Type == "artifact.deleted" || head.Type == "artifact.deleted" {
				return "delete_vs_update_conflict", "ask_user", false
			}
			return "artifact_version_conflict", "create_branch", false
		}
		if semanticEvent(incoming.Type) && semanticEvent(head.Type) {
			return "semantic_conflict", "ask_user", false
		}
	}
	return "non_conflicting_concurrent", "deterministic_merge", true
}

func permissionEvent(value string) bool {
	return value == "permission.granted" || value == "permission.revoked"
}
func semanticEvent(value string) bool {
	return value == "task.metadata.updated" || value == "progress.updated" || value == "task.completed" || value == "task.paused" || value == "task.resumed"
}
func artifactConflict(a, b protocol.Event) bool {
	if len(a.ArtifactRefs) == 0 || len(b.ArtifactRefs) == 0 {
		return false
	}
	if a.Type != "artifact.created" && a.Type != "artifact.version.created" && a.Type != "artifact.deleted" {
		return false
	}
	if b.Type != "artifact.created" && b.Type != "artifact.version.created" && b.Type != "artifact.deleted" {
		return false
	}
	return intersects(a.ArtifactRefs, b.ArtifactRefs)
}
