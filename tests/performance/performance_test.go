package performance

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	artifactstore "github.com/agent-continuity/agent-continuity-master/internal/artifact"
	"github.com/agent-continuity/agent-continuity-master/internal/reconcile"
	"github.com/agent-continuity/agent-continuity-master/internal/storage/memory"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

func TestLocalBacklogReplayAndReconciliationBudget(t *testing.T) {
	store, task := fixture(t)
	const count = 1000
	events := make([]protocol.Event, 0, count)
	parent := ""
	started := time.Now()
	for index := 1; index <= count; index++ {
		payload := json.RawMessage(fmt.Sprintf(`{"index":%d}`, index))
		parents := []string(nil)
		if parent != "" {
			parents = []string{parent}
		}
		id := fmt.Sprintf("event_%04d", index)
		value := protocol.Event{EventID: id, TaskID: task.ID, BranchID: "main", AccountID: task.AccountID, DeviceID: "device_perf", ParentEventIDs: parents, LogicalTime: protocol.HLC{WallTimeMS: int64(index), Node: "device_perf"}, DeviceSequence: uint64(index), Type: "progress.updated", SchemaVersion: protocol.SchemaVersion, ContentType: "application/json", ContentDigest: protocol.Digest(payload), Payload: payload, CreatedAtClient: time.Now(), IdempotencyKey: id}
		if _, err := store.AppendEvent(context.Background(), value); err != nil {
			t.Fatal(err)
		}
		events = append(events, value)
		parent = id
	}
	replayDuration := time.Since(started)
	started = time.Now()
	result := reconcile.Analyze(events)
	reconcileDuration := time.Since(started)
	if len(result.Ordered) != count || len(result.Heads) != 1 || result.Heads[0] != parent {
		t.Fatalf("backlog did not converge: ordered=%d heads=%v", len(result.Ordered), result.Heads)
	}
	if replayDuration > 10*time.Second || reconcileDuration > 2*time.Second {
		t.Fatalf("bounded local SLO exceeded: replay=%s reconciliation=%s", replayDuration, reconcileDuration)
	}
	t.Logf("1000-event replay=%s (%.0f events/s), reconciliation=%s", replayDuration, count/replayDuration.Seconds(), reconcileDuration)
}

func TestLocalArtifactThroughputAndIntegrityBudget(t *testing.T) {
	service, err := artifactstore.New(t.TempDir(), 32<<20)
	if err != nil {
		t.Fatal(err)
	}
	content := strings.Repeat("0123456789abcdef", 1<<20) // 16 MiB
	digest := protocol.Digest([]byte(content))
	started := time.Now()
	ref, size, err := service.Put(strings.NewReader(content), digest)
	duration := time.Since(started)
	if err != nil || size != int64(len(content)) || service.Verify(ref, digest) != nil {
		t.Fatalf("artifact throughput fixture failed: size=%d err=%v", size, err)
	}
	if duration > 10*time.Second {
		t.Fatalf("local Artifact write SLO exceeded: %s", duration)
	}
	t.Logf("Artifact write=%s (%.1f MiB/s)", duration, float64(size)/(1<<20)/duration.Seconds())
}

func BenchmarkAppendBacklog(b *testing.B) {
	for iteration := 0; iteration < b.N; iteration++ {
		store, task := benchmarkFixture(b, iteration)
		for index := 1; index <= 1000; index++ {
			payload := json.RawMessage(`{"progress":50}`)
			id := fmt.Sprintf("event_%d_%d", iteration, index)
			value := protocol.Event{EventID: id, TaskID: task.ID, BranchID: "main", AccountID: task.AccountID, DeviceID: "device_perf", LogicalTime: protocol.HLC{WallTimeMS: int64(index), Node: "device_perf"}, DeviceSequence: uint64(index), Type: "progress.updated", SchemaVersion: protocol.SchemaVersion, ContentType: "application/json", ContentDigest: protocol.Digest(payload), Payload: payload, CreatedAtClient: time.Now(), IdempotencyKey: id}
			if _, err := store.AppendEvent(context.Background(), value); err != nil {
				b.Fatal(err)
			}
		}
	}
}

func fixture(t *testing.T) (*memory.Store, protocol.Task) {
	t.Helper()
	store := memory.New()
	now := time.Now()
	account := protocol.Account{ID: "acct_perf", Name: "performance", CreatedAt: now}
	if err := store.CreateAccount(context.Background(), account); err != nil {
		t.Fatal(err)
	}
	if err := store.RegisterDevice(context.Background(), protocol.Device{ID: "device_perf", AccountID: account.ID, Name: "performance", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	task := protocol.Task{ID: "task_perf", AccountID: account.ID, Title: "Performance", Objective: "bounded replay", Status: "active", HeadEventIDs: []string{}, CreatedAt: now, UpdatedAt: now}
	if err := store.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	return store, task
}

func benchmarkFixture(b *testing.B, iteration int) (*memory.Store, protocol.Task) {
	b.Helper()
	store := memory.New()
	now := time.Now()
	account := protocol.Account{ID: fmt.Sprintf("acct_%d", iteration), Name: "performance", CreatedAt: now}
	if err := store.CreateAccount(context.Background(), account); err != nil {
		b.Fatal(err)
	}
	if err := store.RegisterDevice(context.Background(), protocol.Device{ID: "device_perf", AccountID: account.ID, Name: "performance", CreatedAt: now}); err != nil {
		b.Fatal(err)
	}
	task := protocol.Task{ID: fmt.Sprintf("task_%d", iteration), AccountID: account.ID, Title: "Performance", Objective: "bounded replay", Status: "active", HeadEventIDs: []string{}, CreatedAt: now, UpdatedAt: now}
	if err := store.CreateTask(context.Background(), task); err != nil {
		b.Fatal(err)
	}
	return store, task
}
