package protocol

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	SyncCapabilityBatchPush         = "batch_push"
	SyncCapabilityCursorPull        = "cursor_pull"
	SyncCapabilityMissingDiscovery  = "missing_discovery"
	SyncCapabilityArtifactManifests = "artifact_manifests"
	SyncCapabilityReconciliation    = "reconciliation"
	SyncCapabilityE2EE              = "e2ee"
)

var ServerSyncCapabilities = []string{
	SyncCapabilityBatchPush,
	SyncCapabilityCursorPull,
	SyncCapabilityMissingDiscovery,
	SyncCapabilityArtifactManifests,
	SyncCapabilityReconciliation,
	SyncCapabilityE2EE,
}

type SyncTaskState struct {
	TaskID       string   `json:"task_id"`
	HeadEventIDs []string `json:"head_event_ids"`
	EventCursor  uint64   `json:"event_cursor"`
}

type SyncHelloRequest struct {
	ProtocolVersions []string        `json:"protocol_versions"`
	Capabilities     []string        `json:"capabilities"`
	ClientVersion    string          `json:"client_version"`
	DeviceID         string          `json:"device_id"`
	Nonce            string          `json:"nonce"`
	Timestamp        time.Time       `json:"timestamp"`
	Tasks            []SyncTaskState `json:"tasks"`
	Proof            string          `json:"proof"`
}

type SyncTaskPlan struct {
	Task               Task       `json:"task"`
	ServerCursor       uint64     `json:"server_cursor"`
	AcknowledgedCursor uint64     `json:"acknowledged_cursor"`
	MissingEventIDs    []string   `json:"missing_event_ids,omitempty"`
	ArtifactManifests  []Artifact `json:"artifact_manifests,omitempty"`
}

type SyncHelloResponse struct {
	ProtocolVersion string         `json:"protocol_version"`
	Capabilities    []string       `json:"capabilities"`
	ServerTime      time.Time      `json:"server_time"`
	Tasks           []SyncTaskPlan `json:"tasks"`
}

type SyncPushRequest struct {
	DeviceID string  `json:"device_id"`
	Events   []Event `json:"events"`
}

type SyncEventAck struct {
	EventID        string   `json:"event_id"`
	Accepted       bool     `json:"accepted"`
	ServerSequence uint64   `json:"server_sequence"`
	Duplicate      bool     `json:"duplicate"`
	TaskHeads      []string `json:"task_heads"`
	ErrorCode      string   `json:"error_code,omitempty"`
	ErrorMessage   string   `json:"error_message,omitempty"`
}

type SyncPushResponse struct {
	Acknowledgements []SyncEventAck `json:"acknowledgements"`
}

type SyncPullCursor struct {
	TaskID         string `json:"task_id"`
	After          uint64 `json:"after"`
	Limit          int    `json:"limit,omitempty"`
	IncludePayload bool   `json:"include_payload"`
}

type SyncPullRequest struct {
	DeviceID string           `json:"device_id"`
	Tasks    []SyncPullCursor `json:"tasks"`
}

type SyncTaskBatch struct {
	Task              Task       `json:"task"`
	Events            []Event    `json:"events"`
	ArtifactManifests []Artifact `json:"artifact_manifests"`
	NextCursor        uint64     `json:"next_cursor"`
	HasMore           bool       `json:"has_more"`
}

type SyncPullResponse struct {
	Batches []SyncTaskBatch `json:"batches"`
	Devices []Device        `json:"devices"`
}

type SyncAck struct {
	TaskID string `json:"task_id"`
	Cursor uint64 `json:"cursor"`
}

type SyncAckRequest struct {
	DeviceID string    `json:"device_id"`
	Tasks    []SyncAck `json:"tasks"`
}

type ReconcileRequest struct {
	TaskID       string   `json:"task_id"`
	HeadEventIDs []string `json:"head_event_ids"`
	EventCursor  uint64   `json:"event_cursor"`
}

type ReconcileResult struct {
	TaskID        string     `json:"task_id"`
	ServerHeadIDs []string   `json:"server_head_event_ids"`
	ServerCursor  uint64     `json:"server_cursor"`
	MissingEvents []Event    `json:"missing_events"`
	OpenConflicts []Conflict `json:"open_conflicts"`
}

