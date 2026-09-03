package postgres

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/agent-continuity/agent-continuity-master/internal/storage"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

func (s *Store) CreateAgent(ctx context.Context, value protocol.AgentProfile) error {
	capabilities, _ := json.Marshal(value.Capabilities)
	_, err := s.pool.Exec(ctx, `INSERT INTO agents(agent_id,account_id,name,provider,model,capabilities,card,created_at,revoked_at) VALUES($1,$2,$3,$4,NULLIF($5,''),$6,$7,$8,$9)`, value.ID, value.AccountID, value.Name, value.Provider, value.Model, capabilities, []byte(value.Card), value.CreatedAt, value.RevokedAt)
	return mapError(err)
}

func (s *Store) GetAgent(ctx context.Context, agentID string) (protocol.AgentProfile, error) {
	return scanAgent(s.pool.QueryRow(ctx, `SELECT agent_id,account_id,name,provider,COALESCE(model,''),capabilities,card,created_at,revoked_at FROM agents WHERE agent_id=$1`, agentID))
}

func (s *Store) ListAgents(ctx context.Context, accountID string, limit int) ([]protocol.AgentProfile, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `SELECT agent_id,account_id,name,provider,COALESCE(model,''),capabilities,card,created_at,revoked_at FROM agents WHERE account_id=$1 ORDER BY created_at DESC LIMIT $2`, accountID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]protocol.AgentProfile, 0)
	for rows.Next() {
		value, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func scanAgent(row rowScanner) (protocol.AgentProfile, error) {
	var value protocol.AgentProfile
	var capabilities, card []byte
	err := row.Scan(&value.ID, &value.AccountID, &value.Name, &value.Provider, &value.Model, &capabilities, &card, &value.CreatedAt, &value.RevokedAt)
	if err != nil {
		return protocol.AgentProfile{}, mapError(err)
	}
	_ = json.Unmarshal(capabilities, &value.Capabilities)
	value.Card = card
	return value, nil
}

func (s *Store) RevokeAgent(ctx context.Context, accountID, agentID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var revokedAt time.Time
	err = tx.QueryRow(ctx, `UPDATE agents SET revoked_at=COALESCE(revoked_at,now()) WHERE account_id=$1 AND agent_id=$2 RETURNING revoked_at`, accountID, agentID).Scan(&revokedAt)
	if err != nil {
		return mapError(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE task_participants SET status='revoked',left_at=COALESCE(left_at,$2) WHERE agent_id=$1 AND status='active'`, agentID, revokedAt); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) AddTaskParticipant(ctx context.Context, value protocol.TaskParticipant) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO task_participants(participant_id,task_id,account_id,agent_id,kind,role,status,joined_at,left_at) VALUES($1,$2,NULLIF($3,''),NULLIF($4,''),$5,$6,$7,$8,$9)`, value.ID, value.TaskID, value.AccountID, value.AgentID, value.Kind, value.Role, value.Status, value.JoinedAt, value.LeftAt)
	return mapError(err)
}

func (s *Store) GetTaskParticipant(ctx context.Context, taskID, participantID string) (protocol.TaskParticipant, error) {
	return scanParticipant(s.pool.QueryRow(ctx, `SELECT participant_id,task_id,COALESCE(account_id,''),COALESCE(agent_id,''),kind,role,status,joined_at,left_at FROM task_participants WHERE task_id=$1 AND participant_id=$2`, taskID, participantID))
}

func (s *Store) ListTaskParticipants(ctx context.Context, taskID string) ([]protocol.TaskParticipant, error) {
	rows, err := s.pool.Query(ctx, `SELECT participant_id,task_id,COALESCE(account_id,''),COALESCE(agent_id,''),kind,role,status,joined_at,left_at FROM task_participants WHERE task_id=$1 ORDER BY joined_at`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]protocol.TaskParticipant, 0)
	for rows.Next() {
		value, err := scanParticipant(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func scanParticipant(row rowScanner) (protocol.TaskParticipant, error) {
	var value protocol.TaskParticipant
	err := row.Scan(&value.ID, &value.TaskID, &value.AccountID, &value.AgentID, &value.Kind, &value.Role, &value.Status, &value.JoinedAt, &value.LeftAt)
	return value, mapError(err)
}

func (s *Store) LeaveTaskParticipant(ctx context.Context, taskID, participantID string, leftAt time.Time) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var accountID, role string
	err = tx.QueryRow(ctx, `UPDATE task_participants SET status='left',left_at=$3 WHERE task_id=$1 AND participant_id=$2 AND role<>'owner' RETURNING COALESCE(account_id,''),role`, taskID, participantID, leftAt.UTC()).Scan(&accountID, &role)
	if err != nil {
		return mapError(err)
	}
	if accountID != "" {
		if _, err = tx.Exec(ctx, `DELETE FROM permissions WHERE task_id=$1 AND account_id=$2`, taskID, accountID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) BindTaskAgent(ctx context.Context, binding protocol.TaskAgentBinding, participant protocol.TaskParticipant) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var owner string
	var revoked *time.Time
	if err = tx.QueryRow(ctx, `SELECT account_id,revoked_at FROM agents WHERE agent_id=$1`, binding.AgentID).Scan(&owner, &revoked); err != nil {
		return mapError(err)
	}
	if revoked != nil || owner != participant.AccountID {
		return storage.ErrUnauthorized
	}
	// Agent ownership is validated above, but an Agent participant is not a
	// second human participant for the owner's account. Store a NULL account_id
	// so the scoped human-participant uniqueness constraint remains correct.
	if _, err = tx.Exec(ctx, `INSERT INTO task_participants(participant_id,task_id,account_id,agent_id,kind,role,status,joined_at) VALUES($1,$2,NULL,$3,$4,$5,$6,$7)`, participant.ID, binding.TaskID, binding.AgentID, participant.Kind, participant.Role, participant.Status, participant.JoinedAt); err != nil {
		return mapError(err)
	}
	capabilities, _ := json.Marshal(binding.Capabilities)
	if _, err = tx.Exec(ctx, `INSERT INTO task_agent_bindings(task_id,agent_id,participant_id,native_session_ref,resume_mode,capabilities,metadata,created_at) VALUES($1,$2,$3,NULLIF($4,''),$5,$6,$7,$8)`, binding.TaskID, binding.AgentID, participant.ID, binding.NativeSessionRef, binding.ResumeMode, capabilities, []byte(binding.Metadata), binding.CreatedAt); err != nil {
		return mapError(err)
	}
	return tx.Commit(ctx)
}

func (s *Store) ListTaskAgentBindings(ctx context.Context, taskID string) ([]protocol.TaskAgentBinding, error) {
	rows, err := s.pool.Query(ctx, `SELECT task_id,agent_id,participant_id,COALESCE(native_session_ref,''),resume_mode,capabilities,metadata,created_at FROM task_agent_bindings WHERE task_id=$1 ORDER BY created_at`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]protocol.TaskAgentBinding, 0)
	for rows.Next() {
		var value protocol.TaskAgentBinding
		var capabilities, metadata []byte
		if err := rows.Scan(&value.TaskID, &value.AgentID, &value.ParticipantID, &value.NativeSessionRef, &value.ResumeMode, &capabilities, &metadata, &value.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(capabilities, &value.Capabilities)
		value.Metadata = metadata
		result = append(result, value)
	}
	return result, rows.Err()
}

func (s *Store) CreateNotification(ctx context.Context, value protocol.Notification) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO notifications(notification_id,account_id,task_id,kind,actor_id,subject,details,created_at,read_at) VALUES($1,$2,NULLIF($3,''),$4,NULLIF($5,''),$6,$7,$8,$9)`, value.ID, value.AccountID, value.TaskID, value.Kind, value.ActorID, value.Subject, []byte(value.Details), value.CreatedAt, value.ReadAt)
	return mapError(err)
}

func (s *Store) ListNotifications(ctx context.Context, accountID string, unreadOnly bool, limit int) ([]protocol.Notification, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `SELECT notification_id,account_id,COALESCE(task_id,''),kind,COALESCE(actor_id,''),subject,details,created_at,read_at FROM notifications WHERE account_id=$1 AND ($2=false OR read_at IS NULL) ORDER BY created_at DESC LIMIT $3`, accountID, unreadOnly, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]protocol.Notification, 0)
	for rows.Next() {
		var value protocol.Notification
		var details []byte
		if err := rows.Scan(&value.ID, &value.AccountID, &value.TaskID, &value.Kind, &value.ActorID, &value.Subject, &details, &value.CreatedAt, &value.ReadAt); err != nil {
			return nil, err
		}
		value.Details = details
		result = append(result, value)
	}
	return result, rows.Err()
}

func (s *Store) MarkNotificationRead(ctx context.Context, accountID, notificationID string, readAt time.Time) error {
	tag, err := s.pool.Exec(ctx, `UPDATE notifications SET read_at=COALESCE(read_at,$3) WHERE account_id=$1 AND notification_id=$2`, accountID, notificationID, readAt.UTC())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return storage.ErrNotFound
	}
	return nil
}
