package daemon

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

func TestOutboxSurvivesReopenAndAcknowledges(t *testing.T) {
	root := t.TempDir()
	outbox, err := OpenOutbox(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []protocol.Event{{EventID: "second", DeviceSequence: 2}, {EventID: "first", DeviceSequence: 1}} {
		if err := outbox.Enqueue(event); err != nil {
			t.Fatal(err)
		}
	}
	reopened, err := OpenOutbox(root)
	if err != nil {
		t.Fatal(err)
	}
	items, err := reopened.Peek(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].EventID != "first" {
		t.Fatalf("unexpected durable order: %+v", items)
	}
	if err := reopened.Ack("first"); err != nil {
		t.Fatal(err)
	}
	if length, _ := reopened.Len(); length != 1 {
		t.Fatalf("expected one pending event, got %d", length)
	}
}

func TestOutboxPersistsExponentialBackoffAndAckClearsIt(t *testing.T) {
	root := t.TempDir()
	outbox, _ := OpenOutbox(root)
	event := protocol.Event{EventID: "retry", DeviceSequence: 1}
	if err := outbox.Enqueue(event); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1000, 0).UTC()
	first, err := outbox.RecordFailure(event.EventID, errors.New("temporary"), now)
	if err != nil || first.Attempts != 1 || !first.NextAttempt.After(now) {
		t.Fatalf("first retry=%+v err=%v", first, err)
	}
	reopened, _ := OpenOutbox(root)
	items, delay, err := reopened.Ready(10, now)
	if err != nil || len(items) != 0 || delay <= 0 {
		t.Fatalf("backoff was not preserved: items=%v delay=%v err=%v", items, delay, err)
	}
	second, err := reopened.RecordFailure(event.EventID, errors.New("temporary again"), first.NextAttempt)
	if err != nil || second.Attempts != 2 || second.NextAttempt.Sub(first.NextAttempt) <= first.NextAttempt.Sub(now) {
		t.Fatalf("backoff did not grow: first=%+v second=%+v err=%v", first, second, err)
	}
	if err := reopened.Ack(event.EventID); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.RetryInfo(event.EventID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retry state remained after ack: %v", err)
	}
}

func TestOutboxEnqueueIsIdempotent(t *testing.T) {
	outbox, _ := OpenOutbox(t.TempDir())
	event := protocol.Event{EventID: "same", DeviceSequence: 1}
	if err := outbox.Enqueue(event); err != nil {
		t.Fatal(err)
	}
	if err := outbox.Enqueue(event); err != nil {
		t.Fatal(err)
	}
	if length, _ := outbox.Len(); length != 1 {
		t.Fatalf("expected one event, got %d", length)
	}
}
