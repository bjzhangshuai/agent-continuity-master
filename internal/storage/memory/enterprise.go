package memory

import (
	"context"
	"sort"

	"github.com/agent-continuity/agent-continuity-master/internal/storage"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

func externalIdentityKey(provider, subject string) string { return provider + "\x00" + subject }

func (s *Store) PutExternalIdentity(_ context.Context, value protocol.ExternalIdentity) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.accounts[value.AccountID]; !ok {
		return storage.ErrNotFound
	}
	key := externalIdentityKey(value.Provider, value.Subject)
	if existing, ok := s.externalIdentities[key]; ok && (existing.ID != value.ID || existing.AccountID != value.AccountID) {
		return storage.ErrAlreadyExists
	}
	if keyByID, ok := s.externalIdentityIDs[value.ID]; ok && keyByID != key {
		return storage.ErrAlreadyExists
	}
	s.externalIdentities[key] = value
	s.externalIdentityIDs[value.ID] = key
	return nil
}

func (s *Store) GetExternalIdentity(_ context.Context, provider, subject string) (protocol.ExternalIdentity, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.externalIdentities[externalIdentityKey(provider, subject)]
	if !ok {
		return protocol.ExternalIdentity{}, storage.ErrNotFound
	}
	return value, nil
}

func (s *Store) GetExternalIdentityByID(_ context.Context, id string) (protocol.ExternalIdentity, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	key, ok := s.externalIdentityIDs[id]
	if !ok {
		return protocol.ExternalIdentity{}, storage.ErrNotFound
	}
	return s.externalIdentities[key], nil
}

func (s *Store) ListExternalIdentities(_ context.Context, provider string, limit int) ([]protocol.ExternalIdentity, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	result := make([]protocol.ExternalIdentity, 0)
	for _, value := range s.externalIdentities {
		if value.Provider == provider {
			result = append(result, value)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}
