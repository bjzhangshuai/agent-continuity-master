package memory

import (
	"context"
	"sort"
	"time"

	"github.com/agent-continuity/agent-continuity-master/internal/storage"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

func (s *Store) CreateAuthSession(_ context.Context, value protocol.AuthSession) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.accounts[value.AccountID]; !ok {
		return storage.ErrNotFound
	}
	if _, ok := s.authSessions[value.ID]; ok {
		return storage.ErrAlreadyExists
	}
	if _, ok := s.sessionHashes[value.RefreshTokenHash]; ok {
		return storage.ErrAlreadyExists
	}
	s.authSessions[value.ID] = value
	s.sessionHashes[value.RefreshTokenHash] = value.ID
	return nil
}

func (s *Store) RotateAuthSession(_ context.Context, oldHash string, replacement protocol.AuthSession) (protocol.AuthSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.sessionHashes[oldHash]
	if !ok {
		return protocol.AuthSession{}, storage.ErrRefreshInvalid
	}
	old := s.authSessions[id]
	if old.UsedAt != nil {
		now := s.now().UTC()
		for sessionID, session := range s.authSessions {
			if session.FamilyID == old.FamilyID {
				session.RevokedAt = &now
				s.authSessions[sessionID] = session
			}
		}
		return protocol.AuthSession{}, storage.ErrTokenReuse
	}
	if old.RevokedAt != nil || !s.now().Before(old.ExpiresAt) {
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
	if _, exists := s.sessionHashes[replacement.RefreshTokenHash]; exists {
		return protocol.AuthSession{}, storage.ErrAlreadyExists
	}
	now := s.now().UTC()
	old.UsedAt = &now
	old.ReplacedBy = replacement.ID
	s.authSessions[old.ID] = old
	s.authSessions[replacement.ID] = replacement
	s.sessionHashes[replacement.RefreshTokenHash] = replacement.ID
	return replacement, nil
}

func (s *Store) RevokeAuthSession(_ context.Context, accountID, refreshHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.sessionHashes[refreshHash]
	if !ok {
		return storage.ErrNotFound
	}
	session := s.authSessions[id]
	if session.AccountID != accountID {
		return storage.ErrUnauthorized
	}
	now := s.now().UTC()
	for sessionID, candidate := range s.authSessions {
		if candidate.FamilyID == session.FamilyID {
			candidate.RevokedAt = &now
			s.authSessions[sessionID] = candidate
		}
	}
	return nil
}

func (s *Store) CreateAPIKey(_ context.Context, value protocol.APIKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.accounts[value.AccountID]; !ok {
		return storage.ErrNotFound
	}
	if _, ok := s.apiKeys[value.ID]; ok {
		return storage.ErrAlreadyExists
	}
	if _, ok := s.apiKeyHashes[value.SecretHash]; ok {
		return storage.ErrAlreadyExists
	}
	s.apiKeys[value.ID] = cloneAPIKey(value)
	s.apiKeyHashes[value.SecretHash] = value.ID
	return nil
}

func (s *Store) ListAPIKeys(_ context.Context, accountID string) ([]protocol.APIKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]protocol.APIKey, 0)
	for _, value := range s.apiKeys {
		if value.AccountID == accountID {
			result = append(result, cloneAPIKey(value))
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.After(result[j].CreatedAt) })
	return result, nil
}

func (s *Store) AuthenticateAPIKey(_ context.Context, secretHash string) (protocol.APIKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.apiKeyHashes[secretHash]
	if !ok {
		return protocol.APIKey{}, storage.ErrUnauthorized
	}
	value := s.apiKeys[id]
	if value.RevokedAt != nil || (value.ExpiresAt != nil && !s.now().Before(*value.ExpiresAt)) {
		return protocol.APIKey{}, storage.ErrUnauthorized
	}
	now := s.now().UTC()
	value.LastUsedAt = &now
	s.apiKeys[id] = value
	return cloneAPIKey(value), nil
}

func (s *Store) RevokeAPIKey(_ context.Context, accountID, keyID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.apiKeys[keyID]
	if !ok {
		return storage.ErrNotFound
	}
	if value.AccountID != accountID {
		return storage.ErrUnauthorized
	}
	now := s.now().UTC()
	value.RevokedAt = &now
	s.apiKeys[keyID] = value
	return nil
}

func (s *Store) RecordAuditEvent(_ context.Context, value protocol.AuditEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.accounts[value.AccountID]; !ok {
		return storage.ErrNotFound
	}
	value.Details = append([]byte(nil), value.Details...)
	s.auditEvents = append(s.auditEvents, value)
	return nil
}

func (s *Store) ListAuditEvents(_ context.Context, accountID string, after time.Time, limit int) ([]protocol.AuditEvent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	result := make([]protocol.AuditEvent, 0, limit)
	for i := len(s.auditEvents) - 1; i >= 0 && len(result) < limit; i-- {
		value := s.auditEvents[i]
		if value.AccountID == accountID && (after.IsZero() || value.CreatedAt.After(after)) {
			value.Details = append([]byte(nil), value.Details...)
			result = append(result, value)
		}
	}
	return result, nil
}

func (s *Store) PutTaskKeySet(_ context.Context, value protocol.TaskKeySet) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tasks[value.TaskID]; !ok {
		return storage.ErrNotFound
	}
	device, ok := s.devices[value.CreatedBy]
	if !ok || device.RevokedAt != nil {
		return storage.ErrUnauthorized
	}
	versions := s.taskKeySets[value.TaskID]
	if versions == nil {
		versions = map[uint32]protocol.TaskKeySet{}
		s.taskKeySets[value.TaskID] = versions
	}
	if _, ok := versions[value.Version]; ok {
		return storage.ErrAlreadyExists
	}
	var latest uint32
	for version := range versions {
		if version > latest {
			latest = version
		}
	}
	if value.Version != latest+1 {
		return storage.ErrSequenceReplay
	}
	versions[value.Version] = cloneTaskKeySet(value)
	return nil
}

func (s *Store) GetTaskKeySet(_ context.Context, taskID string, version uint32) (protocol.TaskKeySet, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.taskKeySets[taskID][version]
	if !ok {
		return protocol.TaskKeySet{}, storage.ErrNotFound
	}
	return cloneTaskKeySet(value), nil
}

func (s *Store) LatestTaskKeySet(_ context.Context, taskID string) (protocol.TaskKeySet, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	versions := s.taskKeySets[taskID]
	var latest uint32
	for version := range versions {
		if version > latest {
			latest = version
		}
	}
	if latest == 0 {
		return protocol.TaskKeySet{}, storage.ErrNotFound
	}
	return cloneTaskKeySet(versions[latest]), nil
}

func cloneAPIKey(value protocol.APIKey) protocol.APIKey {
	value.Scopes = append([]string(nil), value.Scopes...)
	return value
}

func cloneTaskKeySet(value protocol.TaskKeySet) protocol.TaskKeySet {
	value.WrappedKeys = append([]protocol.WrappedTaskKey(nil), value.WrappedKeys...)
	return value
}
