package daemon

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/agent-continuity/agent-continuity-master/pkg/client"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

func TestSyncEnginePushesPullsPersistsAndAcknowledges(t *testing.T) {
	root := t.TempDir()
	localPublic, localPrivate, _ := ed25519.GenerateKey(rand.Reader)
	remotePublic, remotePrivate, _ := ed25519.GenerateKey(rand.Reader)
	stateStore, _ := OpenState(root)
	state := DeviceState{AccountID: "acct", DeviceID: "device_local", Name: "local", PrivateKey: base64.RawURLEncoding.EncodeToString(localPrivate), Sequence: 1}
	if err := stateStore.Save(state); err != nil {
		t.Fatal(err)
	}
	outbox, _ := OpenOutbox(root)
	localPayload := json.RawMessage(`{"text":"from local"}`)
	localEvent := protocol.Event{EventID: "event_local", TaskID: "task", BranchID: "main", AccountID: "acct", DeviceID: state.DeviceID, LogicalTime: protocol.HLC{WallTimeMS: time.Now().UnixMilli(), Node: state.DeviceID}, DeviceSequence: 1, Type: "human.message.created", SchemaVersion: protocol.SchemaVersion, ContentType: "application/json", ContentDigest: protocol.Digest(localPayload), Payload: localPayload, CreatedAtClient: time.Now().UTC(), IdempotencyKey: "local"}
	_ = protocol.SignEvent(&localEvent, localPrivate)
	if err := outbox.Enqueue(localEvent); err != nil {
		t.Fatal(err)
	}
	remotePayload := json.RawMessage(`{"text":"from remote"}`)
	remoteEvent := protocol.Event{EventID: "event_remote", TaskID: "task", BranchID: "main", AccountID: "acct", DeviceID: "device_remote", LogicalTime: protocol.HLC{WallTimeMS: time.Now().UnixMilli(), Node: "device_remote"}, DeviceSequence: 1, ServerSequence: 1, Type: "agent.message.created", SchemaVersion: protocol.SchemaVersion, ContentType: "application/json", ContentDigest: protocol.Digest(remotePayload), Payload: remotePayload, CreatedAtClient: time.Now().UTC(), ReceivedAtServer: time.Now().UTC(), IdempotencyKey: "remote"}
	_ = protocol.SignEvent(&remoteEvent, remotePrivate)
	task := protocol.Task{ID: "task", AccountID: "acct", Title: "Synced", HeadEventIDs: []string{remoteEvent.EventID}, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	var lock sync.Mutex
	pullCalls := 0
	ackCursor := uint64(0)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/v1/sync/push":
			var input protocol.SyncPushRequest
			_ = json.NewDecoder(request.Body).Decode(&input)
			if len(input.Events) != 1 || input.Events[0].EventID != localEvent.EventID {
				t.Errorf("unexpected push: %+v", input)
			}
			_ = json.NewEncoder(w).Encode(protocol.SyncPushResponse{Acknowledgements: []protocol.SyncEventAck{{EventID: localEvent.EventID, Accepted: true, ServerSequence: 2}}})
		case "/v1/sync/hello":
			var input protocol.SyncHelloRequest
			_ = json.NewDecoder(request.Body).Decode(&input)
			if err := protocol.VerifySyncHello(input, base64.RawURLEncoding.EncodeToString(localPublic), time.Now()); err != nil {
				t.Errorf("invalid hello: %v", err)
			}
			_ = json.NewEncoder(w).Encode(protocol.SyncHelloResponse{ProtocolVersion: protocol.ProtocolVersion, Capabilities: protocol.ServerSyncCapabilities, Tasks: []protocol.SyncTaskPlan{{Task: task, ServerCursor: 1}}})
		case "/v1/sync/pull":
			lock.Lock()
			pullCalls++
			lock.Unlock()
			_ = json.NewEncoder(w).Encode(protocol.SyncPullResponse{Devices: []protocol.Device{{ID: "device_remote", AccountID: "acct", PublicKey: base64.RawURLEncoding.EncodeToString(remotePublic)}}, Batches: []protocol.SyncTaskBatch{{Task: task, Events: []protocol.Event{remoteEvent}, NextCursor: 1}}})
		case "/v1/sync/ack":
			var input protocol.SyncAckRequest
			_ = json.NewDecoder(request.Body).Decode(&input)
			lock.Lock()
			for _, value := range input.Tasks {
				if value.Cursor > ackCursor {
					ackCursor = value.Cursor
				}
			}
			lock.Unlock()
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()
	localStore, err := OpenLocalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer localStore.Close()
	engine := SyncEngine{API: client.New(server.URL, "token"), State: stateStore, Outbox: outbox, Local: localStore}
	report, err := engine.SyncOnce(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if report.Pushed != 1 || report.Pulled != 1 || report.Acknowledged != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}
	if length, _ := outbox.Len(); length != 0 {
		t.Fatalf("outbox was not acknowledged: %d", length)
	}
	events, err := localStore.ListEvents(task.ID, 0, 10)
	if err != nil || len(events) != 1 || events[0].EventID != remoteEvent.EventID {
		t.Fatalf("remote event not persisted: %+v err=%v", events, err)
	}
	lock.Lock()
	if pullCalls != 1 || ackCursor != 1 {
		t.Fatalf("pullCalls=%d ackCursor=%d", pullCalls, ackCursor)
	}
	lock.Unlock()
	second, err := engine.SyncOnce(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if second.Pulled != 0 {
		t.Fatalf("second sync duplicated pull: %+v", second)
	}
}
