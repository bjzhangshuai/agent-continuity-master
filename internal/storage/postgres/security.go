package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/agent-continuity/agent-continuity-master/internal/storage"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

func (s *Store) CreateAuthSession(ctx context.Context, value protocol.AuthSession) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO auth_sessions(session_id,family_id,account_id,subject,refresh_token_hash,created_at,expires_at,used_at,revoked_at,replaced_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,NULLIF($10,''))`, value.ID, value.FamilyID, value.AccountID, value.Subject, value.RefreshTokenHash, value.CreatedAt, value.ExpiresAt, value.UsedAt, value.RevokedAt, value.ReplacedBy)
	return mapError(err)
}

func (s *Store) RotateAuthSession(ctx context.Context, oldHash string, replacement protocol.AuthSession) (protocol.AuthSession, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return protocol.AuthSession{}, err
	}
	defer tx.Rollback(ctx)
	var old protocol.AuthSession
	err = tx.QueryRow(ctx, `SELECT session_id,family_id,account_id,subject,refresh_token_hash,created_at,expires_at,used_at,revoked_at,COALESCE(replaced_by,'') FROM auth_sessions WHERE refresh_token_hash=$1 FOR UPDATE`, oldHash).Scan(&old.ID, &old.FamilyID, &old.AccountID, &old.Subject, &old.RefreshTokenHash, &old.CreatedAt, &old.ExpiresAt, &old.UsedAt, &old.RevokedAt, &old.ReplacedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return protocol.AuthSession{}, storage.ErrRefreshInvalid
	}
	if err != nil {
		return protocol.AuthSession{}, err
	}
	now := s.now().UTC()
	if old.UsedAt != nil {
		if _, err = tx.Exec(ctx, `UPDATE auth_sessions SET revoked_at=COALESCE(revoked_at,$2) WHERE family_id=$1`, old.FamilyID, now); err != nil {
			return protocol.AuthSession{}, err
		}
		if err = tx.Commit(ctx); err != nil {
			return protocol.AuthSession{}, err
		}
		return protocol.AuthSession{}, storage.ErrTokenReuse
	}
	if old.RevokedAt != nil || !now.Before(old.ExpiresAt) {
		return protocol.AuthSession{}, storage.ErrRefreshInvalid
	}
	if replacement.AccountID == "" {
		replacement.AccountID = old.AccountID
	}
	if replacement.FamilyID == "" {
		replacement.FamilyID = old.FamilyID
	}
	if replacement.Subject == "" {
		replacement.Subject = old.Subject
	}
	if replacement.AccountID != old.AccountID || replacement.FamilyID != old.FamilyID || replacement.Subject != old.Subject {
		return protocol.AuthSession{}, storage.ErrUnauthorized
	}
	if _, err = tx.Exec(ctx, `INSERT INTO auth_sessions(session_id,family_id,account_id,subject,refresh_token_hash,created_at,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, replacement.ID, replacement.FamilyID, replacement.AccountID, replacement.Subject, replacement.RefreshTokenHash, replacement.CreatedAt, replacement.ExpiresAt); err != nil {
		return protocol.AuthSession{}, mapError(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE auth_sessions SET used_at=$2,replaced_by=$3 WHERE session_id=$1`, old.ID, now, replacement.ID); err != nil {
		return protocol.AuthSession{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return protocol.AuthSession{}, err
	}
	return replacement, nil
}

func (s *Store) RevokeAuthSession(ctx context.Context, accountID, refreshHash string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE auth_sessions SET revoked_at=COALESCE(revoked_at,now()) WHERE family_id=(SELECT family_id FROM auth_sessions WHERE account_id=$1 AND refresh_token_hash=$2)`, accountID, refreshHash)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return storage.ErrNotFound
	}
	return nil
}

func (s *Store) CreateAPIKey(ctx context.Context, value protocol.APIKey) error {
	scopes, _ := json.Marshal(value.Scopes)
	_, err := s.pool.Exec(ctx, `INSERT INTO api_keys(api_key_id,account_id,name,prefix,secret_hash,scopes,created_at,expires_at,last_used_at,revoked_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, value.ID, value.AccountID, value.Name, value.Prefix, value.SecretHash, scopes, value.CreatedAt, value.ExpiresAt, value.LastUsedAt, value.RevokedAt)
	return mapError(err)
}

func (s *Store) ListAPIKeys(ctx context.Context, accountID string) ([]protocol.APIKey, error) {
	rows, err := s.pool.Query(ctx, `SELECT api_key_id,account_id,name,prefix,secret_hash,scopes,created_at,expires_at,last_used_at,revoked_at FROM api_keys WHERE account_id=$1 ORDER BY created_at DESC`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]protocol.APIKey, 0)
	for rows.Next() {
		value, err := scanAPIKey(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (s *Store) AuthenticateAPIKey(ctx context.Context, secretHash string) (protocol.APIKey, error) {
	return scanAPIKey(s.pool.QueryRow(ctx, `UPDATE api_keys SET last_used_at=now() WHERE secret_hash=$1 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at>now()) RETURNING api_key_id,account_id,name,prefix,secret_hash,scopes,created_at,expires_at,last_used_at,revoked_at`, secretHash))
}

func (s *Store) RevokeAPIKey(ctx context.Context, accountID, keyID string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE api_keys SET revoked_at=COALESCE(revoked_at,now()) WHERE account_id=$1 AND api_key_id=$2`, accountID, keyID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return storage.ErrNotFound
	}
	return nil
}

