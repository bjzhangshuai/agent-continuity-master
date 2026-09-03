package daemon

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

type SyncSettings struct {
	Paused                  bool  `json:"paused"`
	BandwidthBytesPerSecond int64 `json:"bandwidth_bytes_per_second"`
}

type SyncControl struct {
	path string
	mu   sync.Mutex
}

func OpenSyncControl(root string) (*SyncControl, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, err
	}
	return &SyncControl{path: filepath.Join(absolute, "sync-settings.json")}, nil
}

func (s *SyncControl) Load() (SyncSettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return SyncSettings{}, nil
	}
	if err != nil {
		return SyncSettings{}, err
	}
	var value SyncSettings
	if err := json.Unmarshal(data, &value); err != nil {
		return SyncSettings{}, err
	}
	return value, nil
}

func (s *SyncControl) SetPaused(paused bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, err := s.loadUnlocked()
	if err != nil {
		return err
	}
	value.Paused = paused
	return s.saveUnlocked(value)
}

func (s *SyncControl) SetBandwidth(bytesPerSecond int64) error {
	if bytesPerSecond < 0 {
		return errors.New("bandwidth limit cannot be negative")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	value, err := s.loadUnlocked()
	if err != nil {
		return err
	}
	value.BandwidthBytesPerSecond = bytesPerSecond
	return s.saveUnlocked(value)
}

func (s *SyncControl) loadUnlocked() (SyncSettings, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return SyncSettings{}, nil
	}
	if err != nil {
		return SyncSettings{}, err
	}
	var value SyncSettings
	err = json.Unmarshal(data, &value)
	return value, err
}

func (s *SyncControl) saveUnlocked(value SyncSettings) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err := atomicWrite(s.path, data, 0o600); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(s.path))
}
