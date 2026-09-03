package httpapi

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/agent-continuity/agent-continuity-master/internal/auth"
	"github.com/agent-continuity/agent-continuity-master/internal/storage"
	"github.com/agent-continuity/agent-continuity-master/pkg/e2ee"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

type requestIDContextKey struct{}

var allowedDelegatedScopes = map[string]struct{}{
	"tasks:read": {}, "tasks:write": {}, "tasks:admin": {},
	"devices:read": {}, "devices:write": {},
	"invitations:accept": {}, "invitations:write": {},
	"api_keys:read": {}, "api_keys:write": {}, "audit:read": {},
	"agents:read": {}, "agents:write": {}, "notifications:read": {},
	"organizations:read": {}, "organizations:write": {},
	"oauth:read": {}, "oauth:write": {},
	"webhooks:read": {}, "webhooks:write": {},
	"usage:read": {}, "billing:read": {}, "billing:write": {},
	"treasury:read": {}, "treasury:write": {}, "treasury:approve": {},
	"marketplace:read": {}, "marketplace:write": {}, "marketplace:install": {}, "marketplace:transact": {},
}

func (s *Server) refreshToken(w http.ResponseWriter, r *http.Request) {
	var input struct {
		RefreshToken string `json:"refresh_token"`
	}
	if !s.decodeJSON(w, r, &input) {
		return
	}
	if !strings.HasPrefix(input.RefreshToken, "rft_") || len(input.RefreshToken) < 40 {
		writeError(w, http.StatusUnauthorized, "refresh_invalid", storage.ErrRefreshInvalid.Error())
		return
	}
	newToken, err := auth.NewOpaqueToken("rft")
	if err != nil {
		writeInternal(w, err)
		return
	}
	sessionID, _ := protocol.NewID("session")
	now := time.Now().UTC()
	replacement := protocol.AuthSession{ID: sessionID, RefreshTokenHash: auth.HashOpaque(newToken), CreatedAt: now, ExpiresAt: now.Add(30 * 24 * time.Hour)}
	replacement, err = s.store.RotateAuthSession(r.Context(), auth.HashOpaque(input.RefreshToken), replacement)
	if err != nil {
		if errors.Is(err, storage.ErrTokenReuse) {
			writeError(w, http.StatusUnauthorized, "refresh_reuse_detected", err.Error())
			return
		}
		if errors.Is(err, storage.ErrRefreshInvalid) {
			writeError(w, http.StatusUnauthorized, "refresh_invalid", err.Error())
			return
		}
		writeStorageError(w, err)
		return
	}
	accessTTL := 15 * time.Minute
	accessToken, err := s.tokens.Issue(replacement.AccountID, replacement.Subject, accessTTL)
	if err != nil {
		writeInternal(w, err)
		return
	}
	claims := auth.Claims{AccountID: replacement.AccountID, Subject: replacement.Subject}
	s.audit(r, claims, "auth.session.rotated", "session", replacement.ID, nil)
	writeJSON(w, http.StatusOK, map[string]any{"access_token": accessToken, "refresh_token": newToken, "token_type": "Bearer", "expires_in": int64(accessTTL.Seconds()), "refresh_expires_in": int64((30 * 24 * time.Hour).Seconds())})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorize(w, r)
	if !ok {
		return
	}
	var input struct {
		RefreshToken string `json:"refresh_token"`
	}
	if !s.decodeJSON(w, r, &input) {
		return
	}
	if err := s.store.RevokeAuthSession(r.Context(), claims.AccountID, auth.HashOpaque(input.RefreshToken)); err != nil {
		writeStorageError(w, err)
		return
	}
	s.audit(r, claims, "auth.session.revoked", "session", "family", nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) createAPIKey(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeScope(w, r, "api_keys:write")
	if !ok {
		return
	}
	var input struct {
		Name       string   `json:"name"`
		Scopes     []string `json:"scopes"`
		TTLSeconds int64    `json:"ttl_seconds"`
	}
	if !s.decodeJSON(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Name) > 120 {
		writeError(w, http.StatusBadRequest, "invalid_name", "name must contain 1..120 characters")
		return
	}
	input.Scopes = uniqueStrings(input.Scopes)
	if len(input.Scopes) == 0 || len(input.Scopes) > 20 {
		writeError(w, http.StatusBadRequest, "invalid_scopes", "one to twenty scopes are required")
		return
	}
	for _, scope := range input.Scopes {
		if _, ok := allowedDelegatedScopes[scope]; !ok {
			writeError(w, http.StatusBadRequest, "invalid_scope", "unsupported API key scope: "+scope)
			return
		}
		if !auth.HasScope(claims, scope) {
			writeError(w, http.StatusForbidden, "scope_escalation", "cannot grant a scope the caller does not hold")
			return
		}
	}
	var expiresAt *time.Time
	if input.TTLSeconds != 0 {
		if input.TTLSeconds < 300 || input.TTLSeconds > int64((365*24*time.Hour).Seconds()) {
			writeError(w, http.StatusBadRequest, "invalid_ttl", "API key ttl must be 300 seconds..365 days")
			return
		}
		value := time.Now().UTC().Add(time.Duration(input.TTLSeconds) * time.Second)
		expiresAt = &value
	}
	raw, err := auth.NewOpaqueToken("ack")
	if err != nil {
		writeInternal(w, err)
		return
	}
	id, _ := protocol.NewID("key")
	prefix := raw
	if len(prefix) > 12 {
		prefix = prefix[:12]
	}
	value := protocol.APIKey{ID: id, AccountID: claims.AccountID, Name: input.Name, Prefix: prefix, SecretHash: auth.HashOpaque(raw), Scopes: input.Scopes, CreatedAt: time.Now().UTC(), ExpiresAt: expiresAt}
	if err := s.store.CreateAPIKey(r.Context(), value); err != nil {
		writeStorageError(w, err)
		return
	}
	s.audit(r, claims, "api_key.created", "api_key", value.ID, mustJSON(map[string]any{"scopes": value.Scopes}))
	writeJSON(w, http.StatusCreated, map[string]any{"api_key": value, "secret": raw})
}

func (s *Server) listAPIKeys(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeScope(w, r, "api_keys:read")
	if !ok {
		return
	}
	items, err := s.store.ListAPIKeys(r.Context(), claims.AccountID)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"api_keys": items})
}

