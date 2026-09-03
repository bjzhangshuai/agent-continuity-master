package postgres

import (
	"context"

	"github.com/agent-continuity/agent-continuity-master/internal/storage"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

func (s *Store) PutExternalIdentity(ctx context.Context, value protocol.ExternalIdentity) error {
	tag, err := s.pool.Exec(ctx, `INSERT INTO external_identities(external_identity_id,provider,subject,account_id,email,created_at) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(provider,subject) DO UPDATE SET email=EXCLUDED.email WHERE external_identities.external_identity_id=EXCLUDED.external_identity_id AND external_identities.account_id=EXCLUDED.account_id`, value.ID, value.Provider, value.Subject, value.AccountID, value.Email, value.CreatedAt)
	if err != nil {
		return mapError(err)
	}
	if tag.RowsAffected() == 0 {
		return storage.ErrAlreadyExists
	}
	return nil
}

func (s *Store) GetExternalIdentity(ctx context.Context, provider, subject string) (protocol.ExternalIdentity, error) {
	var value protocol.ExternalIdentity
	err := s.pool.QueryRow(ctx, `SELECT external_identity_id,provider,subject,account_id,email,created_at FROM external_identities WHERE provider=$1 AND subject=$2`, provider, subject).Scan(&value.ID, &value.Provider, &value.Subject, &value.AccountID, &value.Email, &value.CreatedAt)
	return value, mapError(err)
}

func (s *Store) GetExternalIdentityByID(ctx context.Context, id string) (protocol.ExternalIdentity, error) {
	var value protocol.ExternalIdentity
	err := s.pool.QueryRow(ctx, `SELECT external_identity_id,provider,subject,account_id,email,created_at FROM external_identities WHERE external_identity_id=$1`, id).Scan(&value.ID, &value.Provider, &value.Subject, &value.AccountID, &value.Email, &value.CreatedAt)
	return value, mapError(err)
}

func (s *Store) ListExternalIdentities(ctx context.Context, provider string, limit int) ([]protocol.ExternalIdentity, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.pool.Query(ctx, `SELECT external_identity_id,provider,subject,account_id,email,created_at FROM external_identities WHERE provider=$1 ORDER BY external_identity_id LIMIT $2`, provider, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]protocol.ExternalIdentity, 0)
	for rows.Next() {
		var value protocol.ExternalIdentity
		if err := rows.Scan(&value.ID, &value.Provider, &value.Subject, &value.AccountID, &value.Email, &value.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}
