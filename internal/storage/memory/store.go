package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/agent-continuity/agent-continuity-master/internal/storage"
	"github.com/agent-continuity/agent-continuity-master/internal/tenancy"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

type Store struct {
	tenancyControl           tenancy.Control
	portalLogins             map[string]tenancy.Login
	portalSessions           map[string]tenancy.Session
	mu                       sync.RWMutex
	accounts                 map[string]protocol.Account
	externalIdentities       map[string]protocol.ExternalIdentity
	externalIdentityIDs      map[string]string
	authSessions             map[string]protocol.AuthSession
	sessionHashes            map[string]string
	apiKeys                  map[string]protocol.APIKey
	apiKeyHashes             map[string]string
	auditEvents              []protocol.AuditEvent
	devices                  map[string]protocol.Device
	agents                   map[string]protocol.AgentProfile
	tasks                    map[string]protocol.Task
	participants             map[string]protocol.TaskParticipant
	agentBindings            map[string]protocol.TaskAgentBinding
	notifications            map[string]protocol.Notification
	branches                 map[string]protocol.TaskBranch
	events                   map[string][]protocol.Event
	eventIDs                 map[string]protocol.Event
	idempotency              map[string]string
	deviceSequences          map[string]string
	artifacts                map[string]protocol.Artifact
	artifactVersions         map[string][]protocol.Artifact
	artifactUploads          map[string]protocol.ArtifactUploadSession
	artifactChunks           map[string]map[int]protocol.ArtifactChunk
	invitations              map[string]protocol.Invitation
	permissions              map[string]protocol.Permission
	permissionCreatedAt      map[string]time.Time
	conflicts                map[string]protocol.Conflict
	resolutions              map[string]protocol.ConflictResolution
	snapshots                map[string][]protocol.TaskSnapshot
	taskKeySets              map[string]map[uint32]protocol.TaskKeySet
	syncNonces               map[string]time.Time
	syncCursors              map[string]uint64
	organizations            map[string]protocol.Organization
	organizationMembers      map[string]protocol.OrganizationMember
	teams                    map[string]protocol.Team
	teamMembers              map[string]protocol.TeamMember
	accessPolicies           map[string]protocol.AccessPolicy
	artifactPermissions      map[string]protocol.ArtifactPermission
	oauthClients             map[string]protocol.OAuthClient
	oauthClientHashes        map[string]string
	webhooks                 map[string]protocol.WebhookEndpoint
	webhookSecrets           map[string][]byte
	webhookDeliveries        map[string]protocol.WebhookDelivery
	usageRecords             map[string]protocol.UsageRecord
	usageIdempotency         map[string]string
	billingAccounts          map[string]protocol.BillingAccount
	marketplaceListings      map[string]protocol.MarketplaceListing
	marketplaceInstallations map[string]protocol.MarketplaceInstallation
	marketplaceTransactions  map[string]protocol.MarketplaceTransaction
	transactionIdempotency   map[string]string
	billingDocuments         map[string]protocol.BillingDocument
	treasuryDocuments        map[string]protocol.TreasuryDocument
	nextSequence             uint64
	now                      func() time.Time
}

