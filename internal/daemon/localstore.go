package daemon

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

var (
	localMetaBucket      = []byte("meta")
	localTasksBucket     = []byte("tasks")
	localInboxBucket     = []byte("inbox")
	localEventsBucket    = []byte("events")
	localEventIDsBucket  = []byte("event_ids")
	localCursorsBucket   = []byte("cursors")
	localArtifactsBucket = []byte("artifacts")
	localDevicesBucket   = []byte("devices")
)

type LocalStore struct {
	db *bolt.DB
}

type LocalStats struct {
	Tasks     int `json:"tasks"`
	Events    int `json:"events"`
	Inbox     int `json:"inbox"`
	Artifacts int `json:"artifacts"`
}

func OpenLocalStore(root string) (*LocalStore, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(absolute, "continuity.db")
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		return nil, err
	}
	store := &LocalStore{db: db}
	err = db.Update(func(tx *bolt.Tx) error {
		for _, bucket := range [][]byte{localMetaBucket, localTasksBucket, localInboxBucket, localEventsBucket, localEventIDsBucket, localCursorsBucket, localArtifactsBucket, localDevicesBucket} {
			if _, err := tx.CreateBucketIfNotExists(bucket); err != nil {
				return err
			}
		}
		return tx.Bucket(localMetaBucket).Put([]byte("schema_version"), []byte("1"))
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (store *LocalStore) Close() error { return store.db.Close() }

func (store *LocalStore) TaskStates() ([]protocol.SyncTaskState, error) {
	result := []protocol.SyncTaskState{}
	err := store.db.View(func(tx *bolt.Tx) error {
		tasks := tx.Bucket(localTasksBucket)
		cursors := tx.Bucket(localCursorsBucket)
		return tasks.ForEach(func(key, value []byte) error {
			var task protocol.Task
			if err := json.Unmarshal(value, &task); err != nil {
				return err
			}
			result = append(result, protocol.SyncTaskState{TaskID: task.ID, HeadEventIDs: append([]string(nil), task.HeadEventIDs...), EventCursor: decodeUint64(cursors.Get(key))})
			return nil
		})
	})
	sort.Slice(result, func(i, j int) bool { return result[i].TaskID < result[j].TaskID })
	return result, err
}

// StagePull durably writes a verified network batch to Inbox. ReconcileInbox is
// intentionally separate so a process crash between receive and projection can
// be recovered without asking the server to guess whether the batch committed.
func (store *LocalStore) StagePull(response protocol.SyncPullResponse) error {
	deviceKeys := make(map[string]string, len(response.Devices))
	for _, device := range response.Devices {
		deviceKeys[device.ID] = device.PublicKey
	}
	for _, batch := range response.Batches {
		if batch.Task.ID == "" {
			return errors.New("sync batch task is required")
		}
		for _, event := range batch.Events {
			if event.TaskID != batch.Task.ID || event.ServerSequence == 0 {
				return errors.New("sync event task or server sequence is invalid")
			}
			if err := protocol.ValidateEvent(event, time.Now()); err != nil {
				return fmt.Errorf("validate pulled event %s: %w", event.EventID, err)
			}
			publicKey := deviceKeys[event.DeviceID]
			if publicKey == "" {
				return fmt.Errorf("pulled event %s has no device proof", event.EventID)
			}
			if err := protocol.VerifyEventSignature(event, publicKey); err != nil {
				return fmt.Errorf("verify pulled event %s: %w", event.EventID, err)
			}
		}
	}
	return store.db.Update(func(tx *bolt.Tx) error {
		for _, device := range response.Devices {
			if err := putJSON(tx.Bucket(localDevicesBucket), []byte(device.ID), device); err != nil {
				return err
			}
		}
		for _, batch := range response.Batches {
			if err := putJSON(tx.Bucket(localTasksBucket), []byte(batch.Task.ID), batch.Task); err != nil {
				return err
			}
			for _, artifact := range batch.ArtifactManifests {
				if artifact.TaskID != batch.Task.ID {
					return errors.New("artifact manifest task mismatch")
				}
				if err := putJSON(tx.Bucket(localArtifactsBucket), compositeKey(artifact.TaskID, artifact.ID), artifact); err != nil {
					return err
				}
			}
			for _, event := range batch.Events {
				key := sequenceKey(event.TaskID, event.ServerSequence)
				data, err := json.Marshal(event)
				if err != nil {
					return err
				}
				for _, bucketName := range [][]byte{localInboxBucket, localEventsBucket} {
					if existing := tx.Bucket(bucketName).Get(key); existing != nil && protocol.Digest(existing) != protocol.Digest(data) {
						return fmt.Errorf("server sequence %d changed across sync batches", event.ServerSequence)
					}
				}
				if err := tx.Bucket(localInboxBucket).Put(key, data); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func (store *LocalStore) ReconcileInbox() ([]protocol.SyncAck, error) {
	ackByTask := map[string]uint64{}
	err := store.db.Update(func(tx *bolt.Tx) error {
		inbox := tx.Bucket(localInboxBucket)
		events := tx.Bucket(localEventsBucket)
		eventIDs := tx.Bucket(localEventIDsBucket)
		cursors := tx.Bucket(localCursorsBucket)
		cursor := inbox.Cursor()
		for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
			var event protocol.Event
			if err := json.Unmarshal(value, &event); err != nil {
				return err
			}
			digest := protocol.Digest(value)
			if existing := eventIDs.Get([]byte(event.EventID)); existing != nil && string(existing) != digest {
				return fmt.Errorf("event ID %s changed across sync batches", event.EventID)
			}
			if existing := events.Get(key); existing != nil && protocol.Digest(existing) != digest {
				return fmt.Errorf("event sequence changed for %s", event.EventID)
			}
			if err := events.Put(key, value); err != nil {
				return err
			}
			if err := eventIDs.Put([]byte(event.EventID), []byte(digest)); err != nil {
				return err
			}
			current := decodeUint64(cursors.Get([]byte(event.TaskID)))
			if event.ServerSequence > current {
				if err := cursors.Put([]byte(event.TaskID), encodeUint64(event.ServerSequence)); err != nil {
					return err
				}
			}
			if event.ServerSequence > ackByTask[event.TaskID] {
				ackByTask[event.TaskID] = event.ServerSequence
			}
			if err := cursor.Delete(); err != nil {
				return err
			}
		}
		return nil
	})
	acks := make([]protocol.SyncAck, 0, len(ackByTask))
	for taskID, cursor := range ackByTask {
		acks = append(acks, protocol.SyncAck{TaskID: taskID, Cursor: cursor})
	}
	sort.Slice(acks, func(i, j int) bool { return acks[i].TaskID < acks[j].TaskID })
	return acks, err
}

func (store *LocalStore) ApplyTaskPlans(plans []protocol.SyncTaskPlan) error {
	return store.db.Update(func(tx *bolt.Tx) error {
		for _, plan := range plans {
			if plan.Task.ID == "" {
				return errors.New("sync task plan has no task ID")
			}
			if err := putJSON(tx.Bucket(localTasksBucket), []byte(plan.Task.ID), plan.Task); err != nil {
				return err
			}
			for _, artifact := range plan.ArtifactManifests {
				if err := putJSON(tx.Bucket(localArtifactsBucket), compositeKey(plan.Task.ID, artifact.ID), artifact); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func (store *LocalStore) ListTasks() ([]protocol.Task, error) {
	result := []protocol.Task{}
	err := store.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(localTasksBucket).ForEach(func(_, value []byte) error {
			var task protocol.Task
			if err := json.Unmarshal(value, &task); err != nil {
				return err
			}
			result = append(result, task)
			return nil
		})
	})
	sort.Slice(result, func(i, j int) bool { return result[i].UpdatedAt.After(result[j].UpdatedAt) })
	return result, err
}

func (store *LocalStore) ListEvents(taskID string, after uint64, limit int) ([]protocol.Event, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	result := []protocol.Event{}
	err := store.db.View(func(tx *bolt.Tx) error {
		cursor := tx.Bucket(localEventsBucket).Cursor()
		prefix := append([]byte(taskID), 0)
		seek := sequenceKey(taskID, after+1)
		for key, value := cursor.Seek(seek); key != nil && strings.HasPrefix(string(key), string(prefix)) && len(result) < limit; key, value = cursor.Next() {
			var event protocol.Event
			if err := json.Unmarshal(value, &event); err != nil {
				return err
			}
			result = append(result, event)
		}
		return nil
	})
	return result, err
}

func (store *LocalStore) Stats() (LocalStats, error) {
	value := LocalStats{}
	err := store.db.View(func(tx *bolt.Tx) error {
		value.Tasks = tx.Bucket(localTasksBucket).Stats().KeyN
		value.Events = tx.Bucket(localEventsBucket).Stats().KeyN
		value.Inbox = tx.Bucket(localInboxBucket).Stats().KeyN
		value.Artifacts = tx.Bucket(localArtifactsBucket).Stats().KeyN
		return nil
	})
	return value, err
}

func putJSON(bucket *bolt.Bucket, key []byte, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return bucket.Put(key, data)
}

func compositeKey(first, second string) []byte { return []byte(first + "\x00" + second) }

func sequenceKey(taskID string, sequence uint64) []byte {
	key := make([]byte, len(taskID)+1+8)
	copy(key, taskID)
	binary.BigEndian.PutUint64(key[len(taskID)+1:], sequence)
	return key
}

func encodeUint64(value uint64) []byte {
	encoded := make([]byte, 8)
	binary.BigEndian.PutUint64(encoded, value)
	return encoded
}

func decodeUint64(value []byte) uint64 {
	if len(value) != 8 {
		return 0
	}
	return binary.BigEndian.Uint64(value)
}
