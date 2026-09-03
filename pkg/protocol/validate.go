package protocol

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
)

var eventTypes = map[string]struct{}{
	"task.created": {}, "task.metadata.updated": {}, "task.paused": {},
	"task.resumed": {}, "task.completed": {}, "human.message.created": {},
	"agent.message.created": {}, "input.created": {}, "output.created": {},
	"tool.started": {}, "tool.completed": {}, "tool.failed": {},
	"artifact.created": {}, "artifact.version.created": {}, "artifact.deleted": {},
	"agent.joined": {}, "agent.left": {}, "device.joined": {},
	"permission.granted": {}, "permission.revoked": {}, "task.forked": {},
	"task.merged": {}, "conflict.detected": {}, "conflict.resolved": {},
	"decision.recorded": {}, "progress.updated": {},
}

func ValidateEvent(event Event, now time.Time) error {
	if event.EventID == "" || event.TaskID == "" || event.AccountID == "" || event.DeviceID == "" {
		return errors.New("event_id, task_id, account_id, and device_id are required")
	}
	if event.BranchID == "" {
		return errors.New("branch_id is required")
	}
	if event.DeviceSequence == 0 {
		return errors.New("device_sequence must be greater than zero")
	}
	if _, ok := eventTypes[event.Type]; !ok && !strings.Contains(event.Type, ".") {
		return errors.New("event_type must be a namespaced known or extension event")
	}
	if event.SchemaVersion == "" {
		return errors.New("schema_version is required")
	}
	if event.ContentType == "" {
		return errors.New("content_type is required")
	}
	if got := Digest(event.Payload); got != event.ContentDigest {
		return fmt.Errorf("content digest mismatch: got %s want %s", got, event.ContentDigest)
	}
	if event.IdempotencyKey == "" || len(event.IdempotencyKey) > 200 {
		return errors.New("idempotency_key must contain 1..200 characters")
	}
	if event.LogicalTime.WallTimeMS <= 0 || event.LogicalTime.Node == "" {
		return errors.New("logical_time wall_time_ms and node are required")
	}
	clientTime := event.CreatedAtClient
	if clientTime.IsZero() {
		return errors.New("created_at_client is required")
	}
	if clientTime.After(now.Add(24 * time.Hour)) {
		return errors.New("created_at_client exceeds allowed clock skew")
	}
	if event.Signature == "" {
		return errors.New("signature is required")
	}
	signature := strings.TrimPrefix(event.Signature, PortableSignaturePrefix)
	if _, err := base64.RawURLEncoding.DecodeString(signature); err != nil {
		return errors.New("signature must be base64url without padding")
	}
	seen := map[string]struct{}{}
	for _, parent := range event.ParentEventIDs {
		if parent == "" || parent == event.EventID {
			return errors.New("invalid parent_event_ids")
		}
		if _, ok := seen[parent]; ok {
			return errors.New("duplicate parent_event_id")
		}
		seen[parent] = struct{}{}
	}
	return nil
}
