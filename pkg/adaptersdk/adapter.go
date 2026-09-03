package adaptersdk

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

const ContractVersion = "1.0"

type Capability string

const (
	CapabilityDiscover     Capability = "task_discovery"
	CapabilityCapture      Capability = "event_capture"
	CapabilityNativeResume Capability = "native_resume"
	CapabilityRehydrate    Capability = "rehydrate"
	CapabilityArtifacts    Capability = "artifacts"
)

type CapabilityStatus struct {
	Available bool   `json:"available"`
	Version   string `json:"version,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

type CapabilityReport struct {
	Adapter      string                          `json:"adapter"`
	Version      string                          `json:"adapter_version"`
	Capabilities map[Capability]CapabilityStatus `json:"capabilities"`
}

type RecordKind string

const (
	RecordUserInput   RecordKind = "user.input"
	RecordAgentOutput RecordKind = "agent.output"
	RecordToolCall    RecordKind = "tool.call"
	RecordToolResult  RecordKind = "tool.result"
	RecordDecision    RecordKind = "decision"
	RecordStatus      RecordKind = "status.changed"
	RecordError       RecordKind = "agent.error"
	RecordArtifact    RecordKind = "artifact.changed"
)

type NativeSession struct {
	Provider   string            `json:"provider"`
	SessionRef string            `json:"session_ref"`
	SourcePath string            `json:"source_path,omitempty"`
	UpdatedAt  time.Time         `json:"updated_at,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

// CapturedRecord is deliberately unsigned. The local daemon envelopes and signs
// records with the registered device key so an adapter never needs key material.
type CapturedRecord struct {
	ID           string          `json:"record_id"`
	Kind         RecordKind      `json:"kind"`
	OccurredAt   time.Time       `json:"occurred_at"`
	Payload      json.RawMessage `json:"payload"`
	ArtifactRefs []string        `json:"artifact_refs,omitempty"`
	NativeType   string          `json:"native_type,omitempty"`
}

type CapturedArtifact struct {
	Path        string          `json:"path"`
	Filename    string          `json:"filename"`
	ContentType string          `json:"content_type"`
	Size        int64           `json:"size"`
	ContentHash string          `json:"content_hash"`
	Metadata    json.RawMessage `json:"metadata,omitempty"`
}

type CaptureCursor struct {
	Offset int64  `json:"offset"`
	Line   uint64 `json:"line"`
	Digest string `json:"digest,omitempty"`
}

type CaptureRequest struct {
	Session NativeSession `json:"session"`
	Cursor  CaptureCursor `json:"cursor,omitempty"`
}

type CaptureResult struct {
	Session   NativeSession      `json:"session"`
	Records   []CapturedRecord   `json:"records"`
	Artifacts []CapturedArtifact `json:"artifacts,omitempty"`
	Cursor    CaptureCursor      `json:"cursor"`
	Warnings  []string           `json:"warnings,omitempty"`
}

type ResumeRequest struct {
	Task      protocol.Task
	Events    []protocol.Event
	Artifacts []protocol.Artifact
	Native    *NativeSession
}

type ResumeResult struct {
	Mode         string   `json:"mode"`
	SessionRef   string   `json:"session_ref,omitempty"`
	Command      []string `json:"command,omitempty"`
	Instructions string   `json:"instructions,omitempty"`
}

type Adapter interface {
	Name() string
	Version() string
	Capabilities() []Capability
	Detect(context.Context) CapabilityReport
	Discover(context.Context) ([]NativeSession, error)
	Capture(context.Context, CaptureRequest) (CaptureResult, error)
	Resume(context.Context, ResumeRequest) (ResumeResult, error)
}

func Validate(adapter Adapter) error {
	if adapter == nil || strings.TrimSpace(adapter.Name()) == "" {
		return errors.New("adapter name is required")
	}
	if strings.TrimSpace(adapter.Version()) == "" {
		return errors.New("adapter version is required")
	}
	capabilities := map[Capability]bool{}
	for _, capability := range adapter.Capabilities() {
		if capabilities[capability] {
			return fmt.Errorf("adapter capability %q is duplicated", capability)
		}
		capabilities[capability] = true
	}
	if !capabilities[CapabilityDiscover] || !capabilities[CapabilityCapture] {
		return errors.New("adapter must discover tasks and capture events")
	}
	if !capabilities[CapabilityNativeResume] && !capabilities[CapabilityRehydrate] {
		return errors.New("adapter must support native resume or rehydrate")
	}
	return nil
}

type EnvelopeOptions struct {
	TaskID          string
	BranchID        string
	AccountID       string
	DeviceID        string
	ParticipantID   string
	AgentID         string
	ProviderID      string
	SessionRef      string
	StartSequence   uint64
	ParentEventIDs  []string
	PrivateKey      ed25519.PrivateKey
	IdempotencySalt string
}

// Envelope converts adapter records into signed universal events in capture
// order. The first record descends from the supplied task heads and each later
// record descends from the event immediately before it.
func Envelope(records []CapturedRecord, options EnvelopeOptions) ([]protocol.Event, error) {
	if options.TaskID == "" || options.AccountID == "" || options.DeviceID == "" {
		return nil, errors.New("task, account and device IDs are required")
	}
	if len(options.PrivateKey) != ed25519.PrivateKeySize {
		return nil, errors.New("a valid Ed25519 private key is required")
	}
	branch := options.BranchID
	if branch == "" {
		branch = "main"
	}
	parents := append([]string(nil), options.ParentEventIDs...)
	sort.Strings(parents)
	events := make([]protocol.Event, 0, len(records))
	sequence := options.StartSequence
	for index, record := range records {
		if !validKind(record.Kind) {
			return nil, fmt.Errorf("record %d has unsupported kind %q", index, record.Kind)
		}
		if !json.Valid(record.Payload) {
			return nil, fmt.Errorf("record %d payload is not valid JSON", index)
		}
		sequence++
		occurredAt := record.OccurredAt.UTC()
		if occurredAt.IsZero() {
			occurredAt = time.Now().UTC()
		}
		idSeed := strings.Join([]string{options.TaskID, options.DeviceID, options.SessionRef, record.ID, record.NativeType, protocol.Digest(record.Payload), options.IdempotencySalt}, "\x00")
		digest := strings.TrimPrefix(protocol.Digest([]byte(idSeed)), "sha256:")
		eventID := "event_" + digest[:32]
		event := protocol.Event{
			EventID: eventID, TaskID: options.TaskID, BranchID: branch,
			AccountID: options.AccountID, ParticipantID: options.ParticipantID,
			AgentID: options.AgentID, ProviderID: options.ProviderID,
			DeviceID: options.DeviceID, SessionRef: options.SessionRef,
			ParentEventIDs: append([]string(nil), parents...),
			LogicalTime:    protocol.HLC{WallTimeMS: occurredAt.UnixMilli(), Node: options.DeviceID},
			DeviceSequence: sequence, Type: string(record.Kind),
			SchemaVersion: protocol.SchemaVersion, ContentType: "application/json",
			ContentDigest: protocol.Digest(record.Payload), Payload: append(json.RawMessage(nil), record.Payload...),
			ArtifactRefs: append([]string(nil), record.ArtifactRefs...), CreatedAtClient: occurredAt,
			IdempotencyKey: eventID,
		}
		if record.NativeType != "" {
			event.Extensions, _ = json.Marshal(map[string]any{"adapter_native_type": record.NativeType, "adapter_contract_version": ContractVersion})
		}
		if err := protocol.SignPortableEvent(&event, options.PrivateKey); err != nil {
			return nil, fmt.Errorf("sign captured event: %w", err)
		}
		events = append(events, event)
		parents = []string{eventID}
	}
	return events, nil
}

func validKind(kind RecordKind) bool {
	switch kind {
	case RecordUserInput, RecordAgentOutput, RecordToolCall, RecordToolResult, RecordDecision, RecordStatus, RecordError, RecordArtifact:
		return true
	default:
		return false
	}
}
