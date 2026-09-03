package memory

import (
	"context"
	"sort"
	"time"

	"github.com/agent-continuity/agent-continuity-master/internal/storage"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

func (s *Store) ListArtifactVersions(_ context.Context, taskID, artifactID string) ([]protocol.Artifact, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := s.artifactVersions[artifactKey(taskID, artifactID)]
	if len(items) == 0 {
		return nil, storage.ErrNotFound
	}
	result := make([]protocol.Artifact, len(items))
	for i := range items {
		result[i] = cloneArtifact(items[i])
	}
	return result, nil
}

func (s *Store) CreateArtifactUpload(_ context.Context, value protocol.ArtifactUploadSession) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tasks[value.TaskID]; !ok {
		return storage.ErrNotFound
	}
	if _, ok := s.artifactUploads[value.ID]; ok {
		return storage.ErrAlreadyExists
	}
	s.artifactUploads[value.ID] = cloneUpload(value)
	s.artifactChunks[value.ID] = map[int]protocol.ArtifactChunk{}
	return nil
}

func (s *Store) GetArtifactUpload(_ context.Context, uploadID string) (protocol.ArtifactUploadSession, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.artifactUploads[uploadID]
	if !ok {
		return protocol.ArtifactUploadSession{}, storage.ErrNotFound
	}
	return cloneUpload(value), nil
}

func (s *Store) PutArtifactChunk(_ context.Context, value protocol.ArtifactChunk) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.artifactUploads[value.UploadID]
	if !ok {
		return storage.ErrNotFound
	}
	if session.Status != "open" || !s.now().Before(session.ExpiresAt) || value.Index < 0 || value.Index >= session.TotalChunks || value.Offset != int64(value.Index)*session.ChunkSize {
		return storage.ErrUnauthorized
	}
	expectedSize := session.ChunkSize
	if value.Index == session.TotalChunks-1 {
		expectedSize = session.TotalSize - value.Offset
	}
	if value.Size != expectedSize {
		return storage.ErrSequenceReplay
	}
	if existing, ok := s.artifactChunks[value.UploadID][value.Index]; ok {
		if existing.ContentHash == value.ContentHash && existing.Size == value.Size {
			return nil
		}
		return storage.ErrAlreadyExists
	}
	s.artifactChunks[value.UploadID][value.Index] = value
	return nil
}

func (s *Store) ListArtifactChunks(_ context.Context, uploadID string) ([]protocol.ArtifactChunk, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.artifactUploads[uploadID]; !ok {
		return nil, storage.ErrNotFound
	}
	result := make([]protocol.ArtifactChunk, 0, len(s.artifactChunks[uploadID]))
	for _, value := range s.artifactChunks[uploadID] {
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Index < result[j].Index })
	return result, nil
}

func (s *Store) CompleteArtifactUpload(_ context.Context, uploadID string, artifact protocol.Artifact, completedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.artifactUploads[uploadID]
	if !ok {
		return storage.ErrNotFound
	}
	if session.Status == "completed" {
		if current := s.artifacts[artifactKey(artifact.TaskID, artifact.ID)]; current.VersionID == artifact.VersionID && current.ContentHash == artifact.ContentHash {
			return nil
		}
		return storage.ErrAlreadyExists
	}
	if session.Status != "open" || !completedAt.Before(session.ExpiresAt) || artifact.TaskID != session.TaskID || artifact.ID != session.ArtifactID || artifact.VersionID != session.VersionID || artifact.ContentHash != session.ContentHash || artifact.Size != session.TotalSize {
		return storage.ErrUnauthorized
	}
	chunks := s.artifactChunks[uploadID]
	if len(chunks) != session.TotalChunks {
		return storage.ErrSequenceReplay
	}
	key := artifactKey(artifact.TaskID, artifact.ID)
	if old, ok := s.artifacts[key]; ok && artifact.ParentVersionID != old.VersionID {
		return storage.ErrAlreadyExists
	}
	s.artifacts[key] = cloneArtifact(artifact)
	s.artifactVersions[key] = append(s.artifactVersions[key], cloneArtifact(artifact))
	now := completedAt.UTC()
	session.Status = "completed"
	session.CompletedAt = &now
	s.artifactUploads[uploadID] = session
	return nil
}

func cloneUpload(value protocol.ArtifactUploadSession) protocol.ArtifactUploadSession {
	value.EncryptionMetadata = append([]byte(nil), value.EncryptionMetadata...)
	return value
}
