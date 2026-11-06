package archive

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// Expose only aggregate operational metrics; never task/tenant IDs or payloads.
func (w *Worker) HealthHandler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /live", func(rw http.ResponseWriter, r *http.Request) { rw.WriteHeader(200) })
	m.HandleFunc("GET /ready", func(rw http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		err := w.Queue.Ping(ctx)
		if err != nil || !w.connected.Load() {
			http.Error(rw, "archive dependencies unavailable", 503)
			return
		}
		rw.WriteHeader(200)
	})
	m.HandleFunc("GET /metrics", func(rw http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		stats, err := w.Queue.Stats(ctx)
		if err != nil {
			http.Error(rw, "archive metrics unavailable", 503)
			return
		}
		rw.Header().Set("Content-Type", "text/plain; version=0.0.4")
		up := 0
		if w.connected.Load() {
			up = 1
		}
		fmt.Fprintf(rw, "# TYPE continuity_archive_broker_connected gauge\ncontinuity_archive_broker_connected %d\n", up)
		for _, state := range []string{"queued", "processing", "stored", "retry", "dead", "deleted"} {
			fmt.Fprintf(rw, "continuity_archive_jobs{state=%q} %d\n", state, stats.States[state])
		}
		fmt.Fprintf(rw, "continuity_archive_pending_jobs %d\ncontinuity_archive_retained_bytes %d\ncontinuity_archive_oldest_pending_seconds %f\n", stats.States["queued"]+stats.States["retry"]+stats.States["processing"], stats.RetainedBytes, stats.OldestPendingSeconds)
	})
	return m
}
