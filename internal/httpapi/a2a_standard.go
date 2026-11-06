package httpapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"github.com/agent-continuity/agent-continuity-master/internal/auth"
	"github.com/agent-continuity/agent-continuity-master/internal/operations"
	"github.com/agent-continuity/agent-continuity-master/internal/orchestration"
	"net/http"
	"slices"
	"strings"
	"time"
)

type a2aMessage struct {
	ID        string                       `json:"messageId"`
	Role      string                       `json:"role"`
	TaskID    string                       `json:"taskId,omitempty"`
	ContextID string                       `json:"contextId,omitempty"`
	Parts     []map[string]json.RawMessage `json:"parts"`
}
type a2aParams struct {
	ID            string     `json:"id"`
	Tenant        string     `json:"tenant"`
	History       *int       `json:"historyLength"`
	Context       string     `json:"contextId"`
	Status        string     `json:"status"`
	PageSize      int        `json:"pageSize"`
	PageToken     string     `json:"pageToken"`
	After         *time.Time `json:"statusTimestampAfter"`
	Include       bool       `json:"includeArtifacts"`
	Message       a2aMessage `json:"message"`
	Configuration struct {
		Immediate bool            `json:"returnImmediately"`
		History   *int            `json:"historyLength"`
		Modes     []string        `json:"acceptedOutputModes"`
		Push      json.RawMessage `json:"taskPushNotificationConfig"`
	} `json:"configuration"`
}

