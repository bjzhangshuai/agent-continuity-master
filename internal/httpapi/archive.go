package httpapi

import (
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/agent-continuity/agent-continuity-master/internal/archive"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

// Mobile/browser convenience transport. Large files use resumable chunk uploads.
func (s *Server) uploadContent(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authorizeTask(w, r, protocol.PermissionWrite); !ok {
		return
	}
	var input struct {
		ArtifactID      string `json:"artifact_id"`
		VersionID       string `json:"version_id"`
		ParentVersionID string `json:"parent_version_id"`
		Filename        string `json:"filename"`
		ContentType     string `json:"content_type"`
		ContentBase64   string `json:"content_base64"`
	}
	if !s.decodeJSON(w, r, &input) {
		return
	}
	if input.ArtifactID == "" || input.VersionID == "" || len(input.ArtifactID) > 200 || len(input.VersionID) > 200 || strings.ContainsAny(input.ArtifactID+input.VersionID, `/\\`) {
		writeError(w, 400, "invalid_content", "Stable artifact_id and version_id are required")
		return
	}
	if len(input.ContentBase64) > base64.StdEncoding.EncodedLen(8<<20) {
		writeError(w, 413, "content_too_large", "Use the resumable artifact API for content larger than 8 MiB")
		return
	}
	content, err := base64.StdEncoding.DecodeString(input.ContentBase64)
	if err != nil {
		writeError(w, 400, "invalid_base64", "content_base64 must use standard base64")
		return
	}
	copy := r.Clone(r.Context())
	copy.Header = r.Header.Clone()
	copy.Body = io.NopCloser(bytes.NewReader(content))
	copy.ContentLength = int64(len(content))
	copy.SetPathValue("artifactID", input.ArtifactID)
	copy.Header.Set("X-Content-SHA256", protocol.Digest(content))
	copy.Header.Set("X-Artifact-Version", input.VersionID)
	copy.Header.Set("X-Parent-Version", input.ParentVersionID)
	copy.Header.Set("X-Filename", input.Filename)
	copy.Header.Set("Content-Type", input.ContentType)
	s.putArtifact(w, copy)
}

func WithArchive(w *archive.Worker) Option { return func(s *Server) { s.archive = w } }
func (s *Server) archiveConfig(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authorizeScope(w, r, "tasks:read"); !ok {
		return
	}
	if s.archive == nil {
		writeJSON(w, 200, map[string]any{"enabled": false})
		return
	}
	targets := []map[string]string{}
	for _, t := range s.archive.Config.Targets {
		targets = append(targets, map[string]string{"id": t.ID, "provider": t.Provider})
	}
	writeJSON(w, 200, map[string]any{"enabled": s.archive.Config.Enabled, "queue": "nats-jetstream", "consistency": "transactional-outbox-at-least-once-idempotent", "targets": targets, "replicas": s.archive.Config.Broker.Replicas, "concurrency_per_worker": s.archive.Config.Concurrency, "max_attempts": s.archive.Config.MaxAttempts, "max_staging_bytes": s.archive.Config.MaxStagingBytes, "lifecycle": s.archive.Config.Lifecycle})
}
func (s *Server) archiveAvailable(w http.ResponseWriter) bool {
	if s.archive == nil {
		writeError(w, 503, "archive_disabled", "对象归档未配置 / Object archival is not configured")
		return false
	}
	return true
}
func (s *Server) listStorageJobs(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeTask(w, r, protocol.PermissionRead)
	if !ok || !s.archiveAvailable(w) {
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	if after < 0 {
		after = 0
	}
	jobs, err := s.archive.Queue.List(r.Context(), r.PathValue("taskID"), after, 100)
	if err != nil {
		writeInternal(w, err)
		return
	}
	visible := []archive.Job{}
	var next int64
	task, err := s.store.GetTask(r.Context(), r.PathValue("taskID"))
	if err != nil {
		writeStorageError(w, err)
		return
	}
	for _, j := range jobs {
		next = j.ID
		if j.Kind == "artifact" && task.AccountID != claims.AccountID {
			permissions, err := s.store.ListArtifactPermissions(r.Context(), j.TaskID, j.ResourceID)
			if err != nil {
				writeStorageError(w, err)
				return
			}
			if len(permissions) > 0 && !artifactPermissionAllows(permissions, claims.AccountID, protocol.PermissionRead) {
				continue
			}
		}
		visible = append(visible, j)
	}
	writeJSON(w, 200, map[string]any{"jobs": visible, "next_after": next, "has_more": len(jobs) == 100, "cloud_ready_is_state_stored": true})
}
func (s *Server) storageJob(w http.ResponseWriter, r *http.Request, permission protocol.Permission) (archive.Job, bool) {
	claims, ok := s.authorizeTask(w, r, permission)
	if !ok || !s.archiveAvailable(w) {
		return archive.Job{}, false
	}
	id, err := strconv.ParseInt(r.PathValue("jobID"), 10, 64)
	if err != nil || id < 1 {
		writeError(w, 400, "invalid_job", "Invalid archive job ID")
		return archive.Job{}, false
	}
	j, err := s.archive.Queue.Get(r.Context(), id)
	if errors.Is(err, archive.ErrNotFound) || (err == nil && j.TaskID != r.PathValue("taskID")) {
		writeError(w, 404, "not_found", "Archive job not found")
		return archive.Job{}, false
	}
	if err != nil {
		writeInternal(w, err)
		return archive.Job{}, false
	}
	if j.Kind == "artifact" && !s.authorizeArtifactAfterTask(w, r, claims, j.ResourceID, permission) {
		return archive.Job{}, false
	}
	return j, true
}
func (s *Server) getStorageJob(w http.ResponseWriter, r *http.Request) {
	j, ok := s.storageJob(w, r, protocol.PermissionRead)
	if ok {
		writeJSON(w, 200, j)
	}
}
func (s *Server) retryStorageJob(w http.ResponseWriter, r *http.Request) {
	j, ok := s.storageJob(w, r, protocol.PermissionAdmin)
	if !ok {
		return
	}
	if err := s.archive.Queue.Retry(r.Context(), j.ID); err != nil {
		if errors.Is(err, archive.ErrLeaseLost) {
			writeError(w, 409, "not_dead", "Only dead-letter jobs can be replayed")
		} else {
			writeInternal(w, err)
		}
		return
	}
	writeJSON(w, 202, map[string]any{"id": j.ID, "state": "queued"})
}
func (s *Server) backfillStorage(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authorizeTask(w, r, protocol.PermissionAdmin); !ok || !s.archiveAvailable(w) {
		return
	}
	if !s.archive.Config.Enabled {
		writeError(w, 409, "archive_disabled", "Enable archival before backfill")
		return
	}
	n, err := s.archive.Queue.Backfill(r.Context(), r.PathValue("taskID"), 100)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, 202, map[string]any{"enqueued": n, "batch_limit": 100})
}
func (s *Server) downloadStorageJob(w http.ResponseWriter, r *http.Request) {
	j, ok := s.storageJob(w, r, protocol.PermissionRead)
	if !ok {
		return
	}
	if j.State != "stored" {
		w.Header().Set("Retry-After", "5")
		writeError(w, 409, "archive_not_ready", "归档尚未完成 / Archive not ready")
		return
	}
	f, err := s.archive.Download(r.Context(), j)
	if err != nil {
		writeError(w, 503, "archive_unavailable", "归档下载失败或校验不通过 / Archive unavailable or integrity check failed")
		return
	}
	defer f.Close()
	defer os.Remove(f.Name())
	downloadID, err := protocol.NewID("archive_download")
	if err == nil {
		err = s.archive.Queue.RecordDownload(r.Context(), j, downloadID)
	}
	if err != nil {
		writeError(w, 503, "meter_unavailable", "Download metering unavailable; retry later")
		return
	}
	w.Header().Set("Content-Type", j.ContentType)
	w.Header().Set("Content-Disposition", `attachment; filename="archive-content"`)
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-SHA256", j.ContentHash)
	http.ServeContent(w, r, "archive-content", j.UpdatedAt, f)
}
func (s *Server) openArtifactContent(r *http.Request, a protocol.Artifact) (*os.File, bool, error) {
	err := s.artifacts.Verify(a.StorageRef, a.ContentHash)
	if err == nil {
		f, err := s.artifacts.Open(a.StorageRef)
		return f, false, err
	}
	if s.archive == nil {
		return nil, false, err
	}
	j, err := s.archive.Queue.FindArtifact(r.Context(), a.TaskID, a.ID, a.VersionID)
	if err != nil {
		return nil, false, err
	}
	f, err := s.archive.Download(r.Context(), j)
	if err == nil {
		id, e := protocol.NewID("archive_download")
		if e == nil {
			e = s.archive.Queue.RecordDownload(r.Context(), j, id)
		}
		if e != nil {
			f.Close()
			os.Remove(f.Name())
			return nil, false, e
		}
	}
	return f, true, err
}

func (s *Server) deleteStorageJob(w http.ResponseWriter, r *http.Request) {
	j, ok := s.storageJob(w, r, protocol.PermissionAdmin)
	if !ok {
		return
	}
	if !s.archive.Config.Lifecycle.AllowDelete {
		writeError(w, 409, "delete_disabled", "Operator has not enabled archive deletion")
		return
	}
	var in struct {
		ConfirmHash string `json:"confirm_hash"`
		Reason      string `json:"reason"`
	}
	if !s.decodeJSON(w, r, &in) {
		return
	}
	if in.ConfirmHash != j.ContentHash || len(strings.TrimSpace(in.Reason)) < 8 || len(in.Reason) > 500 {
		writeError(w, 400, "confirmation_required", "Exact content hash and a reason (8..500 characters) required")
		return
	}
	c, ok := s.authorizeTask(w, r, protocol.PermissionAdmin)
	if !ok {
		return
	}
	if err := s.archive.Queue.RequestDelete(r.Context(), j.ID, in.ConfirmHash, c.AccountID, in.Reason); err != nil {
		writeError(w, 409, "archive_protected", "State changed, retention/hold active, or archive is required after local cache release")
		return
	}
	current, err := s.archive.Queue.Get(r.Context(), j.ID)
	if err != nil {
		writeInternal(w, err)
		return
	}
	writeJSON(w, 202, map[string]any{"id": j.ID, "operation": "delete", "state": current.State, "scope": "archive-current-object-only"})
}

func (s *Server) holdStorageJob(w http.ResponseWriter, r *http.Request) {
	j, ok := s.storageJob(w, r, protocol.PermissionAdmin)
	if !ok {
		return
	}
	c, ok := s.authorizeTask(w, r, protocol.PermissionAdmin)
	if !ok {
		return
	}
	var in struct {
		Hold   bool   `json:"hold"`
		Reason string `json:"reason"`
	}
	if !s.decodeJSON(w, r, &in) {
		return
	}
	if len(strings.TrimSpace(in.Reason)) < 8 || len(in.Reason) > 500 {
		writeError(w, 400, "reason_required", "Reason must be 8..500 characters")
		return
	}
	if !in.Hold && !s.superAdmin(c) {
		writeError(w, 403, "platform_admin_required", "Only a platform super administrator can release a hold")
		return
	}
	if err := s.archive.Queue.SetHold(r.Context(), j.ID, in.Hold, c.AccountID, in.Reason); err != nil {
		writeError(w, 409, "state_conflict", "A deletion is already in progress or the object is unavailable")
		return
	}
	writeJSON(w, 200, map[string]any{"id": j.ID, "legal_hold": in.Hold})
}
