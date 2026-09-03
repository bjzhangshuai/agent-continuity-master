package memory

import (
	"context"
	"sort"
	"time"

	"github.com/agent-continuity/agent-continuity-master/internal/storage"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

func (s *Store) GetEvent(_ context.Context, taskID, eventID string) (protocol.Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.eventIDs[eventID]
	if !ok || value.TaskID != taskID {
		return protocol.Event{}, storage.ErrNotFound
	}
	return cloneEvent(value), nil
}

func (s *Store) ListBranchEvents(_ context.Context, taskID, branchID string, after uint64, limit int, includePayload bool) ([]protocol.Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.branches[branchKey(taskID, branchID)]; !ok {
		return nil, storage.ErrNotFound
	}
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	result := make([]protocol.Event, 0, limit)
	for _, event := range s.events[taskID] {
		if event.BranchID != branchID || event.ServerSequence <= after {
			continue
		}
		value := cloneEvent(event)
		if !includePayload {
			value.Payload = nil
		}
		result = append(result, value)
		if len(result) == limit {
			break
		}
	}
	return result, nil
}

func (s *Store) CreateBranch(_ context.Context, value protocol.TaskBranch) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tasks[value.TaskID]; !ok {
		return storage.ErrNotFound
	}
	if _, ok := s.permissions[permissionKey(value.TaskID, value.CreatedBy)]; !ok {
		return storage.ErrUnauthorized
	}
	key := branchKey(value.TaskID, value.ID)
	if _, ok := s.branches[key]; ok {
		return storage.ErrAlreadyExists
	}
	if value.BaseEventID != "" {
		base, ok := s.eventIDs[value.BaseEventID]
		if !ok || base.TaskID != value.TaskID {
			return storage.ErrNotFound
		}
		value.HeadEventIDs = []string{value.BaseEventID}
	} else {
		value.HeadEventIDs = []string{}
	}
	s.branches[key] = cloneBranch(value)
	return nil
}

func (s *Store) GetBranch(_ context.Context, taskID, branchID string) (protocol.TaskBranch, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.branches[branchKey(taskID, branchID)]
	if !ok {
		return protocol.TaskBranch{}, storage.ErrNotFound
	}
	return cloneBranch(value), nil
}

func (s *Store) ListBranches(_ context.Context, taskID string) ([]protocol.TaskBranch, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.tasks[taskID]; !ok {
		return nil, storage.ErrNotFound
	}
	result := make([]protocol.TaskBranch, 0)
	for _, value := range s.branches {
		if value.TaskID == taskID {
			result = append(result, cloneBranch(value))
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.Before(result[j].CreatedAt) })
	return result, nil
}

func (s *Store) MarkBranchMerged(_ context.Context, taskID, sourceBranchID, targetBranchID string, mergedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	source, ok := s.branches[branchKey(taskID, sourceBranchID)]
	if !ok {
		return storage.ErrNotFound
	}
	target, ok := s.branches[branchKey(taskID, targetBranchID)]
	if !ok || target.Status != "active" || source.Status != "active" || sourceBranchID == targetBranchID {
		return storage.ErrUnauthorized
	}
	source.Status = "merged"
	source.MergedInto = targetBranchID
	value := mergedAt.UTC()
	source.MergedAt = &value
	s.branches[branchKey(taskID, sourceBranchID)] = source
	return nil
}

