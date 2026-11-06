package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/agent-continuity/agent-continuity-master/internal/archive"
	"github.com/agent-continuity/agent-continuity-master/internal/metering"
	"github.com/agent-continuity/agent-continuity-master/internal/storage"
	"github.com/agent-continuity/agent-continuity-master/migrations"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

type Store struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

func Open(ctx context.Context, databaseURL string) (*Store, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database URL: %w", err)
	}
	config.MaxConns = 10
	config.MinConns = 1
	config.MaxConnIdleTime = 5 * time.Minute
	config.HealthCheckPeriod = 30 * time.Second
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	store := &Store{pool: pool, now: time.Now}
	if err := store.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }
func (s *Store) Close()                         { s.pool.Close() }

func (s *Store) Migrate(ctx context.Context) error {
	lockKey := int64(0x4147434f4e54)
	connection, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer connection.Release()
	if _, err := connection.Exec(ctx, "SELECT pg_advisory_lock($1)", lockKey); err != nil {
		return err
	}
	defer func() {
		unlockContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = connection.Exec(unlockContext, "SELECT pg_advisory_unlock($1)", lockKey)
	}()
	if _, err := connection.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	entries, err := migrations.Files.ReadDir(".")
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".up.sql") {
			continue
		}
		version := strings.TrimSuffix(entry.Name(), ".up.sql")
		var applied bool
		if err := connection.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)`, version).Scan(&applied); err != nil {
			return err
		}
		if applied {
			continue
		}
		data, err := migrations.Files.ReadFile(entry.Name())
		if err != nil {
			return err
		}
		if _, err = connection.Exec(ctx, string(data)); err != nil {
			return fmt.Errorf("apply migration %s: %w", version, err)
		}
		if _, err = connection.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES($1) ON CONFLICT DO NOTHING`, version); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) CreateAccount(ctx context.Context, account protocol.Account) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO accounts(account_id,name,created_at) VALUES($1,$2,$3)`, account.ID, account.Name, account.CreatedAt)
	return mapError(err)
}

func (s *Store) GetAccount(ctx context.Context, id string) (protocol.Account, error) {
	var value protocol.Account
	err := s.pool.QueryRow(ctx, `SELECT account_id,name,created_at FROM accounts WHERE account_id=$1`, id).Scan(&value.ID, &value.Name, &value.CreatedAt)
	return value, mapError(err)
}

func (s *Store) UpdateAccount(ctx context.Context, account protocol.Account) error {
	tag, err := s.pool.Exec(ctx, `UPDATE accounts SET name=$2 WHERE account_id=$1`, account.ID, account.Name)
	if err != nil {
		return mapError(err)
	}
	if tag.RowsAffected() == 0 {
		return storage.ErrNotFound
	}
	return nil
}

func (s *Store) RegisterDevice(ctx context.Context, device protocol.Device) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO devices(device_id,account_id,name,public_key,encryption_public_key,created_at,revoked_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, device.ID, device.AccountID, device.Name, device.PublicKey, device.EncryptionPublicKey, device.CreatedAt, device.RevokedAt)
	return mapError(err)
}

func (s *Store) GetDevice(ctx context.Context, id string) (protocol.Device, error) {
	var value protocol.Device
	err := s.pool.QueryRow(ctx, `SELECT device_id,account_id,name,public_key,encryption_public_key,created_at,revoked_at FROM devices WHERE device_id=$1`, id).Scan(&value.ID, &value.AccountID, &value.Name, &value.PublicKey, &value.EncryptionPublicKey, &value.CreatedAt, &value.RevokedAt)
	return value, mapError(err)
}

func (s *Store) ListDevices(ctx context.Context, accountID string, includeRevoked bool) ([]protocol.Device, error) {
	query := `SELECT device_id,account_id,name,public_key,encryption_public_key,created_at,revoked_at FROM devices WHERE account_id=$1`
	if !includeRevoked {
		query += ` AND revoked_at IS NULL`
	}
	query += ` ORDER BY created_at DESC,device_id`
	rows, err := s.pool.Query(ctx, query, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]protocol.Device, 0)
	for rows.Next() {
		var value protocol.Device
		if err := rows.Scan(&value.ID, &value.AccountID, &value.Name, &value.PublicKey, &value.EncryptionPublicKey, &value.CreatedAt, &value.RevokedAt); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (s *Store) ListTaskDevices(ctx context.Context, taskID string) ([]protocol.Device, error) {
	rows, err := s.pool.Query(ctx, `SELECT d.device_id,d.account_id,d.name,d.public_key,d.encryption_public_key,d.created_at,d.revoked_at FROM devices d JOIN permissions p ON p.account_id=d.account_id WHERE p.task_id=$1 AND d.revoked_at IS NULL AND d.encryption_public_key<>'' ORDER BY d.device_id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]protocol.Device, 0)
	for rows.Next() {
		var value protocol.Device
		if err := rows.Scan(&value.ID, &value.AccountID, &value.Name, &value.PublicKey, &value.EncryptionPublicKey, &value.CreatedAt, &value.RevokedAt); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (s *Store) RevokeDevice(ctx context.Context, accountID, deviceID string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE devices SET revoked_at=COALESCE(revoked_at,now()) WHERE account_id=$1 AND device_id=$2`, accountID, deviceID)
	if err != nil {
		return mapError(err)
	}
	if tag.RowsAffected() == 0 {
		return storage.ErrNotFound
	}
	return nil
}

func (s *Store) CreateTask(ctx context.Context, task protocol.Task) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	heads, _ := json.Marshal(task.HeadEventIDs)
	if _, err = tx.Exec(ctx, `INSERT INTO tasks(task_id,account_id,organization_id,team_id,title,objective,status,origin_device,head_event_ids,metadata,created_at,updated_at) VALUES($1,$2,NULLIF($3,''),NULLIF($4,''),$5,$6,$7,$8,$9,$10,$11,$12)`, task.ID, task.AccountID, task.OrganizationID, task.TeamID, task.Title, task.Objective, task.Status, task.OriginDevice, heads, []byte(task.Metadata), task.CreatedAt, task.UpdatedAt); err != nil {
		return mapError(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO permissions(task_id,account_id,permission) VALUES($1,$2,'admin')`, task.ID, task.AccountID); err != nil {
		return mapError(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO task_branches(task_id,branch_id,name,head_event_ids,status,created_by,created_at) VALUES($1,'main','Main','[]'::jsonb,'active',$2,$3)`, task.ID, task.AccountID, task.CreatedAt); err != nil {
		return mapError(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO task_participants(participant_id,task_id,account_id,kind,role,status,joined_at) VALUES($1,$2,$3,'human','owner','active',$4)`, "participant_owner_"+task.ID, task.ID, task.AccountID, task.CreatedAt); err != nil {
		return mapError(err)
	}
	return tx.Commit(ctx)
}

func (s *Store) GetTask(ctx context.Context, id string) (protocol.Task, error) {
	return scanTask(s.pool.QueryRow(ctx, `SELECT task_id,account_id,COALESCE(organization_id,''),COALESCE(team_id,''),title,objective,status,COALESCE(origin_device,''),head_event_ids,(SELECT count(*) FROM events WHERE task_id=tasks.task_id),(SELECT count(*) FROM artifacts WHERE task_id=tasks.task_id),metadata,created_at,updated_at FROM tasks WHERE task_id=$1`, id))
}

func (s *Store) ListTasks(ctx context.Context, accountID string, limit int) ([]protocol.Task, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT t.task_id,t.account_id,COALESCE(t.organization_id,''),COALESCE(t.team_id,''),t.title,t.objective,t.status,COALESCE(t.origin_device,''),t.head_event_ids,(SELECT count(*) FROM events WHERE task_id=t.task_id),(SELECT count(*) FROM artifacts WHERE task_id=t.task_id),t.metadata,t.created_at,t.updated_at FROM tasks t LEFT JOIN permissions p ON p.task_id=t.task_id AND p.account_id=$1 LEFT JOIN organization_members om ON om.organization_id=t.organization_id AND om.account_id=$1 AND om.status='active' LEFT JOIN team_members tm ON tm.team_id=t.team_id AND tm.account_id=$1 AND tm.status='active' WHERE p.account_id IS NOT NULL OR (om.account_id IS NOT NULL AND (t.team_id IS NULL OR om.role IN ('owner','admin') OR tm.account_id IS NOT NULL)) ORDER BY t.updated_at DESC LIMIT $2`, accountID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]protocol.Task, 0)
	for rows.Next() {
		value, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

type rowScanner interface{ Scan(...any) error }

func scanTask(row rowScanner) (protocol.Task, error) {
	var value protocol.Task
	var heads, metadata []byte
	err := row.Scan(&value.ID, &value.AccountID, &value.OrganizationID, &value.TeamID, &value.Title, &value.Objective, &value.Status, &value.OriginDevice, &heads, &value.EventCount, &value.ArtifactCount, &metadata, &value.CreatedAt, &value.UpdatedAt)
	if err != nil {
		return protocol.Task{}, mapError(err)
	}
	_ = json.Unmarshal(heads, &value.HeadEventIDs)
	value.Metadata = metadata
	return value, nil
}

func (s *Store) AppendEvent(ctx context.Context, value protocol.Event) (protocol.AppendResult, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return protocol.AppendResult{}, err
	}
	defer tx.Rollback(ctx)
	var taskAccount, taskOrganization string
	var headsRaw []byte
	if err = tx.QueryRow(ctx, `SELECT account_id,COALESCE(organization_id,''),head_event_ids FROM tasks WHERE task_id=$1 FOR UPDATE`, value.TaskID).Scan(&taskAccount, &taskOrganization, &headsRaw); err != nil {
		return protocol.AppendResult{}, mapError(err)
	}
	var branchHeadsRaw []byte
	var branchStatus string
	if err = tx.QueryRow(ctx, `SELECT head_event_ids,status FROM task_branches WHERE task_id=$1 AND branch_id=$2 FOR UPDATE`, value.TaskID, value.BranchID).Scan(&branchHeadsRaw, &branchStatus); err != nil {
		return protocol.AppendResult{}, mapError(err)
	}
	if branchStatus != "active" {
		return protocol.AppendResult{}, storage.ErrUnauthorized
	}
	var deviceAccount string
	var revokedAt *time.Time
	if err = tx.QueryRow(ctx, `SELECT account_id,revoked_at FROM devices WHERE device_id=$1`, value.DeviceID).Scan(&deviceAccount, &revokedAt); err != nil {
		return protocol.AppendResult{}, mapError(err)
	}
	if deviceAccount != value.AccountID {
		return protocol.AppendResult{}, storage.ErrUnauthorized
	}
	if revokedAt != nil {
		return protocol.AppendResult{}, storage.ErrDeviceRevoked
	}
	if existing, found, err := findDuplicate(ctx, tx, value); err != nil {
		return protocol.AppendResult{}, err
	} else if found {
		var heads []string
		_ = json.Unmarshal(headsRaw, &heads)
		return protocol.AppendResult{Event: existing, Duplicate: true, TaskHeads: heads}, nil
	}
	var heads []string
	_ = json.Unmarshal(headsRaw, &heads)
	var branchHeads []string
	_ = json.Unmarshal(branchHeadsRaw, &branchHeads)
	conflicts := make([]protocol.Conflict, 0)
	for _, parent := range value.ParentEventIDs {
		var exists bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE event_id=$1 AND task_id=$2)`, parent, value.TaskID).Scan(&exists); err != nil {
			return protocol.AppendResult{}, err
		}
		if !exists {
			conflicts = append(conflicts, newConflict(value, "missing_parent", "defer_until_parent_arrives", map[string]string{"parent_event_id": parent}, s.now()))
		}
	}
	if len(branchHeads) > 0 && !intersects(branchHeads, value.ParentEventIDs) {
		headEvents := make(map[string]protocol.Event, len(branchHeads))
		for _, headID := range branchHeads {
			head, getErr := scanEvent(tx.QueryRow(ctx, `SELECT server_sequence,event_id,task_id,branch_id,account_id,COALESCE(participant_id,''),COALESCE(agent_id,''),COALESCE(provider_id,''),device_id,COALESCE(session_ref,''),parent_event_ids,logical_time,device_sequence,event_type,schema_version,content_type,content_digest,payload,artifact_refs,created_at_client,received_at_server,signature,idempotency_key,extensions FROM events WHERE task_id=$1 AND event_id=$2`, value.TaskID, headID), true)
			if getErr == nil {
				headEvents[headID] = head
			}
		}
		kind, strategy, resolved := classifyConcurrent(value, branchHeads, headEvents)
		conflict := newConflict(value, kind, strategy, map[string][]string{"existing_heads": branchHeads}, s.now())
		conflict.Resolved = resolved
		conflicts = append(conflicts, conflict)
	}
	parentsRaw, _ := json.Marshal(value.ParentEventIDs)
	logicalRaw, _ := json.Marshal(value.LogicalTime)
	refsRaw, _ := json.Marshal(value.ArtifactRefs)
	received := s.now().UTC()
	err = tx.QueryRow(ctx, `INSERT INTO events(event_id,task_id,branch_id,account_id,participant_id,agent_id,provider_id,device_id,session_ref,parent_event_ids,logical_time,device_sequence,event_type,schema_version,content_type,content_digest,payload,artifact_refs,created_at_client,received_at_server,signature,idempotency_key,extensions) VALUES($1,$2,$3,$4,NULLIF($5,''),NULLIF($6,''),NULLIF($7,''),$8,NULLIF($9,''),$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23) RETURNING server_sequence`, value.EventID, value.TaskID, value.BranchID, value.AccountID, value.ParticipantID, value.AgentID, value.ProviderID, value.DeviceID, value.SessionRef, parentsRaw, logicalRaw, int64(value.DeviceSequence), value.Type, value.SchemaVersion, value.ContentType, value.ContentDigest, []byte(value.Payload), refsRaw, value.CreatedAtClient, received, value.Signature, value.IdempotencyKey, []byte(value.Extensions)).Scan(&value.ServerSequence)
	if err != nil {
		if isUnique(err) {
			return protocol.AppendResult{}, storage.ErrSequenceReplay
		}
		return protocol.AppendResult{}, mapError(err)
	}
	value.ReceivedAtServer = received
	usage := metering.AcceptedEvent(value, taskAccount, taskOrganization)
	if _, err = tx.Exec(ctx, `INSERT INTO usage_records(usage_id,account_id,organization_id,metric,quantity,unit,resource_id,idempotency_key,metadata,recorded_at) VALUES($1,$2,NULLIF($3,''),$4,$5,$6,$7,$8,$9,$10)`, usage.ID, usage.AccountID, usage.OrganizationID, usage.Metric, usage.Quantity, usage.Unit, usage.ResourceID, usage.IdempotencyKey, []byte(usage.Metadata), usage.RecordedAt); err != nil {
		return protocol.AppendResult{}, err
	}
	heads = updateHeads(heads, value.ParentEventIDs, value.EventID)
	branchHeads = updateHeads(branchHeads, value.ParentEventIDs, value.EventID)
	var hasKnownChild bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE task_id=$1 AND event_id<>$2 AND parent_event_ids ? $2)`, value.TaskID, value.EventID).Scan(&hasKnownChild); err != nil {
		return protocol.AppendResult{}, err
	}
	if hasKnownChild {
		heads = removeHead(heads, value.EventID)
	}
	var hasKnownBranchChild bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE task_id=$1 AND branch_id=$2 AND event_id<>$3 AND parent_event_ids ? $3)`, value.TaskID, value.BranchID, value.EventID).Scan(&hasKnownBranchChild); err != nil {
		return protocol.AppendResult{}, err
	}
	if hasKnownBranchChild {
		branchHeads = removeHead(branchHeads, value.EventID)
	}
	newHeads, _ := json.Marshal(heads)
	if _, err = tx.Exec(ctx, `UPDATE tasks SET head_event_ids=$2,updated_at=$3 WHERE task_id=$1`, value.TaskID, newHeads, received); err != nil {
		return protocol.AppendResult{}, err
	}
	newBranchHeads, _ := json.Marshal(branchHeads)
	if _, err = tx.Exec(ctx, `UPDATE task_branches SET head_event_ids=$3 WHERE task_id=$1 AND branch_id=$2`, value.TaskID, value.BranchID, newBranchHeads); err != nil {
		return protocol.AppendResult{}, err
	}
	for _, conflict := range conflicts {
		if _, err = tx.Exec(ctx, `INSERT INTO conflicts(conflict_id,task_id,event_id,kind,strategy,details,created_at,resolved) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, conflict.ID, conflict.TaskID, conflict.EventID, conflict.Kind, conflict.Strategy, []byte(conflict.Details), conflict.CreatedAt, conflict.Resolved); err != nil {
			return protocol.AppendResult{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return protocol.AppendResult{}, err
	}
	return protocol.AppendResult{Event: value, Conflicts: conflicts, TaskHeads: heads}, nil
}

func findDuplicate(ctx context.Context, tx pgx.Tx, value protocol.Event) (protocol.Event, bool, error) {
	row := tx.QueryRow(ctx, `SELECT server_sequence,event_id,task_id,branch_id,account_id,COALESCE(participant_id,''),COALESCE(agent_id,''),COALESCE(provider_id,''),device_id,COALESCE(session_ref,''),parent_event_ids,logical_time,device_sequence,event_type,schema_version,content_type,content_digest,payload,artifact_refs,created_at_client,received_at_server,signature,idempotency_key,extensions FROM events WHERE event_id=$1 OR (task_id=$2 AND device_id=$3 AND idempotency_key=$4) LIMIT 1`, value.EventID, value.TaskID, value.DeviceID, value.IdempotencyKey)
	event, err := scanEvent(row, true)
	if errors.Is(err, storage.ErrNotFound) {
		return protocol.Event{}, false, nil
	}
	if err == nil && !storage.SameEventOperation(event, value) {
		return protocol.Event{}, false, storage.ErrSequenceReplay
	}
	return event, err == nil, err
}

func (s *Store) ListEvents(ctx context.Context, taskID string, after uint64, limit int, includePayload bool) ([]protocol.Event, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.pool.Query(ctx, `SELECT server_sequence,event_id,task_id,branch_id,account_id,COALESCE(participant_id,''),COALESCE(agent_id,''),COALESCE(provider_id,''),device_id,COALESCE(session_ref,''),parent_event_ids,logical_time,device_sequence,event_type,schema_version,content_type,content_digest,payload,artifact_refs,created_at_client,received_at_server,signature,idempotency_key,extensions FROM events WHERE task_id=$1 AND server_sequence>$2 ORDER BY server_sequence LIMIT $3`, taskID, int64(after), limit)
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

func (s *Store) GetTaskEventCursor(ctx context.Context, taskID string) (uint64, error) {
	var value int64
	err := s.pool.QueryRow(ctx, `SELECT COALESCE(MAX(server_sequence),0) FROM events WHERE task_id=$1`, taskID).Scan(&value)
	if err != nil {
		return 0, mapError(err)
	}
	return uint64(value), nil
}

func (s *Store) RecordSyncNonce(ctx context.Context, deviceID, nonceHash string, createdAt, expiresAt time.Time) error {
	_, _ = s.pool.Exec(ctx, `DELETE FROM sync_nonces WHERE expires_at<$1`, createdAt)
	_, err := s.pool.Exec(ctx, `INSERT INTO sync_nonces(device_id,nonce_hash,created_at,expires_at) VALUES($1,$2,$3,$4)`, deviceID, nonceHash, createdAt, expiresAt)
	return mapError(err)
}

func (s *Store) GetSyncCursor(ctx context.Context, deviceID, taskID string) (uint64, error) {
	var value int64
	err := s.pool.QueryRow(ctx, `SELECT server_sequence FROM sync_cursors WHERE device_id=$1 AND task_id=$2`, deviceID, taskID).Scan(&value)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, mapError(err)
	}
	return uint64(value), nil
}

func (s *Store) PutSyncCursor(ctx context.Context, deviceID, taskID string, cursor uint64, updatedAt time.Time) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO sync_cursors(device_id,task_id,server_sequence,updated_at) VALUES($1,$2,$3,$4)
		ON CONFLICT(device_id,task_id) DO UPDATE SET server_sequence=GREATEST(sync_cursors.server_sequence,EXCLUDED.server_sequence),updated_at=CASE WHEN EXCLUDED.server_sequence>=sync_cursors.server_sequence THEN EXCLUDED.updated_at ELSE sync_cursors.updated_at END`, deviceID, taskID, int64(cursor), updatedAt)
	return mapError(err)
}

