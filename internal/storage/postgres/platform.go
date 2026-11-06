package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/agent-continuity/agent-continuity-master/internal/billing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/agent-continuity/agent-continuity-master/internal/storage"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

func (s *Store) CreateOrganization(ctx context.Context, organization protocol.Organization, owner protocol.OrganizationMember) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `INSERT INTO organizations(organization_id,name,slug,owner_account_id,plan,data_residency,key_provider,settings,created_at,updated_at,parent_id) VALUES($1,$2,$3,$4,$5,NULLIF($6,''),$7,$8,$9,$10,NULLIF($11,''))`, organization.ID, organization.Name, organization.Slug, organization.OwnerAccountID, organization.Plan, organization.DataResidency, organization.KeyProvider, []byte(organization.Settings), organization.CreatedAt, organization.UpdatedAt, organization.ParentID); err != nil {
		return mapError(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO organization_members(organization_id,account_id,role,status,joined_at) VALUES($1,$2,$3,$4,$5)`, owner.OrganizationID, owner.AccountID, owner.Role, owner.Status, owner.JoinedAt); err != nil {
		return mapError(err)
	}
	return tx.Commit(ctx)
}

func (s *Store) GetOrganization(ctx context.Context, id string) (protocol.Organization, error) {
	return scanOrganization(s.pool.QueryRow(ctx, `SELECT organization_id,name,slug,owner_account_id,plan,COALESCE(data_residency,''),key_provider,settings,created_at,updated_at,COALESCE(parent_id,'') FROM organizations WHERE organization_id=$1`, id))
}

func (s *Store) ListOrganizations(ctx context.Context, accountID string) ([]protocol.Organization, error) {
	rows, err := s.pool.Query(ctx, `SELECT o.organization_id,o.name,o.slug,o.owner_account_id,o.plan,COALESCE(o.data_residency,''),o.key_provider,o.settings,o.created_at,o.updated_at,COALESCE(o.parent_id,'') FROM organizations o JOIN organization_members m ON m.organization_id=o.organization_id WHERE m.account_id=$1 AND m.status='active' ORDER BY o.name`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []protocol.Organization{}
	for rows.Next() {
		value, err := scanOrganization(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (s *Store) UpdateOrganization(ctx context.Context, value protocol.Organization) error {
	tag, err := s.pool.Exec(ctx, `UPDATE organizations SET name=$2,slug=$3,plan=$4,data_residency=NULLIF($5,''),key_provider=$6,settings=$7,updated_at=$8 WHERE organization_id=$1`, value.ID, value.Name, value.Slug, value.Plan, value.DataResidency, value.KeyProvider, []byte(value.Settings), value.UpdatedAt)
	if err != nil {
		return mapError(err)
	}
	if tag.RowsAffected() == 0 {
		return storage.ErrNotFound
	}
	return nil
}

func (s *Store) DeleteOrganization(ctx context.Context, organizationID, ownerAccountID string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM organizations WHERE organization_id=$1 AND owner_account_id=$2 AND NOT EXISTS(SELECT 1 FROM tasks WHERE organization_id=$1) AND NOT EXISTS(SELECT 1 FROM billing_documents WHERE organization_id=$1 OR document->'invoice'->>'payer_organization_id'=$1)`, organizationID, ownerAccountID)
	if err != nil {
		return mapError(err)
	}
	if tag.RowsAffected() == 0 {
		return storage.ErrAlreadyExists
	}
	return nil
}

func scanOrganization(row rowScanner) (protocol.Organization, error) {
	var value protocol.Organization
	var settings []byte
	err := row.Scan(&value.ID, &value.Name, &value.Slug, &value.OwnerAccountID, &value.Plan, &value.DataResidency, &value.KeyProvider, &settings, &value.CreatedAt, &value.UpdatedAt, &value.ParentID)
	value.Settings = settings
	return value, mapError(err)
}

