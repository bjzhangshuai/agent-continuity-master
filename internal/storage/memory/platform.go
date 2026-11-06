package memory

import (
	"context"
	"encoding/json"
	"github.com/agent-continuity/agent-continuity-master/internal/billing"
	"sort"
	"time"

	"github.com/agent-continuity/agent-continuity-master/internal/storage"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

func (s *Store) CreateOrganization(_ context.Context, organization protocol.Organization, owner protocol.OrganizationMember) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.accounts[organization.OwnerAccountID]; !ok {
		return storage.ErrNotFound
	}
	if _, ok := s.organizations[organization.ID]; ok {
		return storage.ErrAlreadyExists
	}
	for _, existing := range s.organizations {
		if existing.Slug == organization.Slug {
			return storage.ErrAlreadyExists
		}
	}
	s.organizations[organization.ID] = organization
	s.organizationMembers[organizationMemberKey(organization.ID, owner.AccountID)] = owner
	return nil
}

func (s *Store) GetOrganization(_ context.Context, id string) (protocol.Organization, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.organizations[id]
	if !ok {
		return protocol.Organization{}, storage.ErrNotFound
	}
	return value, nil
}

func (s *Store) ListOrganizations(_ context.Context, accountID string) ([]protocol.Organization, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := []protocol.Organization{}
	for _, value := range s.organizations {
		if member, ok := s.organizationMembers[organizationMemberKey(value.ID, accountID)]; ok && member.Status == "active" {
			result = append(result, value)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func (s *Store) UpdateOrganization(_ context.Context, value protocol.Organization) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.organizations[value.ID]
	if !ok {
		return storage.ErrNotFound
	}
	for id, candidate := range s.organizations {
		if id != value.ID && candidate.Slug == value.Slug {
			return storage.ErrAlreadyExists
		}
	}
	value.OwnerAccountID = current.OwnerAccountID
	value.ParentID = current.ParentID
	value.CreatedAt = current.CreatedAt
	s.organizations[value.ID] = value
	return nil
}

func (s *Store) DeleteOrganization(_ context.Context, organizationID, ownerAccountID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.organizations[organizationID]
	if !ok || value.OwnerAccountID != ownerAccountID {
		return storage.ErrNotFound
	}
	for _, o := range s.organizations {
		if o.ParentID == organizationID {
			return storage.ErrAlreadyExists
		}
	}
	for _, d := range s.billingDocuments {
		if d.Invoice.OrganizationID == organizationID || d.Invoice.PayerOrganizationID == organizationID {
			return storage.ErrAlreadyExists
		}
	}
	for _, task := range s.tasks {
		if task.OrganizationID == organizationID {
			return storage.ErrAlreadyExists
		}
	}
	delete(s.organizations, organizationID)
	for key, member := range s.organizationMembers {
		if member.OrganizationID == organizationID {
			delete(s.organizationMembers, key)
		}
	}
	for id, team := range s.teams {
		if team.OrganizationID == organizationID {
			delete(s.teams, id)
		}
	}
	for id, rule := range s.accessPolicies {
		if rule.OrganizationID == organizationID {
			delete(s.accessPolicies, id)
		}
	}
	return nil
}

func (s *Store) PutOrganizationMember(_ context.Context, value protocol.OrganizationMember) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.organizations[value.OrganizationID]; !ok {
		return storage.ErrNotFound
	}
	if _, ok := s.accounts[value.AccountID]; !ok {
		return storage.ErrNotFound
	}
	s.organizationMembers[organizationMemberKey(value.OrganizationID, value.AccountID)] = value
	return nil
}

func (s *Store) GetOrganizationMember(_ context.Context, organizationID, accountID string) (protocol.OrganizationMember, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.organizationMembers[organizationMemberKey(organizationID, accountID)]
	if !ok || value.Status != "active" {
		return protocol.OrganizationMember{}, storage.ErrNotFound
	}
	return value, nil
}

func (s *Store) ListOrganizationMembers(_ context.Context, organizationID string) ([]protocol.OrganizationMember, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.organizations[organizationID]; !ok {
		return nil, storage.ErrNotFound
	}
	result := []protocol.OrganizationMember{}
	for _, value := range s.organizationMembers {
		if value.OrganizationID == organizationID && value.Status == "active" {
			result = append(result, value)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].AccountID < result[j].AccountID })
	return result, nil
}

func (s *Store) RemoveOrganizationMember(_ context.Context, organizationID, accountID string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := organizationMemberKey(organizationID, accountID)
	value, ok := s.organizationMembers[key]
	if !ok || value.Status != "active" {
		return storage.ErrNotFound
	}
	value.Status, value.RemovedAt = "removed", pointerTime(now)
	s.organizationMembers[key] = value
	return nil
}