func scanAPIKey(row rowScanner) (protocol.APIKey, error) {
	var value protocol.APIKey
	var scopes []byte
	err := row.Scan(&value.ID, &value.AccountID, &value.Name, &value.Prefix, &value.SecretHash, &scopes, &value.CreatedAt, &value.ExpiresAt, &value.LastUsedAt, &value.RevokedAt)
	if err != nil {
		return protocol.APIKey{}, mapError(err)
	}
	_ = json.Unmarshal(scopes, &value.Scopes)
	return value, nil
}

func (s *Store) RecordAuditEvent(ctx context.Context, value protocol.AuditEvent) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO audit_events(audit_event_id,account_id,actor_id,action,resource_type,resource_id,request_id,source_hash,details,created_at) VALUES($1,$2,$3,$4,$5,$6,NULLIF($7,''),NULLIF($8,''),$9,$10)`, value.ID, value.AccountID, value.ActorID, value.Action, value.ResourceType, value.ResourceID, value.RequestID, value.SourceHash, []byte(value.Details), value.CreatedAt)
	return mapError(err)
}

func (s *Store) ListAuditEvents(ctx context.Context, accountID string, after time.Time, limit int) ([]protocol.AuditEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `SELECT audit_event_id,account_id,actor_id,action,resource_type,resource_id,COALESCE(request_id,''),COALESCE(source_hash,''),details,created_at FROM audit_events WHERE account_id=$1 AND ($2::timestamptz='0001-01-01T00:00:00Z'::timestamptz OR created_at>$2) ORDER BY created_at DESC LIMIT $3`, accountID, after.UTC(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]protocol.AuditEvent, 0)
	for rows.Next() {
		var value protocol.AuditEvent
		var details []byte
		if err := rows.Scan(&value.ID, &value.AccountID, &value.ActorID, &value.Action, &value.ResourceType, &value.ResourceID, &value.RequestID, &value.SourceHash, &details, &value.CreatedAt); err != nil {
			return nil, err
		}
		value.Details = details
		result = append(result, value)
	}
	return result, rows.Err()
}

func (s *Store) PutTaskKeySet(ctx context.Context, value protocol.TaskKeySet) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, value.TaskID); err != nil {
		return err
	}
	var latest uint32
	if err = tx.QueryRow(ctx, `SELECT COALESCE(MAX(key_version),0) FROM task_key_sets WHERE task_id=$1`, value.TaskID).Scan(&latest); err != nil {
		return err
	}
	if value.Version != latest+1 {
		return storage.ErrSequenceReplay
	}
	wrapped, _ := json.Marshal(value.WrappedKeys)
	if _, err = tx.Exec(ctx, `INSERT INTO task_key_sets(task_id,key_version,algorithm,created_by,wrapped_keys,created_at) VALUES($1,$2,$3,$4,$5,$6)`, value.TaskID, value.Version, value.Algorithm, value.CreatedBy, wrapped, value.CreatedAt); err != nil {
		return mapError(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	return nil
}

func (s *Store) GetTaskKeySet(ctx context.Context, taskID string, version uint32) (protocol.TaskKeySet, error) {
	return scanTaskKeySet(s.pool.QueryRow(ctx, `SELECT task_id,key_version,algorithm,created_by,wrapped_keys,created_at FROM task_key_sets WHERE task_id=$1 AND key_version=$2`, taskID, version))
}

func (s *Store) LatestTaskKeySet(ctx context.Context, taskID string) (protocol.TaskKeySet, error) {
	return scanTaskKeySet(s.pool.QueryRow(ctx, `SELECT task_id,key_version,algorithm,created_by,wrapped_keys,created_at FROM task_key_sets WHERE task_id=$1 ORDER BY key_version DESC LIMIT 1`, taskID))
}

func scanTaskKeySet(row rowScanner) (protocol.TaskKeySet, error) {
	var value protocol.TaskKeySet
	var wrapped []byte
	err := row.Scan(&value.TaskID, &value.Version, &value.Algorithm, &value.CreatedBy, &wrapped, &value.CreatedAt)
	if err != nil {
		return protocol.TaskKeySet{}, mapError(err)
	}
	_ = json.Unmarshal(wrapped, &value.WrappedKeys)
	return value, nil
}
