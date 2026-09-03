package httpapi

import (
	"errors"
	"net/http"

	"github.com/agent-continuity/agent-continuity-master/internal/auth"
	"github.com/agent-continuity/agent-continuity-master/internal/storage"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

func (s *Server) connectListTasks(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.connectAuthorize(w, r, "tasks:read")
	if !ok {
		return
	}
	var input struct {
		Limit int `json:"limit"`
	}
	if !s.decodeJSON(w, r, &input) {
		return
	}
	items, err := s.store.ListTasks(r.Context(), claims.AccountID, input.Limit)
	if err != nil {
		writeConnectError(w, err)
		return
	}
	visible := make([]protocol.Task, 0, len(items))
	for _, item := range items {
		allowed, err := s.taskAccessAllowed(r, claims, item.ID, protocol.PermissionRead)
		if err != nil {
			writeConnectError(w, err)
			return
		}
		if allowed {
			visible = append(visible, item)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": visible})
}

func (s *Server) connectGetTask(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.connectAuthorize(w, r, "tasks:read")
	if !ok {
		return
	}
	var input struct {
		TaskID string `json:"task_id"`
	}
	if !s.decodeJSON(w, r, &input) || input.TaskID == "" {
		return
	}
	allowed, err := s.taskAccessAllowed(r, claims, input.TaskID, protocol.PermissionRead)
	if err != nil || !allowed {
		writeConnectError(w, firstError(err, storage.ErrUnauthorized))
		return
	}
	value, err := s.store.GetTask(r.Context(), input.TaskID)
	if err != nil {
		writeConnectError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"task": value})
}

func (s *Server) connectAppendEvent(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.connectAuthorize(w, r, "tasks:write")
	if !ok {
		return
	}
	var input struct {
		TaskID string         `json:"task_id"`
		Event  protocol.Event `json:"event"`
	}
	if !s.decodeJSON(w, r, &input) || input.TaskID == "" {
		return
	}
	result, err := s.appendSignedProtocolEvent(r, claims, input.TaskID, input.Event)
	if err != nil {
		writeConnectError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) connectGetContinuityPackage(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.connectAuthorize(w, r, "tasks:read")
	if !ok {
		return
	}
	var input struct {
		TaskID      string `json:"task_id"`
		TargetAgent string `json:"target_agent"`
	}
	if !s.decodeJSON(w, r, &input) || input.TaskID == "" {
		return
	}
	value, err := s.continuityForAccount(r, claims, input.TaskID, input.TargetAgent)
	if err != nil {
		writeConnectError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"package": value})
}

func (s *Server) connectAuthorize(w http.ResponseWriter, r *http.Request, scope string) (auth.Claims, bool) {
	w.Header().Set("Connect-Protocol-Version", "1")
	claims, ok := s.authorize(w, r)
	if !ok {
		return auth.Claims{}, false
	}
	if !auth.HasScope(claims, scope) {
		writeConnectError(w, storage.ErrUnauthorized)
		return auth.Claims{}, false
	}
	return claims, true
}

func writeConnectError(w http.ResponseWriter, err error) {
	status, code := http.StatusInternalServerError, "internal"
	switch {
	case errors.Is(err, storage.ErrNotFound):
		status, code = http.StatusNotFound, "not_found"
	case errors.Is(err, storage.ErrUnauthorized), errors.Is(err, storage.ErrDeviceRevoked):
		status, code = http.StatusForbidden, "permission_denied"
	case errors.Is(err, storage.ErrAlreadyExists), errors.Is(err, storage.ErrSequenceReplay):
		status, code = http.StatusConflict, "already_exists"
	default:
		if err != nil {
			status, code = http.StatusBadRequest, "invalid_argument"
		}
	}
	writeJSON(w, status, map[string]string{"code": code, "message": err.Error()})
}