func (s *Store) CreateTeam(_ context.Context, value protocol.Team) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.organizations[value.OrganizationID]; !ok {
		return storage.ErrNotFound
	}
	if _, ok := s.teams[value.ID]; ok {
		return storage.ErrAlreadyExists
	}
	for _, existing := range s.teams {
		if existing.OrganizationID == value.OrganizationID && existing.Slug == value.Slug {
			return storage.ErrAlreadyExists
		}
	}
	s.teams[value.ID] = value
	return nil
}

func (s *Store) GetTeam(_ context.Context, id string) (protocol.Team, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.teams[id]
	if !ok {
		return protocol.Team{}, storage.ErrNotFound
	}
	return value, nil
}

func (s *Store) ListTeams(_ context.Context, organizationID string) ([]protocol.Team, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := []protocol.Team{}
	for _, value := range s.teams {
		if value.OrganizationID == organizationID {
			result = append(result, value)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func (s *Store) UpdateTeam(_ context.Context, value protocol.Team) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.teams[value.ID]
	if !ok || current.OrganizationID != value.OrganizationID {
		return storage.ErrNotFound
	}
	for id, candidate := range s.teams {
		if id != value.ID && candidate.OrganizationID == value.OrganizationID && candidate.Slug == value.Slug {
			return storage.ErrAlreadyExists
		}
	}
	value.CreatedAt = current.CreatedAt
	s.teams[value.ID] = value
	return nil
}

func (s *Store) DeleteTeam(_ context.Context, organizationID, teamID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.teams[teamID]
	if !ok || value.OrganizationID != organizationID {
		return storage.ErrNotFound
	}
	for _, task := range s.tasks {
		if task.TeamID == teamID {
			return storage.ErrAlreadyExists
		}
	}
	delete(s.teams, teamID)
	for key, member := range s.teamMembers {
		if member.TeamID == teamID {
			delete(s.teamMembers, key)
		}
	}
	return nil
}

func (s *Store) PutTeamMember(_ context.Context, value protocol.TeamMember) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	team, ok := s.teams[value.TeamID]
	if !ok {
		return storage.ErrNotFound
	}
	member, ok := s.organizationMembers[organizationMemberKey(team.OrganizationID, value.AccountID)]
	if !ok || member.Status != "active" {
		return storage.ErrUnauthorized
	}
	s.teamMembers[teamMemberKey(value.TeamID, value.AccountID)] = value
	return nil
}

func (s *Store) GetTeamMember(_ context.Context, teamID, accountID string) (protocol.TeamMember, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.teamMembers[teamMemberKey(teamID, accountID)]
	if !ok || value.Status != "active" {
		return protocol.TeamMember{}, storage.ErrNotFound
	}
	return value, nil
}

func (s *Store) ListTeamMembers(_ context.Context, teamID string) ([]protocol.TeamMember, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.teams[teamID]; !ok {
		return nil, storage.ErrNotFound
	}
	result := []protocol.TeamMember{}
	for _, value := range s.teamMembers {
		if value.TeamID == teamID && value.Status == "active" {
			result = append(result, value)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].AccountID < result[j].AccountID })
	return result, nil
}

func (s *Store) RemoveTeamMember(_ context.Context, teamID, accountID string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := teamMemberKey(teamID, accountID)
	value, ok := s.teamMembers[key]
	if !ok || value.Status != "active" {
		return storage.ErrNotFound
	}
	value.Status, value.RemovedAt = "removed", pointerTime(now)
	s.teamMembers[key] = value
	return nil
}

func (s *Store) CreateAccessPolicy(_ context.Context, value protocol.AccessPolicy) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.organizations[value.OrganizationID]; !ok {
		return storage.ErrNotFound
	}
	if _, ok := s.accessPolicies[value.ID]; ok {
		return storage.ErrAlreadyExists
	}
	s.accessPolicies[value.ID] = value
	return nil
}

func (s *Store) ListAccessPolicies(_ context.Context, organizationID string) ([]protocol.AccessPolicy, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := []protocol.AccessPolicy{}
	for _, value := range s.accessPolicies {
		if value.OrganizationID == organizationID {
			result = append(result, value)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Priority > result[j].Priority })
	return result, nil
}

func (s *Store) DeleteAccessPolicy(_ context.Context, organizationID, policyID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.accessPolicies[policyID]
	if !ok || value.OrganizationID != organizationID {
		return storage.ErrNotFound
	}
	delete(s.accessPolicies, policyID)
	return nil
}