func New() *Store {
	return &Store{
		portalLogins: map[string]tenancy.Login{}, portalSessions: map[string]tenancy.Session{},
		billingDocuments:  map[string]protocol.BillingDocument{},
		treasuryDocuments: map[string]protocol.TreasuryDocument{},
		accounts:          map[string]protocol.Account{}, externalIdentities: map[string]protocol.ExternalIdentity{}, externalIdentityIDs: map[string]string{}, authSessions: map[string]protocol.AuthSession{},
		sessionHashes: map[string]string{}, apiKeys: map[string]protocol.APIKey{}, apiKeyHashes: map[string]string{},
		devices: map[string]protocol.Device{}, agents: map[string]protocol.AgentProfile{},
		tasks: map[string]protocol.Task{}, branches: map[string]protocol.TaskBranch{}, events: map[string][]protocol.Event{},
		participants: map[string]protocol.TaskParticipant{}, agentBindings: map[string]protocol.TaskAgentBinding{}, notifications: map[string]protocol.Notification{},
		eventIDs: map[string]protocol.Event{}, idempotency: map[string]string{},
		deviceSequences: map[string]string{}, artifacts: map[string]protocol.Artifact{}, artifactVersions: map[string][]protocol.Artifact{},
		artifactUploads: map[string]protocol.ArtifactUploadSession{}, artifactChunks: map[string]map[int]protocol.ArtifactChunk{},
		invitations: map[string]protocol.Invitation{}, permissions: map[string]protocol.Permission{}, permissionCreatedAt: map[string]time.Time{},
		conflicts: map[string]protocol.Conflict{}, resolutions: map[string]protocol.ConflictResolution{}, snapshots: map[string][]protocol.TaskSnapshot{},
		taskKeySets: map[string]map[uint32]protocol.TaskKeySet{},
		syncNonces:  map[string]time.Time{}, syncCursors: map[string]uint64{},
		organizations: map[string]protocol.Organization{}, organizationMembers: map[string]protocol.OrganizationMember{},
		teams: map[string]protocol.Team{}, teamMembers: map[string]protocol.TeamMember{}, accessPolicies: map[string]protocol.AccessPolicy{},
		artifactPermissions: map[string]protocol.ArtifactPermission{}, oauthClients: map[string]protocol.OAuthClient{}, oauthClientHashes: map[string]string{},
		webhooks: map[string]protocol.WebhookEndpoint{}, webhookSecrets: map[string][]byte{}, webhookDeliveries: map[string]protocol.WebhookDelivery{},
		usageRecords: map[string]protocol.UsageRecord{}, usageIdempotency: map[string]string{}, billingAccounts: map[string]protocol.BillingAccount{}, marketplaceListings: map[string]protocol.MarketplaceListing{}, marketplaceInstallations: map[string]protocol.MarketplaceInstallation{}, marketplaceTransactions: map[string]protocol.MarketplaceTransaction{}, transactionIdempotency: map[string]string{},
		now: time.Now,
	}
}

func (s *Store) Ping(context.Context) error { return nil }
func (s *Store) Close()                     {}

func (s *Store) CreateAccount(_ context.Context, account protocol.Account) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.accounts[account.ID]; ok {
		return storage.ErrAlreadyExists
	}
	s.accounts[account.ID] = account
	return nil
}

func (s *Store) GetAccount(_ context.Context, id string) (protocol.Account, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	account, ok := s.accounts[id]
	if !ok {
		return protocol.Account{}, storage.ErrNotFound
	}
	return account, nil
}

func (s *Store) UpdateAccount(_ context.Context, account protocol.Account) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.accounts[account.ID]
	if !ok {
		return storage.ErrNotFound
	}
	account.CreatedAt = current.CreatedAt
	s.accounts[account.ID] = account
	return nil
}

func (s *Store) RegisterDevice(_ context.Context, device protocol.Device) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.accounts[device.AccountID]; !ok {
		return storage.ErrNotFound
	}
	if _, ok := s.devices[device.ID]; ok {
		return storage.ErrAlreadyExists
	}
	s.devices[device.ID] = device
	return nil
}

func (s *Store) GetDevice(_ context.Context, id string) (protocol.Device, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	device, ok := s.devices[id]
	if !ok {
		return protocol.Device{}, storage.ErrNotFound
	}
	return device, nil
}

func (s *Store) ListDevices(_ context.Context, accountID string, includeRevoked bool) ([]protocol.Device, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.accounts[accountID]; !ok {
		return nil, storage.ErrNotFound
	}
	result := make([]protocol.Device, 0)
	for _, device := range s.devices {
		if device.AccountID == accountID && (includeRevoked || device.RevokedAt == nil) {
			result = append(result, device)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].ID < result[j].ID
		}
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})
	return result, nil
}

