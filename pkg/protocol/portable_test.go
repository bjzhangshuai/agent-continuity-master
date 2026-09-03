package protocol

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPortableSignaturesAreAcceptedAndTamperEvident(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	payload := json.RawMessage(`{"z":"<tag>","a":"你好"}`)
	value := Event{EventID: "event_portable", TaskID: "task_1", BranchID: "main", AccountID: "acct_1", DeviceID: "device_1", ParentEventIDs: []string{"event_z", "event_a"}, LogicalTime: HLC{WallTimeMS: 1800000000000, Counter: 2, Node: "device_1"}, DeviceSequence: 7, Type: "progress.updated", SchemaVersion: SchemaVersion, ContentType: "application/json", ContentDigest: Digest(payload), Payload: payload, CreatedAtClient: time.UnixMilli(1800000000000).UTC(), IdempotencyKey: "idem_1"}
	if err := SignPortableEvent(&value, privateKey); err != nil {
		t.Fatal(err)
	}
	if err := VerifyEventSignature(value, base64.RawURLEncoding.EncodeToString(publicKey)); err != nil {
		t.Fatal(err)
	}
	value.ContentDigest = Digest([]byte(`{"changed":true}`))
	if err := VerifyEventSignature(value, base64.RawURLEncoding.EncodeToString(publicKey)); err == nil {
		t.Fatal("tampered portable event was accepted")
	}
}

func TestPythonAndTypeScriptConformanceFixtures(t *testing.T) {
	for _, name := range []string{"python-event.json", "typescript-event.json"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "tests", "conformance", name))
		if err != nil {
			t.Fatal(err)
		}
		var fixture struct {
			PublicKey string `json:"public_key"`
			Event     Event  `json:"event"`
		}
		if err := json.Unmarshal(data, &fixture); err != nil {
			t.Fatal(err)
		}
		if err := ValidateEvent(fixture.Event, fixture.Event.CreatedAtClient.Add(time.Minute)); err != nil {
			t.Fatalf("%s validation: %v", name, err)
		}
		if err := VerifyEventSignature(fixture.Event, fixture.PublicKey); err != nil {
			t.Fatalf("%s signature: %v", name, err)
		}
	}
}

func TestPortableSyncHello(t *testing.T) {
	publicKey, privateKey, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Now().UTC()
	value := SyncHelloRequest{ProtocolVersions: []string{"1.0"}, Capabilities: []string{SyncCapabilityE2EE, SyncCapabilityCursorPull}, ClientVersion: "cross-device", DeviceID: "device_1", Nonce: "0123456789abcdef01234567", Timestamp: now, Tasks: []SyncTaskState{{TaskID: "task_b", HeadEventIDs: []string{"z", "a"}, EventCursor: 4}}}
	if err := SignPortableSyncHello(&value, privateKey); err != nil {
		t.Fatal(err)
	}
	if err := VerifySyncHello(value, base64.RawURLEncoding.EncodeToString(publicKey), now); err != nil {
		t.Fatal(err)
	}
}
