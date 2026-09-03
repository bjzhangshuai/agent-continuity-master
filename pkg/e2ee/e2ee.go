package e2ee

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

const Algorithm = "X25519-HKDF-SHA256+A256GCM"

type DeviceKeyPair struct {
	PrivateKey string `json:"private_key"`
	PublicKey  string `json:"public_key"`
}

func GenerateDeviceKeyPair() (DeviceKeyPair, error) {
	privateKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return DeviceKeyPair{}, fmt.Errorf("generate X25519 key: %w", err)
	}
	return DeviceKeyPair{PrivateKey: encode(privateKey.Bytes()), PublicKey: encode(privateKey.PublicKey().Bytes())}, nil
}

func GenerateTaskKey() ([]byte, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate task key: %w", err)
	}
	return key, nil
}

func WrapTaskKey(taskID string, version uint32, taskKey []byte, recipientPublicKey string, deviceID string) (protocol.WrappedTaskKey, error) {
	if taskID == "" || deviceID == "" || version == 0 || len(taskKey) != 32 {
		return protocol.WrappedTaskKey{}, errors.New("task ID, device ID, positive version, and 32-byte task key are required")
	}
	recipientRaw, err := decode32(recipientPublicKey)
	if err != nil {
		return protocol.WrappedTaskKey{}, fmt.Errorf("recipient key: %w", err)
	}
	recipient, err := ecdh.X25519().NewPublicKey(recipientRaw)
	if err != nil {
		return protocol.WrappedTaskKey{}, err
	}
	ephemeral, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return protocol.WrappedTaskKey{}, err
	}
	shared, err := ephemeral.ECDH(recipient)
	if err != nil {
		return protocol.WrappedTaskKey{}, err
	}
	key := deriveKey(shared, taskID, deviceID, version, ephemeral.PublicKey().Bytes(), recipientRaw)
	block, err := aes.NewCipher(key)
	if err != nil {
		return protocol.WrappedTaskKey{}, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return protocol.WrappedTaskKey{}, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return protocol.WrappedTaskKey{}, err
	}
	ciphertext := aead.Seal(nil, nonce, taskKey, associatedData(taskID, deviceID, version))
	return protocol.WrappedTaskKey{DeviceID: deviceID, EphemeralPublicKey: encode(ephemeral.PublicKey().Bytes()), Nonce: encode(nonce), Ciphertext: encode(ciphertext)}, nil
}

func UnwrapTaskKey(taskID string, version uint32, envelope protocol.WrappedTaskKey, recipientPrivateKey string) ([]byte, error) {
	privateRaw, err := decode32(recipientPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("private key: %w", err)
	}
	privateKey, err := ecdh.X25519().NewPrivateKey(privateRaw)
	if err != nil {
		return nil, err
	}
	ephemeralRaw, err := decode32(envelope.EphemeralPublicKey)
	if err != nil {
		return nil, fmt.Errorf("ephemeral key: %w", err)
	}
	ephemeral, err := ecdh.X25519().NewPublicKey(ephemeralRaw)
	if err != nil {
		return nil, err
	}
	shared, err := privateKey.ECDH(ephemeral)
	if err != nil {
		return nil, err
	}
	key := deriveKey(shared, taskID, envelope.DeviceID, version, ephemeralRaw, privateKey.PublicKey().Bytes())
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce, err := base64.RawURLEncoding.DecodeString(envelope.Nonce)
	if err != nil {
		return nil, errors.New("invalid nonce")
	}
	ciphertext, err := base64.RawURLEncoding.DecodeString(envelope.Ciphertext)
	if err != nil {
		return nil, errors.New("invalid ciphertext")
	}
	plaintext, err := aead.Open(nil, nonce, ciphertext, associatedData(taskID, envelope.DeviceID, version))
	if err != nil {
		return nil, errors.New("task key authentication failed")
	}
	if len(plaintext) != 32 {
		return nil, errors.New("invalid unwrapped task key")
	}
	return plaintext, nil
}

func EncryptPayload(taskKey []byte, keyVersion uint32, contentType string, plaintext []byte, aad []byte) (protocol.EncryptedPayload, error) {
	if len(taskKey) != 32 || keyVersion == 0 || contentType == "" {
		return protocol.EncryptedPayload{}, errors.New("32-byte task key, positive version, and content type are required")
	}
	block, err := aes.NewCipher(taskKey)
	if err != nil {
		return protocol.EncryptedPayload{}, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return protocol.EncryptedPayload{}, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return protocol.EncryptedPayload{}, err
	}
	ciphertext := aead.Seal(nil, nonce, plaintext, aad)
	return protocol.EncryptedPayload{Version: 1, Algorithm: "A256GCM", KeyVersion: keyVersion, Nonce: encode(nonce), Ciphertext: encode(ciphertext), OriginalContentType: contentType, PlaintextDigest: protocol.Digest(plaintext)}, nil
}