func (s *Store) PutOrganizationMember(ctx context.Context, value protocol.OrganizationMember) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO organization_members(organization_id,account_id,role,status,joined_at,removed_at) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(organization_id,account_id) DO UPDATE SET role=EXCLUDED.role,status=EXCLUDED.status,joined_at=CASE WHEN organization_members.status='removed' THEN EXCLUDED.joined_at ELSE organization_members.joined_at END,removed_at=EXCLUDED.removed_at`, value.OrganizationID, value.AccountID, value.Role, value.Status, value.JoinedAt, value.RemovedAt)
	return mapError(err)
}

func (s *Store) GetOrganizationMember(ctx context.Context, organizationID, accountID string) (protocol.OrganizationMember, error) {
	var value protocol.OrganizationMember
	err := s.pool.QueryRow(ctx, `SELECT organization_id,account_id,role,status,joined_at,removed_at FROM organization_members WHERE organization_id=$1 AND account_id=$2 AND status='active'`, organizationID, accountID).Scan(&value.OrganizationID, &value.AccountID, &value.Role, &value.Status, &value.JoinedAt, &value.RemovedAt)
	return value, mapError(err)
}

func (s *Store) ListOrganizationMembers(ctx context.Context, organizationID string) ([]protocol.OrganizationMember, error) {
	rows, err := s.pool.Query(ctx, `SELECT organization_id,account_id,role,status,joined_at,removed_at FROM organization_members WHERE organization_id=$1 AND status='active' ORDER BY account_id`, organizationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []protocol.OrganizationMember{}
	for rows.Next() {
		var value protocol.OrganizationMember
		if err := rows.Scan(&value.OrganizationID, &value.AccountID, &value.Role, &value.Status, &value.JoinedAt, &value.RemovedAt); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (s *Store) RemoveOrganizationMember(ctx context.Context, organizationID, accountID string, now time.Time) error {
	tag, err := s.pool.Exec(ctx, `UPDATE organization_members SET status='removed',removed_at=$3 WHERE organization_id=$1 AND account_id=$2 AND status='active' AND role<>'owner'`, organizationID, accountID, now)
	if err != nil {
		return mapError(err)
	}
	if tag.RowsAffected() == 0 {
		return storage.ErrNotFound
	}
	return nil
}

func (s *Store) CreateTeam(ctx context.Context, value protocol.Team) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO teams(team_id,organization_id,name,slug,settings,created_at) VALUES($1,$2,$3,$4,$5,$6)`, value.ID, value.OrganizationID, value.Name, value.Slug, []byte(value.Settings), value.CreatedAt)
	return mapError(err)
}

func (s *Store) GetTeam(ctx context.Context, id string) (protocol.Team, error) {
	var value protocol.Team
	var settings []byte
	err := s.pool.QueryRow(ctx, `SELECT team_id,organization_id,name,slug,settings,created_at FROM teams WHERE team_id=$1`, id).Scan(&value.ID, &value.OrganizationID, &value.Name, &value.Slug, &settings, &value.CreatedAt)
	value.Settings = settings
	return value, mapError(err)
}

