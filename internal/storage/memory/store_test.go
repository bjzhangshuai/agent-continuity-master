package memory

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/agent-continuity/agent-continuity-master/internal/storage"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

func fixture(t *testing.T) (*Store, ed25519.PrivateKey, protocol.Task, protocol.Device) {
	t.Helper()
	store := New()
	ctx := context.Background()
	now := time.Now().UTC()
	account := protocol.Account{ID: "acct_a", Name: "A", CreatedAt: now}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	device := protocol.Device{ID: "device_a", AccountID: account.ID, Name: "A", PublicKey: base64.RawURLEncoding.EncodeToString(pub), CreatedAt: now}
	if err := store.RegisterDevice(ctx, device); err != nil {
		t.Fatal(err)
	}
	task := protocol.Task{ID: "task_a", AccountID: account.ID, Title: "Continuity", Objective: "test", Status: "active", CreatedAt: now, UpdatedAt: now}
	if err := store.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	return store, private, task, device
}

func signedEvent(t *testing.T, private ed25519.PrivateKey, task protocol.Task, device protocol.Device, id string, seq uint64, parents []string) protocol.Event {
	t.Helper()
	payload := json.RawMessage(`{"text":"hello"}`)
	event := protocol.Event{EventID: id, TaskID: task.ID, BranchID: "main", AccountID: task.AccountID, DeviceID: device.ID, ParentEventIDs: parents, LogicalTime: protocol.HLC{WallTimeMS: time.Now().UnixMilli(), Node: device.ID}, DeviceSequence: seq, Type: "human.message.created", SchemaVersion: protocol.SchemaVersion, ContentType: "application/json", ContentDigest: protocol.Digest(payload), Payload: payload, CreatedAtClient: time.Now().UTC(), IdempotencyKey: id}
	if err := protocol.SignEvent(&event, private); err != nil {
		t.Fatal(err)
	}
	return event
}

func TestAppendIsIdempotentAndUpdatesHeads(t *testing.T) {
	store, private, task, device := fixture(t)
	ctx := context.Background()
	event := signedEvent(t, private, task, device, "event_1", 1, nil)
	first, err := store.AppendEvent(ctx, event)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.AppendEvent(ctx, event)
	if err != nil {
		t.Fatal(err)
	}
	if first.Duplicate || !second.Duplicate || first.Event.ServerSequence != second.Event.ServerSequence {
		t.Fatalf("unexpected results: first=%+v second=%+v", first, second)
	}
	got, _ := store.GetTask(ctx, task.ID)
	if len(got.HeadEventIDs) != 1 || got.HeadEventIDs[0] != event.EventID {
		t.Fatalf("unexpected heads: %v", got.HeadEventIDs)
	}
}

func TestConcurrentNonConflictingEventAutoMergesWithoutDroppingWork(t *testing.T) {
	store, private, task, device := fixture(t)
	ctx := context.Background()
	a := signedEvent(t, private, task, device, "event_a", 1, nil)
	if _, err := store.AppendEvent(ctx, a); err != nil {
		t.Fatal(err)
	}
	b := signedEvent(t, private, task, device, "event_b", 2, nil)
	result, err := store.AppendEvent(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Conflicts) != 1 || result.Conflicts[0].Kind != "non_conflicting_concurrent" || !result.Conflicts[0].Resolved {
		t.Fatalf("expected deterministic concurrent merge, got %+v", result.Conflicts)
	}
	if len(result.TaskHeads) != 2 {
		t.Fatalf("expected two heads, got %v", result.TaskHeads)
	}
	items, _ := store.ListEvents(ctx, task.ID, 0, 10, true)
	if len(items) != 2 {
		t.Fatalf("event was lost: %d", len(items))
	}
}

func TestConcurrentSemanticUpdatesRequireExplicitResolution(t *testing.T) {
	store, private, task, device := fixture(t)
	ctx := context.Background()
	a := signedEvent(t, private, task, device, "event_a", 1, nil)
	a.Type = "progress.updated"
	if err := protocol.SignEvent(&a, private); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(ctx, a); err != nil {
		t.Fatal(err)
	}
	b := signedEvent(t, private, task, device, "event_b", 2, nil)
	b.Type = "progress.updated"
	if err := protocol.SignEvent(&b, private); err != nil {
		t.Fatal(err)
	}
	result, err := store.AppendEvent(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Conflicts) != 1 || result.Conflicts[0].Kind != "semantic_conflict" || result.Conflicts[0].Resolved {
		t.Fatalf("expected unresolved semantic conflict, got %+v", result.Conflicts)
	}
}

func TestDeviceSequenceReplayRejected(t *testing.T) {
	store, private, task, device := fixture(t)
	ctx := context.Background()
	if _, err := store.AppendEvent(ctx, signedEvent(t, private, task, device, "event_a", 1, nil)); err != nil {
		t.Fatal(err)
	}
	_, err := store.AppendEvent(ctx, signedEvent(t, private, task, device, "event_b", 1, nil))
	if !errors.Is(err, storage.ErrSequenceReplay) {
		t.Fatalf("expected sequence replay, got %v", err)
	}
}

func TestOutOfOrderParentDoesNotBecomeFalseHead(t *testing.T) {
	store, private, task, device := fixture(t)
	ctx := context.Background()
	parent := signedEvent(t, private, task, device, "event_parent", 1, nil)
	child := signedEvent(t, private, task, device, "event_child", 2, []string{parent.EventID})
	if result, err := store.AppendEvent(ctx, child); err != nil || len(result.Conflicts) != 1 || result.Conflicts[0].Kind != "missing_parent" {
		t.Fatalf("expected deferred missing parent: result=%+v err=%v", result, err)
	}
	result, err := store.AppendEvent(ctx, parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.TaskHeads) != 1 || result.TaskHeads[0] != child.EventID {
		t.Fatalf("out-of-order replay produced false heads: %v", result.TaskHeads)
	}
}

func TestInvitationSingleUseAndPermission(t *testing.T) {
	store, _, task, _ := fixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := store.CreateAccount(ctx, protocol.Account{ID: "acct_b", Name: "B", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	token := "one-time-secret"
	hash := protocol.Digest([]byte(token))[len("sha256:"):]
	inv := protocol.Invitation{ID: "invite_1", TaskID: task.ID, InviterAccount: task.AccountID, Permission: protocol.PermissionRead, TokenHash: hash, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := store.CreateInvitation(ctx, inv); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcceptInvitation(ctx, token, "acct_b"); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckPermission(ctx, task.ID, "acct_b", protocol.PermissionRead); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcceptInvitation(ctx, token, "acct_b"); !errors.Is(err, storage.ErrInviteInvalid) {
		t.Fatalf("expected one-time rejection, got %v", err)
	}
}
