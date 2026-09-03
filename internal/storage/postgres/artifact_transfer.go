package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/agent-continuity/agent-continuity-master/internal/storage"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

const artifactSelect = `SELECT artifact_id,task_id,account_id,version_id,COALESCE(parent_version_id,''),content_type,filename,size,content_hash,COALESCE(chunk_manifest_hash,''),COALESCE(producer_type,''),COALESCE(producer_id,''),COALESCE(device_id,''),storage_reference,metadata,encryption_metadata,created_at FROM artifact_versions`

func (s *Store) ListArtifactVersions(ctx context.Context, taskID, artifactID string) ([]protocol.Artifact, error) {
	rows, err := s.pool.Query(ctx, artifactSelect+` WHERE task_id=$1 AND artifact_id=$2 ORDER BY created_at`, taskID, artifactID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]protocol.Artifact, 0)
	for rows.Next() {
		value, err := scanArtifact(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	if len(result) == 0 {
		return nil, storage.ErrNotFound
	}
	return result, rows.Err()
}

func (s *Store) CreateArtifactUpload(ctx context.Context, value protocol.ArtifactUploadSession) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO artifact_upload_sessions(upload_id,task_id,artifact_id,account_id,version_id,parent_version_id,filename,content_type,total_size,chunk_size,total_chunks,content_hash,encryption_metadata,status,created_at,expires_at,completed_at) VALUES($1,$2,$3,$4,$5,NULLIF($6,''),$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`, value.ID, value.TaskID, value.ArtifactID, value.AccountID, value.VersionID, value.ParentVersionID, value.Filename, value.ContentType, value.TotalSize, value.ChunkSize, value.TotalChunks, value.ContentHash, []byte(value.EncryptionMetadata), value.Status, value.CreatedAt, value.ExpiresAt, value.CompletedAt)
	return mapError(err)
}

func (s *Store) GetArtifactUpload(ctx context.Context, uploadID string) (protocol.ArtifactUploadSession, error) {
	return scanUpload(s.pool.QueryRow(ctx, `SELECT upload_id,task_id,artifact_id,account_id,version_id,COALESCE(parent_version_id,''),filename,content_type,total_size,chunk_size,total_chunks,content_hash,encryption_metadata,status,created_at,expires_at,completed_at FROM artifact_upload_sessions WHERE upload_id=$1`, uploadID))
}

func scanUpload(row rowScanner) (protocol.ArtifactUploadSession, error) {
	var value protocol.ArtifactUploadSession
	var encryption []byte
	err := row.Scan(&value.ID, &value.TaskID, &value.ArtifactID, &value.AccountID, &value.VersionID, &value.ParentVersionID, &value.Filename, &value.ContentType, &value.TotalSize, &value.ChunkSize, &value.TotalChunks, &value.ContentHash, &encryption, &value.Status, &value.CreatedAt, &value.ExpiresAt, &value.CompletedAt)
	if err != nil {
		return protocol.ArtifactUploadSession{}, mapError(err)
	}
	value.EncryptionMetadata = encryption
	return value, nil
}

func (s *Store) PutArtifactChunk(ctx context.Context, value protocol.ArtifactChunk) error {
	var hash string
	var size int64
	err := s.pool.QueryRow(ctx, `INSERT INTO artifact_chunks(upload_id,chunk_index,byte_offset,size,content_hash,storage_reference,created_at) SELECT $1,$2,$3,$4,$5,$6,$7 FROM artifact_upload_sessions u WHERE u.upload_id=$1 AND u.status='open' AND u.expires_at>now() AND $2>=0 AND $2<u.total_chunks AND $3=($2*u.chunk_size) AND $4=CASE WHEN $2=u.total_chunks-1 THEN u.total_size-$3 ELSE u.chunk_size END ON CONFLICT(upload_id,chunk_index) DO UPDATE SET content_hash=artifact_chunks.content_hash RETURNING content_hash,size`, value.UploadID, value.Index, value.Offset, value.Size, value.ContentHash, value.StorageRef, value.CreatedAt).Scan(&hash, &size)
	if errors.Is(err, pgx.ErrNoRows) {
		return storage.ErrUnauthorized
	}
	if err != nil {
		return mapError(err)
	}
	if hash != value.ContentHash || size != value.Size {
		return storage.ErrAlreadyExists
	}
	return nil
}

func (s *Store) ListArtifactChunks(ctx context.Context, uploadID string) ([]protocol.ArtifactChunk, error) {
	rows, err := s.pool.Query(ctx, `SELECT upload_id,chunk_index,byte_offset,size,content_hash,storage_reference,created_at FROM artifact_chunks WHERE upload_id=$1 ORDER BY chunk_index`, uploadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]protocol.ArtifactChunk, 0)
	for rows.Next() {
		var value protocol.ArtifactChunk
		if err := rows.Scan(&value.UploadID, &value.Index, &value.Offset, &value.Size, &value.ContentHash, &value.StorageRef, &value.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (s *Store) CompleteArtifactUpload(ctx context.Context, uploadID string, artifact protocol.Artifact, completedAt time.Time) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	session, err := scanUpload(tx.QueryRow(ctx, `SELECT upload_id,task_id,artifact_id,account_id,version_id,COALESCE(parent_version_id,''),filename,content_type,total_size,chunk_size,total_chunks,content_hash,encryption_metadata,status,created_at,expires_at,completed_at FROM artifact_upload_sessions WHERE upload_id=$1 FOR UPDATE`, uploadID))
	if err != nil {
		return err
	}
	if session.Status == "completed" {
		var currentHash string
		if err := tx.QueryRow(ctx, `SELECT content_hash FROM artifact_versions WHERE task_id=$1 AND artifact_id=$2 AND version_id=$3`, artifact.TaskID, artifact.ID, artifact.VersionID).Scan(&currentHash); err == nil && currentHash == artifact.ContentHash {
			return nil
		}
		return storage.ErrAlreadyExists
	}
	if session.Status != "open" || !completedAt.Before(session.ExpiresAt) || artifact.TaskID != session.TaskID || artifact.ID != session.ArtifactID || artifact.VersionID != session.VersionID || artifact.ContentHash != session.ContentHash || artifact.Size != session.TotalSize {
		return storage.ErrUnauthorized
	}
	var chunks int
	var size int64
	if err = tx.QueryRow(ctx, `SELECT count(*),COALESCE(sum(size),0) FROM artifact_chunks WHERE upload_id=$1`, uploadID).Scan(&chunks, &size); err != nil {
		return err
	}
	if chunks != session.TotalChunks || size != session.TotalSize {
		return storage.ErrSequenceReplay
	}
	var currentVersion string
	err = tx.QueryRow(ctx, `SELECT version_id FROM artifacts WHERE task_id=$1 AND artifact_id=$2 FOR UPDATE`, artifact.TaskID, artifact.ID).Scan(&currentVersion)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err == nil && currentVersion != artifact.VersionID && artifact.ParentVersionID != currentVersion {
		return storage.ErrAlreadyExists
	}
	if _, err = tx.Exec(ctx, `INSERT INTO artifact_versions(task_id,artifact_id,version_id,parent_version_id,account_id,content_type,filename,size,content_hash,chunk_manifest_hash,producer_type,producer_id,device_id,storage_reference,metadata,encryption_metadata,created_at) VALUES($1,$2,$3,NULLIF($4,''),$5,$6,$7,$8,$9,NULLIF($10,''),NULLIF($11,''),NULLIF($12,''),NULLIF($13,''),$14,$15,$16,$17) ON CONFLICT(task_id,artifact_id,version_id) DO NOTHING`, artifact.TaskID, artifact.ID, artifact.VersionID, artifact.ParentVersionID, artifact.AccountID, artifact.ContentType, artifact.Filename, artifact.Size, artifact.ContentHash, artifact.ChunkManifestHash, artifact.ProducerType, artifact.ProducerID, artifact.DeviceID, artifact.StorageRef, []byte(artifact.Metadata), []byte(artifact.EncryptionMetadata), artifact.CreatedAt); err != nil {
		return mapError(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO artifacts(task_id,artifact_id,account_id,version_id,parent_version_id,content_type,filename,size,content_hash,chunk_manifest_hash,producer_type,producer_id,device_id,storage_reference,metadata,encryption_metadata,created_at) VALUES($1,$2,$3,$4,NULLIF($5,''),$6,$7,$8,$9,NULLIF($10,''),NULLIF($11,''),NULLIF($12,''),NULLIF($13,''),$14,$15,$16,$17) ON CONFLICT(task_id,artifact_id) DO UPDATE SET account_id=EXCLUDED.account_id,version_id=EXCLUDED.version_id,parent_version_id=EXCLUDED.parent_version_id,content_type=EXCLUDED.content_type,filename=EXCLUDED.filename,size=EXCLUDED.size,content_hash=EXCLUDED.content_hash,chunk_manifest_hash=EXCLUDED.chunk_manifest_hash,producer_type=EXCLUDED.producer_type,producer_id=EXCLUDED.producer_id,device_id=EXCLUDED.device_id,storage_reference=EXCLUDED.storage_reference,metadata=EXCLUDED.metadata,encryption_metadata=EXCLUDED.encryption_metadata,created_at=EXCLUDED.created_at`, artifact.TaskID, artifact.ID, artifact.AccountID, artifact.VersionID, artifact.ParentVersionID, artifact.ContentType, artifact.Filename, artifact.Size, artifact.ContentHash, artifact.ChunkManifestHash, artifact.ProducerType, artifact.ProducerID, artifact.DeviceID, artifact.StorageRef, []byte(artifact.Metadata), []byte(artifact.EncryptionMetadata), artifact.CreatedAt); err != nil {
		return mapError(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE artifact_upload_sessions SET status='completed',completed_at=$2 WHERE upload_id=$1`, uploadID, completedAt.UTC()); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