func scanEvent(row rowScanner, includePayload bool) (protocol.Event, error) {
	var value protocol.Event
	var parentsRaw, logicalRaw, payload, refsRaw, extensions []byte
	var seq int64
	err := row.Scan(&value.ServerSequence, &value.EventID, &value.TaskID, &value.BranchID, &value.AccountID, &value.ParticipantID, &value.AgentID, &value.ProviderID, &value.DeviceID, &value.SessionRef, &parentsRaw, &logicalRaw, &seq, &value.Type, &value.SchemaVersion, &value.ContentType, &value.ContentDigest, &payload, &refsRaw, &value.CreatedAtClient, &value.ReceivedAtServer, &value.Signature, &value.IdempotencyKey, &extensions)
	if err != nil {
		return protocol.Event{}, mapError(err)
	}
	value.DeviceSequence = uint64(seq)
	_ = json.Unmarshal(parentsRaw, &value.ParentEventIDs)
	_ = json.Unmarshal(logicalRaw, &value.LogicalTime)
	_ = json.Unmarshal(refsRaw, &value.ArtifactRefs)
	if includePayload {
		value.Payload = payload
	}
	value.Extensions = extensions
	return value, nil
}

func (s *Store) PutArtifact(ctx context.Context, value protocol.Artifact) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var immutableHash string
	immutableErr := tx.QueryRow(ctx, `SELECT content_hash FROM artifact_versions WHERE task_id=$1 AND artifact_id=$2 AND version_id=$3`, value.TaskID, value.ID, value.VersionID).Scan(&immutableHash)
	if immutableErr == nil {
		if immutableHash != value.ContentHash {
			return storage.ErrAlreadyExists
		}
		return nil
	}
	if !errors.Is(immutableErr, pgx.ErrNoRows) {
		return immutableErr
	}
	var currentVersion string
	err = tx.QueryRow(ctx, `SELECT version_id FROM artifacts WHERE task_id=$1 AND artifact_id=$2 FOR UPDATE`, value.TaskID, value.ID).Scan(&currentVersion)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err == nil && currentVersion != value.VersionID && value.ParentVersionID != currentVersion {
		return storage.ErrAlreadyExists
	}
	if _, err = tx.Exec(ctx, `INSERT INTO artifact_versions(task_id,artifact_id,version_id,parent_version_id,account_id,content_type,filename,size,content_hash,chunk_manifest_hash,producer_type,producer_id,device_id,storage_reference,metadata,encryption_metadata,created_at) VALUES($1,$2,$3,NULLIF($4,''),$5,$6,$7,$8,$9,NULLIF($10,''),NULLIF($11,''),NULLIF($12,''),NULLIF($13,''),$14,$15,$16,$17) ON CONFLICT(task_id,artifact_id,version_id) DO NOTHING`, value.TaskID, value.ID, value.VersionID, value.ParentVersionID, value.AccountID, value.ContentType, value.Filename, value.Size, value.ContentHash, value.ChunkManifestHash, value.ProducerType, value.ProducerID, value.DeviceID, value.StorageRef, []byte(value.Metadata), []byte(value.EncryptionMetadata), value.CreatedAt); err != nil {
		return mapError(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO artifacts(task_id,artifact_id,account_id,version_id,parent_version_id,content_type,filename,size,content_hash,chunk_manifest_hash,producer_type,producer_id,device_id,storage_reference,metadata,encryption_metadata,created_at) VALUES($1,$2,$3,$4,NULLIF($5,''),$6,$7,$8,$9,NULLIF($10,''),NULLIF($11,''),NULLIF($12,''),NULLIF($13,''),$14,$15,$16,$17) ON CONFLICT(task_id,artifact_id) DO UPDATE SET account_id=EXCLUDED.account_id,version_id=EXCLUDED.version_id,parent_version_id=EXCLUDED.parent_version_id,content_type=EXCLUDED.content_type,filename=EXCLUDED.filename,size=EXCLUDED.size,content_hash=EXCLUDED.content_hash,chunk_manifest_hash=EXCLUDED.chunk_manifest_hash,producer_type=EXCLUDED.producer_type,producer_id=EXCLUDED.producer_id,device_id=EXCLUDED.device_id,storage_reference=EXCLUDED.storage_reference,metadata=EXCLUDED.metadata,encryption_metadata=EXCLUDED.encryption_metadata,created_at=EXCLUDED.created_at`, value.TaskID, value.ID, value.AccountID, value.VersionID, value.ParentVersionID, value.ContentType, value.Filename, value.Size, value.ContentHash, value.ChunkManifestHash, value.ProducerType, value.ProducerID, value.DeviceID, value.StorageRef, []byte(value.Metadata), []byte(value.EncryptionMetadata), value.CreatedAt); err != nil {
		return mapError(err)
	}
	return tx.Commit(ctx)
}
func (s *Store) GetArtifact(ctx context.Context, taskID, artifactID string) (protocol.Artifact, error) {
	return scanArtifact(s.pool.QueryRow(ctx, `SELECT artifact_id,task_id,account_id,version_id,COALESCE(parent_version_id,''),content_type,filename,size,content_hash,COALESCE(chunk_manifest_hash,''),COALESCE(producer_type,''),COALESCE(producer_id,''),COALESCE(device_id,''),storage_reference,metadata,encryption_metadata,created_at FROM artifacts WHERE task_id=$1 AND artifact_id=$2`, taskID, artifactID))
}
func (s *Store) ListArtifacts(ctx context.Context, taskID string) ([]protocol.Artifact, error) {
	rows, err := s.pool.Query(ctx, `SELECT artifact_id,task_id,account_id,version_id,COALESCE(parent_version_id,''),content_type,filename,size,content_hash,COALESCE(chunk_manifest_hash,''),COALESCE(producer_type,''),COALESCE(producer_id,''),COALESCE(device_id,''),storage_reference,metadata,encryption_metadata,created_at FROM artifacts WHERE task_id=$1 ORDER BY created_at`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]protocol.Artifact, 0)
	for rows.Next() {
		value, err := scanArtifact(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}
func scanArtifact(row rowScanner) (protocol.Artifact, error) {
	var value protocol.Artifact
	var metadata, encryptionMetadata []byte
	err := row.Scan(&value.ID, &value.TaskID, &value.AccountID, &value.VersionID, &value.ParentVersionID, &value.ContentType, &value.Filename, &value.Size, &value.ContentHash, &value.ChunkManifestHash, &value.ProducerType, &value.ProducerID, &value.DeviceID, &value.StorageRef, &metadata, &encryptionMetadata, &value.CreatedAt)
	value.Metadata = metadata
	value.EncryptionMetadata = encryptionMetadata
	return value, mapError(err)
}

func (s *Store) CreateInvitation(ctx context.Context, value protocol.Invitation) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO invitations(invitation_id,task_id,inviter_account_id,permission,token_hash,inviter_device_id,nonce,token_digest,key_exchange_reference,signature,expires_at,created_at) VALUES($1,$2,$3,$4,$5,NULLIF($6,''),NULLIF($7,''),NULLIF($8,''),$9,NULLIF($10,''),$11,$12)`, value.ID, value.TaskID, value.InviterAccount, value.Permission, value.TokenHash, value.InviterDeviceID, value.Nonce, value.TokenDigest, []byte(value.KeyExchangeReference), value.Signature, value.ExpiresAt, value.CreatedAt)
	return mapError(err)
}
func (s *Store) AcceptInvitation(ctx context.Context, token, accountID string) (protocol.Invitation, error) {
	hash := sha256.Sum256([]byte(token))
	tokenHash := hex.EncodeToString(hash[:])
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return protocol.Invitation{}, err
	}
	defer tx.Rollback(ctx)
	var value protocol.Invitation
	var keyExchangeReference []byte
	err = tx.QueryRow(ctx, `SELECT invitation_id,task_id,inviter_account_id,permission,token_hash,COALESCE(inviter_device_id,''),COALESCE(nonce,''),COALESCE(token_digest,''),key_exchange_reference,COALESCE(signature,''),expires_at,created_at,COALESCE(accepted_by,''),accepted_at,revoked_at FROM invitations WHERE token_hash=$1 FOR UPDATE`, tokenHash).Scan(&value.ID, &value.TaskID, &value.InviterAccount, &value.Permission, &value.TokenHash, &value.InviterDeviceID, &value.Nonce, &value.TokenDigest, &keyExchangeReference, &value.Signature, &value.ExpiresAt, &value.CreatedAt, &value.AcceptedBy, &value.AcceptedAt, &value.RevokedAt)
	if err != nil {
		return protocol.Invitation{}, storage.ErrInviteInvalid
	}
	value.KeyExchangeReference = keyExchangeReference
	if value.AcceptedAt != nil || value.RevokedAt != nil || !s.now().Before(value.ExpiresAt) {
		return protocol.Invitation{}, storage.ErrInviteInvalid
	}
	now := s.now().UTC()
	value.AcceptedBy = accountID
	value.AcceptedAt = &now
	if _, err = tx.Exec(ctx, `UPDATE invitations SET accepted_by=$2,accepted_at=$3 WHERE invitation_id=$1`, value.ID, accountID, now); err != nil {
		return protocol.Invitation{}, mapError(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO permissions(task_id,account_id,permission) VALUES($1,$2,$3) ON CONFLICT(task_id,account_id) DO UPDATE SET permission=EXCLUDED.permission`, value.TaskID, accountID, value.Permission); err != nil {
		return protocol.Invitation{}, mapError(err)
	}
	participantID, _ := protocol.NewID("participant")
	role := "viewer"
	if value.Permission == protocol.PermissionWrite || value.Permission == protocol.PermissionAdmin {
		role = "contributor"
	}
	if _, err = tx.Exec(ctx, `INSERT INTO task_participants(participant_id,task_id,account_id,kind,role,status,joined_at) VALUES($1,$2,$3,'human',$4,'active',$5) ON CONFLICT(task_id,account_id) WHERE account_id IS NOT NULL DO UPDATE SET status='active',left_at=NULL,role=EXCLUDED.role`, participantID, value.TaskID, accountID, role, now); err != nil {
		return protocol.Invitation{}, mapError(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return protocol.Invitation{}, err
	}
	return value, nil
}
func (s *Store) RevokeInvitation(ctx context.Context, accountID, invitationID string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE invitations SET revoked_at=COALESCE(revoked_at,now()) WHERE invitation_id=$1 AND inviter_account_id=$2`, invitationID, accountID)
	if err != nil {
		return mapError(err)
	}
	if tag.RowsAffected() == 0 {
		return storage.ErrNotFound
	}
	return nil
}
func (s *Store) CheckPermission(ctx context.Context, taskID, accountID string, required protocol.Permission) error {
	t, err := s.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if t.OrganizationID != "" {
		m, e := s.GetOrganizationMember(ctx, t.OrganizationID, accountID)
		if e != nil || m.Role == "billing" {
			return storage.ErrUnauthorized
		}
		if m.Role == "owner" || m.Role == "admin" {
			return nil
		}
		if required == protocol.PermissionAdmin || required == protocol.PermissionWrite && m.Role != "member" {
			return storage.ErrUnauthorized
		}
		if t.TeamID != "" {
			tm, e := s.GetTeamMember(ctx, t.TeamID, accountID)
			if e != nil || required == protocol.PermissionWrite && tm.Role == "viewer" {
				return storage.ErrUnauthorized
			}
		}
		return nil
	}
	var actual protocol.Permission
	err = s.pool.QueryRow(ctx, `SELECT permission FROM permissions WHERE task_id=$1 AND account_id=$2`, taskID, accountID).Scan(&actual)
	if err != nil {
		return storage.ErrUnauthorized
	}
	if permissionRank(actual) < permissionRank(required) {
		return storage.ErrUnauthorized
	}
	return nil
}

