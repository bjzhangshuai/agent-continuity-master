package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	artifactstore "github.com/agent-continuity/agent-continuity-master/internal/artifact"
	"github.com/agent-continuity/agent-continuity-master/internal/auth"
	"github.com/agent-continuity/agent-continuity-master/internal/storage"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

func (s *Server) createArtifactUpload(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeTask(w, r, protocol.PermissionWrite)
	if !ok {
		return
	}
	var input struct {
		ArtifactID         string          `json:"artifact_id"`
		VersionID          string          `json:"version_id"`
		ParentVersionID    string          `json:"parent_version_id"`
		Filename           string          `json:"filename"`
		ContentType        string          `json:"content_type"`
		TotalSize          int64           `json:"total_size"`
		ChunkSize          int64           `json:"chunk_size"`
		ContentHash        string          `json:"content_hash"`
		EncryptionMetadata json.RawMessage `json:"encryption_metadata"`
	}
	if !s.decodeJSON(w, r, &input) {
		return
	}
	if input.ArtifactID == "" || strings.ContainsAny(input.ArtifactID, `/\\`) || input.TotalSize <= 0 || input.TotalSize > 1<<30 || !validSHA256(input.ContentHash) {
		writeError(w, http.StatusBadRequest, "invalid_upload", "artifact ID, 1..1GiB size, and sha256 content hash are required")
		return
	}
	if input.ChunkSize == 0 {
		input.ChunkSize = min(4<<20, s.maxBodyBytes)
	}
	if input.ChunkSize <= 0 || input.ChunkSize > s.maxBodyBytes {
		writeError(w, http.StatusBadRequest, "invalid_chunk_size", "chunk size must be positive and no larger than the request body limit")
		return
	}
	if input.ContentType == "" {
		input.ContentType = "application/octet-stream"
	}
	if input.VersionID == "" {
		input.VersionID, _ = protocol.NewID("version")
	}
	if _, err := s.store.GetArtifact(r.Context(), r.PathValue("taskID"), input.ArtifactID); err == nil {
		if !s.authorizeArtifactAfterTask(w, r, claims, input.ArtifactID, protocol.PermissionWrite) {
			return
		}
	} else if !errors.Is(err, storage.ErrNotFound) {
		writeStorageError(w, err)
		return
	}
	if current, err := s.store.GetArtifact(r.Context(), r.PathValue("taskID"), input.ArtifactID); err == nil && input.ParentVersionID != current.VersionID {
		writeError(w, http.StatusConflict, "stale_artifact_parent", "parent_version_id must equal the current artifact version")
		return
	}
	totalChunks := int((input.TotalSize + input.ChunkSize - 1) / input.ChunkSize)
	if totalChunks > 10_000 {
		writeError(w, http.StatusBadRequest, "too_many_chunks", "upload cannot exceed 10000 chunks")
		return
	}
	id, _ := protocol.NewID("upload")
	now := time.Now().UTC()
	value := protocol.ArtifactUploadSession{ID: id, TaskID: r.PathValue("taskID"), ArtifactID: input.ArtifactID, AccountID: claims.AccountID, VersionID: input.VersionID, ParentVersionID: input.ParentVersionID, Filename: safeFilename(input.Filename), ContentType: input.ContentType, TotalSize: input.TotalSize, ChunkSize: input.ChunkSize, TotalChunks: totalChunks, ContentHash: input.ContentHash, EncryptionMetadata: input.EncryptionMetadata, Status: "open", CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour)}
	if err := s.store.CreateArtifactUpload(r.Context(), value); err != nil {
		writeStorageError(w, err)
		return
	}
	s.audit(r, claims, "artifact.upload.started", "artifact_upload", value.ID, mustJSON(map[string]any{"task_id": value.TaskID, "artifact_id": value.ArtifactID, "total_size": value.TotalSize, "total_chunks": value.TotalChunks}))
	writeJSON(w, http.StatusCreated, uploadStatus(value, nil))
}