func SignSyncHello(value *SyncHelloRequest, privateKey ed25519.PrivateKey) error {
	if len(privateKey) != ed25519.PrivateKeySize {
		return errors.New("invalid Ed25519 private key")
	}
	data, err := SyncHelloSigningBytes(*value)
	if err != nil {
		return err
	}
	value.Proof = base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, data))
	return nil
}

// SignPortableSyncHello signs the language-neutral AC-SYNC-HELLO-1 form.
func SignPortableSyncHello(value *SyncHelloRequest, privateKey ed25519.PrivateKey) error {
	if len(privateKey) != ed25519.PrivateKeySize {
		return errors.New("invalid Ed25519 private key")
	}
	data := PortableSyncHelloSigningBytes(*value)
	value.Proof = PortableSignaturePrefix + base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, data))
	return nil
}

func VerifySyncHello(value SyncHelloRequest, encodedPublicKey string, now time.Time) error {
	if value.DeviceID == "" || len(value.Nonce) < 16 || len(value.Nonce) > 200 || value.Timestamp.IsZero() {
		return errors.New("device_id, nonce and timestamp are required")
	}
	if value.Timestamp.Before(now.Add(-5*time.Minute)) || value.Timestamp.After(now.Add(5*time.Minute)) {
		return errors.New("sync hello timestamp is outside the replay window")
	}
	publicKey, err := base64.RawURLEncoding.DecodeString(encodedPublicKey)
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return errors.New("invalid device public key")
	}
	proofValue := value.Proof
	portable := strings.HasPrefix(proofValue, PortableSignaturePrefix)
	if portable {
		proofValue = strings.TrimPrefix(proofValue, PortableSignaturePrefix)
	}
	proof, err := base64.RawURLEncoding.DecodeString(proofValue)
	if err != nil || len(proof) != ed25519.SignatureSize {
		return errors.New("invalid sync hello proof")
	}
	var data []byte
	if portable {
		data = PortableSyncHelloSigningBytes(value)
	} else {
		data, err = SyncHelloSigningBytes(value)
		if err != nil {
			return err
		}
	}
	if !ed25519.Verify(ed25519.PublicKey(publicKey), data, proof) {
		return errors.New("invalid sync hello proof")
	}
	return nil
}

// PortableSyncHelloSigningBytes returns a deterministic representation shared
// by every device SDK. Sets and task state are sorted before encoding.
func PortableSyncHelloSigningBytes(value SyncHelloRequest) []byte {
	versions := sortedUnique(value.ProtocolVersions)
	capabilities := sortedUnique(value.Capabilities)
	tasks := append([]SyncTaskState(nil), value.Tasks...)
	for index := range tasks {
		tasks[index].HeadEventIDs = sortedUnique(tasks[index].HeadEventIDs)
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].TaskID < tasks[j].TaskID })
	fields := []string{
		"AC-SYNC-HELLO-1",
		portableStrings(versions),
		portableStrings(capabilities),
		portableString(value.ClientVersion),
		portableString(value.DeviceID),
		portableString(value.Nonce),
		strconv.FormatInt(value.Timestamp.UnixMilli(), 10),
		strconv.Itoa(len(tasks)),
	}
	for _, task := range tasks {
		fields = append(fields,
			portableString(task.TaskID),
			portableStrings(task.HeadEventIDs),
			strconv.FormatUint(task.EventCursor, 10),
		)
	}
	return []byte(strings.Join(fields, "\n"))
}

func SyncHelloSigningBytes(value SyncHelloRequest) ([]byte, error) {
	value.Proof = ""
	value.ProtocolVersions = sortedUnique(value.ProtocolVersions)
	value.Capabilities = sortedUnique(value.Capabilities)
	value.Tasks = append([]SyncTaskState(nil), value.Tasks...)
	for index := range value.Tasks {
		value.Tasks[index].HeadEventIDs = sortedUnique(value.Tasks[index].HeadEventIDs)
	}
	sort.Slice(value.Tasks, func(i, j int) bool { return value.Tasks[i].TaskID < value.Tasks[j].TaskID })
	return json.Marshal(value)
}

func NegotiateCapabilities(client, server []string) []string {
	available := map[string]bool{}
	for _, capability := range server {
		available[capability] = true
	}
	result := make([]string, 0)
	for _, capability := range sortedUnique(client) {
		if available[capability] {
			result = append(result, capability)
		}
	}
	return result
}

func SupportsProtocol(versions []string, supported string) bool {
	for _, value := range versions {
		if value == supported {
			return true
		}
	}
	return false
}

func sortedUnique(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}
