package daemon

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/agent-continuity/agent-continuity-master/pkg/client"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

type SyncEngine struct {
	API    *client.Client
	State  *StateStore
	Outbox *Outbox
	Local  *LocalStore
	Now    func() time.Time
}

type SyncReport struct {
	Pushed         int           `json:"pushed"`
	Duplicates     int           `json:"duplicates"`
	Rejected       int           `json:"rejected"`
	Pulled         int           `json:"pulled"`
	Acknowledged   int           `json:"acknowledged"`
	Tasks          int           `json:"tasks"`
	PushRetryAfter time.Duration `json:"push_retry_after,omitempty"`
}

func (engine *SyncEngine) SyncOnce(ctx context.Context, bandwidthBytesPerSecond int64) (SyncReport, error) {
	if engine.API == nil || engine.State == nil || engine.Outbox == nil || engine.Local == nil {
		return SyncReport{}, errors.New("sync engine dependencies are incomplete")
	}
	now := engine.Now
	if now == nil {
		now = time.Now
	}
	state, err := engine.State.Load()
	if err != nil {
		return SyncReport{}, err
	}
	privateKey, err := state.Private()
	if err != nil {
		return SyncReport{}, err
	}
	report := SyncReport{}
	items, retryAfter, err := engine.Outbox.Ready(100, now())
	if err != nil {
		return report, err
	}
	report.PushRetryAfter = retryAfter
	if retryAfter == 0 && len(items) > 0 {
		if !waitForBandwidth(ctx, estimatedEventBytes(items), bandwidthBytesPerSecond) {
			return report, ctx.Err()
		}
		response, pushErr := engine.API.SyncPush(ctx, protocol.SyncPushRequest{DeviceID: state.DeviceID, Events: items})
		if pushErr != nil {
			_, retryErr := engine.Outbox.RecordFailure(items[0].EventID, pushErr, now())
			if retryErr != nil {
				return report, fmt.Errorf("sync push failed (%v) and retry state failed: %w", pushErr, retryErr)
			}
		} else {
			acknowledgements := make(map[string]protocol.SyncEventAck, len(response.Acknowledgements))
			for _, ack := range response.Acknowledgements {
				acknowledgements[ack.EventID] = ack
			}
			for _, event := range items {
				ack, ok := acknowledgements[event.EventID]
				if !ok {
					return report, fmt.Errorf("sync push omitted acknowledgement for %s", event.EventID)
				}
				if ack.Accepted {
					if err := engine.Outbox.Ack(event.EventID); err != nil {
						return report, err
					}
					report.Pushed++
					if ack.Duplicate {
						report.Duplicates++
					}
					continue
				}
				if ack.ErrorCode == "storage_error" {
					_, retryErr := engine.Outbox.RecordFailure(event.EventID, errors.New(ack.ErrorMessage), now())
					if retryErr != nil {
						return report, retryErr
					}
					break
				}
				if err := engine.Outbox.Reject(event.EventID, ack.ErrorCode, ack.ErrorMessage, now()); err != nil {
					return report, err
				}
				report.Rejected++
			}
		}
	}

	// Recover any network batch staged before a previous process crash. Current
	// local cursors are ACKed on every run, so an ACK response can be lost safely.
	if _, err := engine.Local.ReconcileInbox(); err != nil {
		return report, err
	}
	states, err := engine.Local.TaskStates()
	if err != nil {
		return report, err
	}
	nonce, err := syncNonce()
	if err != nil {
		return report, err
	}
	hello := protocol.SyncHelloRequest{
		ProtocolVersions: []string{protocol.ProtocolVersion}, Capabilities: append([]string(nil), protocol.ServerSyncCapabilities...),
		ClientVersion: "agentcloud-daemon/1.0", DeviceID: state.DeviceID, Nonce: nonce, Timestamp: now().UTC(), Tasks: states,
	}
	if err := protocol.SignPortableSyncHello(&hello, privateKey); err != nil {
		return report, err
	}
	negotiated, err := engine.API.SyncHello(ctx, hello)
	if err != nil {
		return report, err
	}
	if negotiated.ProtocolVersion != protocol.ProtocolVersion || !hasCapability(negotiated.Capabilities, protocol.SyncCapabilityCursorPull) {
		return report, errors.New("server did not negotiate required cursor pull capability")
	}
	if err := engine.Local.ApplyTaskPlans(negotiated.Tasks); err != nil {
		return report, err
	}
	report.Tasks = len(negotiated.Tasks)
	if len(states) > 0 {
		acks := make([]protocol.SyncAck, 0, len(states))
		for _, value := range states {
			if value.EventCursor > 0 {
				acks = append(acks, protocol.SyncAck{TaskID: value.TaskID, Cursor: value.EventCursor})
			}
		}
		if len(acks) > 0 {
			if err := engine.ack(ctx, state.DeviceID, acks); err != nil {
				return report, err
			}
			report.Acknowledged += len(acks)
		}
	}
	localCursors := make(map[string]uint64, len(states))
	for _, value := range states {
		localCursors[value.TaskID] = value.EventCursor
	}
	pending := make([]protocol.SyncPullCursor, 0)
	for _, plan := range negotiated.Tasks {
		if localCursors[plan.Task.ID] < plan.ServerCursor {
			pending = append(pending, protocol.SyncPullCursor{TaskID: plan.Task.ID, After: localCursors[plan.Task.ID], Limit: 200, IncludePayload: true})
		}
	}
	for round := 0; len(pending) > 0; round++ {
		if round >= 1000 {
			return report, errors.New("sync pull exceeded safety round limit")
		}
		batchSize := min(20, len(pending))
		requested := append([]protocol.SyncPullCursor(nil), pending[:batchSize]...)
		pending = pending[batchSize:]
		response, err := engine.API.SyncPull(ctx, protocol.SyncPullRequest{DeviceID: state.DeviceID, Tasks: requested})
		if err != nil {
			return report, err
		}
		encoded, _ := json.Marshal(response)
		if !waitForBandwidth(ctx, int64(len(encoded)), bandwidthBytesPerSecond) {
			return report, ctx.Err()
		}
		if err := engine.Local.StagePull(response); err != nil {
			return report, err
		}
		acks, err := engine.Local.ReconcileInbox()
		if err != nil {
			return report, err
		}
		if len(acks) > 0 {
			if err := engine.ack(ctx, state.DeviceID, acks); err != nil {
				return report, err
			}
			report.Acknowledged += len(acks)
		}
		for _, batch := range response.Batches {
			report.Pulled += len(batch.Events)
			localCursors[batch.Task.ID] = batch.NextCursor
			if batch.HasMore {
				pending = append(pending, protocol.SyncPullCursor{TaskID: batch.Task.ID, After: batch.NextCursor, Limit: 200, IncludePayload: true})
			}
		}
	}
	return report, nil
}

func (engine *SyncEngine) ack(ctx context.Context, deviceID string, values []protocol.SyncAck) error {
	for len(values) > 0 {
		count := min(100, len(values))
		if err := engine.API.SyncAck(ctx, protocol.SyncAckRequest{DeviceID: deviceID, Tasks: values[:count]}); err != nil {
			return err
		}
		values = values[count:]
	}
	return nil
}

func syncNonce() (string, error) {
	value := make([]byte, 24)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func estimatedEventBytes(events []protocol.Event) int64 {
	var result int64
	for _, event := range events {
		result += int64(len(event.Payload) + len(event.Signature) + 1024)
	}
	return result
}

func waitForBandwidth(ctx context.Context, bytes, bytesPerSecond int64) bool {
	if bytesPerSecond <= 0 || bytes <= 0 {
		return true
	}
	delay := time.Duration(float64(time.Second) * float64(bytes) / float64(bytesPerSecond))
	if delay <= 0 {
		return true
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func hasCapability(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