func (s *Server) revokeAPIKey(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeScope(w, r, "api_keys:write")
	if !ok {
		return
	}
	if err := s.store.RevokeAPIKey(r.Context(), claims.AccountID, r.PathValue("keyID")); err != nil {
		writeStorageError(w, err)
		return
	}
	s.audit(r, claims, "api_key.revoked", "api_key", r.PathValue("keyID"), nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listAuditEvents(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeScope(w, r, "audit:read")
	if !ok {
		return
	}
	var after time.Time
	if raw := r.URL.Query().Get("after"); raw != "" {
		parsed, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_after", "after must be an RFC3339 timestamp")
			return
		}
		after = parsed
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := s.store.ListAuditEvents(r.Context(), claims.AccountID, after, limit)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"audit_events": items})
}

func (s *Server) putTaskKeySet(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeTask(w, r, protocol.PermissionWrite)
	if !ok {
		return
	}
	var value protocol.TaskKeySet
	if !s.decodeJSON(w, r, &value) {
		return
	}
	value.TaskID = r.PathValue("taskID")
	if value.Version == 0 || value.Algorithm != e2ee.Algorithm || len(value.WrappedKeys) == 0 || len(value.WrappedKeys) > 256 {
		writeError(w, http.StatusBadRequest, "invalid_task_key_set", "positive version, supported algorithm, and 1..256 wrapped keys are required")
		return
	}
	creator, err := s.store.GetDevice(r.Context(), value.CreatedBy)
	if err != nil || creator.AccountID != claims.AccountID || creator.RevokedAt != nil || creator.EncryptionPublicKey == "" {
		writeError(w, http.StatusBadRequest, "invalid_creator_device", "created_by must be an active encryption-capable device owned by the caller")
		return
	}
	seen := map[string]struct{}{}
	for _, envelope := range value.WrappedKeys {
		if _, duplicate := seen[envelope.DeviceID]; duplicate || !validEnvelope(envelope) {
			writeError(w, http.StatusBadRequest, "invalid_wrapped_key", "wrapped key recipients must be unique and contain valid X25519/AES-GCM fields")
			return
		}
		seen[envelope.DeviceID] = struct{}{}
		device, err := s.store.GetDevice(r.Context(), envelope.DeviceID)
		if err != nil || device.RevokedAt != nil || device.EncryptionPublicKey == "" || s.store.CheckPermission(r.Context(), value.TaskID, device.AccountID, protocol.PermissionRead) != nil {
			writeError(w, http.StatusBadRequest, "invalid_recipient_device", "every recipient must be an active encryption-capable task collaborator device")
			return
		}
	}
	value.CreatedAt = time.Now().UTC()
	if err := s.store.PutTaskKeySet(r.Context(), value); err != nil {
		writeStorageError(w, err)
		return
	}
	s.audit(r, claims, "task.key.rotated", "task", value.TaskID, mustJSON(map[string]any{"version": value.Version, "recipients": len(value.WrappedKeys)}))
	writeJSON(w, http.StatusCreated, value)
}

func (s *Server) listTaskDevices(w http.ResponseWriter, r *http.Request) {
	_, ok := s.authorizeTask(w, r, protocol.PermissionWrite)
	if !ok {
		return
	}
	items, err := s.store.ListTaskDevices(r.Context(), r.PathValue("taskID"))
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": items})
}