func (s *Server) standardAgentCard(w http.ResponseWriter, r *http.Request) {
	base := strings.TrimRight(s.billing.BaseURL, "/")
	if base == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		base = scheme + "://" + r.Host
	}
	skills := []map[string]any{}
	if s.orchestrator != nil && s.orchestrator.Agents["default"] != nil {
		skills = append(skills, map[string]any{"id": "execute", "name": "Execute a durable agent task", "description": "Text input is executed by the configured default agent. Poll GetTask for progress.", "tags": []string{"orchestration", "continuity"}})
	}
	w.Header().Set("Cache-Control", "no-cache")
	writeJSON(w, 200, map[string]any{"name": "Agent Continuity", "description": "Durable agent execution; signed continuity sync remains available through MCP tools and REST.", "version": "0.3.0", "supportedInterfaces": []map[string]string{{"url": base + "/a2a/v1", "protocolBinding": "JSONRPC", "protocolVersion": "1.0"}}, "capabilities": map[string]bool{"streaming": false, "pushNotifications": false, "extendedAgentCard": false}, "defaultInputModes": []string{"text/plain"}, "defaultOutputModes": []string{"text/plain"}, "securitySchemes": map[string]any{"bearer": map[string]any{"httpAuthSecurityScheme": map[string]string{"scheme": "Bearer", "bearerFormat": "JWT"}}}, "securityRequirements": []map[string]any{{"schemes": map[string]any{"bearer": map[string]any{"list": []string{}}}}}, "skills": skills})
}
func a2aState(state string) string {
	switch state {
	case "queued":
		return "TASK_STATE_SUBMITTED"
	case "running":
		return "TASK_STATE_WORKING"
	case "approval_required":
		return "TASK_STATE_INPUT_REQUIRED"
	case "completed":
		return "TASK_STATE_COMPLETED"
	case "failed":
		return "TASK_STATE_FAILED"
	case "canceled":
		return "TASK_STATE_CANCELED"
	}
	return "TASK_STATE_UNKNOWN"
}
func a2aTask(v orchestration.Run, history *int, artifacts bool) map[string]any {
	contextID := v.Request.ContextID
	if contextID == "" {
		contextID = "ctx_" + v.ID
	}
	out := map[string]any{"id": v.ID, "contextId": contextID, "status": map[string]any{"state": a2aState(v.Status), "timestamp": v.Updated}, "metadata": map[string]any{"continuityTaskId": v.Request.TaskID, "runId": v.ID}}
	messages := []any{}
	if len(v.Request.Message) > 0 {
		var m map[string]any
		if json.Unmarshal(v.Request.Message, &m) == nil {
			m["taskId"] = v.ID
			m["contextId"] = contextID
			messages = append(messages, m)
		}
	}
	arts := []any{}
	for _, st := range v.Steps {
		if st.Status == "completed" {
			part := []any{map[string]string{"text": st.Output}}
			arts = append(arts, map[string]any{"artifactId": v.ID + ":" + st.ID, "name": st.ID, "parts": part})
			messages = append(messages, map[string]any{"messageId": v.ID + ":" + st.ID, "role": "ROLE_AGENT", "taskId": v.ID, "contextId": contextID, "parts": part})
		}
	}
	if history != nil && len(messages) > *history {
		messages = messages[len(messages)-*history:]
	}
	if len(messages) > 0 {
		out["history"] = messages
	}
	if artifacts {
		out["artifacts"] = arts
	}
	return out
}
func (s *Server) standardA2A(w http.ResponseWriter, r *http.Request) {
	var q rpcRequest
	if !decodeRPC(w, r, s.maxBodyBytes, &q) {
		return
	}
	fail := func(code int, msg string) { writeJSON(w, 200, rpcFailure(q.ID, code, msg, nil)) }
	if q.JSONRPC != "2.0" || !validRPCID(q.ID) {
		fail(-32600, "invalid JSON-RPC request")
		return
	}
	if v := r.Header.Get("A2A-Version"); v != "" && v != "1.0" {
		fail(-32009, "supported A2A version: 1.0")
		return
	}
	w.Header().Set("A2A-Version", "1.0")
	c, ok := s.authorize(w, r)
	if !ok {
		return
	}
	if s.orchestrator == nil {
		fail(-32004, "agent execution is not configured")
		return
	}
	scope := "tasks:read"
	if q.Method == "SendMessage" || q.Method == "CancelTask" {
		scope = "tasks:write"
	}
	if !auth.HasScope(c, scope) {
		writeJSON(w, 403, rpcFailure(q.ID, -32001, "insufficient scope", nil))
		return
	}
	var p a2aParams
	if len(q.Params) > 0 && json.Unmarshal(q.Params, &p) != nil {
		fail(-32602, "invalid parameters")
		return
	}
	if p.Tenant != "" && p.Tenant != c.AccountID {
		fail(-32001, "tenant unavailable")
		return
	}
	if p.History != nil && *p.History < 0 || p.Configuration.History != nil && *p.Configuration.History < 0 {
		fail(-32602, "negative historyLength")
		return
	}
	switch q.Method {
	case "SendMessage":
		if p.Message.Role != "ROLE_USER" || p.Message.ID == "" || len(p.Message.ID) > 200 || len(p.Message.Parts) == 0 {
			fail(-32602, "user messageId and parts required")
			return
		}
		if len(p.Configuration.Push) > 0 && string(p.Configuration.Push) != "null" {
			fail(-32003, "push notifications unsupported")
			return
		}
		if len(p.Configuration.Modes) > 0 && !slices.Contains(p.Configuration.Modes, "text/plain") {
			fail(-32005, "only text/plain output supported")
			return
		}
		if p.Message.TaskID != "" {
			if _, e := s.orchestrator.Get(r.Context(), c.AccountID, p.Message.TaskID); e != nil {
				fail(-32001, "task unavailable")
			} else {
				fail(-32004, "this skill accepts one request per task; use contextId for follow-up tasks")
			}
			return
		}
		texts := []string{}
		for _, part := range p.Message.Parts {
			var text string
			valid := true
			for name := range part {
				if !slices.Contains([]string{"text", "mediaType", "filename", "metadata"}, name) {
					valid = false
				}
			}
			var media string
			if len(part["mediaType"]) > 0 && (json.Unmarshal(part["mediaType"], &media) != nil || media != "text/plain") {
				valid = false
			}
			if !valid || json.Unmarshal(part["text"], &text) != nil {
				fail(-32005, "only text parts are supported")
				return
			}
			texts = append(texts, text)
		}
		v, e := s.orchestrator.Create(r.Context(), c.AccountID, operations.ID("a2a-message", c.AccountID, p.Message.ID), orchestration.Request{ContextID: p.Message.ContextID, MessageID: p.Message.ID, Message: operations.Encode(p.Message), Steps: []orchestration.Step{{ID: "response", Agent: "default", Input: strings.Join(texts, "\n"), MaxAttempts: 3}}, Budget: 0})
		if e != nil {
			fail(-32602, "invalid message or reused messageId with different content")
			return
		}
		if !p.Configuration.Immediate {
			timer := time.NewTicker(100 * time.Millisecond)
			defer timer.Stop()
			for v.Status == "queued" || v.Status == "running" {
				select {
				case <-r.Context().Done():
					return
				case <-timer.C:
					v, e = s.orchestrator.Get(r.Context(), c.AccountID, v.ID)
					if e != nil {
						fail(-32603, "cannot retrieve task")
						return
					}
				}
			}
		}
		writeJSON(w, 200, rpcSuccess(q.ID, map[string]any{"task": a2aTask(v, p.Configuration.History, true)}))
	case "GetTask", "CancelTask":
		if p.ID == "" {
			fail(-32602, "id required")
			return
		}
		v, e := s.orchestrator.Get(r.Context(), c.AccountID, p.ID)
		if e != nil {
			fail(-32001, "task unavailable")
			return
		}
		if q.Method == "CancelTask" {
			v, e = s.orchestrator.Control(r.Context(), c.AccountID, p.ID, "cancel")
			if e != nil {
				fail(-32002, "task cannot be canceled")
				return
			}
		}
		writeJSON(w, 200, rpcSuccess(q.ID, a2aTask(v, p.History, true)))
	case "ListTasks":
		result, e := s.a2aList(r, c.AccountID, p)
		if e != nil {
			fail(-32602, "invalid filter or page token")
			return
		}
		writeJSON(w, 200, rpcSuccess(q.ID, result))
	case "CreateTaskPushNotificationConfig", "GetTaskPushNotificationConfig", "ListTaskPushNotificationConfigs", "DeleteTaskPushNotificationConfig":
		fail(-32003, "push notifications unsupported")
	case "SendStreamingMessage", "SubscribeToTask", "GetExtendedAgentCard":
		fail(-32004, "optional capability not supported")
	default:
		fail(-32601, "method not found")
	}
}

