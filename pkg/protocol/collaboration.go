package protocol

import (
	"encoding/json"
	"time"
)

type AgentProfile struct {
	ID           string          `json:"agent_id"`
	AccountID    string          `json:"account_id"`
	Name         string          `json:"name"`
	Provider     string          `json:"provider"`
	Model        string          `json:"model,omitempty"`
	Capabilities []string        `json:"capabilities"`
	Card         json.RawMessage `json:"card,omitempty"`
	CreatedAt    time.Time       `json:"created_at"`
	RevokedAt    *time.Time      `json:"revoked_at,omitempty"`
}

type TaskParticipant struct {
	ID        string     `json:"participant_id"`
	TaskID    string     `json:"task_id"`
	AccountID string     `json:"account_id,omitempty"`
	AgentID   string     `json:"agent_id,omitempty"`
	Kind      string     `json:"kind"`
	Role      string     `json:"role"`
	Status    string     `json:"status"`
	JoinedAt  time.Time  `json:"joined_at"`
	LeftAt    *time.Time `json:"left_at,omitempty"`
}

type TaskAgentBinding struct {
	TaskID           string          `json:"task_id"`
	AgentID          string          `json:"agent_id"`
	ParticipantID    string          `json:"participant_id"`
	NativeSessionRef string          `json:"native_session_ref,omitempty"`
	ResumeMode       string          `json:"resume_mode"`
	Capabilities     []string        `json:"capabilities"`
	Metadata         json.RawMessage `json:"metadata,omitempty"`
	CreatedAt        time.Time       `json:"created_at"`
}

type Notification struct {
	ID        string          `json:"notification_id"`
	AccountID string          `json:"account_id"`
	TaskID    string          `json:"task_id,omitempty"`
	Kind      string          `json:"kind"`
	ActorID   string          `json:"actor_id,omitempty"`
	Subject   string          `json:"subject"`
	Details   json.RawMessage `json:"details,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	ReadAt    *time.Time      `json:"read_at,omitempty"`
}

type Presence struct {
	TaskID        string    `json:"task_id"`
	ParticipantID string    `json:"participant_id"`
	AccountID     string    `json:"account_id"`
	DeviceID      string    `json:"device_id,omitempty"`
	AgentID       string    `json:"agent_id,omitempty"`
	Status        string    `json:"status"`
	LastSeenAt    time.Time `json:"last_seen_at"`
}

type RealtimeMessage struct {
	Type           string          `json:"type"`
	TaskID         string          `json:"task_id"`
	ServerSequence uint64          `json:"server_sequence,omitempty"`
	ResourceID     string          `json:"resource_id,omitempty"`
	Payload        json.RawMessage `json:"payload,omitempty"`
	SentAt         time.Time       `json:"sent_at"`
}
