package protocol

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"
	"time"
)

func TestSyncHelloProofIsCanonicalAndTamperEvident(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	value := SyncHelloRequest{ProtocolVersions: []string{"2.0", ProtocolVersion}, Capabilities: []string{SyncCapabilityE2EE, SyncCapabilityCursorPull}, DeviceID: "device", Nonce: "0123456789abcdef", Timestamp: now, Tasks: []SyncTaskState{{TaskID: "b", HeadEventIDs: []string{"z", "a"}}, {TaskID: "a"}}}
	if err := SignSyncHello(&value, privateKey); err != nil {
		t.Fatal(err)
	}
	if err := VerifySyncHello(value, base64.RawURLEncoding.EncodeToString(publicKey), now); err != nil {
		t.Fatal(err)
	}
	value.Capabilities = append(value.Capabilities, SyncCapabilityBatchPush)
	if err := VerifySyncHello(value, base64.RawURLEncoding.EncodeToString(publicKey), now); err == nil {
		t.Fatal("tampered hello proof verified")
	}
}

func TestCapabilityNegotiationUsesIntersection(t *testing.T) {
	got := NegotiateCapabilities([]string{"unknown", SyncCapabilityE2EE, SyncCapabilityCursorPull, SyncCapabilityE2EE}, []string{SyncCapabilityCursorPull, SyncCapabilityE2EE})
	if len(got) != 2 || got[0] != SyncCapabilityCursorPull || got[1] != SyncCapabilityE2EE {
		t.Fatalf("unexpected capabilities: %v", got)
	}
}
