package daemon

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/agent-continuity/agent-continuity-master/pkg/adaptersdk"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

type AdapterCheckpoint struct {
	Provider   string                   `json:"provider"`
	SessionRef string                   `json:"session_ref"`
	SourceID   string                   `json:"source_id"`
	Cursor     adaptersdk.CaptureCursor `json:"cursor"`
	UpdatedAt  time.Time                `json:"updated_at"`
}

type AdapterCheckpoints struct {
	path string
	mu   sync.Mutex
}

func OpenAdapterCheckpoints(root string) (*AdapterCheckpoints, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, err
	}
	return &AdapterCheckpoints{path: filepath.Join(absolute, "adapter-checkpoints.json")}, nil
}

func (store *AdapterCheckpoints) Get(session adaptersdk.NativeSession) (adaptersdk.CaptureCursor, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	values, err := store.load()
	if err != nil {
		return adaptersdk.CaptureCursor{}, err
	}
	value, ok := values[checkpointKey(session)]
	if !ok {
		return adaptersdk.CaptureCursor{}, os.ErrNotExist
	}
	return value.Cursor, nil
}

func (store *AdapterCheckpoints) Put(session adaptersdk.NativeSession, cursor adaptersdk.CaptureCursor) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	values, err := store.load()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if values == nil {
		values = map[string]AdapterCheckpoint{}
	}
	value := AdapterCheckpoint{Provider: session.Provider, SessionRef: session.SessionRef, SourceID: protocol.Digest([]byte(session.SourcePath)), Cursor: cursor, UpdatedAt: time.Now().UTC()}
	values[checkpointKey(session)] = value
	data, err := json.MarshalIndent(values, "", "  ")
	if err != nil {
		return err
	}
	if err := atomicWrite(store.path, data, 0o600); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(store.path))
}

func (store *AdapterCheckpoints) load() (map[string]AdapterCheckpoint, error) {
	data, err := os.ReadFile(store.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]AdapterCheckpoint{}, nil
		}
		return nil, err
	}
	values := map[string]AdapterCheckpoint{}
	if err := json.Unmarshal(data, &values); err != nil {
		return nil, err
	}
	return values, nil
}

func checkpointKey(session adaptersdk.NativeSession) string {
	return protocol.Digest([]byte(session.Provider + "\x00" + session.SourcePath))
}
