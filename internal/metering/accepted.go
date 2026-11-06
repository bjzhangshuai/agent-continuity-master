// Package metering defines server-owned consumption records, independent of transport.
package metering

import (
	"encoding/json"
	"strings"

	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

// AcceptedEvent must be persisted in the same transaction as a newly accepted
// event. Replays return before this call. The task owner (or its organization)
// pays; an invited collaborator must not receive a personal bill for that task.
// This retains the existing api.mutation price, without adding a second charge.
func AcceptedEvent(event protocol.Event, owner, organization string) protocol.UsageRecord {
	id := "usage_event_" + strings.TrimPrefix(protocol.Digest([]byte(event.EventID)), "sha256:")
	metadata, _ := json.Marshal(map[string]string{
		"action": "event.appended", "resource_type": "event", "task_id": event.TaskID,
		"actor_account_id": event.AccountID, "device_id": event.DeviceID,
		"provider_id": event.ProviderID, "agent_id": event.AgentID,
		"metering_contract": "accepted-event/1",
	})
	return protocol.UsageRecord{ID: id, AccountID: owner, OrganizationID: organization,
		Metric: "api.mutation", Quantity: 1, Unit: "operation", ResourceID: event.EventID,
		IdempotencyKey: id, Metadata: metadata, RecordedAt: event.ReceivedAtServer}
}
