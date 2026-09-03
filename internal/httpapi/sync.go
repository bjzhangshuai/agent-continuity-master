package httpapi

import (
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/agent-continuity/agent-continuity-master/internal/auth"
	"github.com/agent-continuity/agent-continuity-master/internal/storage"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

func (s *Server) syncHello(w http.ResponseWriter, r *http.Request) {
	started, succeeded := time.Now(), false
	defer func() { s.metricSet.ObserveSync(started, succeeded) }()
	claims, ok := s.authorizeScope(w, r, "tasks:read")
	if !ok {
		return
	}
	var input protocol.SyncHelloRequest
	if !s.decodeJSON(w, r, &input) {
		return
	}
	device, ok := s.activeSyncDevice(w, r, claims, input.DeviceID)
	if !ok {
		return
	}
	now := time.Now().UTC()
	if err := protocol.VerifySyncHello(input, device.PublicKey, now); err != nil {
		writeError(w, http.StatusUnauthorized, "invalid_device_proof", err.Error())
		return
	}
	if !protocol.SupportsProtocol(input.ProtocolVersions, protocol.ProtocolVersion) {
		writeError(w, http.StatusUpgradeRequired, "protocol_version_mismatch", "no mutually supported sync protocol version")
		return
	}
	if err := s.store.RecordSyncNonce(r.Context(), device.ID, protocol.Digest([]byte(input.Nonce)), now, now.Add(5*time.Minute)); err != nil {
		if errors.Is(err, storage.ErrAlreadyExists) {
			writeError(w, http.StatusConflict, "hello_replay", "sync hello nonce was already used")
			return
		}
		writeStorageError(w, err)
		return
	}
	clientTasks := make(map[string]protocol.SyncTaskState, len(input.Tasks))
	for _, value := range input.Tasks {
		clientTasks[value.TaskID] = value
	}
	capabilities := protocol.NegotiateCapabilities(input.Capabilities, protocol.ServerSyncCapabilities)
	tasks, err := s.store.ListTasks(r.Context(), claims.AccountID, 200)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	plans := make([]protocol.SyncTaskPlan, 0, len(tasks))
	for _, task := range tasks {
		allowed, err := s.taskAccessAllowed(r, claims, task.ID, protocol.PermissionRead)
		if err != nil {
			writeStorageError(w, err)
			return
		}
		if !allowed {
			continue
		}
		serverCursor, err := s.store.GetTaskEventCursor(r.Context(), task.ID)
		if err != nil {
			writeStorageError(w, err)
			return
		}
		acknowledged, err := s.store.GetSyncCursor(r.Context(), device.ID, task.ID)
		if err != nil {
			writeStorageError(w, err)
			return
		}
		clientState := clientTasks[task.ID]
		missing, err := s.store.ListEvents(r.Context(), task.ID, clientState.EventCursor, 100, false)
		if err != nil {
			writeStorageError(w, err)
			return
		}
		ids := make([]string, len(missing))
		for index := range missing {
			ids[index] = missing[index].EventID
		}
		plan := protocol.SyncTaskPlan{Task: task, ServerCursor: serverCursor, AcknowledgedCursor: acknowledged, MissingEventIDs: ids}
		if containsString(capabilities, protocol.SyncCapabilityArtifactManifests) {
			plan.ArtifactManifests, err = s.store.ListArtifacts(r.Context(), task.ID)
			if err != nil {
				writeStorageError(w, err)
				return
			}
		}
		plans = append(plans, plan)
	}
	s.audit(r, claims, "sync.hello", "device", device.ID, mustJSON(map[string]any{"client_version": input.ClientVersion, "capabilities": capabilities, "task_count": len(plans)}))
	succeeded = true
	writeJSON(w, http.StatusOK, protocol.SyncHelloResponse{ProtocolVersion: protocol.ProtocolVersion, Capabilities: capabilities, ServerTime: now, Tasks: plans})
}

func (s *Server) syncPush(w http.ResponseWriter, r *http.Request) {
	started, succeeded := time.Now(), false
	defer func() { s.metricSet.ObserveSync(started, succeeded) }()
	claims, ok := s.authorizeScope(w, r, "tasks:write")
	if !ok {
		return
	}
	var input protocol.SyncPushRequest
	if !s.decodeJSON(w, r, &input) {
		return
	}
	device, ok := s.activeSyncDevice(w, r, claims, input.DeviceID)
	if !ok {
		return
	}
	if len(input.Events) == 0 || len(input.Events) > 100 {
		writeError(w, http.StatusBadRequest, "invalid_sync_batch", "sync push requires 1..100 events")
		return
	}
	response := protocol.SyncPushResponse{Acknowledgements: make([]protocol.SyncEventAck, 0, len(input.Events))}
	for _, value := range input.Events {
		ack := protocol.SyncEventAck{EventID: value.EventID}
		if value.DeviceID != device.ID || value.AccountID != claims.AccountID {
			ack.ErrorCode, ack.ErrorMessage = "identity_mismatch", "event identity does not match sync device and account"
			response.Acknowledgements = append(response.Acknowledgements, ack)
			continue
		}
		allowed, err := s.taskAccessAllowed(r, claims, value.TaskID, protocol.PermissionWrite)
		if err != nil || !allowed {
			ack.ErrorCode, ack.ErrorMessage = "forbidden", "task write permission denied"
			response.Acknowledgements = append(response.Acknowledgements, ack)
			continue
		}
		if err := protocol.ValidateEvent(value, time.Now()); err != nil {
			ack.ErrorCode, ack.ErrorMessage = "invalid_event", err.Error()
			response.Acknowledgements = append(response.Acknowledgements, ack)
			continue
		}
		if err := protocol.VerifyEventSignature(value, device.PublicKey); err != nil {
			ack.ErrorCode, ack.ErrorMessage = "invalid_event_signature", err.Error()
			response.Acknowledgements = append(response.Acknowledgements, ack)
			continue
		}
		result, err := s.store.AppendEvent(r.Context(), value)
		if err != nil {
			ack.ErrorCode, ack.ErrorMessage = syncStorageCode(err), err.Error()
			response.Acknowledgements = append(response.Acknowledgements, ack)
			continue
		}
		ack.Accepted, ack.ServerSequence, ack.Duplicate, ack.TaskHeads = true, result.Event.ServerSequence, result.Duplicate, result.TaskHeads
		response.Acknowledgements = append(response.Acknowledgements, ack)
		if !result.Duplicate {
			s.metricSet.EventsAccepted.Add(1)
			s.audit(r, claims, "event.appended", "event", result.Event.EventID, mustJSON(map[string]any{"task_id": value.TaskID, "event_type": value.Type, "transport": "sync_batch"}))
			s.createMentionNotifications(r, claims, result.Event)
			s.publish(protocol.RealtimeMessage{Type: "event.available", TaskID: value.TaskID, ServerSequence: result.Event.ServerSequence, ResourceID: result.Event.EventID, SentAt: time.Now().UTC()})
		} else {
			s.metricSet.EventDuplicates.Add(1)
		}
	}
	succeeded = true
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) syncPull(w http.ResponseWriter, r *http.Request) {
	started, succeeded := time.Now(), false
	defer func() { s.metricSet.ObserveSync(started, succeeded) }()
	claims, ok := s.authorizeScope(w, r, "tasks:read")
	if !ok {
		return
	}
	var input protocol.SyncPullRequest
	if !s.decodeJSON(w, r, &input) {
		return
	}
	if _, ok := s.activeSyncDevice(w, r, claims, input.DeviceID); !ok {
		return
	}
	if len(input.Tasks) == 0 || len(input.Tasks) > 50 {
		writeError(w, http.StatusBadRequest, "invalid_pull", "sync pull requires 1..50 task cursors")
		return
	}
	response := protocol.SyncPullResponse{Batches: make([]protocol.SyncTaskBatch, 0, len(input.Tasks))}
	devices := map[string]protocol.Device{}
	for _, cursor := range input.Tasks {
		allowed, err := s.taskAccessAllowed(r, claims, cursor.TaskID, protocol.PermissionRead)
		if err != nil || !allowed {
			writeError(w, http.StatusForbidden, "forbidden", "task read permission denied")
			return
		}
		limit := cursor.Limit
		if limit <= 0 || limit > 500 {
			limit = 200
		}
		items, err := s.store.ListEvents(r.Context(), cursor.TaskID, cursor.After, limit+1, cursor.IncludePayload)
		if err != nil {
			writeStorageError(w, err)
			return
		}
		hasMore := len(items) > limit
		if hasMore {
			items = items[:limit]
		}
		task, err := s.store.GetTask(r.Context(), cursor.TaskID)
		if err != nil {
			writeStorageError(w, err)
			return
		}
		manifests, err := s.store.ListArtifacts(r.Context(), cursor.TaskID)
		if err != nil {
			writeStorageError(w, err)
			return
		}
		visibleManifests := make([]protocol.Artifact, 0, len(manifests))
		for _, manifest := range manifests {
			permissions, err := s.store.ListArtifactPermissions(r.Context(), cursor.TaskID, manifest.ID)
			if err != nil {
				writeStorageError(w, err)
				return
			}
			if task.AccountID == claims.AccountID || len(permissions) == 0 || artifactPermissionAllows(permissions, claims.AccountID, protocol.PermissionRead) {
				visibleManifests = append(visibleManifests, manifest)
			}
		}
		for _, event := range items {
			if _, exists := devices[event.DeviceID]; exists {
				continue
			}
			device, err := s.store.GetDevice(r.Context(), event.DeviceID)
			if err != nil {
				writeStorageError(w, err)
				return
			}
			devices[device.ID] = device
		}
		response.Batches = append(response.Batches, protocol.SyncTaskBatch{Task: task, Events: items, ArtifactManifests: visibleManifests, NextCursor: eventCursor(items, cursor.After), HasMore: hasMore})
	}
	for _, device := range devices {
		response.Devices = append(response.Devices, device)
	}
	sort.Slice(response.Devices, func(i, j int) bool { return response.Devices[i].ID < response.Devices[j].ID })
	succeeded = true
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) syncAck(w http.ResponseWriter, r *http.Request) {
	started, succeeded := time.Now(), false
	defer func() { s.metricSet.ObserveSync(started, succeeded) }()
	claims, ok := s.authorizeScope(w, r, "tasks:read")
	if !ok {
		return
	}
	var input protocol.SyncAckRequest
	if !s.decodeJSON(w, r, &input) {
		return
	}
	if _, ok := s.activeSyncDevice(w, r, claims, input.DeviceID); !ok {
		return
	}
	if len(input.Tasks) == 0 || len(input.Tasks) > 100 {
		writeError(w, http.StatusBadRequest, "invalid_ack", "sync ack requires 1..100 task cursors")
		return
	}
	now := time.Now().UTC()
	for _, ack := range input.Tasks {
		allowed, err := s.taskAccessAllowed(r, claims, ack.TaskID, protocol.PermissionRead)
		if err != nil || !allowed {
			writeError(w, http.StatusForbidden, "forbidden", "task read permission denied")
			return
		}
		serverCursor, err := s.store.GetTaskEventCursor(r.Context(), ack.TaskID)
		if err != nil {
			writeStorageError(w, err)
			return
		}
		if ack.Cursor > serverCursor {
			writeError(w, http.StatusConflict, "ack_ahead_of_server", "ack cursor exceeds the task server cursor")
			return
		}
		if err := s.store.PutSyncCursor(r.Context(), input.DeviceID, ack.TaskID, ack.Cursor, now); err != nil {
			writeStorageError(w, err)
			return
		}
	}
	succeeded = true
	writeJSON(w, http.StatusOK, map[string]any{"acknowledged": input.Tasks})
}

func (s *Server) reconcileTask(w http.ResponseWriter, r *http.Request) {
	started, succeeded := time.Now(), false
	defer func() {
		if succeeded {
			s.metricSet.Reconciliations.Add(1)
			s.metricSet.ReconciliationDuration.Add(uint64(max(time.Since(started).Milliseconds(), 0)))
		}
	}()
	claims, ok := s.authorizeScope(w, r, "tasks:read")
	if !ok {
		return
	}
	var input protocol.ReconcileRequest
	if !s.decodeJSON(w, r, &input) {
		return
	}
	allowed, err := s.taskAccessAllowed(r, claims, input.TaskID, protocol.PermissionRead)
	if err != nil || !allowed {
		writeError(w, http.StatusForbidden, "forbidden", "task read permission denied")
		return
	}
	task, err := s.store.GetTask(r.Context(), input.TaskID)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	serverCursor, err := s.store.GetTaskEventCursor(r.Context(), input.TaskID)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	missing, err := s.store.ListEvents(r.Context(), input.TaskID, input.EventCursor, 500, true)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	conflicts, err := s.store.ListConflicts(r.Context(), input.TaskID, false, 200)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	s.metricSet.Conflicts.Add(uint64(len(conflicts)))
	succeeded = true
	writeJSON(w, http.StatusOK, protocol.ReconcileResult{TaskID: input.TaskID, ServerHeadIDs: task.HeadEventIDs, ServerCursor: serverCursor, MissingEvents: missing, OpenConflicts: conflicts})
}

func (s *Server) activeSyncDevice(w http.ResponseWriter, r *http.Request, claims auth.Claims, deviceID string) (protocol.Device, bool) {
	device, err := s.store.GetDevice(r.Context(), deviceID)
	if err != nil {
		writeStorageError(w, err)
		return protocol.Device{}, false
	}
	if device.AccountID != claims.AccountID || device.RevokedAt != nil {
		writeError(w, http.StatusForbidden, "device_forbidden", "sync device is not active for this account")
		return protocol.Device{}, false
	}
	return device, true
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func syncStorageCode(err error) string {
	switch {
	case errors.Is(err, storage.ErrSequenceReplay):
		return "sequence_replay"
	case errors.Is(err, storage.ErrDeviceRevoked):
		return "device_revoked"
	case errors.Is(err, storage.ErrNotFound):
		return "not_found"
	case errors.Is(err, storage.ErrUnauthorized):
		return "forbidden"
	default:
		return "storage_error"
	}
}