func (s *Store) ListTaskPermissions(ctx context.Context, taskID string) ([]protocol.TaskPermission, error) {
	rows, err := s.pool.Query(ctx, `SELECT task_id,account_id,permission,created_at FROM permissions WHERE task_id=$1 ORDER BY created_at,account_id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]protocol.TaskPermission, 0)
	for rows.Next() {
		var value protocol.TaskPermission
		if err := rows.Scan(&value.TaskID, &value.AccountID, &value.Permission, &value.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(result) == 0 {
		if _, err := s.GetTask(ctx, taskID); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (s *Store) PutTaskPermission(ctx context.Context, value protocol.TaskPermission) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var owner string
	if err = tx.QueryRow(ctx, `SELECT account_id FROM tasks WHERE task_id=$1 FOR UPDATE`, value.TaskID).Scan(&owner); err != nil {
		return mapError(err)
	}
	if owner == value.AccountID && value.Permission != protocol.PermissionAdmin {
		return storage.ErrUnauthorized
	}
	if _, err = tx.Exec(ctx, `INSERT INTO permissions(task_id,account_id,permission,created_at) VALUES($1,$2,$3,$4) ON CONFLICT(task_id,account_id) DO UPDATE SET permission=EXCLUDED.permission,created_at=EXCLUDED.created_at`, value.TaskID, value.AccountID, value.Permission, value.CreatedAt.UTC()); err != nil {
		return mapError(err)
	}
	role := "viewer"
	if value.Permission == protocol.PermissionWrite || value.Permission == protocol.PermissionAdmin {
		role = "contributor"
	}
	participantID, _ := protocol.NewID("participant")
	if _, err = tx.Exec(ctx, `INSERT INTO task_participants(participant_id,task_id,account_id,kind,role,status,joined_at) VALUES($1,$2,$3,'human',$4,'active',$5) ON CONFLICT(task_id,account_id) WHERE account_id IS NOT NULL DO UPDATE SET role=CASE WHEN task_participants.role='owner' THEN 'owner' ELSE EXCLUDED.role END,status='active',left_at=NULL`, participantID, value.TaskID, value.AccountID, role, value.CreatedAt.UTC()); err != nil {
		return mapError(err)
	}
	return tx.Commit(ctx)
}

func (s *Store) DeleteTaskPermission(ctx context.Context, taskID, accountID string, removedAt time.Time) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var owner string
	if err = tx.QueryRow(ctx, `SELECT account_id FROM tasks WHERE task_id=$1 FOR UPDATE`, taskID).Scan(&owner); err != nil {
		return mapError(err)
	}
	if owner == accountID {
		return storage.ErrUnauthorized
	}
	tag, err := tx.Exec(ctx, `DELETE FROM permissions WHERE task_id=$1 AND account_id=$2`, taskID, accountID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return storage.ErrNotFound
	}
	if _, err = tx.Exec(ctx, `UPDATE task_participants SET status='left',left_at=$3 WHERE task_id=$1 AND account_id=$2 AND status='active'`, taskID, accountID, removedAt.UTC()); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func mapError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return storage.ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "P0001":
			if pgErr.Message == "archive_queue_full" {
				return archive.ErrQueueFull
			}
		case "23505":
			return storage.ErrAlreadyExists
		case "23503":
			return storage.ErrNotFound
		}
	}
	return err
}
func isUnique(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
func permissionRank(permission protocol.Permission) int {
	switch permission {
	case protocol.PermissionRead:
		return 1
	case protocol.PermissionWrite:
		return 2
	case protocol.PermissionAdmin:
		return 3
	}
	return 0
}
func updateHeads(heads, parents []string, eventID string) []string {
	parentSet := map[string]struct{}{}
	for _, id := range parents {
		parentSet[id] = struct{}{}
	}
	result := make([]string, 0, len(heads)+1)
	for _, head := range heads {
		if _, ok := parentSet[head]; !ok {
			result = append(result, head)
		}
	}
	result = append(result, eventID)
	sort.Strings(result)
	return result
}
func intersects(a, b []string) bool {
	set := map[string]struct{}{}
	for _, x := range a {
		set[x] = struct{}{}
	}
	for _, x := range b {
		if _, ok := set[x]; ok {
			return true
		}
	}
	return false
}
func removeHead(values []string, unwanted string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != unwanted {
			result = append(result, value)
		}
	}
	return result
}
func newConflict(value protocol.Event, kind, strategy string, details any, now time.Time) protocol.Conflict {
	id, _ := protocol.NewID("conflict")
	raw, _ := json.Marshal(details)
	return protocol.Conflict{ID: id, TaskID: value.TaskID, EventID: value.EventID, Kind: kind, Strategy: strategy, Details: raw, CreatedAt: now.UTC()}
}