func (s *Store) ListTaskDevices(_ context.Context, taskID string) ([]protocol.Device, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.tasks[taskID]; !ok {
		return nil, storage.ErrNotFound
	}
	result := make([]protocol.Device, 0)
	for _, device := range s.devices {
		if device.RevokedAt != nil || device.EncryptionPublicKey == "" {
			continue
		}
		if _, ok := s.permissions[permissionKey(taskID, device.AccountID)]; ok {
			result = append(result, device)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func (s *Store) RevokeDevice(_ context.Context, accountID, deviceID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	device, ok := s.devices[deviceID]
	if !ok {
		return storage.ErrNotFound
	}
	if device.AccountID != accountID {
		return storage.ErrUnauthorized
	}
	now := s.now().UTC()
	device.RevokedAt = &now
	s.devices[deviceID] = device
	return nil
}

func (s *Store) CreateTask(_ context.Context, task protocol.Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.accounts[task.AccountID]; !ok {
		return storage.ErrNotFound
	}
	if _, ok := s.tasks[task.ID]; ok {
		return storage.ErrAlreadyExists
	}
	s.tasks[task.ID] = cloneTask(task)
	s.permissions[permissionKey(task.ID, task.AccountID)] = protocol.PermissionAdmin
	s.permissionCreatedAt[permissionKey(task.ID, task.AccountID)] = task.CreatedAt.UTC()
	s.branches[branchKey(task.ID, "main")] = protocol.TaskBranch{TaskID: task.ID, ID: "main", Name: "Main", HeadEventIDs: []string{}, Status: "active", CreatedBy: task.AccountID, CreatedAt: task.CreatedAt}
	participantID := "participant_owner_" + task.ID
	s.participants[participantID] = protocol.TaskParticipant{ID: participantID, TaskID: task.ID, AccountID: task.AccountID, Kind: "human", Role: "owner", Status: "active", JoinedAt: task.CreatedAt}
	return nil
}

func (s *Store) GetTask(_ context.Context, id string) (protocol.Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	task, ok := s.tasks[id]
	if !ok {
		return protocol.Task{}, storage.ErrNotFound
	}
	return cloneTask(task), nil
}

func (s *Store) ListTasks(_ context.Context, accountID string, limit int) ([]protocol.Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	tasks := make([]protocol.Task, 0)
	for _, task := range s.tasks {
		_, direct := s.permissions[permissionKey(task.ID, accountID)]
		member, organizationMember := s.organizationMembers[organizationMemberKey(task.OrganizationID, accountID)]
		teamMember, belongsToTeam := s.teamMembers[teamMemberKey(task.TeamID, accountID)]
		organizationAccess := organizationMember && member.Status == "active" && (task.TeamID == "" || member.Role == "owner" || member.Role == "admin" || belongsToTeam && teamMember.Status == "active")
		if direct || organizationAccess {
			tasks = append(tasks, cloneTask(task))
		}
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].UpdatedAt.After(tasks[j].UpdatedAt) })
	if len(tasks) > limit {
		tasks = tasks[:limit]
	}
	return tasks, nil
}

func (s *Store) AppendEvent(_ context.Context, event protocol.Event) (protocol.AppendResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[event.TaskID]
	if !ok {
		return protocol.AppendResult{}, storage.ErrNotFound
	}
	branch, ok := s.branches[branchKey(event.TaskID, event.BranchID)]
	if !ok || branch.Status != "active" {
		return protocol.AppendResult{}, storage.ErrNotFound
	}
	device, ok := s.devices[event.DeviceID]
	if !ok || device.AccountID != event.AccountID {
		return protocol.AppendResult{}, storage.ErrUnauthorized
	}
	if device.RevokedAt != nil {
		return protocol.AppendResult{}, storage.ErrDeviceRevoked
	}
	if existing, ok := s.eventIDs[event.EventID]; ok {
		return protocol.AppendResult{Event: cloneEvent(existing), Duplicate: true, TaskHeads: append([]string(nil), task.HeadEventIDs...)}, nil
	}
	idemKey := event.TaskID + "\x00" + event.DeviceID + "\x00" + event.IdempotencyKey
	if eventID, ok := s.idempotency[idemKey]; ok {
		existing := s.eventIDs[eventID]
		return protocol.AppendResult{Event: cloneEvent(existing), Duplicate: true, TaskHeads: append([]string(nil), task.HeadEventIDs...)}, nil
	}
	sequenceKey := event.TaskID + "\x00" + event.DeviceID + "\x00" + strconv.FormatUint(event.DeviceSequence, 10)
	if eventID, ok := s.deviceSequences[sequenceKey]; ok && eventID != event.EventID {
		return protocol.AppendResult{}, storage.ErrSequenceReplay
	}

	conflicts := make([]protocol.Conflict, 0)
	for _, parent := range event.ParentEventIDs {
		if existing, ok := s.eventIDs[parent]; !ok || existing.TaskID != event.TaskID {
			conflicts = append(conflicts, newConflict(event, "missing_parent", "defer_until_parent_arrives", map[string]string{"parent_event_id": parent}, s.now()))
		}
	}
	if len(branch.HeadEventIDs) > 0 && !intersects(branch.HeadEventIDs, event.ParentEventIDs) {
		kind, strategy, resolved := classifyConcurrent(event, branch.HeadEventIDs, s.eventIDs)
		conflict := newConflict(event, kind, strategy, map[string][]string{"existing_heads": branch.HeadEventIDs}, s.now())
		conflict.Resolved = resolved
		conflicts = append(conflicts, conflict)
	}

	s.nextSequence++
	event.ServerSequence = s.nextSequence
	event.ReceivedAtServer = s.now().UTC()
	s.events[event.TaskID] = append(s.events[event.TaskID], cloneEvent(event))
	s.eventIDs[event.EventID] = cloneEvent(event)
	s.idempotency[idemKey] = event.EventID
	s.deviceSequences[sequenceKey] = event.EventID
	task.HeadEventIDs = updateHeads(task.HeadEventIDs, event.ParentEventIDs, event.EventID)
	branch.HeadEventIDs = updateHeads(branch.HeadEventIDs, event.ParentEventIDs, event.EventID)
	for _, existing := range s.events[event.TaskID] {
		if existing.EventID != event.EventID && contains(existing.ParentEventIDs, event.EventID) {
			task.HeadEventIDs = removeHead(task.HeadEventIDs, event.EventID)
			if existing.BranchID == event.BranchID {
				branch.HeadEventIDs = removeHead(branch.HeadEventIDs, event.EventID)
			}
			break
		}
	}
	task.UpdatedAt = event.ReceivedAtServer
	s.tasks[task.ID] = task
	s.branches[branchKey(event.TaskID, event.BranchID)] = branch
	for _, conflict := range conflicts {
		s.conflicts[conflict.ID] = conflict
	}
	return protocol.AppendResult{Event: cloneEvent(event), Conflicts: conflicts, TaskHeads: append([]string(nil), task.HeadEventIDs...)}, nil
}

func (s *Store) ListEvents(_ context.Context, taskID string, after uint64, limit int, includePayload bool) ([]protocol.Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.tasks[taskID]; !ok {
		return nil, storage.ErrNotFound
	}
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	result := make([]protocol.Event, 0, limit)
	for _, event := range s.events[taskID] {
		if event.ServerSequence <= after {
			continue
		}
		copy := cloneEvent(event)
		if !includePayload {
			copy.Payload = nil
		}
		result = append(result, copy)
		if len(result) == limit {
			break
		}
	}
	return result, nil
}

func (s *Store) GetTaskEventCursor(_ context.Context, taskID string) (uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.tasks[taskID]; !ok {
		return 0, storage.ErrNotFound
	}
	events := s.events[taskID]
	if len(events) == 0 {
		return 0, nil
	}
	return events[len(events)-1].ServerSequence, nil
}

func (s *Store) RecordSyncNonce(_ context.Context, deviceID, nonceHash string, createdAt, expiresAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.devices[deviceID]; !ok {
		return storage.ErrNotFound
	}
	key := deviceID + "\x00" + nonceHash
	if expiry, ok := s.syncNonces[key]; ok && expiry.After(createdAt) {
		return storage.ErrAlreadyExists
	}
	s.syncNonces[key] = expiresAt
	return nil
}

func (s *Store) GetSyncCursor(_ context.Context, deviceID, taskID string) (uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.syncCursors[deviceID+"\x00"+taskID], nil
}

func (s *Store) PutSyncCursor(_ context.Context, deviceID, taskID string, cursor uint64, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.devices[deviceID]; !ok {
		return storage.ErrNotFound
	}
	if _, ok := s.tasks[taskID]; !ok {
		return storage.ErrNotFound
	}
	key := deviceID + "\x00" + taskID
	if cursor > s.syncCursors[key] {
		s.syncCursors[key] = cursor
	}
	return nil
}

func (s *Store) PutArtifact(_ context.Context, artifact protocol.Artifact) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tasks[artifact.TaskID]; !ok {
		return storage.ErrNotFound
	}
	key := artifactKey(artifact.TaskID, artifact.ID)
	if old, ok := s.artifacts[key]; ok {
		if old.VersionID == artifact.VersionID {
			if old.ContentHash == artifact.ContentHash {
				return nil
			}
			return storage.ErrAlreadyExists
		}
		if artifact.ParentVersionID != old.VersionID {
			return storage.ErrAlreadyExists
		}
	}
	s.artifacts[key] = cloneArtifact(artifact)
	s.artifactVersions[key] = append(s.artifactVersions[key], cloneArtifact(artifact))
	return nil
}

