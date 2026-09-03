package storage

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/agent-continuity/agent-continuity-master/internal/billing"
	"github.com/agent-continuity/agent-continuity-master/internal/tenancy"
	"github.com/agent-continuity/agent-continuity-master/internal/treasury"
	"time"

	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

var (
	ErrNotFound       = errors.New("not found")
	ErrAlreadyExists  = errors.New("already exists")
	ErrUnauthorized   = errors.New("unauthorized")
	ErrDeviceRevoked  = errors.New("device revoked")
	ErrSequenceReplay = errors.New("device sequence already used by another event")
	ErrInviteInvalid  = errors.New("invitation is invalid, expired, revoked, or already used")
	ErrRefreshInvalid = errors.New("refresh token is invalid, expired, or revoked")
	ErrTokenReuse     = errors.New("refresh token reuse detected; session family revoked")
)

type Store interface {
	tenancy.Repository
	billing.Repository
	treasury.Repository
	Ping(context.Context) error
	Close()

	CreateAccount(context.Context, protocol.Account) error
	GetAccount(context.Context, string) (protocol.Account, error)
	UpdateAccount(context.Context, protocol.Account) error
	PutExternalIdentity(context.Context, protocol.ExternalIdentity) error
	GetExternalIdentity(context.Context, string, string) (protocol.ExternalIdentity, error)
	GetExternalIdentityByID(context.Context, string) (protocol.ExternalIdentity, error)
	ListExternalIdentities(context.Context, string, int) ([]protocol.ExternalIdentity, error)
	CreateAuthSession(context.Context, protocol.AuthSession) error
	RotateAuthSession(context.Context, string, protocol.AuthSession) (protocol.AuthSession, error)
	RevokeAuthSession(context.Context, string, string) error
	CreateAPIKey(context.Context, protocol.APIKey) error
	ListAPIKeys(context.Context, string) ([]protocol.APIKey, error)
	AuthenticateAPIKey(context.Context, string) (protocol.APIKey, error)
	RevokeAPIKey(context.Context, string, string) error
	RecordAuditEvent(context.Context, protocol.AuditEvent) error
	ListAuditEvents(context.Context, string, time.Time, int) ([]protocol.AuditEvent, error)

	RegisterDevice(context.Context, protocol.Device) error
	GetDevice(context.Context, string) (protocol.Device, error)
	ListDevices(context.Context, string, bool) ([]protocol.Device, error)
	ListTaskDevices(context.Context, string) ([]protocol.Device, error)
	RevokeDevice(context.Context, string, string) error

	CreateTask(context.Context, protocol.Task) error
	GetTask(context.Context, string) (protocol.Task, error)
	ListTasks(context.Context, string, int) ([]protocol.Task, error)
	AppendEvent(context.Context, protocol.Event) (protocol.AppendResult, error)
	GetEvent(context.Context, string, string) (protocol.Event, error)
	ListEvents(context.Context, string, uint64, int, bool) ([]protocol.Event, error)
	ListBranchEvents(context.Context, string, string, uint64, int, bool) ([]protocol.Event, error)
	GetTaskEventCursor(context.Context, string) (uint64, error)
	RecordSyncNonce(context.Context, string, string, time.Time, time.Time) error
	GetSyncCursor(context.Context, string, string) (uint64, error)
	PutSyncCursor(context.Context, string, string, uint64, time.Time) error

	CreateBranch(context.Context, protocol.TaskBranch) error
	GetBranch(context.Context, string, string) (protocol.TaskBranch, error)
	ListBranches(context.Context, string) ([]protocol.TaskBranch, error)
	MarkBranchMerged(context.Context, string, string, string, time.Time) error
	ListConflicts(context.Context, string, bool, int) ([]protocol.Conflict, error)
	ResolveConflict(context.Context, protocol.ConflictResolution) error
	PutSnapshot(context.Context, protocol.TaskSnapshot) error
	LatestSnapshot(context.Context, string, string) (protocol.TaskSnapshot, error)

	CreateAgent(context.Context, protocol.AgentProfile) error
	GetAgent(context.Context, string) (protocol.AgentProfile, error)
	ListAgents(context.Context, string, int) ([]protocol.AgentProfile, error)
	RevokeAgent(context.Context, string, string) error
	AddTaskParticipant(context.Context, protocol.TaskParticipant) error
	GetTaskParticipant(context.Context, string, string) (protocol.TaskParticipant, error)
	ListTaskParticipants(context.Context, string) ([]protocol.TaskParticipant, error)
	LeaveTaskParticipant(context.Context, string, string, time.Time) error
	BindTaskAgent(context.Context, protocol.TaskAgentBinding, protocol.TaskParticipant) error
	ListTaskAgentBindings(context.Context, string) ([]protocol.TaskAgentBinding, error)
	CreateNotification(context.Context, protocol.Notification) error
	ListNotifications(context.Context, string, bool, int) ([]protocol.Notification, error)
	MarkNotificationRead(context.Context, string, string, time.Time) error

	PutArtifact(context.Context, protocol.Artifact) error
	GetArtifact(context.Context, string, string) (protocol.Artifact, error)
	ListArtifacts(context.Context, string) ([]protocol.Artifact, error)
	ListArtifactVersions(context.Context, string, string) ([]protocol.Artifact, error)
	CreateArtifactUpload(context.Context, protocol.ArtifactUploadSession) error
	GetArtifactUpload(context.Context, string) (protocol.ArtifactUploadSession, error)
	PutArtifactChunk(context.Context, protocol.ArtifactChunk) error
	ListArtifactChunks(context.Context, string) ([]protocol.ArtifactChunk, error)
	CompleteArtifactUpload(context.Context, string, protocol.Artifact, time.Time) error

	CreateInvitation(context.Context, protocol.Invitation) error
	AcceptInvitation(context.Context, string, string) (protocol.Invitation, error)
	RevokeInvitation(context.Context, string, string) error
	CheckPermission(context.Context, string, string, protocol.Permission) error
	ListTaskPermissions(context.Context, string) ([]protocol.TaskPermission, error)
	PutTaskPermission(context.Context, protocol.TaskPermission) error
	DeleteTaskPermission(context.Context, string, string, time.Time) error

	PutTaskKeySet(context.Context, protocol.TaskKeySet) error
	GetTaskKeySet(context.Context, string, uint32) (protocol.TaskKeySet, error)
	LatestTaskKeySet(context.Context, string) (protocol.TaskKeySet, error)

	CreateOrganization(context.Context, protocol.Organization, protocol.OrganizationMember) error
	GetOrganization(context.Context, string) (protocol.Organization, error)
	ListOrganizations(context.Context, string) ([]protocol.Organization, error)
	UpdateOrganization(context.Context, protocol.Organization) error
	DeleteOrganization(context.Context, string, string) error
	PutOrganizationMember(context.Context, protocol.OrganizationMember) error
	GetOrganizationMember(context.Context, string, string) (protocol.OrganizationMember, error)
	ListOrganizationMembers(context.Context, string) ([]protocol.OrganizationMember, error)
	RemoveOrganizationMember(context.Context, string, string, time.Time) error
	CreateTeam(context.Context, protocol.Team) error
	GetTeam(context.Context, string) (protocol.Team, error)
	ListTeams(context.Context, string) ([]protocol.Team, error)
	UpdateTeam(context.Context, protocol.Team) error
	DeleteTeam(context.Context, string, string) error
	PutTeamMember(context.Context, protocol.TeamMember) error
	GetTeamMember(context.Context, string, string) (protocol.TeamMember, error)
	ListTeamMembers(context.Context, string) ([]protocol.TeamMember, error)
	RemoveTeamMember(context.Context, string, string, time.Time) error
	CreateAccessPolicy(context.Context, protocol.AccessPolicy) error
	ListAccessPolicies(context.Context, string) ([]protocol.AccessPolicy, error)
	DeleteAccessPolicy(context.Context, string, string) error

	PutArtifactPermission(context.Context, protocol.ArtifactPermission) error
	ListArtifactPermissions(context.Context, string, string) ([]protocol.ArtifactPermission, error)
	CheckArtifactPermission(context.Context, string, string, string, protocol.Permission) error
	DeleteArtifactPermission(context.Context, string, string, string) error

	CreateOAuthClient(context.Context, protocol.OAuthClient) error
	ListOAuthClients(context.Context, string) ([]protocol.OAuthClient, error)
	AuthenticateOAuthClient(context.Context, string) (protocol.OAuthClient, error)
	RevokeOAuthClient(context.Context, string, string) error

	CreateWebhookEndpoint(context.Context, protocol.WebhookEndpoint, []byte) error
	ListWebhookEndpoints(context.Context, string) ([]protocol.WebhookEndpoint, error)
	GetWebhookEndpoint(context.Context, string) (protocol.WebhookEndpoint, []byte, error)
	GetWebhookSecret(context.Context, string) ([]byte, error)
	RevokeWebhookEndpoint(context.Context, string, string, time.Time) error
	CreateWebhookDeliveries(context.Context, string, string, json.RawMessage, time.Time) error
	ListReadyWebhookDeliveries(context.Context, time.Time, int) ([]protocol.WebhookDelivery, error)
	UpdateWebhookDelivery(context.Context, protocol.WebhookDelivery) error

	RecordUsage(context.Context, protocol.UsageRecord) error
	ListUsage(context.Context, string, time.Time, time.Time, int) ([]protocol.UsageRecord, error)
	PutBillingAccount(context.Context, protocol.BillingAccount) error
	ListBillingAccounts(context.Context, string) ([]protocol.BillingAccount, error)
	CreateMarketplaceListing(context.Context, protocol.MarketplaceListing) error
	GetMarketplaceListing(context.Context, string) (protocol.MarketplaceListing, error)
	ListMarketplaceListings(context.Context, string, string, int) ([]protocol.MarketplaceListing, error)
	InstallMarketplaceListing(context.Context, protocol.MarketplaceInstallation) error
	ListMarketplaceInstallations(context.Context, string) ([]protocol.MarketplaceInstallation, error)
	CreateMarketplaceTransaction(context.Context, protocol.MarketplaceTransaction) error
	GetMarketplaceTransaction(context.Context, string) (protocol.MarketplaceTransaction, error)
	ListMarketplaceTransactions(context.Context, string, int) ([]protocol.MarketplaceTransaction, error)
	TransitionMarketplaceTransaction(context.Context, string, string, string, time.Time) (protocol.MarketplaceTransaction, error)
	HasCompletedMarketplaceTransaction(context.Context, string, string) (bool, error)
}