func (s *Store) ListConflicts(_ context.Context, taskID string, includeResolved bool, limit int) ([]protocol.Conflict, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.tasks[taskID]; !ok {
		return nil, storage.ErrNotFound
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	result := make([]protocol.Conflict, 0)
	for _, value := range s.conflicts {
		if value.TaskID == taskID && (includeResolved || !value.Resolved) {
			result = append(result, value)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.Before(result[j].CreatedAt) })
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (s *Store) ResolveConflict(_ context.Context, value protocol.ConflictResolution) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	conflict, ok := s.conflicts[value.ConflictID]
	if !ok || conflict.TaskID != value.TaskID {
		return storage.ErrNotFound
	}
	if conflict.Resolved {
		return storage.ErrAlreadyExists
	}
	if permissionRank(s.permissions[permissionKey(value.TaskID, value.AccountID)]) < permissionRank(protocol.PermissionWrite) {
		return storage.ErrUnauthorized
	}
	if value.ResolutionEventID != "" {
		event, ok := s.eventIDs[value.ResolutionEventID]
		if !ok || event.TaskID != value.TaskID {
			return storage.ErrNotFound
		}
	}
	conflict.Resolved = true
	s.conflicts[conflict.ID] = conflict
	s.resolutions[value.ID] = value
	return nil
}

func (s *Store) PutSnapshot(_ context.Context, value protocol.TaskSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := branchKey(value.TaskID, value.BranchID)
	if _, ok := s.branches[key]; !ok {
		return storage.ErrNotFound
	}
	for _, existing := range s.snapshots[key] {
		if existing.ThroughServerSequence == value.ThroughServerSequence {
			if existing.StateDigest == value.StateDigest {
				return nil
			}
			return storage.ErrAlreadyExists
		}
	}
	s.snapshots[key] = append(s.snapshots[key], cloneSnapshot(value))
	return nil
}

func (s *Store) LatestSnapshot(_ context.Context, taskID, branchID string) (protocol.TaskSnapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := s.snapshots[branchKey(taskID, branchID)]
	if len(items) == 0 {
		return protocol.TaskSnapshot{}, storage.ErrNotFound
	}
	latest := items[0]
	for _, value := range items[1:] {
		if value.ThroughServerSequence > latest.ThroughServerSequence {
			latest = value
		}
	}
	return cloneSnapshot(latest), nil
}

func classifyConcurrent(incoming protocol.Event, headIDs []string, events map[string]protocol.Event) (string, string, bool) {
	for _, headID := range headIDs {
		head := events[headID]
		if permissionEvent(incoming.Type) && permissionEvent(head.Type) {
			return "permission_conflict", "ask_user", false
		}
		if artifactConflict(incoming, head) {
			if incoming.Type == "artifact.deleted" || head.Type == "artifact.deleted" {
				return "delete_vs_update_conflict", "ask_user", false
			}
			return "artifact_version_conflict", "create_branch", false
		}
		if semanticEvent(incoming.Type) && semanticEvent(head.Type) {
			return "semantic_conflict", "ask_user", false
		}
	}
	return "non_conflicting_concurrent", "deterministic_merge", true
}

func permissionEvent(value string) bool {
	return value == "permission.granted" || value == "permission.revoked"
}
func semanticEvent(value string) bool {
	return value == "task.metadata.updated" || value == "progress.updated" || value == "task.completed" || value == "task.paused" || value == "task.resumed"
}
func artifactConflict(a, b protocol.Event) bool {
	if len(a.ArtifactRefs) == 0 || len(b.ArtifactRefs) == 0 {
		return false
	}
	if a.Type != "artifact.created" && a.Type != "artifact.version.created" && a.Type != "artifact.deleted" {
		return false
	}
	if b.Type != "artifact.created" && b.Type != "artifact.version.created" && b.Type != "artifact.deleted" {
		return false
	}
	return intersects(a.ArtifactRefs, b.ArtifactRefs)
}

func branchKey(taskID, branchID string) string { return taskID + "\x00" + branchID }
func cloneBranch(value protocol.TaskBranch) protocol.TaskBranch {
	value.HeadEventIDs = append([]string(nil), value.HeadEventIDs...)
	return value
}
func cloneSnapshot(value protocol.TaskSnapshot) protocol.TaskSnapshot {
	value.State = cloneSnapshotState(value.State)
	return value
}
func cloneSnapshotState(value protocol.TaskSnapshotState) protocol.TaskSnapshotState {
	value.Task = cloneTask(value.Task)
	value.EventIDs = append([]string(nil), value.EventIDs...)
	value.HeadEventIDs = append([]string(nil), value.HeadEventIDs...)
	value.Progress = append([]byte(nil), value.Progress...)
	value.ArtifactRefs = append([]string(nil), value.ArtifactRefs...)
	value.UnresolvedIssues = append([]string(nil), value.UnresolvedIssues...)
	value.Decisions = append([]protocol.SnapshotDecision(nil), value.Decisions...)
	for i := range value.Decisions {
		value.Decisions[i].Value = append([]byte(nil), value.Decisions[i].Value...)
	}
	return value
}
