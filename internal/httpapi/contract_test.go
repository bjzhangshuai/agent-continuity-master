package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

func TestDuplicateEventCannotReadAnotherTenant(t *testing.T) {
	f := newFixture(t)
	var original protocol.Event
	for i := 0; i < 2; i++ {
		account, token := f.account(fmt.Sprintf("collision-%d", i))
		device, key := f.device(token, account, "producer")
		status, raw := f.request("POST", "/v1/tasks", token, map[string]string{"title": "Private task", "objective": "isolation"}, nil)
		if status != 201 {
			t.Fatal(status, string(raw))
		}
		task := decode[protocol.Task](t, raw)
		event := protocol.Event{EventID: "event_shared_collision", TaskID: task.ID, BranchID: "main", AccountID: account.ID, DeviceID: device.ID, ParentEventIDs: []string{}, LogicalTime: protocol.HLC{WallTimeMS: time.Now().UnixMilli(), Node: device.ID}, DeviceSequence: 1, Type: "agent.output", SchemaVersion: protocol.SchemaVersion, ContentType: "application/json", Payload: json.RawMessage(`{"text":"tenant-private-value"}`), CreatedAtClient: time.Now().UTC(), IdempotencyKey: "collision-key"}
		if i == 1 {
			event.Payload = json.RawMessage(`{"text":"attacker supplied value"}`)
		}
		event.ContentDigest = protocol.Digest(event.Payload)
		if err := protocol.SignPortableEvent(&event, key); err != nil {
			t.Fatal(err)
		}
		status, raw = f.request("POST", "/v1/tasks/"+task.ID+"/events", token, event, nil)
		if i == 0 {
			if status != 201 {
				t.Fatal(status, string(raw))
			}
			original = event
			changed := event
			changed.Payload = json.RawMessage(`{"text":"replaced"}`)
			changed.ContentDigest = protocol.Digest(changed.Payload)
			if err := protocol.SignPortableEvent(&changed, key); err != nil {
				t.Fatal(err)
			}
			status, raw = f.request("POST", "/v1/tasks/"+task.ID+"/events", token, changed, nil)
			if status != 409 {
				t.Fatalf("changed payload accepted as duplicate: %d %s", status, raw)
			}
		} else if status != 409 || bytes.Contains(raw, original.Payload) || bytes.Contains(raw, []byte(original.TaskID)) {
			t.Fatalf("cross-tenant collision disclosed event: %d %s", status, raw)
		}
	}
}