func (s *Store) PutArtifactPermission(_ context.Context, value protocol.ArtifactPermission) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.artifacts[artifactKey(value.TaskID, value.ArtifactID)]; !ok {
		return storage.ErrNotFound
	}
	s.artifactPermissions[artifactPermissionKey(value.TaskID, value.ArtifactID, value.AccountID)] = value
	return nil
}

func (s *Store) ListArtifactPermissions(_ context.Context, taskID, artifactID string) ([]protocol.ArtifactPermission, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := []protocol.ArtifactPermission{}
	for _, value := range s.artifactPermissions {
		if value.TaskID == taskID && value.ArtifactID == artifactID {
			result = append(result, value)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].AccountID < result[j].AccountID })
	return result, nil
}

func (s *Store) CheckArtifactPermission(_ context.Context, taskID, artifactID, accountID string, required protocol.Permission) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	task, ok := s.tasks[taskID]
	if !ok {
		return storage.ErrNotFound
	}
	if task.AccountID == accountID {
		return nil
	}
	hasRestrictions := false
	for _, value := range s.artifactPermissions {
		if value.TaskID == taskID && value.ArtifactID == artifactID {
			hasRestrictions = true
			if value.AccountID == accountID && permissionRank(value.Permission) >= permissionRank(required) {
				return nil
			}
		}
	}
	if hasRestrictions {
		return storage.ErrUnauthorized
	}
	actual := s.permissions[permissionKey(taskID, accountID)]
	if permissionRank(actual) < permissionRank(required) {
		return storage.ErrUnauthorized
	}
	return nil
}

func (s *Store) DeleteArtifactPermission(_ context.Context, taskID, artifactID, accountID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := artifactPermissionKey(taskID, artifactID, accountID)
	if _, ok := s.artifactPermissions[key]; !ok {
		return storage.ErrNotFound
	}
	delete(s.artifactPermissions, key)
	return nil
}

func (s *Store) CreateOAuthClient(_ context.Context, value protocol.OAuthClient) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.accounts[value.AccountID]; !ok {
		return storage.ErrNotFound
	}
	if _, ok := s.oauthClients[value.ID]; ok {
		return storage.ErrAlreadyExists
	}
	s.oauthClients[value.ID] = value
	s.oauthClientHashes[value.SecretHash] = value.ID
	return nil
}

func (s *Store) ListOAuthClients(_ context.Context, accountID string) ([]protocol.OAuthClient, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := []protocol.OAuthClient{}
	for _, value := range s.oauthClients {
		if value.AccountID == accountID {
			result = append(result, value)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.After(result[j].CreatedAt) })
	return result, nil
}

func (s *Store) AuthenticateOAuthClient(_ context.Context, secretHash string) (protocol.OAuthClient, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id := s.oauthClientHashes[secretHash]
	value, ok := s.oauthClients[id]
	if !ok || value.RevokedAt != nil {
		return protocol.OAuthClient{}, storage.ErrUnauthorized
	}
	return value, nil
}

func (s *Store) RevokeOAuthClient(_ context.Context, accountID, clientID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.oauthClients[clientID]
	if !ok || value.AccountID != accountID {
		return storage.ErrNotFound
	}
	now := s.now().UTC()
	value.RevokedAt = &now
	s.oauthClients[clientID] = value
	return nil
}

func (s *Store) CreateWebhookEndpoint(_ context.Context, value protocol.WebhookEndpoint, encryptedSecret []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.accounts[value.AccountID]; !ok {
		return storage.ErrNotFound
	}
	if _, ok := s.webhooks[value.ID]; ok {
		return storage.ErrAlreadyExists
	}
	s.webhooks[value.ID] = value
	s.webhookSecrets[value.ID] = append([]byte(nil), encryptedSecret...)
	return nil
}

func (s *Store) ListWebhookEndpoints(_ context.Context, accountID string) ([]protocol.WebhookEndpoint, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := []protocol.WebhookEndpoint{}
	for _, value := range s.webhooks {
		if value.AccountID == accountID {
			result = append(result, value)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.After(result[j].CreatedAt) })
	return result, nil
}

func (s *Store) GetWebhookSecret(_ context.Context, webhookID string) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.webhookSecrets[webhookID]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return append([]byte(nil), value...), nil
}

func (s *Store) GetWebhookEndpoint(_ context.Context, webhookID string) (protocol.WebhookEndpoint, []byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.webhooks[webhookID]
	if !ok || value.DisabledAt != nil {
		return protocol.WebhookEndpoint{}, nil, storage.ErrNotFound
	}
	secret := s.webhookSecrets[webhookID]
	return value, append([]byte(nil), secret...), nil
}

