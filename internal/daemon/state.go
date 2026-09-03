package daemon

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/agent-continuity/agent-continuity-master/pkg/e2ee"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

type DeviceState struct {
	AccountID            string `json:"account_id"`
	DeviceID             string `json:"device_id"`
	Name                 string `json:"name"`
	PrivateKey           string `json:"private_key"`
	EncryptionPrivateKey string `json:"encryption_private_key,omitempty"`
	EncryptionPublicKey  string `json:"encryption_public_key,omitempty"`
	Sequence             uint64 `json:"sequence"`
}
type StateStore struct {
	path string
	mu   sync.Mutex
}

func OpenState(root string) (*StateStore, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, err
	}
	return &StateStore{path: filepath.Join(absolute, "device.json")}, nil
}
func (s *StateStore) Load() (DeviceState, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.load() }
func (s *StateStore) Save(value DeviceState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.save(value)
}
func (s *StateStore) NextSequence() (DeviceState, error) {
	return s.ReserveSequences(1)
}

func (s *StateStore) ReserveSequences(count uint64) (DeviceState, error) {
	if count == 0 {
		return s.Load()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	value, err := s.load()
	if err != nil {
		return DeviceState{}, err
	}
	if ^uint64(0)-value.Sequence < count {
		return DeviceState{}, errors.New("device sequence overflow")
	}
	value.Sequence += count
	if err := s.save(value); err != nil {
		return DeviceState{}, err
	}
	return value, nil
}
func (s *StateStore) load() (DeviceState, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return DeviceState{}, err
	}
	var value DeviceState
	if err := json.Unmarshal(data, &value); err != nil {
		return DeviceState{}, err
	}
	if value.AccountID == "" || value.DeviceID == "" || value.PrivateKey == "" {
		return DeviceState{}, errors.New("device state is incomplete")
	}
	return value, nil
}
func (s *StateStore) save(value DeviceState) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".device-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, s.path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(s.path))
}
func (value DeviceState) Private() (ed25519.PrivateKey, error) {
	raw, err := base64.RawURLEncoding.DecodeString(value.PrivateKey)
	if err != nil {
		return nil, err
	}
	if len(raw) != ed25519.PrivateKeySize {
		return nil, errors.New("invalid Ed25519 private key length")
	}
	return ed25519.PrivateKey(raw), nil
}

func (value DeviceState) EncryptionPrivate() (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(value.EncryptionPrivateKey)
	if err != nil || len(raw) != 32 {
		return "", errors.New("device has no valid X25519 private key; re-register this device")
	}
	return value.EncryptionPrivateKey, nil
}

func NewDeviceState(accountID, name string) (DeviceState, string, error) {
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		return DeviceState{}, "", err
	}
	id, err := protocol.NewID("device")
	if err != nil {
		return DeviceState{}, "", err
	}
	encryptionKeys, err := e2ee.GenerateDeviceKeyPair()
	if err != nil {
		return DeviceState{}, "", err
	}
	return DeviceState{AccountID: accountID, DeviceID: id, Name: name, PrivateKey: base64.RawURLEncoding.EncodeToString(privateKey), EncryptionPrivateKey: encryptionKeys.PrivateKey, EncryptionPublicKey: encryptionKeys.PublicKey}, base64.RawURLEncoding.EncodeToString(publicKey), nil
}