func (s *Store) GetArtifact(_ context.Context, taskID, artifactID string) (protocol.Artifact, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	artifact, ok := s.artifacts[artifactKey(taskID, artifactID)]
	if !ok {
		return protocol.Artifact{}, storage.ErrNotFound
	}
	return cloneArtifact(artifact), nil
}

func (s *Store) ListArtifacts(_ context.Context, taskID string) ([]protocol.Artifact, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.tasks[taskID]; !ok {
		return nil, storage.ErrNotFound
	}
	result := make([]protocol.Artifact, 0)
	for _, artifact := range s.artifacts {
		if artifact.TaskID == taskID {
			result = append(result, cloneArtifact(artifact))
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.Before(result[j].CreatedAt) })
	return result, nil
}

func (s *Store) CreateInvitation(_ context.Context, invitation protocol.Invitation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tasks[invitation.TaskID]; !ok {
		return storage.ErrNotFound
	}
	if _, ok := s.invitations[invitation.ID]; ok {
		return storage.ErrAlreadyExists
	}
	if invitation.InviterDeviceID != "" && invitation.Nonce != "" {
		for _, existing := range s.invitations {
			if existing.InviterDeviceID == invitation.InviterDeviceID && existing.Nonce == invitation.Nonce {
				return storage.ErrAlreadyExists
			}
		}
	}
	s.invitations[invitation.ID] = invitation
	return nil
}