func (s *Store) RevokeWebhookEndpoint(_ context.Context, accountID, webhookID string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.webhooks[webhookID]
	if !ok || value.AccountID != accountID {
		return storage.ErrNotFound
	}
	value.DisabledAt = pointerTime(now)
	s.webhooks[webhookID] = value
	return nil
}

func (s *Store) CreateWebhookDeliveries(_ context.Context, accountID, eventType string, payload json.RawMessage, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, endpoint := range s.webhooks {
		if endpoint.AccountID != accountID || endpoint.DisabledAt != nil || !webhookMatches(endpoint.Events, eventType) {
			continue
		}
		id, _ := protocol.NewID("delivery")
		s.webhookDeliveries[id] = protocol.WebhookDelivery{ID: id, WebhookID: endpoint.ID, EventType: eventType, Payload: append(json.RawMessage(nil), payload...), NextAttempt: now.UTC(), CreatedAt: now.UTC()}
	}
	return nil
}

func (s *Store) ListReadyWebhookDeliveries(_ context.Context, now time.Time, limit int) ([]protocol.WebhookDelivery, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	result := []protocol.WebhookDelivery{}
	for _, value := range s.webhookDeliveries {
		if value.DeliveredAt == nil && value.DeadLetteredAt == nil && !value.NextAttempt.After(now) {
			result = append(result, value)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].NextAttempt.Before(result[j].NextAttempt) })
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (s *Store) UpdateWebhookDelivery(_ context.Context, value protocol.WebhookDelivery) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.webhookDeliveries[value.ID]; !ok {
		return storage.ErrNotFound
	}
	s.webhookDeliveries[value.ID] = value
	return nil
}

func (s *Store) RecordUsage(_ context.Context, value protocol.UsageRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := value.AccountID + "\x00" + value.Metric + "\x00" + value.IdempotencyKey
	if _, ok := s.usageIdempotency[key]; ok {
		return nil
	}
	s.usageRecords[value.ID] = value
	s.usageIdempotency[key] = value.ID
	return nil
}

func (s *Store) ListUsage(_ context.Context, accountID string, from, to time.Time, limit int) ([]protocol.UsageRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	result := []protocol.UsageRecord{}
	for _, value := range s.usageRecords {
		if value.AccountID == accountID && !value.RecordedAt.Before(from) && (to.IsZero() || value.RecordedAt.Before(to)) {
			result = append(result, value)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].RecordedAt.After(result[j].RecordedAt) })
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (s *Store) PutBillingAccount(_ context.Context, value protocol.BillingAccount) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.accounts[value.AccountID]; !ok {
		return storage.ErrNotFound
	}
	if value.OrganizationID != "" {
		organization, ok := s.organizations[value.OrganizationID]
		if !ok || organization.OwnerAccountID != value.AccountID {
			return storage.ErrUnauthorized
		}
	}
	key := value.AccountID + "\x00" + value.OrganizationID
	if existing, ok := s.billingAccounts[key]; ok && existing.ID != value.ID {
		return storage.ErrAlreadyExists
	}
	s.billingAccounts[key] = value
	return nil
}

func (s *Store) ListBillingAccounts(_ context.Context, accountID string) ([]protocol.BillingAccount, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]protocol.BillingAccount, 0)
	for _, value := range s.billingAccounts {
		if value.AccountID == accountID {
			result = append(result, value)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.Before(result[j].CreatedAt) })
	return result, nil
}

func (s *Store) CreateMarketplaceListing(_ context.Context, value protocol.MarketplaceListing) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.accounts[value.PublisherID]; !ok {
		return storage.ErrNotFound
	}
	if _, ok := s.marketplaceListings[value.ID]; ok {
		return storage.ErrAlreadyExists
	}
	s.marketplaceListings[value.ID] = value
	return nil
}

func (s *Store) GetMarketplaceListing(_ context.Context, id string) (protocol.MarketplaceListing, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.marketplaceListings[id]
	if !ok {
		return protocol.MarketplaceListing{}, storage.ErrNotFound
	}
	return value, nil
}