func (s *Server) getArtifactUpload(w http.ResponseWriter, r *http.Request) {
	claims, session, ok := s.authorizeArtifactUpload(w, r)
	if !ok {
		return
	}
	_ = claims
	chunks, err := s.store.ListArtifactChunks(r.Context(), session.ID)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, uploadStatus(session, chunks))
}

func (s *Server) putArtifactChunk(w http.ResponseWriter, r *http.Request) {
	_, session, ok := s.authorizeArtifactUpload(w, r)
	if !ok {
		return
	}
	index, err := strconv.Atoi(r.PathValue("chunkIndex"))
	if err != nil || index < 0 || index >= session.TotalChunks {
		writeError(w, http.StatusBadRequest, "invalid_chunk_index", "chunk index is outside the upload manifest")
		return
	}
	offset := int64(index) * session.ChunkSize
	expectedSize := session.ChunkSize
	if index == session.TotalChunks-1 {
		expectedSize = session.TotalSize - offset
	}
	hash := r.Header.Get("X-Content-SHA256")
	if !validSHA256(hash) {
		writeError(w, http.StatusBadRequest, "invalid_chunk_hash", "X-Content-SHA256 must contain a sha256 digest")
		return
	}
	ref, size, err := s.artifacts.PutChunk(session.ID, index, http.MaxBytesReader(w, r.Body, expectedSize), hash, expectedSize)
	if err != nil {
		if errors.Is(err, artifactstore.ErrHashMismatch) {
			writeError(w, http.StatusUnprocessableEntity, "hash_mismatch", err.Error())
		} else {
			if errors.Is(err, artifactstore.ErrStagingFull) {
				writeError(w, 507, "staging_full", "Artifact staging capacity exhausted")
			} else {
				writeError(w, http.StatusBadRequest, "chunk_rejected", err.Error())
			}
		}
		return
	}
	value := protocol.ArtifactChunk{UploadID: session.ID, Index: index, Offset: offset, Size: size, ContentHash: hash, StorageRef: ref, CreatedAt: time.Now().UTC()}
	if err := s.store.PutArtifactChunk(r.Context(), value); err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func (s *Server) completeArtifactUpload(w http.ResponseWriter, r *http.Request) {
	claims, session, ok := s.authorizeArtifactUpload(w, r)
	if !ok {
		return
	}
	chunks, err := s.store.ListArtifactChunks(r.Context(), session.ID)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	if len(chunks) != session.TotalChunks {
		writeJSON(w, http.StatusConflict, uploadStatus(session, chunks))
		return
	}
	refs := make([]string, len(chunks))
	manifest := make([]map[string]any, len(chunks))
	for i, chunk := range chunks {
		if chunk.Index != i {
			writeError(w, http.StatusConflict, "missing_chunk", "chunk manifest is not contiguous")
			return
		}
		refs[i] = chunk.StorageRef
		manifest[i] = map[string]any{"index": chunk.Index, "size": chunk.Size, "content_hash": chunk.ContentHash}
	}
	manifestRaw, _ := json.Marshal(manifest)
	ref, size, err := s.artifacts.Assemble(session.ID, refs, session.ContentHash, session.TotalSize)
	if err != nil {
		if errors.Is(err, artifactstore.ErrHashMismatch) {
			writeError(w, http.StatusUnprocessableEntity, "hash_mismatch", err.Error())
		} else {
			if errors.Is(err, artifactstore.ErrStagingFull) {
				writeError(w, 507, "staging_full", "Artifact staging capacity exhausted")
			} else {
				writeError(w, http.StatusBadRequest, "assembly_failed", err.Error())
			}
		}
		return
	}
	now := time.Now().UTC()
	artifact := protocol.Artifact{ID: session.ArtifactID, TaskID: session.TaskID, AccountID: session.AccountID, VersionID: session.VersionID, ParentVersionID: session.ParentVersionID, ContentType: session.ContentType, Filename: session.Filename, Size: size, ContentHash: session.ContentHash, ChunkManifestHash: protocol.Digest(manifestRaw), StorageRef: ref, EncryptionMetadata: session.EncryptionMetadata, CreatedAt: now}
	if err := s.store.CompleteArtifactUpload(r.Context(), session.ID, artifact, now); err != nil {
		writeStorageError(w, err)
		return
	}
	if err := s.artifacts.RemoveUpload(session.ID); err != nil {
		s.logger.Warn("remove completed artifact chunks", "upload_id", session.ID, "error", err)
	}
	s.audit(r, claims, "artifact.upload.completed", "artifact", artifact.ID, mustJSON(map[string]any{"task_id": artifact.TaskID, "version_id": artifact.VersionID, "size": artifact.Size, "content_hash": artifact.ContentHash}))
	s.recordUsage(r, claims, "artifact.bytes", artifact.Size, "byte", artifact.ID, artifact.VersionID, mustJSON(map[string]string{"task_id": artifact.TaskID, "content_hash": artifact.ContentHash}))
	s.metricSet.ArtifactUploads.Add(1)
	s.metricSet.ArtifactUploadBytes.Add(uint64(artifact.Size))
	s.publish(protocol.RealtimeMessage{Type: "artifact.available", TaskID: artifact.TaskID, ResourceID: artifact.ID, SentAt: now})
	writeJSON(w, http.StatusCreated, artifact)
}

func (s *Server) listArtifactVersions(w http.ResponseWriter, r *http.Request) {
	_, ok := s.authorizeArtifact(w, r, protocol.PermissionRead)
	if !ok {
		return
	}
	items, err := s.store.ListArtifactVersions(r.Context(), r.PathValue("taskID"), r.PathValue("artifactID"))
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"versions": items})
}

