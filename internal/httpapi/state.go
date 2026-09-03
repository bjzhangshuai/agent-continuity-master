package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/agent-continuity/agent-continuity-master/internal/reconcile"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

func (s *Server) createBranch(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeTask(w, r, protocol.PermissionWrite)
	if !ok {
		return
	}
	var input struct {
		BranchID    string `json:"branch_id"`
		Name        string `json:"name"`
		BaseEventID string `json:"base_event_id"`
	}
	if !s.decodeJSON(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Name) > 120 {
		writeError(w, http.StatusBadRequest, "invalid_branch_name", "branch name must contain 1..120 characters")
		return
	}
	if input.BranchID == "" {
		input.BranchID, _ = protocol.NewID("branch")
	}
	if input.BranchID == "main" || len(input.BranchID) > 120 || strings.ContainsAny(input.BranchID, "/\\") {
		writeError(w, http.StatusBadRequest, "invalid_branch_id", "branch ID is reserved or invalid")
		return
	}
	value := protocol.TaskBranch{TaskID: r.PathValue("taskID"), ID: input.BranchID, Name: input.Name, BaseEventID: input.BaseEventID, HeadEventIDs: []string{}, Status: "active", CreatedBy: claims.AccountID, CreatedAt: time.Now().UTC()}
	if err := s.store.CreateBranch(r.Context(), value); err != nil {
		writeStorageError(w, err)
		return
	}
	value, _ = s.store.GetBranch(r.Context(), value.TaskID, value.ID)
	s.audit(r, claims, "task.branch.created", "branch", value.ID, mustJSON(map[string]string{"task_id": value.TaskID, "base_event_id": value.BaseEventID}))
	writeJSON(w, http.StatusCreated, value)
}

func (s *Server) listBranches(w http.ResponseWriter, r *http.Request) {
	_, ok := s.authorizeTask(w, r, protocol.PermissionRead)
	if !ok {
		return
	}
	items, err := s.store.ListBranches(r.Context(), r.PathValue("taskID"))
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"branches": items})
}

func (s *Server) listBranchEvents(w http.ResponseWriter, r *http.Request) {
	_, ok := s.authorizeTask(w, r, protocol.PermissionRead)
	if !ok {
		return
	}
	after, _ := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	include := r.URL.Query().Get("include_payload") != "false"
	items, err := s.store.ListBranchEvents(r.Context(), r.PathValue("taskID"), r.PathValue("branchID"), after, limit, include)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": items, "next_cursor": eventCursor(items, after)})
}

func (s *Server) mergeBranch(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeTask(w, r, protocol.PermissionWrite)
	if !ok {
		return
	}
	var input struct {
		TargetBranchID string          `json:"target_branch_id"`
		Decision       json.RawMessage `json:"decision"`
		MergeEvent     protocol.Event  `json:"merge_event"`
	}
	if !s.decodeJSON(w, r, &input) {
		return
	}
	source, err := s.store.GetBranch(r.Context(), r.PathValue("taskID"), r.PathValue("branchID"))
	if err != nil {
		writeStorageError(w, err)
		return
	}
	target, err := s.store.GetBranch(r.Context(), source.TaskID, input.TargetBranchID)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	if source.Status != "active" || target.Status != "active" || source.ID == target.ID {
		writeError(w, http.StatusConflict, "invalid_branch_merge", "source and target must be distinct active branches")
		return
	}
	event := input.MergeEvent
	if event.TaskID != source.TaskID || event.BranchID != target.ID || event.AccountID != claims.AccountID || event.Type != "task.merged" {
		writeError(w, http.StatusBadRequest, "invalid_merge_event", "merge event identity, target branch, and event type must match the merge")
		return
	}
	expectedParents := uniqueStrings(append(append([]string(nil), source.HeadEventIDs...), target.HeadEventIDs...))
	actualParents := uniqueStrings(event.ParentEventIDs)
	if !equalStrings(expectedParents, actualParents) {
		writeError(w, http.StatusConflict, "stale_branch_heads", "merge event parents must exactly match current source and target heads")
		return
	}
	if len(input.Decision) == 0 || !json.Valid(input.Decision) || !jsonSemanticallyEqual(input.Decision, event.Payload) {
		writeError(w, http.StatusBadRequest, "invalid_merge_decision", "decision must be valid JSON and equal the signed merge event payload")
		return
	}
	device, err := s.store.GetDevice(r.Context(), event.DeviceID)
	if err != nil || device.AccountID != claims.AccountID || device.RevokedAt != nil {
		writeError(w, http.StatusForbidden, "device_forbidden", "merge event device is not active for this account")
		return
	}
	if err := protocol.ValidateEvent(event, time.Now()); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_merge_event", err.Error())
		return
	}
	if err := protocol.VerifyEventSignature(event, device.PublicKey); err != nil {
		writeError(w, http.StatusUnauthorized, "invalid_event_signature", err.Error())
		return
	}
	result, err := s.store.AppendEvent(r.Context(), event)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	if err := s.store.MarkBranchMerged(r.Context(), source.TaskID, source.ID, target.ID, time.Now()); err != nil {
		writeStorageError(w, err)
		return
	}
	s.audit(r, claims, "task.branch.merged", "branch", source.ID, mustJSON(map[string]any{"task_id": source.TaskID, "target_branch_id": target.ID, "event_id": event.EventID}))
	s.publish(protocol.RealtimeMessage{Type: "branch.merged", TaskID: source.TaskID, ServerSequence: result.Event.ServerSequence, ResourceID: source.ID, SentAt: time.Now().UTC()})
	writeJSON(w, http.StatusOK, map[string]any{"append_result": result, "source_branch_id": source.ID, "target_branch_id": target.ID})
}

