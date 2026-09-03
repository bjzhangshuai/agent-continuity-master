package daemon

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestKeyringPersistsVersionsAndRefusesReplacement(t *testing.T) {
	root := t.TempDir()
	keyring, err := OpenKeyring(root)
	if err != nil {
		t.Fatal(err)
	}
	key1 := bytes.Repeat([]byte{1}, 32)
	key2 := bytes.Repeat([]byte{2}, 32)
	if err := keyring.Put("task_a", 1, key1); err != nil {
		t.Fatal(err)
	}
	if err := keyring.Put("task_a", 2, key2); err != nil {
		t.Fatal(err)
	}
	got, version, err := keyring.Latest("task_a")
	if err != nil || version != 2 || !bytes.Equal(got, key2) {
		t.Fatalf("latest=%x version=%d err=%v", got, version, err)
	}
	if err := keyring.Put("task_a", 2, key1); err == nil {
		t.Fatal("task key replacement should fail closed")
	}
	info, err := os.Stat(filepath.Join(root, "task-keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("keyring mode = %o", info.Mode().Perm())
	}
	if _, err := keyring.Get("missing", 1); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing key error = %v", err)
	}
}

func TestNewDeviceStateIncludesSigningAndEncryptionKeys(t *testing.T) {
	state, publicKey, err := NewDeviceState("acct_a", "device")
	if err != nil {
		t.Fatal(err)
	}
	if publicKey == "" || state.EncryptionPublicKey == "" {
		t.Fatal("device keys were not generated")
	}
	if _, err := state.Private(); err != nil {
		t.Fatal(err)
	}
	if _, err := state.EncryptionPrivate(); err != nil {
		t.Fatal(err)
	}
}
