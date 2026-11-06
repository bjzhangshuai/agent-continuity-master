package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/agent-continuity/agent-continuity-master/internal/auth"
	"github.com/agent-continuity/agent-continuity-master/internal/reconcile"
	"github.com/agent-continuity/agent-continuity-master/internal/storage"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (s *Server) agentCard(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"name":                    "Agent Continuity",
		"description":             "Legacy Continuity JSON-RPC gateway; custom signed-event parameters, not a conformant A2A server. See /.well-known/agent-continuity.json.",
		"protocolVersion":         "0.3.0",
		"version":                 protocol.ProtocolVersion,
		"url":                     "/v1/a2a",
		"preferredTransport":      "JSONRPC",
		"capabilities":            map[string]bool{"streaming": false, "pushNotifications": false, "stateTransitionHistory": false},
		"continuityCompatibility": map[string]any{"status": "legacy-custom-gateway", "a2aConformant": false},
		"securitySchemes": map[string]any{
			"oauth2": map[string]any{"type": "oauth2", "flows": map[string]any{"clientCredentials": map[string]any{"tokenUrl": "/v1/oauth/token", "scopes": map[string]string{"tasks:read": "Read continuity tasks", "tasks:write": "Append signed task events"}}}},
		},
		"security": []map[string][]string{{"oauth2": {"tasks:read"}}},
		"skills": []map[string]any{
			{"id": "continuity.read", "name": "Read task continuity", "description": "Retrieve a task, causal events, and artifact manifests.", "tags": []string{"continuity", "handoff"}},
			{"id": "continuity.append", "name": "Append signed progress", "description": "Append a device-signed universal event.", "tags": []string{"events", "sync"}},
		},
	})
}

func (s *Server) a2a(w http.ResponseWriter, r *http.Request) {
	var request rpcRequest
	if !s.decodeJSON(w, r, &request) {
		return
	}
	if request.JSONRPC != "2.0" || len(request.ID) == 0 {
		writeJSON(w, http.StatusBadRequest, rpcFailure(request.ID, -32600, "invalid JSON-RPC request", nil))
		return
	}
	claims, ok := s.authorize(w, r)
	if !ok {
		return
	}
	switch request.Method {
	case "tasks/get":
		if !auth.HasScope(claims, "tasks:read") {
			writeJSON(w, http.StatusForbidden, rpcFailure(request.ID, -32001, "tasks:read scope required", nil))
			return
		}
		var params struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(request.Params, &params) != nil || params.ID == "" {
			writeJSON(w, http.StatusBadRequest, rpcFailure(request.ID, -32602, "task id is required", nil))
			return
		}
		value, err := s.continuityForAccount(r, claims, params.ID, "a2a")
		if err != nil {
			writeJSON(w, rpcHTTPStatus(err), rpcFailure(request.ID, -32004, "task unavailable", nil))
			return
		}
		writeJSON(w, http.StatusOK, rpcSuccess(request.ID, map[string]any{"id": params.ID, "status": map[string]any{"state": value.Task.Status}, "artifacts": []any{}, "metadata": map[string]any{"continuity_package": value}}))
	case "message/send":
		if !auth.HasScope(claims, "tasks:write") {
			writeJSON(w, http.StatusForbidden, rpcFailure(request.ID, -32001, "tasks:write scope required", nil))
			return
		}
		var params struct {
			TaskID string         `json:"task_id"`
			Event  protocol.Event `json:"event"`
		}
		if json.Unmarshal(request.Params, &params) != nil || params.TaskID == "" {
			writeJSON(w, http.StatusBadRequest, rpcFailure(request.ID, -32602, "task_id and signed event are required", nil))
			return
		}
		result, err := s.appendSignedProtocolEvent(r, claims, params.TaskID, params.Event)
		if err != nil {
			writeJSON(w, rpcHTTPStatus(err), rpcFailure(request.ID, -32002, err.Error(), nil))
			return
		}
		writeJSON(w, http.StatusOK, rpcSuccess(request.ID, map[string]any{"id": params.TaskID, "status": map[string]any{"state": "working"}, "metadata": map[string]any{"append_result": result}}))
	default:
		writeJSON(w, http.StatusNotFound, rpcFailure(request.ID, -32601, "method not found", nil))
	}
}

