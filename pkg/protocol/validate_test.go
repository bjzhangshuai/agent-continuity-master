package protocol

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

func validEvent(t *testing.T) (Event, string) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	payload := json.RawMessage(`{"text":"portable"}`)
	value := Event{EventID: "event_1", TaskID: "task_1", BranchID: "main", AccountID: "acct_1", DeviceID: "device_1", ParentEventIDs: []string{}, LogicalTime: HLC{WallTimeMS: time.Now().UnixMilli(), Node: "device_1"}, DeviceSequence: 1, Type: "human.message.created", SchemaVersion: SchemaVersion, ContentType: "application/json", ContentDigest: Digest(payload), Payload: payload, CreatedAtClient: time.Now().UTC(), IdempotencyKey: "idem_1"}
	if err := SignEvent(&value, privateKey); err != nil {
		t.Fatal(err)
	}
	return value, base64.RawURLEncoding.EncodeToString(publicKey)
}

func TestSignedEventValidation(t *testing.T) {
	value, publicKey := validEvent(t)
	if err := ValidateEvent(value, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := VerifyEventSignature(value, publicKey); err != nil {
		t.Fatal(err)
	}
	value.Payload = json.RawMessage(`{"text":"tampered"}`)
	if err := VerifyEventSignature(value, publicKey); err == nil {
		t.Fatal("expected signature failure")
	}
}
func TestDigestMismatchRejected(t *testing.T) {
	value, _ := validEvent(t)
	value.ContentDigest = Digest([]byte("other"))
	if err := ValidateEvent(value, time.Now()); err == nil {
		t.Fatal("expected digest mismatch")
	}
}
