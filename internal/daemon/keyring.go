package daemon

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

type TaskKeyRecord struct {
	TaskID  string `json:"task_id"`
	Version uint32 `json:"version"`
	Key     string `json:"key"`
}

type Keyring struct {
	path string
	mu   sync.Mutex
}

func OpenKeyring(root string) (*Keyring, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, err
	}
	return &Keyring{path: filepath.Join(absolute, "task-keys.json")}, nil
}

func (k *Keyring) Put(taskID string, version uint32, key []byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if taskID == "" || version == 0 || len(key) != 32 {
		return errors.New("task ID, positive version, and 32-byte key are required")
	}
	records, err := k.load()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	encoded := base64.RawURLEncoding.EncodeToString(key)
	found := false
	for i := range records {
		if records[i].TaskID == taskID && records[i].Version == version {
			if records[i].Key != encoded {
				return errors.New("refusing to replace a task key with different key material")
			}
			found = true
		}
	}
	if !found {
		records = append(records, TaskKeyRecord{TaskID: taskID, Version: version, Key: encoded})
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].TaskID == records[j].TaskID {
			return records[i].Version < records[j].Version
		}
		return records[i].TaskID < records[j].TaskID
	})
	return k.save(records)
}

func (k *Keyring) Get(taskID string, version uint32) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	records, err := k.load()
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		if record.TaskID == taskID && record.Version == version {
			return decodeTaskKey(record.Key)
		}
	}
	return nil, os.ErrNotExist
}

func (k *Keyring) Latest(taskID string) ([]byte, uint32, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	records, err := k.load()
	if err != nil {
		return nil, 0, err
	}
	var selected TaskKeyRecord
	for _, record := range records {
		if record.TaskID == taskID && record.Version > selected.Version {
			selected = record
		}
	}
	if selected.Version == 0 {
		return nil, 0, os.ErrNotExist
	}
	key, err := decodeTaskKey(selected.Key)
	return key, selected.Version, err
}

func (k *Keyring) load() ([]TaskKeyRecord, error) {
	data, err := os.ReadFile(k.path)
	if err != nil {
		return nil, err
	}
	var records []TaskKeyRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, err
	}
	return records, nil
}

func (k *Keyring) save(records []TaskKeyRecord) error {
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(k.path), ".task-keys-*")
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
	if err := os.Rename(name, k.path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(k.path))
}

func decodeTaskKey(value string) ([]byte, error) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(raw) != 32 {
		return nil, errors.New("invalid stored task key")
	}
	return raw, nil
}