func (s *Server) mcp(w http.ResponseWriter, r *http.Request) {
	var request rpcRequest
	if !s.decodeJSON(w, r, &request) {
		return
	}
	if request.JSONRPC != "2.0" {
		writeJSON(w, http.StatusBadRequest, rpcFailure(request.ID, -32600, "invalid JSON-RPC request", nil))
		return
	}
	if request.Method == "initialize" {
		writeJSON(w, http.StatusOK, rpcSuccess(request.ID, map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{"listChanged": false}}, "serverInfo": map[string]string{"name": "agent-continuity", "version": protocol.ProtocolVersion}}))
		return
	}
	claims, ok := s.authorize(w, r)
	if !ok {
		return
	}
	switch request.Method {
	case "notifications/initialized":
		w.WriteHeader(http.StatusAccepted)
	case "tools/list":
		writeJSON(w, http.StatusOK, rpcSuccess(request.ID, map[string]any{"tools": mcpTools()}))
	case "tools/call":
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if json.Unmarshal(request.Params, &params) != nil {
			writeJSON(w, http.StatusBadRequest, rpcFailure(request.ID, -32602, "invalid tool call", nil))
			return
		}
		result, err := s.callMCPTool(r, claims, params.Name, params.Arguments)
		if err != nil {
			writeJSON(w, rpcHTTPStatus(err), rpcSuccess(request.ID, map[string]any{"isError": true, "content": []map[string]string{{"type": "text", "text": err.Error()}}}))
			return
		}
		data, _ := json.Marshal(result)
		writeJSON(w, http.StatusOK, rpcSuccess(request.ID, map[string]any{"content": []map[string]string{{"type": "text", "text": string(data)}}, "structuredContent": result, "isError": false}))
	default:
		writeJSON(w, http.StatusNotFound, rpcFailure(request.ID, -32601, "method not found", nil))
	}
}

