package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/agent-continuity/agent-continuity-master/internal/archive"
	artifactstore "github.com/agent-continuity/agent-continuity-master/internal/artifact"
	"github.com/agent-continuity/agent-continuity-master/internal/auth"
	"github.com/agent-continuity/agent-continuity-master/internal/billing"
	"github.com/agent-continuity/agent-continuity-master/internal/config"
	"github.com/agent-continuity/agent-continuity-master/internal/httpapi"
	"github.com/agent-continuity/agent-continuity-master/internal/identity"
	"github.com/agent-continuity/agent-continuity-master/internal/observability"
	"github.com/agent-continuity/agent-continuity-master/internal/payments"
	"github.com/agent-continuity/agent-continuity-master/internal/secrets"
	"github.com/agent-continuity/agent-continuity-master/internal/storage"
	"github.com/agent-continuity/agent-continuity-master/internal/storage/memory"
	pgstore "github.com/agent-continuity/agent-continuity-master/internal/storage/postgres"
	"github.com/agent-continuity/agent-continuity-master/internal/treasury"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)
	cfg, err := config.Load()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	shutdownTracing, err := observability.ConfigureTracing(ctx, cfg.ServiceName, cfg.OTLPEndpoint)
	if err != nil {
		logger.Error("configure tracing", "error", err)
		os.Exit(1)
	}
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if err := shutdownTracing(shutdownCtx); err != nil {
			logger.Error("flush traces", "error", err)
		}
	}()
	var store storage.Store
	var postgresStore *pgstore.Store
	if cfg.Store == "postgres" {
		postgresStore, err = pgstore.Open(ctx, cfg.DatabaseURL)
		store = postgresStore
	} else {
		store = memory.New()
	}
	if err != nil {
		logger.Error("open store", "error", err, "database", config.RedactedDatabaseURL(cfg.DatabaseURL))
		os.Exit(1)
	}
	defer store.Close()
	command := "serve"
	if len(os.Args) > 1 {
		command = strings.ToLower(os.Args[1])
	}
	if command == "migrate" {
		if postgresStore == nil {
			logger.Info("memory store needs no migrations")
			return
		}
		if err := postgresStore.Migrate(ctx); err != nil {
			logger.Error("migration failed", "error", err)
			os.Exit(1)
		}
		logger.Info("migrations applied")
		return
	}
	if command != "serve" && command != "archive-worker" {
		logger.Error("unknown command", "command", command)
		os.Exit(2)
	}
	if cfg.AutoMigrate && postgresStore != nil {
		if err := postgresStore.Migrate(ctx); err != nil {
			logger.Error("automatic migration failed", "error", err)
			os.Exit(1)
		}
	}
	var artifacts *artifactstore.Service
	if cfg.ArtifactBackend == "s3" {
		artifacts, err = artifactstore.NewS3(cfg.ArtifactDir, cfg.MaxBodyBytes, artifactstore.S3Config{Endpoint: cfg.S3Endpoint, Bucket: cfg.S3Bucket, Region: cfg.S3Region, AccessKey: cfg.S3AccessKey, SecretKey: cfg.S3SecretKey, SessionToken: cfg.S3SessionToken, AutoCreate: cfg.S3AutoCreate})
	} else {
		artifacts, err = artifactstore.New(cfg.ArtifactDir, cfg.MaxBodyBytes)
	}
	if err != nil {
		logger.Error("artifact store", "error", err)
		os.Exit(1)
	}
	archiveConfig, err := archive.Parse(cfg.ArchiveConfigJSON)
	if err != nil {
		logger.Error("invalid archive configuration", "error", err)
		os.Exit(2)
	}
	var archiveWorker *archive.Worker
	if archiveConfig.Enabled {
		if postgresStore == nil || cfg.ArtifactBackend != "local" {
			logger.Error("asynchronous archival requires PostgreSQL and durable local/shared staging; legacy S3 remains a separate synchronous mode")
			os.Exit(2)
		}
		artifacts.SetQuota(archiveConfig.MaxStagingBytes)
		archiveWorker, err = archive.New(archiveConfig, postgresStore.ArchiveQueue(), store, artifacts, cfg.ArtifactDir)
		if err != nil {
			logger.Error("initialize archive providers", "error", err)
			os.Exit(2)
		}
	}
	if command == "archive-worker" {
		if archiveWorker == nil {
			logger.Error("archive-worker requires enabled archive config")
			os.Exit(2)
		}
		workerCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
		defer stop()
		archiveWorker.Run(workerCtx)
		return
	}
	// Only the API activates routing. Workers never overwrite operator routing.
	if postgresStore != nil {
		if err := postgresStore.ArchiveQueue().Configure(ctx, archiveConfig); err != nil {
			logger.Error("configure transactional archive outbox", "error", err)
			os.Exit(2)
		}
	}
	tokens, err := auth.NewManager(cfg.JWTSecret)
	if err != nil {
		logger.Error("token manager", "error", err)
		os.Exit(1)
	}
	masterKey, err := secrets.DecodeKey(cfg.MasterKey)
	if err != nil {
		logger.Error("master key", "error", err)
		os.Exit(1)
	}
	secretCipher, err := secrets.New(masterKey)
	if err != nil {
		logger.Error("secret cipher", "error", err)
		os.Exit(1)
	}
	var oidcVerifier *identity.OIDCVerifier
	if cfg.OIDCIssuer != "" {
		oidcVerifier, err = identity.NewOIDCVerifier(cfg.OIDCIssuer, cfg.OIDCAudience, []byte(cfg.OIDCJWKS))
		if err != nil {
			logger.Error("OIDC configuration", "error", err)
			os.Exit(1)
		}
	}
	paymentConfig, err := payments.ParseConfig(cfg.PaymentConfigJSON)
	if err != nil {
		logger.Error("invalid payment configuration", "error", err)
		os.Exit(1)
	}
	providers, err := payments.Build(paymentConfig)
	if err != nil {
		logger.Error("invalid payment provider configuration", "error", err)
		os.Exit(1)
	}
	priceBook, err := billing.ParsePriceBook(cfg.BillingPriceBookJSON)
	if err != nil {
		logger.Error("invalid billing price book", "error", err)
		os.Exit(1)
	}
	treasuryConfig, err := treasury.ParseConfig(cfg.TreasuryConfigJSON)
	if err != nil {
		logger.Error("invalid treasury configuration", "error", err)
		os.Exit(1)
	}
	payoutProvider, err := treasury.Build(treasuryConfig)
	if err != nil {
		logger.Error("invalid treasury provider configuration", "error", err)
		os.Exit(1)
	}
	api := httpapi.New(store, artifacts, tokens, cfg.BootstrapToken, cfg.MaxBodyBytes, logger,
		httpapi.WithArchive(archiveWorker),
		httpapi.WithPlatformAdmins(os.Getenv("CONTINUITY_PLATFORM_SUPER_ADMINS")),
		httpapi.WithTreasury(treasuryConfig.OwnerAccountID, treasuryConfig.DemoEnabled, payoutProvider),
		httpapi.WithPayments(providers, priceBook, paymentConfig.PublicBaseURL),
		httpapi.WithSecretCipher(secretCipher), httpapi.WithDataResidency(cfg.DataResidency),
		httpapi.WithEnterpriseIdentity(httpapi.EnterpriseIdentityConfig{OIDCVerifier: oidcVerifier, OIDCOrganizationID: cfg.OIDCOrganizationID, OIDCAutoProvision: cfg.OIDCAutoProvision, SCIMBearerToken: cfg.SCIMBearerToken, SCIMOrganizationID: cfg.SCIMOrganizationID}))
	server := &http.Server{Addr: cfg.ListenAddress, Handler: api.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 1 << 20}
	stopCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	api.StartBackground(stopCtx)
	go func() {
		<-stopCtx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownGrace)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("graceful shutdown", "error", err)
		}
	}()
	logger.Info("server starting", "address", cfg.ListenAddress, "store", cfg.Store)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("server stopped unexpectedly", "error", err)
		os.Exit(1)
	}
	logger.Info("server stopped")
}
