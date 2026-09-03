package adaptersdk

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

func TestEnvelopeSignsCausalAdapterEvents(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	records := []CapturedRecord{
		{ID: "input", Kind: RecordUserInput, OccurredAt: time.Unix(1, 0), Payload: json.RawMessage(`{"text":"hello"}`)},
		{ID: "output", Kind: RecordAgentOutput, OccurredAt: time.Unix(2, 0), Payload: json.RawMessage(`{"text":"world"}`)},
	}
	events, err := Envelope(records, EnvelopeOptions{TaskID: "task_1", AccountID: "acct_1", DeviceID: "device_1", SessionRef: "session_1", StartSequence: 10, ParentEventIDs: []string{"event_head"}, PrivateKey: privateKey})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].DeviceSequence != 11 || events[1].DeviceSequence != 12 || events[1].ParentEventIDs[0] != events[0].EventID {
		t.Fatalf("invalid envelope: %+v", events)
	}
	for _, event := range events {
		if err := protocol.VerifyEventSignature(event, encodePublicKey(publicKey)); err != nil {
			t.Fatal(err)
		}
	}
	duplicate, err := Envelope(records, EnvelopeOptions{TaskID: "task_1", AccountID: "acct_1", DeviceID: "device_1", SessionRef: "session_1", StartSequence: 10, ParentEventIDs: []string{"event_head"}, PrivateKey: privateKey})
	if err != nil || duplicate[0].EventID != events[0].EventID {
		t.Fatalf("envelope is not idempotent: %v", err)
	}
}

func TestJSONLReaderLeavesIncompleteRecordForNextCapture(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	if err := os.WriteFile(path, []byte("{\"type\":\"one\"}\n{\"type\":"), 0o600); err != nil {
		t.Fatal(err)
	}
	reader := JSONLReader{Root: root}
	count := 0
	cursor, err := reader.Read(context.Background(), path, CaptureCursor{}, func([]byte, uint64) error { count++; return nil })
	if err != nil || count != 1 {
		t.Fatalf("first capture count=%d err=%v", count, err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("\"two\"}\n"); err != nil {
		t.Fatal(err)
	}
	file.Close()
	count = 0
	_, err = reader.Read(context.Background(), path, cursor, func(line []byte, _ uint64) error {
		count++
		if !json.Valid(line) {
			return errors.New("invalid record")
		}
		return nil
	})
	if err != nil || count != 1 {
		t.Fatalf("resumed capture count=%d err=%v", count, err)
	}
}

func encodePublicKey(key ed25519.PublicKey) string {
	return base64.RawURLEncoding.EncodeToString(key)
}