func mcpTools() []map[string]any {
	return []map[string]any{
		{"name": "task.list", "description": "List continuity tasks visible to the authenticated account.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 500}}, "additionalProperties": false}},
		{"name": "task.get", "description": "Get one continuity task without loading event payloads.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"task_id": map[string]string{"type": "string"}}, "required": []string{"task_id"}, "additionalProperties": false}},
		{"name": "continuity.get", "description": "Get a portable continuity package with causal events and artifact manifests.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"task_id": map[string]string{"type": "string"}, "target_agent": map[string]string{"type": "string"}}, "required": []string{"task_id"}, "additionalProperties": false}},
		{"name": "event.append", "description": "Append a device-signed universal event to a task.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"task_id": map[string]string{"type": "string"}, "event": map[string]string{"type": "object"}}, "required": []string{"task_id", "event"}, "additionalProperties": false}},
	}
}

func (s *Server) callMCPTool(r *http.Request, claims auth.Claims, name string, arguments json.RawMessage) (any, error) {
	switch name {
	case "task.list":
		if !auth.HasScope(claims, "tasks:read") {
			return nil, storage.ErrUnauthorized
		}
		var input struct {
			Limit int `json:"limit"`
		}
		if len(arguments) > 0 && json.Unmarshal(arguments, &input) != nil {
			return nil, errors.New("invalid arguments")
		}
		items, err := s.store.ListTasks(r.Context(), claims.AccountID, input.Limit)
		return map[string]any{"tasks": items}, err
	case "task.get":
		if !auth.HasScope(claims, "tasks:read") {
			return nil, storage.ErrUnauthorized
		}
		var input struct {
			TaskID string `json:"task_id"`
		}
		if json.Unmarshal(arguments, &input) != nil || input.TaskID == "" {
			return nil, errors.New("task_id is required")
		}
		allowed, err := s.taskAccessAllowed(r, claims, input.TaskID, protocol.PermissionRead)
		if err != nil || !allowed {
			return nil, firstError(err, storage.ErrUnauthorized)
		}
		return s.store.GetTask(r.Context(), input.TaskID)
	case "continuity.get":
		if !auth.HasScope(claims, "tasks:read") {
			return nil, storage.ErrUnauthorized
		}
		var input struct {
			TaskID      string `json:"task_id"`
			TargetAgent string `json:"target_agent"`
		}
		if json.Unmarshal(arguments, &input) != nil || input.TaskID == "" {
			return nil, errors.New("task_id is required")
		}
		return s.continuityForAccount(r, claims, input.TaskID, input.TargetAgent)
	case "event.append":
		if !auth.HasScope(claims, "tasks:write") {
			return nil, storage.ErrUnauthorized
		}
		var input struct {
			TaskID string         `json:"task_id"`
			Event  protocol.Event `json:"event"`
		}
		if json.Unmarshal(arguments, &input) != nil || input.TaskID == "" {
			return nil, errors.New("task_id and event are required")
		}
		return s.appendSignedProtocolEvent(r, claims, input.TaskID, input.Event)
	default:
		return nil, errors.New("unknown tool")
	}
}

func (s *Server) continuityForAccount(r *http.Request, claims auth.Claims, taskID, target string) (protocol.ContinuityPackage, error) {
	allowed, err := s.taskAccessAllowed(r, claims, taskID, protocol.PermissionRead)
	if err != nil || !allowed {
		return protocol.ContinuityPackage{}, firstError(err, storage.ErrUnauthorized)
	}
	task, err := s.store.GetTask(r.Context(), taskID)
	if err != nil {
		return protocol.ContinuityPackage{}, err
	}
	events, err := s.allEvents(r, taskID)
	if err != nil {
		return protocol.ContinuityPackage{}, err
	}
	artifacts, err := s.store.ListArtifacts(r.Context(), taskID)
	if err != nil {
		return protocol.ContinuityPackage{}, err
	}
	visible := make([]protocol.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		permissions, err := s.store.ListArtifactPermissions(r.Context(), taskID, artifact.ID)
		if err != nil {
			return protocol.ContinuityPackage{}, err
		}
		if task.AccountID == claims.AccountID || len(permissions) == 0 || artifactPermissionAllows(permissions, claims.AccountID, protocol.PermissionRead) {
			visible = append(visible, artifact)
		}
	}
	if target == "" {
		target = "generic"
	}
	branches, err := s.store.ListBranches(r.Context(), taskID)
	if err != nil {
		return protocol.ContinuityPackage{}, err
	}
	participants, err := s.store.ListTaskParticipants(r.Context(), taskID)
	if err != nil {
		return protocol.ContinuityPackage{}, err
	}
	agentBindings, err := s.store.ListTaskAgentBindings(r.Context(), taskID)
	if err != nil {
		return protocol.ContinuityPackage{}, err
	}
	conflicts, err := s.store.ListConflicts(r.Context(), taskID, true, 500)
	if err != nil {
		return protocol.ContinuityPackage{}, err
	}
	now := time.Now().UTC()
	snapshots := make([]protocol.TaskSnapshot, 0, len(branches))
	for _, branch := range branches {
		snapshot, err := reconcile.BuildSnapshot(task, branch, events, conflicts, now)
		if err != nil {
			return protocol.ContinuityPackage{}, err
		}
		snapshots = append(snapshots, snapshot)
	}
	return protocol.ContinuityPackage{
		ProtocolVersion: protocol.ProtocolVersion,
		SchemaVersion:   protocol.SchemaVersion,
		Capabilities:    append([]string(nil), protocol.ServerSyncCapabilities...),
		TargetAgent:     target,
		Task:            task,
		Events:          events,
		Artifacts:       visible,
		Branches:        branches,
		Participants:    participants,
		AgentBindings:   agentBindings,
		Conflicts:       conflicts,
		Snapshots:       snapshots,
		GeneratedAt:     now,
	}, nil
}

func (s *Server) appendSignedProtocolEvent(r *http.Request, claims auth.Claims, taskID string, value protocol.Event) (protocol.AppendResult, error) {
	allowed, err := s.taskAccessAllowed(r, claims, taskID, protocol.PermissionWrite)
	if err != nil || !allowed {
		return protocol.AppendResult{}, firstError(err, storage.ErrUnauthorized)
	}
	if value.TaskID != taskID || value.AccountID != claims.AccountID {
		return protocol.AppendResult{}, errors.New("event identity mismatch")
	}
	device, err := s.store.GetDevice(r.Context(), value.DeviceID)
	if err != nil {
		return protocol.AppendResult{}, err
	}
	if device.AccountID != claims.AccountID || device.RevokedAt != nil {
		return protocol.AppendResult{}, storage.ErrDeviceRevoked
	}
	if err := protocol.ValidateEvent(value, time.Now()); err != nil {
		return protocol.AppendResult{}, err
	}
	if err := protocol.VerifyEventSignature(value, device.PublicKey); err != nil {
		return protocol.AppendResult{}, err
	}
	result, err := s.store.AppendEvent(r.Context(), value)
	if err == nil && !result.Duplicate {
		s.metricSet.EventsAccepted.Add(1)
		s.audit(r, claims, "event.appended", "event", result.Event.EventID, mustJSON(map[string]any{"task_id": taskID, "event_type": value.Type, "transport": "agent_protocol"}))
		s.createMentionNotifications(r, claims, result.Event)
		s.publish(protocol.RealtimeMessage{Type: "event.available", TaskID: taskID, ServerSequence: result.Event.ServerSequence, ResourceID: result.Event.EventID, SentAt: time.Now().UTC()})
	}
	if err == nil && result.Duplicate {
		s.metricSet.EventDuplicates.Add(1)
	}
	return result, err
}

func rpcSuccess(id json.RawMessage, result any) rpcResponse {
	return rpcResponse{JSONRPC: "2.0", ID: id, Result: result}
}

func rpcFailure(id json.RawMessage, code int, message string, data any) rpcResponse {
	return rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message, Data: data}}
}

func rpcHTTPStatus(err error) int {
	switch {
	case errors.Is(err, storage.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, storage.ErrUnauthorized), errors.Is(err, storage.ErrDeviceRevoked):
		return http.StatusForbidden
	default:
		return http.StatusBadRequest
	}
}

func firstError(actual, fallback error) error {
	if actual != nil {
		return actual
	}
	return fallback
}
