package memory

import (
	"context"
	"sort"
	"time"

	"github.com/agent-continuity/agent-continuity-master/internal/storage"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

func (s *Store) CreateAgent(_ context.Context, value protocol.AgentProfile) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.accounts[value.AccountID]; !ok {
		return storage.ErrNotFound
	}
	if _, ok := s.agents[value.ID]; ok {
		return storage.ErrAlreadyExists
	}
	s.agents[value.ID] = cloneAgent(value)
	return nil
}

func (s *Store) GetAgent(_ context.Context, agentID string) (protocol.AgentProfile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.agents[agentID]
	if !ok {
		return protocol.AgentProfile{}, storage.ErrNotFound
	}
	return cloneAgent(value), nil
}

func (s *Store) ListAgents(_ context.Context, accountID string, limit int) ([]protocol.AgentProfile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	result := make([]protocol.AgentProfile, 0)
	for _, value := range s.agents {
		if value.AccountID == accountID {
			result = append(result, cloneAgent(value))
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.After(result[j].CreatedAt) })
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (s *Store) RevokeAgent(_ context.Context, accountID, agentID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.agents[agentID]
	if !ok {
		return storage.ErrNotFound
	}
	if value.AccountID != accountID {
		return storage.ErrUnauthorized
	}
	now := s.now().UTC()
	value.RevokedAt = &now
	s.agents[agentID] = value
	for id, participant := range s.participants {
		if participant.AgentID == agentID && participant.Status == "active" {
			participant.Status = "revoked"
			participant.LeftAt = &now
			s.participants[id] = participant
		}
	}
	return nil
}

func (s *Store) AddTaskParticipant(_ context.Context, value protocol.TaskParticipant) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tasks[value.TaskID]; !ok {
		return storage.ErrNotFound
	}
	for _, current := range s.participants {
		if current.TaskID == value.TaskID && ((value.AccountID != "" && current.AccountID == value.AccountID) || (value.AgentID != "" && current.AgentID == value.AgentID)) {
			return storage.ErrAlreadyExists
		}
	}
	s.participants[value.ID] = value
	return nil
}

func (s *Store) GetTaskParticipant(_ context.Context, taskID, participantID string) (protocol.TaskParticipant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.participants[participantID]
	if !ok || value.TaskID != taskID {
		return protocol.TaskParticipant{}, storage.ErrNotFound
	}
	return value, nil
}

func (s *Store) ListTaskParticipants(_ context.Context, taskID string) ([]protocol.TaskParticipant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.tasks[taskID]; !ok {
		return nil, storage.ErrNotFound
	}
	result := make([]protocol.TaskParticipant, 0)
	for _, value := range s.participants {
		if value.TaskID == taskID {
			result = append(result, value)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].JoinedAt.Before(result[j].JoinedAt) })
	return result, nil
}

func (s *Store) LeaveTaskParticipant(_ context.Context, taskID, participantID string, leftAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.participants[participantID]
	if !ok || value.TaskID != taskID {
		return storage.ErrNotFound
	}
	if value.Role == "owner" {
		return storage.ErrUnauthorized
	}
	when := leftAt.UTC()
	value.Status = "left"
	value.LeftAt = &when
	s.participants[participantID] = value
	if value.AccountID != "" {
		delete(s.permissions, permissionKey(taskID, value.AccountID))
		delete(s.permissionCreatedAt, permissionKey(taskID, value.AccountID))
	}
	return nil
}

func (s *Store) BindTaskAgent(_ context.Context, binding protocol.TaskAgentBinding, participant protocol.TaskParticipant) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	agent, ok := s.agents[binding.AgentID]
	if !ok || agent.RevokedAt != nil || agent.AccountID != participant.AccountID {
		return storage.ErrUnauthorized
	}
	if _, ok := s.tasks[binding.TaskID]; !ok {
		return storage.ErrNotFound
	}
	key := branchKey(binding.TaskID, binding.AgentID)
	if _, ok := s.agentBindings[key]; ok {
		return storage.ErrAlreadyExists
	}
	participant.TaskID = binding.TaskID
	participant.AgentID = binding.AgentID
	participant.AccountID = ""
	s.participants[participant.ID] = participant
	s.agentBindings[key] = cloneAgentBinding(binding)
	return nil
}

func (s *Store) ListTaskAgentBindings(_ context.Context, taskID string) ([]protocol.TaskAgentBinding, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]protocol.TaskAgentBinding, 0)
	for _, value := range s.agentBindings {
		if value.TaskID == taskID {
			result = append(result, cloneAgentBinding(value))
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.Before(result[j].CreatedAt) })
	return result, nil
}

func (s *Store) CreateNotification(_ context.Context, value protocol.Notification) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.accounts[value.AccountID]; !ok {
		return storage.ErrNotFound
	}
	if _, ok := s.notifications[value.ID]; ok {
		return storage.ErrAlreadyExists
	}
	value.Details = append([]byte(nil), value.Details...)
	s.notifications[value.ID] = value
	return nil
}

func (s *Store) ListNotifications(_ context.Context, accountID string, unreadOnly bool, limit int) ([]protocol.Notification, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	result := make([]protocol.Notification, 0)
	for _, value := range s.notifications {
		if value.AccountID == accountID && (!unreadOnly || value.ReadAt == nil) {
			value.Details = append([]byte(nil), value.Details...)
			result = append(result, value)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.After(result[j].CreatedAt) })
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (s *Store) MarkNotificationRead(_ context.Context, accountID, notificationID string, readAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.notifications[notificationID]
	if !ok || value.AccountID != accountID {
		return storage.ErrNotFound
	}
	when := readAt.UTC()
	value.ReadAt = &when
	s.notifications[notificationID] = value
	return nil
}

func cloneAgent(value protocol.AgentProfile) protocol.AgentProfile {
	value.Capabilities = append([]string(nil), value.Capabilities...)
	value.Card = append([]byte(nil), value.Card...)
	return value
}
func cloneAgentBinding(value protocol.TaskAgentBinding) protocol.TaskAgentBinding {
	value.Capabilities = append([]string(nil), value.Capabilities...)
	value.Metadata = append([]byte(nil), value.Metadata...)
	return value
}
