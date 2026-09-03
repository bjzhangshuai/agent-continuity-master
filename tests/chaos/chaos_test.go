package chaos

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agent-continuity/agent-continuity-master/internal/daemon"
	clock "github.com/agent-continuity/agent-continuity-master/internal/event"
	"github.com/agent-continuity/agent-continuity-master/pkg/p2p"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

func TestProcessCrashBetweenInboxStageAndProjectionRecovers(t *testing.T) {
	root := t.TempDir()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	payload := json.RawMessage(`{"after":"crash"}`)
	event := protocol.Event{EventID: "event_crash_recovery", TaskID: "task_crash", BranchID: "main", AccountID: "acct", DeviceID: "device_remote", LogicalTime: protocol.HLC{WallTimeMS: time.Now().UnixMilli(), Node: "device_remote"}, DeviceSequence: 1, ServerSequence: 1, Type: "progress.updated", SchemaVersion: protocol.SchemaVersion, ContentType: "application/json", ContentDigest: protocol.Digest(payload), Payload: payload, CreatedAtClient: time.Now().UTC(), ReceivedAtServer: time.Now().UTC(), IdempotencyKey: "crash-recovery"}
	if err := protocol.SignEvent(&event, privateKey); err != nil {
		t.Fatal(err)
	}
	response := protocol.SyncPullResponse{Devices: []protocol.Device{{ID: event.DeviceID, AccountID: event.AccountID, PublicKey: base64.RawURLEncoding.EncodeToString(publicKey)}}, Batches: []protocol.SyncTaskBatch{{Task: protocol.Task{ID: event.TaskID, AccountID: event.AccountID, Title: "Crash safe", Objective: "recover", Status: "active", HeadEventIDs: []string{event.EventID}, CreatedAt: time.Now(), UpdatedAt: time.Now()}, Events: []protocol.Event{event}, NextCursor: 1}}}
	store, err := daemon.OpenLocalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.StagePull(response); err != nil {
		t.Fatal(err)
	}
	// Closing here models a kill after durable Inbox commit but before projection.
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := daemon.OpenLocalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	acks, err := restarted.ReconcileInbox()
	if err != nil || len(acks) != 1 || acks[0].Cursor != 1 {
		t.Fatalf("crash recovery acknowledgements: %+v err=%v", acks, err)
	}
	events, err := restarted.ListEvents(event.TaskID, 0, 10)
	if err != nil || len(events) != 1 || events[0].EventID != event.EventID {
		t.Fatalf("durable Inbox did not project exactly once: %+v err=%v", events, err)
	}
}

func TestQueueFailureAndRestartRetainsOutboxForRetry(t *testing.T) {
	root := t.TempDir()
	outbox, err := daemon.OpenOutbox(root)
	if err != nil {
		t.Fatal(err)
	}
	event := protocol.Event{EventID: "event_outage", TaskID: "task_outage", IdempotencyKey: "outage"}
	if err := outbox.Enqueue(event); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	retry, err := outbox.RecordFailure(event.EventID, errors.New("queue unavailable"), now)
	if err != nil || retry.Attempts != 1 || !retry.NextAttempt.After(now) {
		t.Fatalf("retry state: %+v err=%v", retry, err)
	}
	restarted, err := daemon.OpenOutbox(root)
	if err != nil {
		t.Fatal(err)
	}
	if length, _ := restarted.Len(); length != 1 {
		t.Fatalf("process restart lost queued event: %d", length)
	}
	items, _, err := restarted.Ready(10, retry.NextAttempt.Add(time.Millisecond))
	if err != nil || len(items) != 1 || items[0].EventID != event.EventID {
		t.Fatalf("queued event did not become retryable: %+v err=%v", items, err)
	}
	if err := restarted.Ack(event.EventID); err != nil {
		t.Fatal(err)
	}
	if length, _ := restarted.Len(); length != 0 {
		t.Fatalf("acknowledged event remained queued: %d", length)
	}
}

func TestObjectStorageTimeoutNeverPublishesPartialDestination(t *testing.T) {
	content := strings.Repeat("slow-object", 1024)
	digest := protocol.Digest([]byte(content))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-SHA256", digest)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(content[:100]))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	destination := filepath.Join(t.TempDir(), "artifact.bin")
	err := (p2p.Receiver{Client: server.Client()}).Download(ctx, server.URL+"/v1/artifacts/"+strings.TrimPrefix(digest, "sha256:"), "p2p_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", digest, destination)
	if err == nil {
		t.Fatal("storage timeout unexpectedly succeeded")
	}
	if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("partial object became visible at final destination: %v", statErr)
	}
}

func TestClockSkewFailsClosedWhileHLCRemainsMonotonic(t *testing.T) {
	payload := json.RawMessage(`{}`)
	event := protocol.Event{EventID: "event_future", TaskID: "task", BranchID: "main", AccountID: "acct", DeviceID: "device", LogicalTime: protocol.HLC{WallTimeMS: time.Now().Add(25 * time.Hour).UnixMilli(), Node: "device"}, DeviceSequence: 1, Type: "progress.updated", SchemaVersion: protocol.SchemaVersion, ContentType: "application/json", ContentDigest: protocol.Digest(payload), Payload: payload, CreatedAtClient: time.Now().Add(25 * time.Hour), Signature: "AA", IdempotencyKey: "future"}
	if err := protocol.ValidateEvent(event, time.Now()); err == nil {
		t.Fatal("event outside the clock-skew window was accepted")
	}
	value := clock.NewClock("local")
	first := value.Now()
	second := value.Observe(protocol.HLC{WallTimeMS: time.Now().Add(5 * time.Second).UnixMilli(), Counter: 7, Node: "remote"})
	third := value.Now()
	if clock.Compare(first, second) >= 0 || clock.Compare(second, third) >= 0 {
		t.Fatalf("HLC regressed under skew: first=%+v second=%+v third=%+v", first, second, third)
	}
}