func (s *Server) listConflicts(w http.ResponseWriter, r *http.Request) {
	_, ok := s.authorizeTask(w, r, protocol.PermissionRead)
	if !ok {
		return
	}
	includeResolved := r.URL.Query().Get("include_resolved") == "true"
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := s.store.ListConflicts(r.Context(), r.PathValue("taskID"), includeResolved, limit)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"conflicts": items})
}

func (s *Server) resolveConflict(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeTask(w, r, protocol.PermissionWrite)
	if !ok {
		return
	}
	var input struct {
		Strategy          string `json:"strategy"`
		ResolutionEventID string `json:"resolution_event_id"`
		Notes             string `json:"notes"`
	}
	if !s.decodeJSON(w, r, &input) {
		return
	}
	allowed := map[string]bool{"auto_merge": true, "deterministic_merge": true, "create_branch": true, "keep_source": true, "keep_target": true, "manual": true, "ask_review_agent": true}
	if !allowed[input.Strategy] || len(input.Notes) > 2000 {
		writeError(w, http.StatusBadRequest, "invalid_resolution", "unsupported strategy or notes exceed 2000 characters")
		return
	}
	id, _ := protocol.NewID("resolution")
	value := protocol.ConflictResolution{ID: id, ConflictID: r.PathValue("conflictID"), TaskID: r.PathValue("taskID"), AccountID: claims.AccountID, Strategy: input.Strategy, ResolutionEventID: input.ResolutionEventID, Notes: input.Notes, CreatedAt: time.Now().UTC()}
	if err := s.store.ResolveConflict(r.Context(), value); err != nil {
		writeStorageError(w, err)
		return
	}
	s.audit(r, claims, "conflict.resolved", "conflict", value.ConflictID, mustJSON(value))
	s.publish(protocol.RealtimeMessage{Type: "conflict.resolved", TaskID: value.TaskID, ResourceID: value.ConflictID, SentAt: value.CreatedAt})
	writeJSON(w, http.StatusOK, value)
}

func (s *Server) rebuildSnapshot(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeTask(w, r, protocol.PermissionWrite)
	if !ok {
		return
	}
	task, err := s.store.GetTask(r.Context(), r.PathValue("taskID"))
	if err != nil {
		writeStorageError(w, err)
		return
	}
	branch, err := s.store.GetBranch(r.Context(), task.ID, r.PathValue("branchID"))
	if err != nil {
		writeStorageError(w, err)
		return
	}
	events, err := s.allEvents(r, task.ID)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	conflicts, err := s.store.ListConflicts(r.Context(), task.ID, false, 500)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	snapshot, err := reconcile.BuildSnapshot(task, branch, events, conflicts, time.Now())
	if err != nil {
		writeError(w, http.StatusConflict, "snapshot_rebuild_failed", err.Error())
		return
	}
	if err := s.store.PutSnapshot(r.Context(), snapshot); err != nil {
		writeStorageError(w, err)
		return
	}
	s.audit(r, claims, "task.snapshot.rebuilt", "snapshot", snapshot.ID, mustJSON(map[string]any{"task_id": task.ID, "branch_id": branch.ID, "event_count": snapshot.EventCount}))
	writeJSON(w, http.StatusCreated, snapshot)
}

func (s *Server) latestSnapshot(w http.ResponseWriter, r *http.Request) {
	_, ok := s.authorizeTask(w, r, protocol.PermissionRead)
	if !ok {
		return
	}
	value, err := s.store.LatestSnapshot(r.Context(), r.PathValue("taskID"), r.PathValue("branchID"))
	if err != nil {
		writeStorageError(w, err)
		return
	}
	if err := reconcile.VerifySnapshot(value); err != nil {
		writeError(w, http.StatusInternalServerError, "snapshot_corrupt", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func (s *Server) allEvents(r *http.Request, taskID string) ([]protocol.Event, error) {
	result := make([]protocol.Event, 0)
	var cursor uint64
	for {
		items, err := s.store.ListEvents(r.Context(), taskID, cursor, 1000, true)
		if err != nil {
			return nil, err
		}
		result = append(result, items...)
		if len(items) < 1000 {
			return result, nil
		}
		cursor = items[len(items)-1].ServerSequence
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	a = append([]string(nil), a...)
	b = append([]string(nil), b...)
	sort.Strings(a)
	sort.Strings(b)
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func jsonSemanticallyEqual(a, b []byte) bool {
	var av, bv any
	return json.Unmarshal(a, &av) == nil && json.Unmarshal(b, &bv) == nil && bytes.Equal(mustCanonicalJSON(av), mustCanonicalJSON(bv))
}

func mustCanonicalJSON(value any) []byte {
	data, _ := json.Marshal(value)
	return data
}