// These are actual HTTP responses, also validated by the independent JSON
// Schema runner. No synthetic duplicate of the implementation's DTOs is used.
func TestProtocolContractAndMetering(t *testing.T) {
	f := newFixture(t)
	status, raw := f.request("GET", "/.well-known/agent-continuity.json", "", nil, nil)
	profile := decode[map[string]any](t, raw)
	if status != 200 || profile["industry_standard"] != false || profile["schema_version"] != protocol.SchemaVersion {
		t.Fatalf("discovery mismatch: %d %s", status, raw)
	}
	for _, path := range profile["schemas"].(map[string]any) {
		status, raw = f.request("GET", path.(string), "", nil, nil)
		if status != 200 || !json.Valid(raw) {
			t.Fatalf("schema unavailable: %s", path)
		}
	}
	owner, token := f.account("meter-owner")
	guest, guestToken := f.account("meter-collaborator")
	device, key := f.device(guestToken, guest, "producer")
	status, raw = f.request("POST", "/v1/organizations", token, map[string]any{"name": "Meter Org", "slug": "meter-org", "plan": "team", "key_provider": "platform"}, nil)
	if status != 201 {
		t.Fatalf("organization: %d %s", status, raw)
	}
	org := decode[protocol.Organization](t, raw)
	status, raw = f.request("PUT", "/v1/organizations/"+org.ID+"/members/"+guest.ID, token, map[string]string{"role": "member"}, nil)
	if status != 200 {
		t.Fatalf("member: %d %s", status, raw)
	}
	status, raw = f.request("POST", "/v1/tasks", token, map[string]string{"title": "Transport-independent consumption", "objective": "bill exactly once", "organization_id": org.ID}, nil)
	if status != 201 {
		t.Fatalf("task: %d %s", status, raw)
	}
	task := decode[protocol.Task](t, raw)
	status, emptyPackage := f.request("GET", "/v1/tasks/"+task.ID+"/continuity-package", token, nil, nil)
	if status != 200 {
		t.Fatalf("empty package: %d %s", status, emptyPackage)
	}
	if err := f.api.store.PutTaskPermission(context.Background(), protocol.TaskPermission{TaskID: task.ID, AccountID: guest.ID, Permission: protocol.PermissionWrite, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	for i, transport := range []string{"rest", "sync", "mcp", "a2a-legacy"} {
		event := protocol.Event{EventID: fmt.Sprintf("event_contract_%d", i), TaskID: task.ID, BranchID: "main", AccountID: guest.ID, DeviceID: device.ID,
			ParentEventIDs: []string{}, LogicalTime: protocol.HLC{WallTimeMS: time.Now().UnixMilli(), Node: device.ID}, DeviceSequence: uint64(i + 1),
			Type: "agent.output", SchemaVersion: protocol.SchemaVersion, ContentType: "application/json", Payload: json.RawMessage(`{"text":"portable progress"}`), CreatedAtClient: time.Now().UTC(), IdempotencyKey: fmt.Sprintf("contract-event-%d", i)}
		event.ContentDigest = protocol.Digest(event.Payload)
		if err := protocol.SignPortableEvent(&event, key); err != nil {
			t.Fatal(err)
		}
		path, body := "/v1/tasks/"+task.ID+"/events", any(event)
		switch transport {
		case "sync":
			path, body = "/v1/sync/push", protocol.SyncPushRequest{DeviceID: device.ID, Events: []protocol.Event{event}}
		case "mcp":
			path, body = "/v1/mcp", map[string]any{"jsonrpc": "2.0", "id": i, "method": "tools/call", "params": map[string]any{"name": "event.append", "arguments": map[string]any{"task_id": task.ID, "event": event}}}
		case "a2a-legacy":
			path, body = "/v1/a2a", map[string]any{"jsonrpc": "2.0", "id": i, "method": "message/send", "params": map[string]any{"task_id": task.ID, "event": event}}
		}
		for retry := 0; retry < 2; retry++ {
			status, raw = f.request("POST", path, guestToken, body, nil)
			if status < 200 || status > 201 {
				t.Fatalf("%s: %d %s", transport, status, raw)
			}
		}
	}
	usage, err := f.api.store.ListUsage(context.Background(), owner.ID, time.Time{}, time.Now().Add(time.Second), 200)
	if err != nil {
		t.Fatal(err)
	}
	accepted := []protocol.UsageRecord{}
	for _, item := range usage {
		var metadata map[string]string
		_ = json.Unmarshal(item.Metadata, &metadata)
		if metadata["action"] == "event.appended" {
			if item.OrganizationID != org.ID || item.Quantity != 1 || metadata["actor_account_id"] != guest.ID {
				t.Fatalf("payer/quantity mismatch: %+v", item)
			}
			accepted = append(accepted, item)
		}
	}
	if len(accepted) != 4 {
		t.Fatalf("expected 4 accepted-event charges, got %d", len(accepted))
	}
	guestUsage, _ := f.api.store.ListUsage(context.Background(), guest.ID, time.Time{}, time.Now().Add(time.Second), 200)
	for _, item := range guestUsage {
		if item.ResourceID == "event_contract_0" {
			t.Fatal("collaborator received personal consumption")
		}
	}
	status, raw = f.request("GET", "/v1/tasks/"+task.ID+"/continuity-package", token, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("package: %d %s", status, raw)
	}
	status, private := f.request("GET", "/v1/tasks/"+task.ID+"/continuity-package", "", nil, nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("public profile leaked private task: %d %s", status, private)
	}
	audits, err := f.api.store.ListAuditEvents(context.Background(), guest.ID, time.Time{}, 100)
	if err != nil || len(audits) == 0 {
		t.Fatalf("audit missing: %v", err)
	}
	if path := os.Getenv("CONTINUITY_CONTRACT_OUTPUT"); path != "" {
		output, err := json.Marshal(map[string]any{"package": json.RawMessage(raw), "empty_package": json.RawMessage(emptyPackage), "usage": accepted, "audit": audits})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, output, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
