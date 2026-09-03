package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/agent-continuity/agent-continuity-master/internal/daemon"
	"github.com/agent-continuity/agent-continuity-master/pkg/client"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	baseURL := env("AGENTCLOUD_API_URL", "http://127.0.0.1:8080")
	token := os.Getenv("AGENTCLOUD_TOKEN")
	stateDir := env("AGENTCLOUD_STATE_DIR", defaultStateDir())
	interval := durationEnv("AGENTCLOUD_SYNC_INTERVAL", 5*time.Second)
	outbox, err := daemon.OpenOutbox(stateDir)
	if err != nil {
		logger.Error("open outbox", "error", err)
		os.Exit(1)
	}
	control, err := daemon.OpenSyncControl(stateDir)
	if err != nil {
		logger.Error("open sync settings", "error", err)
		os.Exit(1)
	}
	stateStore, err := daemon.OpenState(stateDir)
	if err != nil {
		logger.Error("open device state", "error", err)
		os.Exit(1)
	}
	localStore, err := daemon.OpenLocalStore(stateDir)
	if err != nil {
		logger.Error("open local embedded store", "error", err)
		os.Exit(1)
	}
	defer localStore.Close()
	api := client.New(baseURL, token)
	engine := &daemon.SyncEngine{API: api, State: stateStore, Outbox: outbox, Local: localStore}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	logger.Info("daemon started", "api_url", baseURL, "sync_interval", interval.String())
	runSync(ctx, logger, engine, control)
	for {
		select {
		case <-ctx.Done():
			logger.Info("daemon stopped")
			return
		case <-ticker.C:
			runSync(ctx, logger, engine, control)
		}
	}
}

func runSync(ctx context.Context, logger *slog.Logger, engine *daemon.SyncEngine, control *daemon.SyncControl) {
	settings, err := control.Load()
	if err != nil {
		logger.Error("read sync settings", "error", err)
		return
	}
	if settings.Paused {
		return
	}
	requestCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	report, err := engine.SyncOnce(requestCtx, settings.BandwidthBytesPerSecond)
	if err != nil {
		logger.Warn("sync cycle deferred", "error", err, "report", report)
		return
	}
	logger.Info("sync cycle complete", "pushed", report.Pushed, "duplicates", report.Duplicates, "rejected", report.Rejected, "pulled", report.Pulled, "acknowledged", report.Acknowledged, "tasks", report.Tasks, "push_retry_after", report.PushRetryAfter.String())
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func durationEnv(name string, fallback time.Duration) time.Duration {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func defaultStateDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ".agent-continuity"
	}
	return filepath.Join(dir, "agent-continuity")
}
