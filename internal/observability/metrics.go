package observability

import (
	"fmt"
	"io"
	"sync/atomic"
	"time"
)

type Metrics struct {
	SyncSuccess              atomic.Uint64
	SyncFailure              atomic.Uint64
	SyncLatencyMS            atomic.Uint64
	SyncLatencyCount         atomic.Uint64
	EventsAccepted           atomic.Uint64
	EventDuplicates          atomic.Uint64
	Conflicts                atomic.Uint64
	Reconciliations          atomic.Uint64
	ReconciliationDuration   atomic.Uint64
	ArtifactUploads          atomic.Uint64
	ArtifactUploadBytes      atomic.Uint64
	ArtifactDownloads        atomic.Uint64
	ArtifactDownloadBytes    atomic.Uint64
	TaskResumeRequests       atomic.Uint64
	CrossAgentResumeRequests atomic.Uint64
	InvitationsCreated       atomic.Uint64
	InvitationsAccepted      atomic.Uint64
}

func (m *Metrics) ObserveSync(start time.Time, success bool) {
	if success {
		m.SyncSuccess.Add(1)
	} else {
		m.SyncFailure.Add(1)
	}
	m.SyncLatencyMS.Add(uint64(max(time.Since(start).Milliseconds(), 0)))
	m.SyncLatencyCount.Add(1)
}

func (m *Metrics) WritePrometheus(w io.Writer, requests, serverErrors uint64) {
	writeCounter(w, "continuity_http_requests_total", "HTTP requests handled.", requests)
	writeCounter(w, "continuity_http_server_errors_total", "HTTP 5xx responses.", serverErrors)
	writeCounter(w, "continuity_sync_success_total", "Successful sync protocol operations.", m.SyncSuccess.Load())
	writeCounter(w, "continuity_sync_failure_total", "Failed sync protocol operations.", m.SyncFailure.Load())
	writeCounter(w, "continuity_sync_latency_milliseconds_total", "Cumulative sync latency in milliseconds.", m.SyncLatencyMS.Load())
	writeCounter(w, "continuity_sync_latency_count", "Observed sync latency samples.", m.SyncLatencyCount.Load())
	writeCounter(w, "continuity_events_accepted_total", "Durably accepted events.", m.EventsAccepted.Load())
	writeCounter(w, "continuity_event_duplicates_total", "Idempotently deduplicated events.", m.EventDuplicates.Load())
	writeCounter(w, "continuity_conflicts_total", "Detected causal conflicts.", m.Conflicts.Load())
	writeCounter(w, "continuity_reconciliations_total", "Completed reconciliation operations.", m.Reconciliations.Load())
	writeCounter(w, "continuity_reconciliation_duration_milliseconds_total", "Cumulative reconciliation duration.", m.ReconciliationDuration.Load())
	writeCounter(w, "continuity_artifact_uploads_total", "Completed artifact uploads.", m.ArtifactUploads.Load())
	writeCounter(w, "continuity_artifact_upload_bytes_total", "Uploaded artifact bytes.", m.ArtifactUploadBytes.Load())
	writeCounter(w, "continuity_artifact_downloads_total", "Artifact download requests.", m.ArtifactDownloads.Load())
	writeCounter(w, "continuity_artifact_download_bytes_total", "Artifact bytes scheduled for download.", m.ArtifactDownloadBytes.Load())
	writeCounter(w, "continuity_task_resume_requests_total", "Continuity package requests.", m.TaskResumeRequests.Load())
	writeCounter(w, "continuity_cross_agent_resume_requests_total", "Cross-agent continuity package requests.", m.CrossAgentResumeRequests.Load())
	writeCounter(w, "continuity_invitations_created_total", "Created task invitations.", m.InvitationsCreated.Load())
	writeCounter(w, "continuity_invitations_accepted_total", "Accepted task invitations.", m.InvitationsAccepted.Load())
}

func writeCounter(w io.Writer, name, help string, value uint64) {
	_, _ = fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n%s %d\n", name, help, name, name, value)
}
