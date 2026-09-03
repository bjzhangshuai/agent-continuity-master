package daemon

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

type Outbox struct {
	dir string
	mu  sync.Mutex
}

type RetryState struct {
	EventID     string    `json:"event_id"`
	Attempts    int       `json:"attempts"`
	NextAttempt time.Time `json:"next_attempt"`
	LastError   string    `json:"last_error"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Rejection struct {
	EventID    string    `json:"event_id"`
	Code       string    `json:"code"`
	Message    string    `json:"message"`
	RejectedAt time.Time `json:"rejected_at"`
}

func OpenOutbox(root string) (*Outbox, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(absolute, "outbox")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create outbox: %w", err)
	}
	return &Outbox{dir: dir}, nil
}

func (o *Outbox) Enqueue(event protocol.Event) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if event.EventID == "" {
		return errors.New("event_id is required")
	}
	if strings.ContainsAny(event.EventID, `/\\`) {
		return errors.New("event_id cannot contain path separators")
	}
	entries, err := os.ReadDir(o.dir)
	if err != nil {
		return err
	}
	suffix := "-" + event.EventID + ".json"
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), suffix) {
			return nil
		}
	}
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(o.dir, ".pending-*")
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
	final := filepath.Join(o.dir, fmt.Sprintf("%020d-%s.json", event.DeviceSequence, event.EventID))
	if _, err := os.Stat(final); err == nil {
		return nil
	}
	if err := os.Rename(name, final); err != nil {
		return err
	}
	return syncDirectory(o.dir)
}

func (o *Outbox) Peek(limit int) ([]protocol.Event, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	entries, err := os.ReadDir(o.dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	if len(names) > limit {
		names = names[:limit]
	}
	result := make([]protocol.Event, 0, len(names))
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(o.dir, name))
		if err != nil {
			return nil, err
		}
		var event protocol.Event
		if err := json.Unmarshal(data, &event); err != nil {
			return nil, fmt.Errorf("decode %s: %w", name, err)
		}
		result = append(result, event)
	}
	return result, nil
}

func (o *Outbox) Ack(eventID string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	entries, err := os.ReadDir(o.dir)
	if err != nil {
		return err
	}
	suffix := "-" + eventID + ".json"
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), suffix) {
			if err := os.Remove(filepath.Join(o.dir, entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err := os.Remove(o.retryPath(eventID)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			return syncDirectory(o.dir)
		}
	}
	return nil
}

// Reject moves a permanently rejected event to a recoverable dead-letter
// directory so one bad event cannot block every later device sequence.
func (o *Outbox) Reject(eventID, code, message string, now time.Time) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	directory := filepath.Join(filepath.Dir(o.dir), "rejected")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	entries, err := os.ReadDir(o.dir)
	if err != nil {
		return err
	}
	suffix := "-" + eventID + ".json"
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), suffix) {
			continue
		}
		source := filepath.Join(o.dir, entry.Name())
		destination := filepath.Join(directory, entry.Name())
		if err := os.Rename(source, destination); err != nil {
			return err
		}
		reason, err := json.MarshalIndent(Rejection{EventID: eventID, Code: code, Message: message, RejectedAt: now.UTC()}, "", "  ")
		if err != nil {
			return err
		}
		if err := atomicWrite(destination+".reason", reason, 0o600); err != nil {
			return err
		}
		if err := os.Remove(o.retryPath(eventID)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := syncDirectory(directory); err != nil {
			return err
		}
		return syncDirectory(o.dir)
	}
	return nil
}

func (o *Outbox) Ready(limit int, now time.Time) ([]protocol.Event, time.Duration, error) {
	items, err := o.Peek(limit)
	if err != nil || len(items) == 0 {
		return items, 0, err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	retry, err := o.loadRetry(items[0].EventID)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, 0, err
	}
	if err == nil && now.Before(retry.NextAttempt) {
		return nil, retry.NextAttempt.Sub(now), nil
	}
	return items, 0, nil
}

func (o *Outbox) RecordFailure(eventID string, failure error, now time.Time) (RetryState, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	state, err := o.loadRetry(eventID)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return RetryState{}, err
	}
	state.EventID = eventID
	state.Attempts++
	state.LastError = failure.Error()
	state.UpdatedAt = now.UTC()
	delay := time.Second
	for i := 1; i < state.Attempts && delay < 5*time.Minute; i++ {
		delay *= 2
	}
	if delay > 5*time.Minute {
		delay = 5 * time.Minute
	}
	digest := sha256.Sum256([]byte(eventID))
	jitterBasis := float64(binary.BigEndian.Uint16(digest[:2])) / 65535
	jitter := 0.8 + jitterBasis*0.4
	delay = time.Duration(float64(delay) * jitter)
	state.NextAttempt = now.UTC().Add(delay)
	data, err := json.Marshal(state)
	if err != nil {
		return RetryState{}, err
	}
	if err := atomicWrite(o.retryPath(eventID), data, 0o600); err != nil {
		return RetryState{}, err
	}
	return state, syncDirectory(o.dir)
}

func (o *Outbox) RetryInfo(eventID string) (RetryState, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.loadRetry(eventID)
}

func (o *Outbox) Len() (int, error) {
	items, err := o.Peek(1000)
	return len(items), err
}

func (o *Outbox) retryPath(eventID string) string { return filepath.Join(o.dir, eventID+".retry") }

func (o *Outbox) loadRetry(eventID string) (RetryState, error) {
	data, err := os.ReadFile(o.retryPath(eventID))
	if err != nil {
		return RetryState{}, err
	}
	var value RetryState
	if err := json.Unmarshal(data, &value); err != nil {
		return RetryState{}, err
	}
	return value, nil
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".atomic-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if err := tmp.Chmod(mode); err != nil {
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
	return os.Rename(name, path)
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
