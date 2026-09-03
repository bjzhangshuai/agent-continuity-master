package protocol

import (
	"encoding/json"
	"time"
)

type AuthSession struct {
	ID               string     `json:"session_id"`
	FamilyID         string     `json:"family_id"`
	AccountID        string     `json:"account_id"`
	Subject          string     `json:"subject"`
	RefreshTokenHash string     `json:"-"`
	CreatedAt        time.Time  `json:"created_at"`
	ExpiresAt        time.Time  `json:"expires_at"`
	UsedAt           *time.Time `json:"used_at,omitempty"`
	RevokedAt        *time.Time `json:"revoked_at,omitempty"`
	ReplacedBy       string     `json:"replaced_by,omitempty"`
}

type APIKey struct {
	ID         string     `json:"api_key_id"`
	AccountID  string     `json:"account_id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	SecretHash string     `json:"-"`
	Scopes     []string   `json:"scopes"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

type AuditEvent struct {
	ID           string          `json:"audit_event_id"`
	AccountID    string          `json:"account_id"`
	ActorID      string          `json:"actor_id"`
	Action       string          `json:"action"`
	ResourceType string          `json:"resource_type"`
	ResourceID   string          `json:"resource_id"`
	RequestID    string          `json:"request_id,omitempty"`
	SourceHash   string          `json:"source_hash,omitempty"`
	Details      json.RawMessage `json:"details,omitempty"`
	CreatedAt    time.Time       `json:"created_at"`
}

type WrappedTaskKey struct {
	DeviceID           string `json:"device_id"`
	EphemeralPublicKey string `json:"ephemeral_public_key"`
	Nonce              string `json:"nonce"`
	Ciphertext         string `json:"ciphertext"`
}

type TaskKeySet struct {
	TaskID      string           `json:"task_id"`
	Version     uint32           `json:"key_version"`
	Algorithm   string           `json:"algorithm"`
	CreatedBy   string           `json:"created_by"`
	WrappedKeys []WrappedTaskKey `json:"wrapped_keys"`
	CreatedAt   time.Time        `json:"created_at"`
}

type EncryptedPayload struct {
	Version             uint32 `json:"version"`
	Algorithm           string `json:"algorithm"`
	KeyVersion          uint32 `json:"key_version"`
	Nonce               string `json:"nonce"`
	Ciphertext          string `json:"ciphertext"`
	OriginalContentType string `json:"original_content_type"`
	PlaintextDigest     string `json:"plaintext_digest"`
}