func (s *Store) AcceptInvitation(_ context.Context, token, accountID string) (protocol.Invitation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	hash := sha256.Sum256([]byte(token))
	tokenHash := hex.EncodeToString(hash[:])
	for id, invitation := range s.invitations {
		if invitation.TokenHash != tokenHash {
			continue
		}
		if invitation.RevokedAt != nil || invitation.AcceptedAt != nil || !s.now().Before(invitation.ExpiresAt) {
			return protocol.Invitation{}, storage.ErrInviteInvalid
		}
		if _, ok := s.accounts[accountID]; !ok {
			return protocol.Invitation{}, storage.ErrNotFound
		}
		now := s.now().UTC()
		invitation.AcceptedAt = &now
		invitation.AcceptedBy = accountID
		s.invitations[id] = invitation
		s.permissions[permissionKey(invitation.TaskID, accountID)] = invitation.Permission
		s.permissionCreatedAt[permissionKey(invitation.TaskID, accountID)] = now
		participantID, _ := protocol.NewID("participant")
		role := "viewer"
		if invitation.Permission == protocol.PermissionWrite || invitation.Permission == protocol.PermissionAdmin {
			role = "contributor"
		}
		s.participants[participantID] = protocol.TaskParticipant{ID: participantID, TaskID: invitation.TaskID, AccountID: accountID, Kind: "human", Role: role, Status: "active", JoinedAt: now}
		return invitation, nil
	}
	return protocol.Invitation{}, storage.ErrInviteInvalid
}

func (s *Store) RevokeInvitation(_ context.Context, accountID, invitationID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	invitation, ok := s.invitations[invitationID]
	if !ok {
		return storage.ErrNotFound
	}
	if invitation.InviterAccount != accountID {
		return storage.ErrUnauthorized
	}
	now := s.now().UTC()
	invitation.RevokedAt = &now
	s.invitations[invitationID] = invitation
	return nil
}

func (s *Store) CheckPermission(_ context.Context, taskID, accountID string, required protocol.Permission) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if t, ok := s.tasks[taskID]; ok && t.OrganizationID != "" {
		m, exists := s.organizationMembers[organizationMemberKey(t.OrganizationID, accountID)]
		if !exists || m.Status != "active" || m.Role == "billing" {
			return storage.ErrUnauthorized
		}
		if m.Role == "owner" || m.Role == "admin" {
			return nil
		}
		if required == protocol.PermissionAdmin || required == protocol.PermissionWrite && m.Role != "member" {
			return storage.ErrUnauthorized
		}
		if t.TeamID != "" {
			tm, exists := s.teamMembers[teamMemberKey(t.TeamID, accountID)]
			if !exists || tm.Status != "active" || required == protocol.PermissionWrite && tm.Role == "viewer" {
				return storage.ErrUnauthorized
			}
		}
		return nil
	}
	actual, ok := s.permissions[permissionKey(taskID, accountID)]
	if !ok || permissionRank(actual) < permissionRank(required) {
		return storage.ErrUnauthorized
	}
	return nil
}

func (s *Store) ListTaskPermissions(_ context.Context, taskID string) ([]protocol.TaskPermission, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.tasks[taskID]; !ok {
		return nil, storage.ErrNotFound
	}
	result := make([]protocol.TaskPermission, 0)
	prefix := taskID + "\x00"
	for key, permission := range s.permissions {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		result = append(result, protocol.TaskPermission{TaskID: taskID, AccountID: strings.TrimPrefix(key, prefix), Permission: permission, CreatedAt: s.permissionCreatedAt[key]})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].AccountID < result[j].AccountID
		}
		return result[i].CreatedAt.Before(result[j].CreatedAt)
	})
	return result, nil
}

