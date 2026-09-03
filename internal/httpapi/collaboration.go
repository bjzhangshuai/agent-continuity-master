package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/agent-continuity/agent-continuity-master/internal/auth"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

func (s *Server) createAgent(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeScope(w, r, "agents:write")
	if !ok {
		return
	}
	var input struct {
		Name         string          `json:"name"`
		Provider     string          `json:"provider"`
		Model        string          `json:"model"`
		Capabilities []string        `json:"capabilities"`
		Card         json.RawMessage `json:"card"`
	}
	if !s.decodeJSON(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	input.Provider = strings.TrimSpace(input.Provider)
	input.Capabilities = uniqueStrings(input.Capabilities)
	if input.Name == "" || len(input.Name) > 120 || input.Provider == "" || len(input.Provider) > 80 || len(input.Capabilities) > 100 {
		writeError(w, http.StatusBadRequest, "invalid_agent", "name, provider, and no more than 100 capabilities are required")
		return
	}
	id, _ := protocol.NewID("agent")
	value := protocol.AgentProfile{ID: id, AccountID: claims.AccountID, Name: input.Name, Provider: input.Provider, Model: input.Model, Capabilities: input.Capabilities, Card: input.Card, CreatedAt: time.Now().UTC()}
	if err := s.store.CreateAgent(r.Context(), value); err != nil {
		writeStorageError(w, err)
		return
	}
	s.audit(r, claims, "agent.created", "agent", value.ID, nil)
	writeJSON(w, http.StatusCreated, value)
}

func (s *Server) listAgents(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeScope(w, r, "agents:read")
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := s.store.ListAgents(r.Context(), claims.AccountID, limit)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": items})
}

func (s *Server) revokeAgent(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeScope(w, r, "agents:write")
	if !ok {
		return
	}
	if err := s.store.RevokeAgent(r.Context(), claims.AccountID, r.PathValue("agentID")); err != nil {
		writeStorageError(w, err)
		return
	}
	s.audit(r, claims, "agent.revoked", "agent", r.PathValue("agentID"), nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) bindTaskAgent(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeTask(w, r, protocol.PermissionAdmin)
	if !ok {
		return
	}
	var input struct {
		AgentID          string          `json:"agent_id"`
		Role             string          `json:"role"`
		NativeSessionRef string          `json:"native_session_ref"`
		ResumeMode       string          `json:"resume_mode"`
		Capabilities     []string        `json:"capabilities"`
		Metadata         json.RawMessage `json:"metadata"`
	}
	if !s.decodeJSON(w, r, &input) {
		return
	}
	agent, err := s.store.GetAgent(r.Context(), input.AgentID)
	if err != nil || agent.AccountID != claims.AccountID || agent.RevokedAt != nil {
		writeError(w, http.StatusForbidden, "agent_forbidden", "agent must be active and owned by the caller")
		return
	}
	if input.Role == "" {
		input.Role = "contributor"
	}
	if input.Role != "contributor" && input.Role != "reviewer" {
		writeError(w, http.StatusBadRequest, "invalid_agent_role", "agent role must be contributor or reviewer")
		return
	}
	if input.ResumeMode != "native" && input.ResumeMode != "rehydrate" && input.ResumeMode != "unsupported" {
		writeError(w, http.StatusBadRequest, "invalid_resume_mode", "resume_mode must be native, rehydrate, or unsupported")
		return
	}
	input.Capabilities = uniqueStrings(input.Capabilities)
	if !subset(input.Capabilities, agent.Capabilities) {
		writeError(w, http.StatusBadRequest, "invalid_capabilities", "binding capabilities must be a subset of the agent profile")
		return
	}
	participantID, _ := protocol.NewID("participant")
	now := time.Now().UTC()
	participant := protocol.TaskParticipant{ID: participantID, TaskID: r.PathValue("taskID"), AccountID: claims.AccountID, AgentID: agent.ID, Kind: "agent", Role: input.Role, Status: "active", JoinedAt: now}
	binding := protocol.TaskAgentBinding{TaskID: participant.TaskID, AgentID: agent.ID, ParticipantID: participant.ID, NativeSessionRef: input.NativeSessionRef, ResumeMode: input.ResumeMode, Capabilities: input.Capabilities, Metadata: input.Metadata, CreatedAt: now}
	if err := s.store.BindTaskAgent(r.Context(), binding, participant); err != nil {
		writeStorageError(w, err)
		return
	}
	s.audit(r, claims, "task.agent.bound", "agent", agent.ID, mustJSON(map[string]string{"task_id": binding.TaskID, "participant_id": participant.ID, "resume_mode": binding.ResumeMode}))
	s.publish(protocol.RealtimeMessage{Type: "participant.joined", TaskID: binding.TaskID, ResourceID: participant.ID, SentAt: now})
	writeJSON(w, http.StatusCreated, map[string]any{"participant": participant, "binding": binding})
}

func (s *Server) listTaskAgents(w http.ResponseWriter, r *http.Request) {
	_, ok := s.authorizeTask(w, r, protocol.PermissionRead)
	if !ok {
		return
	}
	items, err := s.store.ListTaskAgentBindings(r.Context(), r.PathValue("taskID"))
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agent_bindings": items})
}

func (s *Server) listParticipants(w http.ResponseWriter, r *http.Request) {
	_, ok := s.authorizeTask(w, r, protocol.PermissionRead)
	if !ok {
		return
	}
	items, err := s.store.ListTaskParticipants(r.Context(), r.PathValue("taskID"))
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"participants": items})
}