func (s *Store) ListTeams(ctx context.Context, organizationID string) ([]protocol.Team, error) {
	rows, err := s.pool.Query(ctx, `SELECT team_id,organization_id,name,slug,settings,created_at FROM teams WHERE organization_id=$1 ORDER BY name`, organizationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []protocol.Team{}
	for rows.Next() {
		var value protocol.Team
		var settings []byte
		if err := rows.Scan(&value.ID, &value.OrganizationID, &value.Name, &value.Slug, &settings, &value.CreatedAt); err != nil {
			return nil, err
		}
		value.Settings = settings
		result = append(result, value)
	}
	return result, rows.Err()
}

func (s *Store) UpdateTeam(ctx context.Context, value protocol.Team) error {
	tag, err := s.pool.Exec(ctx, `UPDATE teams SET name=$3,slug=$4,settings=$5 WHERE team_id=$1 AND organization_id=$2`, value.ID, value.OrganizationID, value.Name, value.Slug, []byte(value.Settings))
	if err != nil {
		return mapError(err)
	}
	if tag.RowsAffected() == 0 {
		return storage.ErrNotFound
	}
	return nil
}

func (s *Store) DeleteTeam(ctx context.Context, organizationID, teamID string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM teams WHERE team_id=$1 AND organization_id=$2 AND NOT EXISTS(SELECT 1 FROM tasks WHERE team_id=$1)`, teamID, organizationID)
	if err != nil {
		return mapError(err)
	}
	if tag.RowsAffected() == 0 {
		return storage.ErrAlreadyExists
	}
	return nil
}

func (s *Store) PutTeamMember(ctx context.Context, value protocol.TeamMember) error {
	var member bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM teams t JOIN organization_members m ON m.organization_id=t.organization_id WHERE t.team_id=$1 AND m.account_id=$2 AND m.status='active')`, value.TeamID, value.AccountID).Scan(&member); err != nil {
		return err
	}
	if !member {
		return storage.ErrUnauthorized
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO team_members(team_id,account_id,role,status,joined_at,removed_at) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(team_id,account_id) DO UPDATE SET role=EXCLUDED.role,status=EXCLUDED.status,removed_at=EXCLUDED.removed_at`, value.TeamID, value.AccountID, value.Role, value.Status, value.JoinedAt, value.RemovedAt)
	return mapError(err)
}

func (s *Store) GetTeamMember(ctx context.Context, teamID, accountID string) (protocol.TeamMember, error) {
	var value protocol.TeamMember
	err := s.pool.QueryRow(ctx, `SELECT team_id,account_id,role,status,joined_at,removed_at FROM team_members WHERE team_id=$1 AND account_id=$2 AND status='active'`, teamID, accountID).Scan(&value.TeamID, &value.AccountID, &value.Role, &value.Status, &value.JoinedAt, &value.RemovedAt)
	return value, mapError(err)
}

func (s *Store) ListTeamMembers(ctx context.Context, teamID string) ([]protocol.TeamMember, error) {
	rows, err := s.pool.Query(ctx, `SELECT team_id,account_id,role,status,joined_at,removed_at FROM team_members WHERE team_id=$1 AND status='active' ORDER BY account_id`, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []protocol.TeamMember{}
	for rows.Next() {
		var value protocol.TeamMember
		if err := rows.Scan(&value.TeamID, &value.AccountID, &value.Role, &value.Status, &value.JoinedAt, &value.RemovedAt); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (s *Store) RemoveTeamMember(ctx context.Context, teamID, accountID string, now time.Time) error {
	tag, err := s.pool.Exec(ctx, `UPDATE team_members SET status='removed',removed_at=$3 WHERE team_id=$1 AND account_id=$2 AND status='active'`, teamID, accountID, now)
	if err != nil {
		return mapError(err)
	}
	if tag.RowsAffected() == 0 {
		return storage.ErrNotFound
	}
	return nil
}

func (s *Store) CreateAccessPolicy(ctx context.Context, value protocol.AccessPolicy) error {
	subjects, _ := json.Marshal(value.Subjects)
	actions, _ := json.Marshal(value.Actions)
	conditions, _ := json.Marshal(value.Conditions)
	_, err := s.pool.Exec(ctx, `INSERT INTO access_policies(policy_id,organization_id,name,effect,subjects,actions,resource_type,conditions,priority,created_by,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, value.ID, value.OrganizationID, value.Name, value.Effect, subjects, actions, value.ResourceType, conditions, value.Priority, value.CreatedBy, value.CreatedAt)
	return mapError(err)
}

func (s *Store) ListAccessPolicies(ctx context.Context, organizationID string) ([]protocol.AccessPolicy, error) {
	rows, err := s.pool.Query(ctx, `SELECT policy_id,organization_id,name,effect,subjects,actions,resource_type,conditions,priority,created_by,created_at FROM access_policies WHERE organization_id=$1 ORDER BY priority DESC,policy_id`, organizationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []protocol.AccessPolicy{}
	for rows.Next() {
		var value protocol.AccessPolicy
		var subjects, actions, conditions []byte
		if err := rows.Scan(&value.ID, &value.OrganizationID, &value.Name, &value.Effect, &subjects, &actions, &value.ResourceType, &conditions, &value.Priority, &value.CreatedBy, &value.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(subjects, &value.Subjects)
		_ = json.Unmarshal(actions, &value.Actions)
		_ = json.Unmarshal(conditions, &value.Conditions)
		result = append(result, value)
	}
	return result, rows.Err()
}

func (s *Store) DeleteAccessPolicy(ctx context.Context, organizationID, policyID string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM access_policies WHERE organization_id=$1 AND policy_id=$2`, organizationID, policyID)
	if err != nil {
		return mapError(err)
	}
	if tag.RowsAffected() == 0 {
		return storage.ErrNotFound
	}
	return nil
}

func (s *Store) PutArtifactPermission(ctx context.Context, value protocol.ArtifactPermission) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO artifact_permissions(task_id,artifact_id,account_id,permission,granted_by,created_at) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(task_id,artifact_id,account_id) DO UPDATE SET permission=EXCLUDED.permission,granted_by=EXCLUDED.granted_by,created_at=EXCLUDED.created_at`, value.TaskID, value.ArtifactID, value.AccountID, value.Permission, value.GrantedBy, value.CreatedAt)
	return mapError(err)
}

func (s *Store) ListArtifactPermissions(ctx context.Context, taskID, artifactID string) ([]protocol.ArtifactPermission, error) {
	rows, err := s.pool.Query(ctx, `SELECT task_id,artifact_id,account_id,permission,granted_by,created_at FROM artifact_permissions WHERE task_id=$1 AND artifact_id=$2 ORDER BY account_id`, taskID, artifactID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []protocol.ArtifactPermission{}
	for rows.Next() {
		var value protocol.ArtifactPermission
		if err := rows.Scan(&value.TaskID, &value.ArtifactID, &value.AccountID, &value.Permission, &value.GrantedBy, &value.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (s *Store) CheckArtifactPermission(ctx context.Context, taskID, artifactID, accountID string, required protocol.Permission) error {
	var owner string
	if err := s.pool.QueryRow(ctx, `SELECT account_id FROM tasks WHERE task_id=$1`, taskID).Scan(&owner); err != nil {
		return mapError(err)
	}
	if owner == accountID {
		return nil
	}
	var restrictions int
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM artifact_permissions WHERE task_id=$1 AND artifact_id=$2`, taskID, artifactID).Scan(&restrictions); err != nil {
		return err
	}
	if restrictions == 0 {
		return s.CheckPermission(ctx, taskID, accountID, required)
	}
	var permission protocol.Permission
	if err := s.pool.QueryRow(ctx, `SELECT permission FROM artifact_permissions WHERE task_id=$1 AND artifact_id=$2 AND account_id=$3`, taskID, artifactID, accountID).Scan(&permission); err != nil {
		return storage.ErrUnauthorized
	}
	if permissionRank(permission) < permissionRank(required) {
		return storage.ErrUnauthorized
	}
	return nil
}

func (s *Store) DeleteArtifactPermission(ctx context.Context, taskID, artifactID, accountID string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM artifact_permissions WHERE task_id=$1 AND artifact_id=$2 AND account_id=$3`, taskID, artifactID, accountID)
	if err != nil {
		return mapError(err)
	}
	if tag.RowsAffected() == 0 {
		return storage.ErrNotFound
	}
	return nil
}

func (s *Store) CreateOAuthClient(ctx context.Context, value protocol.OAuthClient) error {
	scopes, _ := json.Marshal(value.Scopes)
	redirects, _ := json.Marshal(value.RedirectURIs)
	_, err := s.pool.Exec(ctx, `INSERT INTO oauth_clients(client_id,account_id,name,secret_hash,scopes,redirect_uris,created_at,revoked_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, value.ID, value.AccountID, value.Name, value.SecretHash, scopes, redirects, value.CreatedAt, value.RevokedAt)
	return mapError(err)
}

func (s *Store) ListOAuthClients(ctx context.Context, accountID string) ([]protocol.OAuthClient, error) {
	rows, err := s.pool.Query(ctx, `SELECT client_id,account_id,name,secret_hash,scopes,redirect_uris,created_at,revoked_at FROM oauth_clients WHERE account_id=$1 ORDER BY created_at DESC`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []protocol.OAuthClient{}
	for rows.Next() {
		value, err := scanOAuthClient(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (s *Store) AuthenticateOAuthClient(ctx context.Context, secretHash string) (protocol.OAuthClient, error) {
	value, err := scanOAuthClient(s.pool.QueryRow(ctx, `SELECT client_id,account_id,name,secret_hash,scopes,redirect_uris,created_at,revoked_at FROM oauth_clients WHERE secret_hash=$1 AND revoked_at IS NULL`, secretHash))
	if errors.Is(err, storage.ErrNotFound) {
		return protocol.OAuthClient{}, storage.ErrUnauthorized
	}
	return value, err
}

func scanOAuthClient(row rowScanner) (protocol.OAuthClient, error) {
	var value protocol.OAuthClient
	var scopes, redirects []byte
	err := row.Scan(&value.ID, &value.AccountID, &value.Name, &value.SecretHash, &scopes, &redirects, &value.CreatedAt, &value.RevokedAt)
	_ = json.Unmarshal(scopes, &value.Scopes)
	_ = json.Unmarshal(redirects, &value.RedirectURIs)
	return value, mapError(err)
}

func (s *Store) RevokeOAuthClient(ctx context.Context, accountID, clientID string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE oauth_clients SET revoked_at=COALESCE(revoked_at,now()) WHERE client_id=$1 AND account_id=$2`, clientID, accountID)
	if err != nil {
		return mapError(err)
	}
	if tag.RowsAffected() == 0 {
		return storage.ErrNotFound
	}
	return nil
}

func (s *Store) CreateWebhookEndpoint(ctx context.Context, value protocol.WebhookEndpoint, encryptedSecret []byte) error {
	events, _ := json.Marshal(value.Events)
	_, err := s.pool.Exec(ctx, `INSERT INTO webhook_endpoints(webhook_id,account_id,url,events,secret_hash,secret_ciphertext,created_at,disabled_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, value.ID, value.AccountID, value.URL, events, value.SecretHash, encryptedSecret, value.CreatedAt, value.DisabledAt)
	return mapError(err)
}

func (s *Store) ListWebhookEndpoints(ctx context.Context, accountID string) ([]protocol.WebhookEndpoint, error) {
	rows, err := s.pool.Query(ctx, `SELECT webhook_id,account_id,url,events,secret_hash,created_at,disabled_at FROM webhook_endpoints WHERE account_id=$1 ORDER BY created_at DESC`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []protocol.WebhookEndpoint{}
	for rows.Next() {
		var value protocol.WebhookEndpoint
		var events []byte
		if err := rows.Scan(&value.ID, &value.AccountID, &value.URL, &events, &value.SecretHash, &value.CreatedAt, &value.DisabledAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(events, &value.Events)
		result = append(result, value)
	}
	return result, rows.Err()
}

func (s *Store) GetWebhookSecret(ctx context.Context, webhookID string) ([]byte, error) {
	var value []byte
	err := s.pool.QueryRow(ctx, `SELECT secret_ciphertext FROM webhook_endpoints WHERE webhook_id=$1 AND disabled_at IS NULL`, webhookID).Scan(&value)
	return value, mapError(err)
}

func (s *Store) GetWebhookEndpoint(ctx context.Context, webhookID string) (protocol.WebhookEndpoint, []byte, error) {
	var value protocol.WebhookEndpoint
	var events, secret []byte
	err := s.pool.QueryRow(ctx, `SELECT webhook_id,account_id,url,events,secret_hash,secret_ciphertext,created_at,disabled_at FROM webhook_endpoints WHERE webhook_id=$1 AND disabled_at IS NULL`, webhookID).Scan(&value.ID, &value.AccountID, &value.URL, &events, &value.SecretHash, &secret, &value.CreatedAt, &value.DisabledAt)
	if err != nil {
		return protocol.WebhookEndpoint{}, nil, mapError(err)
	}
	if err := json.Unmarshal(events, &value.Events); err != nil {
		return protocol.WebhookEndpoint{}, nil, err
	}
	return value, secret, nil
}

func (s *Store) RevokeWebhookEndpoint(ctx context.Context, accountID, webhookID string, now time.Time) error {
	tag, err := s.pool.Exec(ctx, `UPDATE webhook_endpoints SET disabled_at=COALESCE(disabled_at,$3) WHERE webhook_id=$1 AND account_id=$2`, webhookID, accountID, now)
	if err != nil {
		return mapError(err)
	}
	if tag.RowsAffected() == 0 {
		return storage.ErrNotFound
	}
	return nil
}

func (s *Store) CreateWebhookDeliveries(ctx context.Context, accountID, eventType string, payload json.RawMessage, now time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT webhook_id FROM webhook_endpoints WHERE account_id=$1 AND disabled_at IS NULL AND (events ? $2 OR events ? '*')`, accountID, eventType)
	if err != nil {
		return mapError(err)
	}
	webhookIDs := make([]string, 0)
	for rows.Next() {
		var webhookID string
		if err := rows.Scan(&webhookID); err != nil {
			rows.Close()
			return err
		}
		webhookIDs = append(webhookIDs, webhookID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, webhookID := range webhookIDs {
		deliveryID, err := protocol.NewID("delivery")
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO webhook_deliveries(delivery_id,webhook_id,event_type,payload,next_attempt,created_at) VALUES($1,$2,$3,$4,$5,$5)`, deliveryID, webhookID, eventType, []byte(payload), now); err != nil {
			return mapError(err)
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) ListReadyWebhookDeliveries(ctx context.Context, now time.Time, limit int) ([]protocol.WebhookDelivery, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `SELECT delivery_id,webhook_id,event_type,payload,attempts,next_attempt,COALESCE(last_status,0),COALESCE(last_error,''),created_at,delivered_at,dead_lettered_at FROM webhook_deliveries WHERE delivered_at IS NULL AND dead_lettered_at IS NULL AND next_attempt<=$1 ORDER BY next_attempt LIMIT $2`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []protocol.WebhookDelivery{}
	for rows.Next() {
		var value protocol.WebhookDelivery
		var payload []byte
		if err := rows.Scan(&value.ID, &value.WebhookID, &value.EventType, &payload, &value.Attempts, &value.NextAttempt, &value.LastStatus, &value.LastError, &value.CreatedAt, &value.DeliveredAt, &value.DeadLetteredAt); err != nil {
			return nil, err
		}
		value.Payload = payload
		result = append(result, value)
	}
	return result, rows.Err()
}

func (s *Store) UpdateWebhookDelivery(ctx context.Context, value protocol.WebhookDelivery) error {
	tag, err := s.pool.Exec(ctx, `UPDATE webhook_deliveries SET attempts=$2,next_attempt=$3,last_status=NULLIF($4,0),last_error=NULLIF($5,''),delivered_at=$6,dead_lettered_at=$7 WHERE delivery_id=$1`, value.ID, value.Attempts, value.NextAttempt, value.LastStatus, value.LastError, value.DeliveredAt, value.DeadLetteredAt)
	if err != nil {
		return mapError(err)
	}
	if tag.RowsAffected() == 0 {
		return storage.ErrNotFound
	}
	return nil
}

func (s *Store) RecordUsage(ctx context.Context, value protocol.UsageRecord) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO usage_records(usage_id,account_id,organization_id,metric,quantity,unit,resource_id,idempotency_key,metadata,recorded_at) VALUES($1,$2,NULLIF($3,''),$4,$5,$6,NULLIF($7,''),$8,$9,$10) ON CONFLICT(account_id,metric,idempotency_key) DO NOTHING`, value.ID, value.AccountID, value.OrganizationID, value.Metric, value.Quantity, value.Unit, value.ResourceID, value.IdempotencyKey, []byte(value.Metadata), value.RecordedAt)
	return mapError(err)
}

func (s *Store) ListUsage(ctx context.Context, accountID string, from, to time.Time, limit int) ([]protocol.UsageRecord, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	if to.IsZero() {
		to = time.Now().UTC().Add(time.Second)
	}
	rows, err := s.pool.Query(ctx, `SELECT usage_id,account_id,COALESCE(organization_id,''),metric,quantity,unit,COALESCE(resource_id,''),idempotency_key,metadata,recorded_at FROM usage_records WHERE account_id=$1 AND recorded_at>=$2 AND recorded_at<$3 ORDER BY recorded_at DESC LIMIT $4`, accountID, from, to, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []protocol.UsageRecord{}
	for rows.Next() {
		var value protocol.UsageRecord
		var metadata []byte
		if err := rows.Scan(&value.ID, &value.AccountID, &value.OrganizationID, &value.Metric, &value.Quantity, &value.Unit, &value.ResourceID, &value.IdempotencyKey, &metadata, &value.RecordedAt); err != nil {
			return nil, err
		}
		value.Metadata = metadata
		result = append(result, value)
	}
	return result, rows.Err()
}

func (s *Store) PutBillingAccount(ctx context.Context, value protocol.BillingAccount) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO billing_accounts(billing_account_id,account_id,organization_id,plan,status,currency,provider_customer_reference,limits,current_period_start,current_period_end,created_at,updated_at) VALUES($1,$2,NULLIF($3,''),$4,$5,$6,NULLIF($7,''),$8,$9,$10,$11,$12) ON CONFLICT(billing_account_id) DO UPDATE SET plan=EXCLUDED.plan,status=EXCLUDED.status,currency=EXCLUDED.currency,provider_customer_reference=EXCLUDED.provider_customer_reference,limits=EXCLUDED.limits,current_period_start=EXCLUDED.current_period_start,current_period_end=EXCLUDED.current_period_end,updated_at=EXCLUDED.updated_at WHERE billing_accounts.account_id=EXCLUDED.account_id`, value.ID, value.AccountID, value.OrganizationID, value.Plan, value.Status, value.Currency, value.ProviderCustomerRef, []byte(value.Limits), value.CurrentPeriodStart, value.CurrentPeriodEnd, value.CreatedAt, value.UpdatedAt)
	return mapError(err)
}

func (s *Store) ListBillingAccounts(ctx context.Context, accountID string) ([]protocol.BillingAccount, error) {
	rows, err := s.pool.Query(ctx, `SELECT billing_account_id,account_id,COALESCE(organization_id,''),plan,status,currency,COALESCE(provider_customer_reference,''),limits,current_period_start,current_period_end,created_at,updated_at FROM billing_accounts WHERE account_id=$1 ORDER BY created_at`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]protocol.BillingAccount, 0)
	for rows.Next() {
		value, err := scanBillingAccount(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func scanBillingAccount(row rowScanner) (protocol.BillingAccount, error) {
	var value protocol.BillingAccount
	var limits []byte
	err := row.Scan(&value.ID, &value.AccountID, &value.OrganizationID, &value.Plan, &value.Status, &value.Currency, &value.ProviderCustomerRef, &limits, &value.CurrentPeriodStart, &value.CurrentPeriodEnd, &value.CreatedAt, &value.UpdatedAt)
	value.Limits = limits
	return value, mapError(err)
}

func (s *Store) CreateMarketplaceListing(ctx context.Context, value protocol.MarketplaceListing) error {
	capabilities, _ := json.Marshal(value.Capabilities)
	_, err := s.pool.Exec(ctx, `INSERT INTO marketplace_listings(listing_id,publisher_account_id,kind,name,description,version,capabilities,manifest,price_minor,currency,commission_basis_points,status,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`, value.ID, value.PublisherID, value.Kind, value.Name, value.Description, value.Version, capabilities, []byte(value.Manifest), value.PriceMinor, value.Currency, value.CommissionBP, value.Status, value.CreatedAt, value.UpdatedAt)
	return mapError(err)
}

func (s *Store) GetMarketplaceListing(ctx context.Context, id string) (protocol.MarketplaceListing, error) {
	return scanMarketplaceListing(s.pool.QueryRow(ctx, `SELECT listing_id,publisher_account_id,kind,name,description,version,capabilities,manifest,price_minor,currency,commission_basis_points,status,created_at,updated_at FROM marketplace_listings WHERE listing_id=$1`, id))
}

func (s *Store) ListMarketplaceListings(ctx context.Context, kind, status string, limit int) ([]protocol.MarketplaceListing, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `SELECT listing_id,publisher_account_id,kind,name,description,version,capabilities,manifest,price_minor,currency,commission_basis_points,status,created_at,updated_at FROM marketplace_listings WHERE ($1='' OR kind=$1) AND ($2='' OR status=$2) ORDER BY updated_at DESC LIMIT $3`, kind, status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []protocol.MarketplaceListing{}
	for rows.Next() {
		value, err := scanMarketplaceListing(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func scanMarketplaceListing(row rowScanner) (protocol.MarketplaceListing, error) {
	var value protocol.MarketplaceListing
	var capabilities, manifest []byte
	err := row.Scan(&value.ID, &value.PublisherID, &value.Kind, &value.Name, &value.Description, &value.Version, &capabilities, &manifest, &value.PriceMinor, &value.Currency, &value.CommissionBP, &value.Status, &value.CreatedAt, &value.UpdatedAt)
	_ = json.Unmarshal(capabilities, &value.Capabilities)
	value.Manifest = manifest
	return value, mapError(err)
}

func (s *Store) InstallMarketplaceListing(ctx context.Context, value protocol.MarketplaceInstallation) error {
	tag, err := s.pool.Exec(ctx, `INSERT INTO marketplace_installations(installation_id,listing_id,account_id,version,status,installed_at) SELECT $1,listing_id,$3,$4,$5,$6 FROM marketplace_listings WHERE listing_id=$2 AND status='published'`, value.ID, value.ListingID, value.AccountID, value.Version, value.Status, value.InstalledAt)
	if err != nil {
		return mapError(err)
	}
	if tag.RowsAffected() == 0 {
		return storage.ErrNotFound
	}
	return nil
}

func (s *Store) ListMarketplaceInstallations(ctx context.Context, accountID string) ([]protocol.MarketplaceInstallation, error) {
	rows, err := s.pool.Query(ctx, `SELECT installation_id,listing_id,account_id,version,status,installed_at FROM marketplace_installations WHERE account_id=$1 ORDER BY installed_at DESC`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []protocol.MarketplaceInstallation{}
	for rows.Next() {
		var value protocol.MarketplaceInstallation
		if err := rows.Scan(&value.ID, &value.ListingID, &value.AccountID, &value.Version, &value.Status, &value.InstalledAt); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (s *Store) CreateMarketplaceTransaction(ctx context.Context, value protocol.MarketplaceTransaction) error {
	tag, err := s.pool.Exec(ctx, `INSERT INTO marketplace_transactions(transaction_id,listing_id,buyer_account_id,seller_account_id,buyer_agent_id,seller_agent_id,task_id,quantity,amount_minor,commission_minor,currency,status,idempotency_key,metadata,created_at,updated_at) SELECT $1,l.listing_id,$3,l.publisher_account_id,NULLIF($5,''),NULLIF($6,''),NULLIF($7,''),$8,$9,$10,$11,$12,$13,$14,$15,$16 FROM marketplace_listings l WHERE l.listing_id=$2 AND l.status='published' AND l.publisher_account_id=$4`, value.ID, value.ListingID, value.BuyerAccountID, value.SellerAccountID, value.BuyerAgentID, value.SellerAgentID, value.TaskID, value.Quantity, value.AmountMinor, value.CommissionMinor, value.Currency, value.Status, value.IdempotencyKey, []byte(value.Metadata), value.CreatedAt, value.UpdatedAt)
	if err != nil {
		return mapError(err)
	}
	if tag.RowsAffected() == 0 {
		return storage.ErrNotFound
	}
	return nil
}

func (s *Store) GetMarketplaceTransaction(ctx context.Context, id string) (protocol.MarketplaceTransaction, error) {
	return scanMarketplaceTransaction(s.pool.QueryRow(ctx, marketplaceTransactionSelect+` WHERE transaction_id=$1`, id))
}

func (s *Store) ListMarketplaceTransactions(ctx context.Context, accountID string, limit int) ([]protocol.MarketplaceTransaction, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, marketplaceTransactionSelect+` WHERE buyer_account_id=$1 OR seller_account_id=$1 ORDER BY updated_at DESC LIMIT $2`, accountID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]protocol.MarketplaceTransaction, 0)
	for rows.Next() {
		value, err := scanMarketplaceTransaction(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (s *Store) TransitionMarketplaceTransaction(ctx context.Context, transactionID, actorAccountID, targetStatus string, now time.Time) (protocol.MarketplaceTransaction, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return protocol.MarketplaceTransaction{}, err
	}
	defer tx.Rollback(ctx)
	value, err := scanMarketplaceTransaction(tx.QueryRow(ctx, marketplaceTransactionSelect+` WHERE transaction_id=$1 FOR UPDATE`, transactionID))
	if err != nil {
		return protocol.MarketplaceTransaction{}, err
	}
	allowed := targetStatus == "authorized" && value.Status == "pending" && value.BuyerAccountID == actorAccountID ||
		targetStatus == "completed" && value.Status == "authorized" && value.SellerAccountID == actorAccountID ||
		targetStatus == "cancelled" && (value.Status == "pending" || value.Status == "authorized") && (value.BuyerAccountID == actorAccountID || value.SellerAccountID == actorAccountID)
	if !allowed {
		return protocol.MarketplaceTransaction{}, storage.ErrUnauthorized
	}
	value.Status, value.UpdatedAt = targetStatus, now.UTC()
	if _, err := tx.Exec(ctx, `UPDATE marketplace_transactions SET status=$2,updated_at=$3 WHERE transaction_id=$1`, value.ID, value.Status, value.UpdatedAt); err != nil {
		return protocol.MarketplaceTransaction{}, mapError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return protocol.MarketplaceTransaction{}, err
	}
	return value, nil
}

func (s *Store) HasCompletedMarketplaceTransaction(ctx context.Context, listingID, accountID string) (bool, error) {
	rows, err := s.pool.Query(ctx, `SELECT t.transaction_id,t.amount_minor,b.document FROM marketplace_transactions t LEFT JOIN billing_documents b ON b.account_id=t.buyer_account_id AND b.document->'invoice'->>'source_id'=t.transaction_id WHERE t.listing_id=$1 AND t.buyer_account_id=$2 AND t.status='completed'`, listingID, accountID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var amount int64
		var raw []byte
		if err = rows.Scan(&id, &amount, &raw); err != nil {
			return false, err
		}
		if amount == 0 {
			return true, nil
		}
		var d protocol.BillingDocument
		if len(raw) > 0 && json.Unmarshal(raw, &d) == nil && billing.PurchasePaid(d, id) {
			return true, nil
		}
	}
	return false, rows.Err()
}

const marketplaceTransactionSelect = `SELECT transaction_id,listing_id,buyer_account_id,seller_account_id,COALESCE(buyer_agent_id,''),COALESCE(seller_agent_id,''),COALESCE(task_id,''),quantity,amount_minor,commission_minor,currency,status,idempotency_key,metadata,created_at,updated_at FROM marketplace_transactions`

func scanMarketplaceTransaction(row rowScanner) (protocol.MarketplaceTransaction, error) {
	var value protocol.MarketplaceTransaction
	var metadata []byte
	err := row.Scan(&value.ID, &value.ListingID, &value.BuyerAccountID, &value.SellerAccountID, &value.BuyerAgentID, &value.SellerAgentID, &value.TaskID, &value.Quantity, &value.AmountMinor, &value.CommissionMinor, &value.Currency, &value.Status, &value.IdempotencyKey, &metadata, &value.CreatedAt, &value.UpdatedAt)
	value.Metadata = metadata
	return value, mapError(err)
}
