package protocol

import (
	"encoding/json"
	"time"
)

const (
	SchemaVersion   = "1.0"
	ProtocolVersion = "1.0"
)

type Account struct {
	ID        string    `json:"account_id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

type Device struct {
	ID                  string     `json:"device_id"`
	AccountID           string     `json:"account_id"`
	Name                string     `json:"name"`
	PublicKey           string     `json:"public_key"`
	EncryptionPublicKey string     `json:"encryption_public_key,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	RevokedAt           *time.Time `json:"revoked_at,omitempty"`
}

type Task struct {
	ID             string          `json:"task_id"`
	AccountID      string          `json:"account_id"`
	OrganizationID string          `json:"organization_id,omitempty"`
	TeamID         string          `json:"team_id,omitempty"`
	Title          string          `json:"title"`
	Objective      string          `json:"objective"`
	Status         string          `json:"status"`
	OriginDevice   string          `json:"origin_device,omitempty"`
	HeadEventIDs   []string        `json:"head_event_ids"`
	EventCount     int64           `json:"event_count"`
	ArtifactCount  int64           `json:"artifact_count"`
	Metadata       json.RawMessage `json:"metadata,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

type HLC struct {
	WallTimeMS int64  `json:"wall_time_ms"`
	Counter    uint32 `json:"counter"`
	Node       string `json:"node"`
}

type Event struct {
	EventID          string          `json:"event_id"`
	TaskID           string          `json:"task_id"`
	BranchID         string          `json:"branch_id"`
	AccountID        string          `json:"account_id"`
	ParticipantID    string          `json:"participant_id,omitempty"`
	AgentID          string          `json:"agent_id,omitempty"`
	ProviderID       string          `json:"provider_id,omitempty"`
	DeviceID         string          `json:"device_id"`
	SessionRef       string          `json:"session_ref,omitempty"`
	ParentEventIDs   []string        `json:"parent_event_ids"`
	LogicalTime      HLC             `json:"logical_time"`
	DeviceSequence   uint64          `json:"device_sequence"`
	ServerSequence   uint64          `json:"server_sequence,omitempty"`
	Type             string          `json:"event_type"`
	SchemaVersion    string          `json:"schema_version"`
	ContentType      string          `json:"content_type"`
	ContentDigest    string          `json:"content_digest"`
	Payload          json.RawMessage `json:"payload,omitempty"`
	ArtifactRefs     []string        `json:"artifact_refs,omitempty"`
	CreatedAtClient  time.Time       `json:"created_at_client"`
	ReceivedAtServer time.Time       `json:"received_at_server,omitempty"`
	Signature        string          `json:"signature"`
	IdempotencyKey   string          `json:"idempotency_key"`
	Extensions       json.RawMessage `json:"extensions,omitempty"`
}

type AppendResult struct {
	Event     Event      `json:"event"`
	Duplicate bool       `json:"duplicate"`
	Conflicts []Conflict `json:"conflicts,omitempty"`
	TaskHeads []string   `json:"task_heads"`
}

type Conflict struct {
	ID        string          `json:"conflict_id"`
	TaskID    string          `json:"task_id"`
	EventID   string          `json:"event_id"`
	Kind      string          `json:"kind"`
	Strategy  string          `json:"strategy"`
	Details   json.RawMessage `json:"details,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	Resolved  bool            `json:"resolved"`
}

type Artifact struct {
	ID                 string          `json:"artifact_id"`
	TaskID             string          `json:"task_id"`
	AccountID          string          `json:"account_id"`
	VersionID          string          `json:"version_id"`
	ParentVersionID    string          `json:"parent_version_id,omitempty"`
	ContentType        string          `json:"content_type"`
	Filename           string          `json:"filename"`
	Size               int64           `json:"size"`
	ContentHash        string          `json:"content_hash"`
	ChunkManifestHash  string          `json:"chunk_manifest_hash,omitempty"`
	ProducerType       string          `json:"producer_type,omitempty"`
	ProducerID         string          `json:"producer_id,omitempty"`
	DeviceID           string          `json:"device_id,omitempty"`
	StorageRef         string          `json:"storage_reference"`
	Metadata           json.RawMessage `json:"metadata,omitempty"`
	EncryptionMetadata json.RawMessage `json:"encryption_metadata,omitempty"`
	CreatedAt          time.Time       `json:"created_at"`
}

type Permission string

const (
	PermissionRead  Permission = "read"
	PermissionWrite Permission = "write"
	PermissionAdmin Permission = "admin"
)

type TaskPermission struct {
	TaskID     string     `json:"task_id"`
	AccountID  string     `json:"account_id"`
	Permission Permission `json:"permission"`
	CreatedAt  time.Time  `json:"created_at"`
}

type Invitation struct {
	ID                   string          `json:"invitation_id"`
	TaskID               string          `json:"task_id"`
	InviterAccount       string          `json:"inviter_account_id"`
	Permission           Permission      `json:"permission"`
	TokenHash            string          `json:"-"`
	InviterDeviceID      string          `json:"inviter_device_id,omitempty"`
	Nonce                string          `json:"nonce,omitempty"`
	TokenDigest          string          `json:"token_digest,omitempty"`
	KeyExchangeReference json.RawMessage `json:"key_exchange_material,omitempty"`
	Signature            string          `json:"signature,omitempty"`
	ExpiresAt            time.Time       `json:"expires_at"`
	CreatedAt            time.Time       `json:"created_at"`
	AcceptedBy           string          `json:"accepted_by,omitempty"`
	AcceptedAt           *time.Time      `json:"accepted_at,omitempty"`
	RevokedAt            *time.Time      `json:"revoked_at,omitempty"`
}

// ExecutionReport contains server execution output; it does not impersonate a
// signed device event. Consumers display it as external agent-produced content.
type ExecutionReport struct {
	OutputTruncated bool      `json:"output_truncated,omitempty"`
	RunID           string    `json:"run_id"`
	StepID          string    `json:"step_id"`
	Agent           string    `json:"agent"`
	Status          string    `json:"status"`
	Output          string    `json:"output"`
	UpdatedAt       time.Time `json:"updated_at"`
}
type ContinuityPackage struct {
	ExecutionReports []ExecutionReport  `json:"execution_reports,omitempty"`
	ProtocolVersion  string             `json:"protocol_version"`
	SchemaVersion    string             `json:"schema_version"`
	Capabilities     []string           `json:"capabilities"`
	TargetAgent      string             `json:"target_agent"`
	Task             Task               `json:"task"`
	Events           []Event            `json:"events"`
	Artifacts        []Artifact         `json:"artifact_manifests"`
	Branches         []TaskBranch       `json:"branches,omitempty"`
	Participants     []TaskParticipant  `json:"participants,omitempty"`
	AgentBindings    []TaskAgentBinding `json:"agent_bindings,omitempty"`
	Conflicts        []Conflict         `json:"conflicts,omitempty"`
	Snapshots        []TaskSnapshot     `json:"snapshots,omitempty"`
	GeneratedAt      time.Time          `json:"generated_at"`
}