func (s *Store) PutTaskPermission(_ context.Context, value protocol.TaskPermission) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[value.TaskID]
	if !ok {
		return storage.ErrNotFound
	}
	if _, ok := s.accounts[value.AccountID]; !ok {
		return storage.ErrNotFound
	}
	if value.AccountID == task.AccountID && value.Permission != protocol.PermissionAdmin {
		return storage.ErrUnauthorized
	}
	key := permissionKey(value.TaskID, value.AccountID)
	s.permissions[key] = value.Permission
	s.permissionCreatedAt[key] = value.CreatedAt.UTC()
	role := "viewer"
	if value.Permission == protocol.PermissionWrite || value.Permission == protocol.PermissionAdmin {
		role = "contributor"
	}
	for id, participant := range s.participants {
		if participant.TaskID == value.TaskID && participant.AccountID == value.AccountID {
			if participant.Role != "owner" {
				participant.Role = role
			}
			participant.Status = "active"
			participant.LeftAt = nil
			s.participants[id] = participant
			return nil
		}
	}
	participantID, _ := protocol.NewID("participant")
	s.participants[participantID] = protocol.TaskParticipant{ID: participantID, TaskID: value.TaskID, AccountID: value.AccountID, Kind: "human", Role: role, Status: "active", JoinedAt: value.CreatedAt.UTC()}
	return nil
}

func (s *Store) DeleteTaskPermission(_ context.Context, taskID, accountID string, removedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[taskID]
	if !ok {
		return storage.ErrNotFound
	}
	if accountID == task.AccountID {
		return storage.ErrUnauthorized
	}
	key := permissionKey(taskID, accountID)
	if _, ok := s.permissions[key]; !ok {
		return storage.ErrNotFound
	}
	delete(s.permissions, key)
	delete(s.permissionCreatedAt, key)
	when := removedAt.UTC()
	for id, participant := range s.participants {
		if participant.TaskID == taskID && participant.AccountID == accountID && participant.Status == "active" {
			participant.Status = "left"
			participant.LeftAt = &when
			s.participants[id] = participant
		}
	}
	return nil
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
func permissionKey(taskID, accountID string) string { return taskID + "\x00" + accountID }
func artifactKey(taskID, artifactID string) string  { return taskID + "\x00" + artifactID }

func updateHeads(heads, parents []string, eventID string) []string {
	parentSet := map[string]struct{}{}
	for _, id := range parents {
		parentSet[id] = struct{}{}
	}
	result := make([]string, 0, len(heads)+1)
	for _, head := range heads {
		if _, consumed := parentSet[head]; !consumed {
			result = append(result, head)
		}
	}
	result = append(result, eventID)
	sort.Strings(result)
	return result
}

func intersects(a, b []string) bool {
	set := map[string]struct{}{}
	for _, value := range a {
		set[value] = struct{}{}
	}
	for _, value := range b {
		if _, ok := set[value]; ok {
			return true
		}
	}
	return false
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
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

func newConflict(event protocol.Event, kind, strategy string, details any, now time.Time) protocol.Conflict {
	id, _ := protocol.NewID("conflict")
	raw, _ := json.Marshal(details)
	return protocol.Conflict{ID: id, TaskID: event.TaskID, EventID: event.EventID, Kind: kind, Strategy: strategy, Details: raw, CreatedAt: now.UTC()}
}

func cloneTask(value protocol.Task) protocol.Task {
	value.HeadEventIDs = append([]string(nil), value.HeadEventIDs...)
	value.Metadata = append([]byte(nil), value.Metadata...)
	return value
}
func cloneEvent(value protocol.Event) protocol.Event {
	value.ParentEventIDs = append([]string(nil), value.ParentEventIDs...)
	value.ArtifactRefs = append([]string(nil), value.ArtifactRefs...)
	value.Payload = append([]byte(nil), value.Payload...)
	value.Extensions = append([]byte(nil), value.Extensions...)
	return value
}
func cloneArtifact(value protocol.Artifact) protocol.Artifact {
	value.Metadata = append([]byte(nil), value.Metadata...)
	value.EncryptionMetadata = append([]byte(nil), value.EncryptionMetadata...)
	return value
}