func (s *Server) authorizeArtifactUpload(w http.ResponseWriter, r *http.Request) (auth.Claims, protocol.ArtifactUploadSession, bool) {
	claims, ok := s.authorizeScope(w, r, "tasks:write")
	if !ok {
		return auth.Claims{}, protocol.ArtifactUploadSession{}, false
	}
	session, err := s.store.GetArtifactUpload(r.Context(), r.PathValue("uploadID"))
	if err != nil {
		writeStorageError(w, err)
		return auth.Claims{}, protocol.ArtifactUploadSession{}, false
	}
	allowed, accessErr := s.taskAccessAllowed(r, claims, session.TaskID, protocol.PermissionWrite)
	if session.AccountID != claims.AccountID || accessErr != nil || !allowed {
		writeError(w, http.StatusForbidden, "forbidden", "artifact upload permission denied")
		return auth.Claims{}, protocol.ArtifactUploadSession{}, false
	}
	return claims, session, true
}

func uploadStatus(session protocol.ArtifactUploadSession, chunks []protocol.ArtifactChunk) protocol.ArtifactUploadStatus {
	received := make([]int, 0, len(chunks))
	receivedSet := map[int]struct{}{}
	var receivedBytes int64
	for _, chunk := range chunks {
		received = append(received, chunk.Index)
		receivedSet[chunk.Index] = struct{}{}
		receivedBytes += chunk.Size
	}
	missing := make([]int, 0, session.TotalChunks-len(received))
	for i := 0; i < session.TotalChunks; i++ {
		if _, ok := receivedSet[i]; !ok {
			missing = append(missing, i)
		}
	}
	return protocol.ArtifactUploadStatus{Session: session, ReceivedChunks: received, MissingChunks: missing, ReceivedBytes: receivedBytes}
}

func validSHA256(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+64 {
		return false
	}
	for _, char := range strings.TrimPrefix(value, "sha256:") {
		if !strings.ContainsRune("0123456789abcdef", char) {
			return false
		}
	}
	return true
}
