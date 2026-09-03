package daemon

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

func TestLocalStoreRecoversInboxAndProjectsCursor(t *testing.T) {
	root := t.TempDir()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	device := protocol.Device{ID: "device_remote", AccountID: "acct", PublicKey: base64.RawURLEncoding.EncodeToString(publicKey), CreatedAt: time.Now().UTC()}
	task := protocol.Task{ID: "task_local", AccountID: "acct", Title: "Local", HeadEventIDs: []string{"event_remote"}, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	payload := json.RawMessage(`{"text":"durable"}`)
	event := protocol.Event{EventID: "event_remote", TaskID: task.ID, BranchID: "main", AccountID: "acct", DeviceID: device.ID, LogicalTime: protocol.HLC{WallTimeMS: time.Now().UnixMilli(), Node: device.ID}, DeviceSequence: 1, ServerSequence: 7, Type: "human.message.created", SchemaVersion: protocol.SchemaVersion, ContentType: "application/json", ContentDigest: protocol.Digest(payload), Payload: payload, CreatedAtClient: time.Now().UTC(), ReceivedAtServer: time.Now().UTC(), IdempotencyKey: "event-remote"}
	if err := protocol.SignEvent(&event, privateKey); err != nil {
		t.Fatal(err)
	}
	artifact := protocol.Artifact{ID: "artifact_1", TaskID: task.ID, ContentHash: protocol.Digest([]byte("blob"))}
	response := protocol.SyncPullResponse{Devices: []protocol.Device{device}, Batches: []protocol.SyncTaskBatch{{Task: task, Events: []protocol.Event{event}, ArtifactManifests: []protocol.Artifact{artifact}, NextCursor: 7}}}
	store, err := OpenLocalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.StagePull(response); err != nil {
		t.Fatal(err)
	}
	stats, _ := store.Stats()
	if stats.Inbox != 1 || stats.Events != 0 {
		t.Fatalf("pull did not stage in inbox: %+v", stats)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenLocalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	acks, err := store.ReconcileInbox()
	if err != nil || len(acks) != 1 || acks[0].TaskID != task.ID || acks[0].Cursor != 7 {
		t.Fatalf("inbox recovery: acks=%+v err=%v", acks, err)
	}
	states, err := store.TaskStates()
	if err != nil || len(states) != 1 || states[0].EventCursor != 7 || states[0].HeadEventIDs[0] != event.EventID {
		t.Fatalf("local task index: %+v err=%v", states, err)
	}
	events, err := store.ListEvents(task.ID, 0, 10)
	if err != nil || len(events) != 1 || events[0].EventID != event.EventID {
		t.Fatalf("local event log: %+v err=%v", events, err)
	}
	stats, _ = store.Stats()
	if stats.Inbox != 0 || stats.Events != 1 || stats.Artifacts != 1 {
		t.Fatalf("local projection incomplete: %+v", stats)
	}
}

func TestLocalStoreRejectsTamperedPulledEvent(t *testing.T) {
	store, err := OpenLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	publicKey, privateKey, _ := ed25519.GenerateKey(rand.Reader)
	device := protocol.Device{ID: "device", PublicKey: base64.RawURLEncoding.EncodeToString(publicKey)}
	payload := json.RawMessage(`{"ok":true}`)
	event := protocol.Event{EventID: "event", TaskID: "task", BranchID: "main", AccountID: "acct", DeviceID: device.ID, LogicalTime: protocol.HLC{WallTimeMS: time.Now().UnixMilli(), Node: device.ID}, DeviceSequence: 1, ServerSequence: 1, Type: "human.message.created", SchemaVersion: protocol.SchemaVersion, ContentType: "application/json", ContentDigest: protocol.Digest(payload), Payload: payload, CreatedAtClient: time.Now().UTC(), IdempotencyKey: "event"}
	_ = protocol.SignEvent(&event, privateKey)
	event.Payload = json.RawMessage(`{"ok":false}`)
	err = store.StagePull(protocol.SyncPullResponse{Devices: []protocol.Device{device}, Batches: []protocol.SyncTaskBatch{{Task: protocol.Task{ID: "task"}, Events: []protocol.Event{event}}}})
	if err == nil {
		t.Fatal("tampered event entered local inbox")
	}
}
