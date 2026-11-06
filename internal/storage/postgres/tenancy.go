package postgres

import (
	"context"
	"encoding/json"
	"github.com/agent-continuity/agent-continuity-master/internal/storage"
	"github.com/agent-continuity/agent-continuity-master/internal/tenancy"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

func (s *Store) ProvisionPortalUser(ctx context.Context, a *protocol.Account, l tenancy.Login, m *protocol.OrganizationMember) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if a != nil {
		if _, err = tx.Exec(ctx, `INSERT INTO accounts(account_id,name,created_at) VALUES($1,$2,$3)`, a.ID, a.Name, a.CreatedAt); err != nil {
			return mapError(err)
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO portal_logins(username,account_id,display_name,password_hash) VALUES($1,$2,$3,$4)`, l.Username, l.AccountID, l.DisplayName, l.PasswordHash); err != nil {
		return mapError(err)
	}
	if m != nil {
		if _, err = tx.Exec(ctx, `INSERT INTO organization_members(organization_id,account_id,role,status,joined_at) VALUES($1,$2,$3,$4,$5)`, m.OrganizationID, m.AccountID, m.Role, m.Status, m.JoinedAt); err != nil {
			return mapError(err)
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) LoadTenancyControl(ctx context.Context) (tenancy.Control, error) {
	var v tenancy.Control
	var raw []byte
	err := s.pool.QueryRow(ctx, `SELECT document FROM tenancy_control WHERE id=true`).Scan(&raw)
	if err != nil {
		return v, err
	}
	err = json.Unmarshal(raw, &v)
	return v, err
}
func (s *Store) SaveTenancyControl(ctx context.Context, v tenancy.Control, version int64) error {
	v.Version = version + 1
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `UPDATE tenancy_control SET version=$1,document=$2 WHERE id=true AND version=$3`, v.Version, raw, version)
	if err == nil && tag.RowsAffected() != 1 {
		return tenancy.ErrConflict
	}
	return err
}
func (s *Store) AllOrganizations(ctx context.Context) ([]protocol.Organization, error) {
	rows, err := s.pool.Query(ctx, `SELECT organization_id,name,slug,owner_account_id,plan,COALESCE(data_residency,''),key_provider,settings,created_at,updated_at,COALESCE(parent_id,'') FROM organizations ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []protocol.Organization{}
	for rows.Next() {
		o, err := scanOrganization(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
func (s *Store) CreatePortalLogin(ctx context.Context, v tenancy.Login) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO portal_logins(username,account_id,display_name,password_hash) VALUES($1,$2,$3,$4)`, v.Username, v.AccountID, v.DisplayName, v.PasswordHash)
	return mapError(err)
}
func (s *Store) GetPortalLogin(ctx context.Context, id string) (tenancy.Login, error) {
	var v tenancy.Login
	err := s.pool.QueryRow(ctx, `SELECT username,account_id,display_name,password_hash FROM portal_logins WHERE username=$1`, id).Scan(&v.Username, &v.AccountID, &v.DisplayName, &v.PasswordHash)
	return v, mapError(err)
}
func (s *Store) CreatePortalSession(ctx context.Context, v tenancy.Session) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM portal_sessions WHERE expires_at < now() OR last_seen < now()-interval '30 minutes'`)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO portal_sessions(token_hash,account_id,username,created_at,expires_at,last_seen) VALUES($1,$2,$3,$4,$5,$6)`, v.Hash, v.AccountID, v.Username, v.CreatedAt, v.ExpiresAt, v.LastSeen)
	return err
}
func (s *Store) GetPortalSession(ctx context.Context, id string) (tenancy.Session, error) {
	var v tenancy.Session
	err := s.pool.QueryRow(ctx, `UPDATE portal_sessions SET last_seen=now() WHERE token_hash=$1 AND expires_at>now() AND last_seen>now()-interval '30 minutes' RETURNING token_hash,account_id,username,created_at,expires_at,last_seen`, id).Scan(&v.Hash, &v.AccountID, &v.Username, &v.CreatedAt, &v.ExpiresAt, &v.LastSeen)
	return v, mapError(err)
}
func (s *Store) DeletePortalSession(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM portal_sessions WHERE token_hash=$1`, id)
	return err
}
func (s *Store) GetBillByID(ctx context.Context, id string) (protocol.BillingDocument, error) {
	var raw []byte
	err := s.pool.QueryRow(ctx, `SELECT document FROM billing_documents WHERE invoice_id=$1`, id).Scan(&raw)
	return decodeBill(raw, err)
}
func (s *Store) ListMerchantBills(ctx context.Context, limit int) ([]protocol.BillingDocument, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.pool.Query(ctx, `SELECT document FROM billing_documents WHERE status IN ('paid','refunded','disputed') AND jsonb_array_length(document->'receipts')>0 ORDER BY created_at DESC LIMIT $1`, limit)
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
func (s *Store) ListTenantBills(ctx context.Context, id string, limit int) ([]protocol.BillingDocument, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `SELECT document FROM billing_documents WHERE organization_id=$1 OR document->'invoice'->>'payer_organization_id'=$1 ORDER BY created_at DESC,invoice_id LIMIT $2`, id, limit)
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
		v, err := decodeBill(raw, nil)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) GetTenantBillingAccount(ctx context.Context, id string) (protocol.BillingAccount, error) {
	o, err := s.GetOrganization(ctx, id)
	if err != nil {
		return protocol.BillingAccount{}, err
	}
	items, err := s.ListBillingAccounts(ctx, o.OwnerAccountID)
	if err != nil {
		return protocol.BillingAccount{}, err
	}
	for _, b := range items {
		if b.OrganizationID == id && b.Status == "active" {
			return b, nil
		}
	}
	return protocol.BillingAccount{}, storage.ErrNotFound
}
