package simulation

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	mathrand "math/rand/v2"
	"testing"
	"time"

	"github.com/agent-continuity/agent-continuity-master/internal/storage/memory"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

type simulatedDevice struct {
	value   protocol.Device
	private ed25519.PrivateKey
	seq     uint64
}

func TestDistributedDeviceCounts(t *testing.T) {
	for _, count := range []int{2, 10, 100} {
		t.Run(string(rune('A'+count%26)), func(t *testing.T) {
			store, account, task, devices := simulation(t, count)
			started := time.Now()
			var root string
			for index, device := range devices {
				parents := []string{}
				if index > 0 {
					parents = []string{root}
				}
				event := signedEvent(t, account, task, device, parents, time.Now().UTC(), "distributed")
				if index == 0 {
					root = event.EventID
				}
				if _, err := store.AppendEvent(context.Background(), event); err != nil {
					t.Fatal(err)
				}
			}
			items, err := store.ListEvents(context.Background(), task.ID, 0, count+1, true)
			if err != nil || len(items) != count {
				t.Fatalf("devices=%d events=%d err=%v", count, len(items), err)
			}
			if count == 100 && time.Since(started) > 5*time.Second {
				t.Fatalf("100-device behavior simulation exceeded five seconds: %s", time.Since(started))
			}
		})
	}
}

func TestOfflineDurationsAndLongBacklog(t *testing.T) {
	store, account, task, devices := simulation(t, 1)
	for _, offline := range []time.Duration{time.Minute, time.Hour, 24 * time.Hour, 30 * 24 * time.Hour} {
		event := signedEvent(t, account, task, devices[0], nil, time.Now().Add(-offline), "offline-duration")
		if err := protocol.ValidateEvent(event, time.Now()); err != nil {
			t.Fatalf("offline duration %s was rejected: %v", offline, err)
		}
	}
	parent := ""
	backlog := make([]protocol.Event, 1000)
	for index := range backlog {
		parents := []string{}
		if parent != "" {
			parents = []string{parent}
		}
		backlog[index] = signedEvent(t, account, task, devices[0], parents, time.Now().Add(-24*time.Hour), "offline-backlog")
		parent = backlog[index].EventID
	}
	for index := len(backlog) - 1; index >= 0; index-- {
		first, err := store.AppendEvent(context.Background(), backlog[index])
		if err != nil {
			t.Fatal(err)
		}
		second, err := store.AppendEvent(context.Background(), backlog[index])
		if err != nil || !second.Duplicate || first.Event.ServerSequence != second.Event.ServerSequence {
			t.Fatalf("backlog duplicate failed at %d: %+v %+v %v", index, first, second, err)
		}
	}
	items, err := listAll(store, task.ID)
	if err != nil || len(items) != len(backlog) {
		t.Fatalf("backlog convergence failed: events=%d err=%v", len(items), err)
	}
}

func listAll(store *memory.Store, taskID string) ([]protocol.Event, error) {
	result := []protocol.Event{}
	cursor := uint64(0)
	for {
		batch, err := store.ListEvents(context.Background(), taskID, cursor, 200, false)
		if err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			return result, nil
		}
		result = append(result, batch...)
		cursor = batch[len(batch)-1].ServerSequence
	}
}

func TestPacketLossDuplicationReorderAndPartitionConverge(t *testing.T) {
	store, account, task, devices := simulation(t, 2)
	queue := make([]protocol.Event, 300)
	parent := ""
	for index := range queue {
		device := devices[index%len(devices)]
		parents := []string{}
		if parent != "" {
			parents = []string{parent}
		}
		queue[index] = signedEvent(t, account, task, device, parents, time.Now().UTC(), "chaos")
		parent = queue[index].EventID
	}
	mathrand.Shuffle(len(queue), func(i, j int) { queue[i], queue[j] = queue[j], queue[i] })
	for index, event := range queue {
		if index%4 == 0 { // simulated packet loss during the first partition window
			continue
		}
		if _, err := store.AppendEvent(context.Background(), event); err != nil {
			t.Fatal(err)
		}
		if index%3 == 0 { // duplicate packet
			if duplicate, err := store.AppendEvent(context.Background(), event); err != nil || !duplicate.Duplicate {
				t.Fatalf("duplicate delivery failed: %+v %v", duplicate, err)
			}
		}
	}
	for _, event := range queue { // partition heals and the complete durable outbox replays
		if _, err := store.AppendEvent(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	items, err := store.ListEvents(context.Background(), task.ID, 0, 500, false)
	if err != nil || len(items) != len(queue) {
		t.Fatalf("chaos replay lost or duplicated data: got=%d want=%d err=%v", len(items), len(queue), err)
	}
	updated, _ := store.GetTask(context.Background(), task.ID)
	if len(updated.HeadEventIDs) != 1 || updated.HeadEventIDs[0] != parent {
		t.Fatalf("causal head did not converge: %v want %s", updated.HeadEventIDs, parent)
	}
}

func simulation(t *testing.T, deviceCount int) (*memory.Store, protocol.Account, protocol.Task, []*simulatedDevice) {
	t.Helper()
	store := memory.New()
	now := time.Now().UTC()
	account := protocol.Account{ID: "acct_sim", Name: "simulation", CreatedAt: now}
	if err := store.CreateAccount(context.Background(), account); err != nil {
		t.Fatal(err)
	}
	devices := make([]*simulatedDevice, deviceCount)
	for index := range devices {
		public, private, _ := ed25519.GenerateKey(rand.Reader)
		value := protocol.Device{ID: "device_" + protocol.Digest([]byte{byte(index)})[7:23], AccountID: account.ID, Name: "simulated", PublicKey: base64.RawURLEncoding.EncodeToString(public), CreatedAt: now}
		if err := store.RegisterDevice(context.Background(), value); err != nil {
			t.Fatal(err)
		}
		devices[index] = &simulatedDevice{value: value, private: private}
	}
	task := protocol.Task{ID: "task_sim", AccountID: account.ID, Title: "simulation", Objective: "converge", Status: "active", HeadEventIDs: []string{}, CreatedAt: now, UpdatedAt: now}
	if err := store.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	return store, account, task, devices
}

func signedEvent(t *testing.T, account protocol.Account, task protocol.Task, device *simulatedDevice, parents []string, createdAt time.Time, label string) protocol.Event {
	t.Helper()
	device.seq++
	id, _ := protocol.NewID("event")
	payload, _ := json.Marshal(map[string]any{"label": label, "sequence": device.seq})
	value := protocol.Event{EventID: id, TaskID: task.ID, BranchID: "main", AccountID: account.ID, DeviceID: device.value.ID, ParentEventIDs: parents, LogicalTime: protocol.HLC{WallTimeMS: createdAt.UnixMilli(), Counter: uint32(device.seq), Node: device.value.ID}, DeviceSequence: device.seq, Type: "progress.updated", SchemaVersion: protocol.SchemaVersion, ContentType: "application/json", ContentDigest: protocol.Digest(payload), Payload: payload, CreatedAtClient: createdAt, IdempotencyKey: id}
	if err := protocol.SignEvent(&value, device.private); err != nil {
		t.Fatal(err)
	}
	return value
}