func DecryptPayload(taskKey []byte, payload protocol.EncryptedPayload, aad []byte) ([]byte, error) {
	if len(taskKey) != 32 || payload.Version != 1 || payload.Algorithm != "A256GCM" {
		return nil, errors.New("unsupported encrypted payload")
	}
	block, err := aes.NewCipher(taskKey)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce, err := base64.RawURLEncoding.DecodeString(payload.Nonce)
	if err != nil {
		return nil, errors.New("invalid nonce")
	}
	ciphertext, err := base64.RawURLEncoding.DecodeString(payload.Ciphertext)
	if err != nil {
		return nil, errors.New("invalid ciphertext")
	}
	plaintext, err := aead.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return nil, errors.New("payload authentication failed")
	}
	if protocol.Digest(plaintext) != payload.PlaintextDigest {
		return nil, errors.New("plaintext digest mismatch")
	}
	return plaintext, nil
}

func EncryptArtifactChunk(taskKey []byte, keyVersion uint32, artifactID, versionID string, chunkIndex int, plaintext []byte) ([]byte, protocol.EncryptedArtifactChunk, error) {
	if len(taskKey) != 32 || keyVersion == 0 || artifactID == "" || versionID == "" || chunkIndex < 0 {
		return nil, protocol.EncryptedArtifactChunk{}, errors.New("valid task key, key version, artifact/version IDs, and chunk index are required")
	}
	block, err := aes.NewCipher(taskKey)
	if err != nil {
		return nil, protocol.EncryptedArtifactChunk{}, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, protocol.EncryptedArtifactChunk{}, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, protocol.EncryptedArtifactChunk{}, err
	}
	ciphertext := aead.Seal(nil, nonce, plaintext, artifactAAD(artifactID, versionID, chunkIndex))
	metadata := protocol.EncryptedArtifactChunk{Version: 1, Algorithm: "A256GCM", KeyVersion: keyVersion, ChunkIndex: chunkIndex, Nonce: encode(nonce), CiphertextDigest: protocol.Digest(ciphertext), PlaintextDigest: protocol.Digest(plaintext)}
	return ciphertext, metadata, nil
}

func DecryptArtifactChunk(taskKey []byte, artifactID, versionID string, metadata protocol.EncryptedArtifactChunk, ciphertext []byte) ([]byte, error) {
	if len(taskKey) != 32 || metadata.Version != 1 || metadata.Algorithm != "A256GCM" || metadata.KeyVersion == 0 || metadata.ChunkIndex < 0 {
		return nil, errors.New("unsupported encrypted artifact chunk")
	}
	if protocol.Digest(ciphertext) != metadata.CiphertextDigest {
		return nil, errors.New("artifact ciphertext digest mismatch")
	}
	nonce, err := base64.RawURLEncoding.DecodeString(metadata.Nonce)
	if err != nil {
		return nil, errors.New("invalid artifact chunk nonce")
	}
	block, err := aes.NewCipher(taskKey)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plaintext, err := aead.Open(nil, nonce, ciphertext, artifactAAD(artifactID, versionID, metadata.ChunkIndex))
	if err != nil {
		return nil, errors.New("artifact chunk authentication failed")
	}
	if protocol.Digest(plaintext) != metadata.PlaintextDigest {
		return nil, errors.New("artifact plaintext digest mismatch")
	}
	return plaintext, nil
}

func deriveKey(shared []byte, taskID, deviceID string, version uint32, ephemeral, recipient []byte) []byte {
	salt := sha256.Sum256([]byte("agent-continuity/task-key/v1"))
	extract := hmac.New(sha256.New, salt[:])
	_, _ = extract.Write(shared)
	pseudorandom := extract.Sum(nil)
	info := append([]byte(taskID+"\x00"+deviceID+"\x00"), versionBytes(version)...)
	info = append(info, ephemeral...)
	info = append(info, recipient...)
	expand := hmac.New(sha256.New, pseudorandom)
	_, _ = expand.Write(info)
	_, _ = expand.Write([]byte{1})
	return expand.Sum(nil)[:32]
}

func associatedData(taskID, deviceID string, version uint32) []byte {
	return append([]byte(taskID+"\x00"+deviceID+"\x00"), versionBytes(version)...)
}
func artifactAAD(artifactID, versionID string, chunkIndex int) []byte {
	index := make([]byte, 8)
	binary.BigEndian.PutUint64(index, uint64(chunkIndex))
	return append([]byte(artifactID+"\x00"+versionID+"\x00"), index...)
}
func versionBytes(version uint32) []byte {
	result := make([]byte, 4)
	binary.BigEndian.PutUint32(result, version)
	return result
}
func encode(value []byte) string { return base64.RawURLEncoding.EncodeToString(value) }
func decode32(value string) ([]byte, error) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, errors.New("invalid base64url")
	}
	if len(raw) != 32 {
		return nil, errors.New("key must be 32 bytes")
	}
	return raw, nil
}
