package storage

import "github.com/agent-continuity/agent-continuity-master/pkg/protocol"

// A globally unique event ID is not an access capability. Never return an
// existing payload from a different task, account or producer on a collision.
// An idempotency key also cannot silently substitute a different payload.
func SameEventOperation(existing, incoming protocol.Event) bool {
	return existing.TaskID == incoming.TaskID && existing.AccountID == incoming.AccountID &&
		existing.DeviceID == incoming.DeviceID && existing.BranchID == incoming.BranchID &&
		existing.Type == incoming.Type && existing.ContentDigest == incoming.ContentDigest
}