type a2aCursor struct {
	Time   time.Time `json:"time"`
	ID     string    `json:"id"`
	Filter string    `json:"filter"`
}

func (s *Server) a2aList(r *http.Request, owner string, p a2aParams) (map[string]any, error) {
	if p.PageSize == 0 {
		p.PageSize = 50
	}
	if p.PageSize < 1 || p.PageSize > 100 {
		return nil, operations.ErrInvalid
	}
	if p.Status != "" && !slices.Contains([]string{"TASK_STATE_SUBMITTED", "TASK_STATE_WORKING", "TASK_STATE_INPUT_REQUIRED", "TASK_STATE_COMPLETED", "TASK_STATE_FAILED", "TASK_STATE_CANCELED", "TASK_STATE_AUTH_REQUIRED", "TASK_STATE_REJECTED", "TASK_STATE_UNKNOWN"}, p.Status) {
		return nil, operations.ErrInvalid
	}
	filter := string(operations.Encode([]any{owner, p.Context, p.Status, p.After, p.Include, p.History}))
	var cursor a2aCursor
	if p.PageToken != "" {
		b, e := base64.RawURLEncoding.DecodeString(p.PageToken)
		if e != nil || len(b) < 32 {
			return nil, operations.ErrInvalid
		}
		mac := hmac.New(sha256.New, []byte(s.bootstrapToken))
		mac.Write(b[32:])
		if !hmac.Equal(b[:32], mac.Sum(nil)) || json.Unmarshal(b[32:], &cursor) != nil || cursor.Filter != filter {
			return nil, operations.ErrInvalid
		}
	}
	nativeState := ""
	if p.Status != "" {
		for _, state := range []string{"queued", "running", "approval_required", "completed", "failed", "canceled"} {
			if a2aState(state) == p.Status {
				nativeState = state
			}
		}
		if nativeState == "" {
			nativeState = "unsupported-state"
		}
	}
	query := operations.RunQuery{ContextID: p.Context, Status: nativeState, Before: cursor.Time, BeforeID: cursor.ID, Limit: p.PageSize + 1}
	if p.After != nil {
		query.After = *p.After
	}
	rows, total, e := s.store.QueryRuns(r.Context(), owner, query)
	if e != nil {
		return nil, e
	}
	all := []orchestration.Run{}
	for _, o := range rows {
		var v orchestration.Run
		if e = json.Unmarshal(o.Data, &v); e != nil {
			return nil, e
		}
		all = append(all, v)
	}

	tasks := []any{}
	selected := []orchestration.Run{}
	more := false
	for _, v := range all {
		if p.PageToken != "" && (v.Updated.After(cursor.Time) || v.Updated.Equal(cursor.Time) && v.ID >= cursor.ID) {
			continue
		}
		if len(selected) == p.PageSize {
			more = true
			break
		}
		selected = append(selected, v)
		tasks = append(tasks, a2aTask(v, p.History, p.Include))
	}
	next := ""
	if more {
		last := selected[len(selected)-1]
		b := operations.Encode(a2aCursor{Time: last.Updated, ID: last.ID, Filter: filter})
		mac := hmac.New(sha256.New, []byte(s.bootstrapToken))
		mac.Write(b)
		next = base64.RawURLEncoding.EncodeToString(append(mac.Sum(nil), b...))
	}
	return map[string]any{"tasks": tasks, "nextPageToken": next, "pageSize": p.PageSize, "totalSize": total}, nil
}
