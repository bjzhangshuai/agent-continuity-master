package protocol

import (
	"encoding/json"
	"time"
)

type ArtifactUploadSession struct {
	ID                 string          `json:"upload_id"`
	TaskID             string          `json:"task_id"`
	ArtifactID         string          `json:"artifact_id"`
	AccountID          string          `json:"account_id"`
	VersionID          string          `json:"version_id"`
	ParentVersionID    string          `json:"parent_version_id,omitempty"`
	Filename           string          `json:"filename"`
	ContentType        string          `json:"content_type"`
	TotalSize          int64           `json:"total_size"`
	ChunkSize          int64           `json:"chunk_size"`
	TotalChunks        int             `json:"total_chunks"`
	ContentHash        string          `json:"content_hash"`
	EncryptionMetadata json.RawMessage `json:"encryption_metadata,omitempty"`
	Status             string          `json:"status"`
	CreatedAt          time.Time       `json:"created_at"`
	ExpiresAt          time.Time       `json:"expires_at"`
	CompletedAt        *time.Time      `json:"completed_at,omitempty"`
}

type ArtifactChunk struct {
	UploadID    string    `json:"upload_id"`
	Index       int       `json:"index"`
	Offset      int64     `json:"offset"`
	Size        int64     `json:"size"`
	ContentHash string    `json:"content_hash"`
	StorageRef  string    `json:"-"`
	CreatedAt   time.Time `json:"created_at"`
}

type ArtifactUploadStatus struct {
	Session        ArtifactUploadSession `json:"session"`
	ReceivedChunks []int                 `json:"received_chunks"`
	MissingChunks  []int                 `json:"missing_chunks"`
	ReceivedBytes  int64                 `json:"received_bytes"`
}

type EncryptedArtifactChunk struct {
	Version          uint32 `json:"version"`
	Algorithm        string `json:"algorithm"`
	KeyVersion       uint32 `json:"key_version"`
	ChunkIndex       int    `json:"chunk_index"`
	Nonce            string `json:"nonce"`
	CiphertextDigest string `json:"ciphertext_digest"`
	PlaintextDigest  string `json:"plaintext_digest"`
}

type ArtifactEncryptionMetadata struct {
	Version             uint32                   `json:"version"`
	Algorithm           string                   `json:"algorithm"`
	KeyVersion          uint32                   `json:"key_version"`
	OriginalContentType string                   `json:"original_content_type"`
	PlaintextSize       int64                    `json:"plaintext_size"`
	PlaintextDigest     string                   `json:"plaintext_digest"`
	PlaintextChunkSize  int64                    `json:"plaintext_chunk_size"`
	Chunks              []EncryptedArtifactChunk `json:"chunks"`
}