func (s *Server) getTaskKeySet(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeTask(w, r, protocol.PermissionRead)
	if !ok {
		return
	}
	deviceID := r.URL.Query().Get("device_id")
	device, err := s.store.GetDevice(r.Context(), deviceID)
	if err != nil || device.AccountID != claims.AccountID || device.RevokedAt != nil {
		writeError(w, http.StatusForbidden, "device_forbidden", "an active device_id owned by the caller is required")
		return
	}
	version64, _ := strconv.ParseUint(r.URL.Query().Get("version"), 10, 32)
	var value protocol.TaskKeySet
	if version64 == 0 {
		value, err = s.store.LatestTaskKeySet(r.Context(), r.PathValue("taskID"))
	} else {
		value, err = s.store.GetTaskKeySet(r.Context(), r.PathValue("taskID"), uint32(version64))
	}
	if err != nil {
		writeStorageError(w, err)
		return
	}
	for _, wrapped := range value.WrappedKeys {
		if wrapped.DeviceID == deviceID {
			value.WrappedKeys = []protocol.WrappedTaskKey{wrapped}
			writeJSON(w, http.StatusOK, value)
			return
		}
	}
	writeError(w, http.StatusNotFound, "key_not_wrapped_for_device", "this key version has no envelope for the requested device")
}

func (s *Server) audit(r *http.Request, claims auth.Claims, action, resourceType, resourceID string, details json.RawMessage) {
	id, err := protocol.NewID("audit")
	if err != nil {
		return
	}
	source := sha256.Sum256([]byte(r.RemoteAddr))
	requestID, _ := r.Context().Value(requestIDContextKey{}).(string)
	value := protocol.AuditEvent{ID: id, AccountID: claims.AccountID, ActorID: claims.Subject, Action: action, ResourceType: resourceType, ResourceID: resourceID, RequestID: requestID, SourceHash: hex.EncodeToString(source[:]), Details: details, CreatedAt: time.Now().UTC()}
	if err := s.store.RecordAuditEvent(r.Context(), value); err != nil {
		s.logger.Error("record audit event", "error", err, "action", action, "resource_id", resourceID)
	}
	payload := mustJSON(map[string]any{"id": value.ID, "type": action, "account_id": claims.AccountID, "created_at": value.CreatedAt, "data": map[string]any{"actor_id": claims.Subject, "resource_type": resourceType, "resource_id": resourceID, "details": details}})
	if err := s.store.CreateWebhookDeliveries(r.Context(), claims.AccountID, action, payload, value.CreatedAt); err != nil {
		s.logger.Error("enqueue webhook deliveries", "error", err, "action", action, "resource_id", resourceID)
	}
	usageID, _ := protocol.NewID("usage")
	usage := protocol.UsageRecord{ID: usageID, AccountID: claims.AccountID, Metric: "api.mutation", Quantity: 1, Unit: "operation", ResourceID: resourceID, IdempotencyKey: value.ID, Metadata: mustJSON(map[string]string{"action": action, "resource_type": resourceType}), RecordedAt: value.CreatedAt}
	taskID := r.PathValue("taskID")
	if resourceType == "task" {
		taskID = resourceID
	}
	if taskID != "" {
		if task, e := s.store.GetTask(r.Context(), taskID); e == nil {
			usage.OrganizationID = task.OrganizationID
		}
	}
	if err := s.store.RecordUsage(r.Context(), usage); err != nil {
		s.logger.Error("record usage", "error", err, "action", action, "resource_id", resourceID)
	}
}

func (s *Server) recordUsage(r *http.Request, claims auth.Claims, metric string, quantity int64, unit, resourceID, idempotencyKey string, metadata json.RawMessage) {
	id, err := protocol.NewID("usage")
	if err != nil {
		return
	}
	organizationID := ""
	if taskID := r.PathValue("taskID"); taskID != "" {
		if task, err := s.store.GetTask(r.Context(), taskID); err == nil {
			organizationID = task.OrganizationID
		}
	}
	value := protocol.UsageRecord{ID: id, AccountID: claims.AccountID, OrganizationID: organizationID, Metric: metric, Quantity: quantity, Unit: unit, ResourceID: resourceID, IdempotencyKey: idempotencyKey, Metadata: metadata, RecordedAt: time.Now().UTC()}
	if err := s.store.RecordUsage(r.Context(), value); err != nil {
		s.logger.Error("record usage", "error", err, "metric", metric, "resource_id", resourceID)
	}
}

func validEnvelope(value protocol.WrappedTaskKey) bool {
	epk, err1 := base64.RawURLEncoding.DecodeString(value.EphemeralPublicKey)
	nonce, err2 := base64.RawURLEncoding.DecodeString(value.Nonce)
	ciphertext, err3 := base64.RawURLEncoding.DecodeString(value.Ciphertext)
	return value.DeviceID != "" && err1 == nil && err2 == nil && err3 == nil && len(epk) == 32 && len(nonce) == 12 && len(ciphertext) == 48
}

func uniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; !ok {
			seen[value] = struct{}{}
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func mustJSON(value any) json.RawMessage {
	data, _ := json.Marshal(value)
	return data
}