func (s *Store) ListMarketplaceListings(_ context.Context, kind, status string, limit int) ([]protocol.MarketplaceListing, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	result := []protocol.MarketplaceListing{}
	for _, value := range s.marketplaceListings {
		if (kind == "" || value.Kind == kind) && (status == "" || value.Status == status) {
			result = append(result, value)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].UpdatedAt.After(result[j].UpdatedAt) })
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (s *Store) InstallMarketplaceListing(_ context.Context, value protocol.MarketplaceInstallation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	listing, ok := s.marketplaceListings[value.ListingID]
	if !ok || listing.Status != "published" {
		return storage.ErrNotFound
	}
	key := marketplaceInstallationKey(value.ListingID, value.AccountID)
	if _, ok := s.marketplaceInstallations[key]; ok {
		return storage.ErrAlreadyExists
	}
	s.marketplaceInstallations[key] = value
	return nil
}

func (s *Store) ListMarketplaceInstallations(_ context.Context, accountID string) ([]protocol.MarketplaceInstallation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := []protocol.MarketplaceInstallation{}
	for _, value := range s.marketplaceInstallations {
		if value.AccountID == accountID {
			result = append(result, value)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].InstalledAt.After(result[j].InstalledAt) })
	return result, nil
}

func (s *Store) CreateMarketplaceTransaction(_ context.Context, value protocol.MarketplaceTransaction) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	listing, ok := s.marketplaceListings[value.ListingID]
	if !ok || listing.Status != "published" || listing.PublisherID != value.SellerAccountID {
		return storage.ErrNotFound
	}
	if _, ok := s.accounts[value.BuyerAccountID]; !ok {
		return storage.ErrNotFound
	}
	if _, exists := s.marketplaceTransactions[value.ID]; exists {
		return storage.ErrAlreadyExists
	}
	idempotencyKey := value.BuyerAccountID + "\x00" + value.IdempotencyKey
	if _, exists := s.transactionIdempotency[idempotencyKey]; exists {
		return storage.ErrAlreadyExists
	}
	s.marketplaceTransactions[value.ID] = value
	s.transactionIdempotency[idempotencyKey] = value.ID
	return nil
}

func (s *Store) GetMarketplaceTransaction(_ context.Context, id string) (protocol.MarketplaceTransaction, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.marketplaceTransactions[id]
	if !ok {
		return protocol.MarketplaceTransaction{}, storage.ErrNotFound
	}
	return value, nil
}

func (s *Store) ListMarketplaceTransactions(_ context.Context, accountID string, limit int) ([]protocol.MarketplaceTransaction, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	result := make([]protocol.MarketplaceTransaction, 0)
	for _, value := range s.marketplaceTransactions {
		if value.BuyerAccountID == accountID || value.SellerAccountID == accountID {
			result = append(result, value)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].UpdatedAt.After(result[j].UpdatedAt) })
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (s *Store) TransitionMarketplaceTransaction(_ context.Context, transactionID, actorAccountID, targetStatus string, now time.Time) (protocol.MarketplaceTransaction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.marketplaceTransactions[transactionID]
	if !ok {
		return protocol.MarketplaceTransaction{}, storage.ErrNotFound
	}
	allowed := false
	switch targetStatus {
	case "authorized":
		allowed = value.Status == "pending" && value.BuyerAccountID == actorAccountID
	case "completed":
		allowed = value.Status == "authorized" && value.SellerAccountID == actorAccountID
	case "cancelled":
		allowed = (value.Status == "pending" || value.Status == "authorized") && (value.BuyerAccountID == actorAccountID || value.SellerAccountID == actorAccountID)
	}
	if !allowed {
		return protocol.MarketplaceTransaction{}, storage.ErrUnauthorized
	}
	value.Status, value.UpdatedAt = targetStatus, now.UTC()
	s.marketplaceTransactions[transactionID] = value
	return value, nil
}

func (s *Store) HasCompletedMarketplaceTransaction(_ context.Context, listingID, accountID string) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, value := range s.marketplaceTransactions {
		if value.ListingID == listingID && value.BuyerAccountID == accountID && value.Status == "completed" {
			if value.AmountMinor == 0 {
				return true, nil
			}
			for _, d := range s.billingDocuments {
				if d.Invoice.AccountID == accountID && billing.PurchasePaid(d, value.ID) {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

func organizationMemberKey(organizationID, accountID string) string {
	return organizationID + "\x00" + accountID
}
func teamMemberKey(teamID, accountID string) string { return teamID + "\x00" + accountID }
func artifactPermissionKey(taskID, artifactID, accountID string) string {
	return taskID + "\x00" + artifactID + "\x00" + accountID
}
func marketplaceInstallationKey(listingID, accountID string) string {
	return listingID + "\x00" + accountID
}
func pointerTime(value time.Time) *time.Time { value = value.UTC(); return &value }
func webhookMatches(patterns []string, value string) bool {
	for _, pattern := range patterns {
		if pattern == "*" || pattern == value {
			return true
		}
	}
	return false
}
