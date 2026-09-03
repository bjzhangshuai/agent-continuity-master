package e2ee

import (
	"encoding/base64"
	"testing"
)

func TestTaskKeyWrapAndPayloadRoundTrip(t *testing.T) {
	device, err := GenerateDeviceKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	taskKey, err := GenerateTaskKey()
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := WrapTaskKey("task_1", 1, taskKey, device.PublicKey, "device_1")
	if err != nil {
		t.Fatal(err)
	}
	unwrapped, err := UnwrapTaskKey("task_1", 1, envelope, device.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	plaintext := []byte(`{"secret":"portable"}`)
	aad := []byte("event_1")
	encrypted, err := EncryptPayload(unwrapped, 1, "application/json", plaintext, aad)
	if err != nil {
		t.Fatal(err)
	}
	decrypted, err := DecryptPayload(taskKey, encrypted, aad)
	if err != nil {
		t.Fatal(err)
	}
	if string(decrypted) != string(plaintext) {
		t.Fatalf("got %s", decrypted)
	}
}

func TestWrongDeviceAndTamperingFailClosed(t *testing.T) {
	a, _ := GenerateDeviceKeyPair()
	b, _ := GenerateDeviceKeyPair()
	taskKey, _ := GenerateTaskKey()
	envelope, err := WrapTaskKey("task_1", 1, taskKey, a.PublicKey, "device_a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := UnwrapTaskKey("task_1", 1, envelope, b.PrivateKey); err == nil {
		t.Fatal("wrong device unwrapped task key")
	}
	payload, err := EncryptPayload(taskKey, 1, "application/json", []byte("secret"), []byte("event"))
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, _ := base64.RawURLEncoding.DecodeString(payload.Ciphertext)
	ciphertext[0] ^= 1
	payload.Ciphertext = base64.RawURLEncoding.EncodeToString(ciphertext)
	if _, err := DecryptPayload(taskKey, payload, []byte("event")); err == nil {
		t.Fatal("tampered payload decrypted")
	}
}

func TestArtifactChunkEncryptionBindsVersionAndIndex(t *testing.T) {
	key, _ := GenerateTaskKey()
	ciphertext, metadata, err := EncryptArtifactChunk(key, 2, "artifact_a", "version_a", 3, []byte("chunk payload"))
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := DecryptArtifactChunk(key, "artifact_a", "version_a", metadata, ciphertext)
	if err != nil || string(plaintext) != "chunk payload" {
		t.Fatalf("round trip=%q err=%v", plaintext, err)
	}
	if _, err := DecryptArtifactChunk(key, "artifact_a", "version_b", metadata, ciphertext); err == nil {
		t.Fatal("artifact chunk was not bound to its version")
	}
	metadata.ChunkIndex++
	if _, err := DecryptArtifactChunk(key, "artifact_a", "version_a", metadata, ciphertext); err == nil {
		t.Fatal("artifact chunk was not bound to its index")
	}
}