func (s *Server) leaveParticipant(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeTask(w, r, protocol.PermissionRead)
	if !ok {
		return
	}
	participant, err := s.store.GetTaskParticipant(r.Context(), r.PathValue("taskID"), r.PathValue("participantID"))
	if err != nil {
		writeStorageError(w, err)
		return
	}
	self := participant.AccountID == claims.AccountID && participant.Kind == "human"
	admin := s.store.CheckPermission(r.Context(), participant.TaskID, claims.AccountID, protocol.PermissionAdmin) == nil
	if !self && !admin {
		writeError(w, http.StatusForbidden, "forbidden", "only the participant or a task administrator can remove this participant")
		return
	}
	if err := s.store.LeaveTaskParticipant(r.Context(), participant.TaskID, participant.ID, time.Now()); err != nil {
		writeStorageError(w, err)
		return
	}
	s.audit(r, claims, "task.participant.left", "participant", participant.ID, mustJSON(map[string]string{"task_id": participant.TaskID}))
	s.publish(protocol.RealtimeMessage{Type: "participant.left", TaskID: participant.TaskID, ResourceID: participant.ID, SentAt: time.Now().UTC()})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listNotifications(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeScope(w, r, "notifications:read")
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := s.store.ListNotifications(r.Context(), claims.AccountID, r.URL.Query().Get("unread_only") == "true", limit)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"notifications": items})
}

func (s *Server) markNotificationRead(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeScope(w, r, "notifications:read")
	if !ok {
		return
	}
	if err := s.store.MarkNotificationRead(r.Context(), claims.AccountID, r.PathValue("notificationID"), time.Now()); err != nil {
		writeStorageError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) createMentionNotifications(r *http.Request, claims auth.Claims, event protocol.Event) {
	if event.ContentType != "application/json" {
		return
	}
	var payload struct {
		Mentions []string `json:"mentions"`
	}
	if json.Unmarshal(event.Payload, &payload) != nil {
		return
	}
	for _, participantID := range uniqueStrings(payload.Mentions) {
		participant, err := s.store.GetTaskParticipant(r.Context(), event.TaskID, participantID)
		if err != nil || participant.AccountID == "" || participant.AccountID == claims.AccountID || participant.Status != "active" {
			continue
		}
		id, _ := protocol.NewID("notification")
		value := protocol.Notification{ID: id, AccountID: participant.AccountID, TaskID: event.TaskID, Kind: "mention", ActorID: event.ParticipantID, Subject: "You were mentioned in a task", Details: mustJSON(map[string]string{"event_id": event.EventID, "participant_id": participantID}), CreatedAt: time.Now().UTC()}
		if err := s.store.CreateNotification(r.Context(), value); err != nil {
			s.logger.Error("create mention notification", "error", err, "event_id", event.EventID)
		}
	}
}

func subset(values, allowed []string) bool {
	set := map[string]struct{}{}
	for _, value := range allowed {
		set[value] = struct{}{}
	}
	for _, value := range values {
		if _, ok := set[value]; !ok {
			return false
		}
	}
	return true
}
