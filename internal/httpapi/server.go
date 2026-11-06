package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/agent-continuity/agent-continuity-master/internal/archive"
	artifactstore "github.com/agent-continuity/agent-continuity-master/internal/artifact"
	"github.com/agent-continuity/agent-continuity-master/internal/auth"
	"github.com/agent-continuity/agent-continuity-master/internal/billing"
	"github.com/agent-continuity/agent-continuity-master/internal/commerce"
	"github.com/agent-continuity/agent-continuity-master/internal/identity"
	"github.com/agent-continuity/agent-continuity-master/internal/kms"
	"github.com/agent-continuity/agent-continuity-master/internal/observability"
	"github.com/agent-continuity/agent-continuity-master/internal/orchestration"
	"github.com/agent-continuity/agent-continuity-master/internal/payments"
	"github.com/agent-continuity/agent-continuity-master/internal/secrets"
	"github.com/agent-continuity/agent-continuity-master/internal/storage"
	"github.com/agent-continuity/agent-continuity-master/internal/treasury"
	webhookdispatcher "github.com/agent-continuity/agent-continuity-master/internal/webhook"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/trace"
)

type Server struct {
	keyManager         *kms.Manager
	orchestrator       *orchestration.Service
	commerce           *commerce.Service
	archive            *archive.Worker
	platformAdmins     map[string]bool
	billing            *billing.Service
	treasury           *treasury.Service
	store              storage.Store
	artifacts          *artifactstore.Service
	tokens             *auth.Manager
	bootstrapToken     string
	maxBodyBytes       int64
	logger             *slog.Logger
	requests           atomic.Uint64
	errors             atomic.Uint64
	limiter            *rateLimiter
	hub                *realtimeHub
	secretCipher       *secrets.Cipher
	metricSet          *observability.Metrics
	dataResidency      string
	oidcVerifier       *identity.OIDCVerifier
	oidcOrganizationID string
	oidcAutoProvision  bool
	scimBearerToken    string
	scimOrganizationID string
}

type Option func(*Server)

func WithSecretCipher(cipher *secrets.Cipher) Option {
	return func(server *Server) { server.secretCipher = cipher }
}

func WithDataResidency(residency string) Option {
	return func(server *Server) { server.dataResidency = strings.ToLower(strings.TrimSpace(residency)) }
}

type EnterpriseIdentityConfig struct {
	OIDCVerifier       *identity.OIDCVerifier
	OIDCOrganizationID string
	OIDCAutoProvision  bool
	SCIMBearerToken    string
	SCIMOrganizationID string
}

func WithEnterpriseIdentity(config EnterpriseIdentityConfig) Option {
	return func(server *Server) {
		server.oidcVerifier = config.OIDCVerifier
		server.oidcOrganizationID = config.OIDCOrganizationID
		server.oidcAutoProvision = config.OIDCAutoProvision
		server.scimBearerToken = config.SCIMBearerToken
		server.scimOrganizationID = config.SCIMOrganizationID
	}
}

func New(store storage.Store, artifacts *artifactstore.Service, tokens *auth.Manager, bootstrapToken string, maxBodyBytes int64, logger *slog.Logger, options ...Option) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	defaultKey := sha256.Sum256([]byte("agent-continuity-local:" + bootstrapToken))
	secretCipher, _ := secrets.New(defaultKey[:])
	server := &Server{store: store, artifacts: artifacts, tokens: tokens, bootstrapToken: bootstrapToken, maxBodyBytes: maxBodyBytes, logger: logger, limiter: newRateLimiter(), hub: newRealtimeHub(), secretCipher: secretCipher, metricSet: &observability.Metrics{}}
	server.billing = billing.New(store, payments.NewRegistry(), billing.DemoPriceBook(), "", defaultKey[:])
	for _, option := range options {
		option(server)
	}
	return server
}

func (s *Server) StartBackground(ctx context.Context) {
	if s.orchestrator != nil {
		go s.orchestrator.Run(ctx, s.logger)
	}
	if s.commerce != nil && s.commerce.Gateway != nil {
		go s.commerce.Run(ctx, s.logger)
	}
	go s.billing.Run(ctx, s.logger)
	dispatcher := webhookdispatcher.New(s.store, s.secretCipher, s.logger)
	go dispatcher.Run(ctx)
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.registerWorkflowRoutes(mux)
	s.registerVaultRoutes(mux)
	mux.HandleFunc("POST /a2a/v1", s.standardA2A)
	mux.HandleFunc("GET /v1/health/live", s.live)
	mux.HandleFunc("GET /v1/health/ready", s.ready)
	mux.HandleFunc("GET /metrics", s.metrics)
	mux.HandleFunc("POST /v1/accounts", s.createAccount)
	mux.HandleFunc("POST /v1/auth/token", s.issueToken)
	mux.HandleFunc("POST /v1/auth/refresh", s.refreshToken)
	mux.HandleFunc("POST /v1/auth/logout", s.logout)
	mux.HandleFunc("POST /v1/auth/sso/exchange", s.exchangeSSO)
	mux.HandleFunc("GET /scim/v2/ServiceProviderConfig", s.scimServiceProviderConfig)
	mux.HandleFunc("GET /scim/v2/ResourceTypes", s.scimResourceTypes)
	mux.HandleFunc("GET /scim/v2/Schemas", s.scimSchemas)
	mux.HandleFunc("GET /scim/v2/Users", s.listSCIMUsers)
	mux.HandleFunc("POST /scim/v2/Users", s.createSCIMUser)
	mux.HandleFunc("GET /scim/v2/Users/{identityID}", s.getSCIMUser)
	mux.HandleFunc("PUT /scim/v2/Users/{identityID}", s.replaceSCIMUser)
	mux.HandleFunc("PATCH /scim/v2/Users/{identityID}", s.patchSCIMUser)
	mux.HandleFunc("DELETE /scim/v2/Users/{identityID}", s.deleteSCIMUser)
	mux.HandleFunc("GET /scim/v2/Groups", s.listSCIMGroups)
	mux.HandleFunc("POST /scim/v2/Groups", s.createSCIMGroup)
	mux.HandleFunc("GET /scim/v2/Groups/{groupID}", s.getSCIMGroup)
	mux.HandleFunc("PUT /scim/v2/Groups/{groupID}", s.replaceSCIMGroup)
	mux.HandleFunc("PATCH /scim/v2/Groups/{groupID}", s.patchSCIMGroup)
	mux.HandleFunc("DELETE /scim/v2/Groups/{groupID}", s.deleteSCIMGroup)
	mux.HandleFunc("GET /v1/api-keys", s.listAPIKeys)
	mux.HandleFunc("POST /v1/api-keys", s.createAPIKey)
	mux.HandleFunc("DELETE /v1/api-keys/{keyID}", s.revokeAPIKey)
	mux.HandleFunc("GET /v1/audit/events", s.listAuditEvents)
	mux.HandleFunc("GET /v1/notifications", s.listNotifications)
	mux.HandleFunc("POST /v1/notifications/{notificationID}/read", s.markNotificationRead)
	mux.HandleFunc("POST /v1/devices", s.registerDevice)
	mux.HandleFunc("GET /v1/devices", s.listDevices)
	mux.HandleFunc("DELETE /v1/devices/{deviceID}", s.revokeDevice)
	mux.HandleFunc("GET /v1/agents", s.listAgents)
	mux.HandleFunc("POST /v1/agents", s.createAgent)
	mux.HandleFunc("DELETE /v1/agents/{agentID}", s.revokeAgent)
	mux.HandleFunc("GET /v1/organizations", s.listOrganizations)
	mux.HandleFunc("POST /v1/organizations", s.createOrganization)
	mux.HandleFunc("GET /v1/organizations/{organizationID}", s.getOrganization)
	mux.HandleFunc("PUT /v1/organizations/{organizationID}", s.updateOrganization)
	mux.HandleFunc("DELETE /v1/organizations/{organizationID}", s.deleteOrganization)
	mux.HandleFunc("GET /v1/organizations/{organizationID}/members", s.listOrganizationMembers)
	mux.HandleFunc("PUT /v1/organizations/{organizationID}/members/{accountID}", s.putOrganizationMember)
	mux.HandleFunc("DELETE /v1/organizations/{organizationID}/members/{accountID}", s.removeOrganizationMember)
	mux.HandleFunc("GET /v1/organizations/{organizationID}/teams", s.listTeams)
	mux.HandleFunc("POST /v1/organizations/{organizationID}/teams", s.createTeam)
	mux.HandleFunc("PUT /v1/organizations/{organizationID}/teams/{teamID}", s.updateTeam)
	mux.HandleFunc("DELETE /v1/organizations/{organizationID}/teams/{teamID}", s.deleteTeam)
	mux.HandleFunc("GET /v1/organizations/{organizationID}/teams/{teamID}/members", s.listTeamMembers)
	mux.HandleFunc("PUT /v1/organizations/{organizationID}/teams/{teamID}/members/{accountID}", s.putTeamMember)
	mux.HandleFunc("DELETE /v1/organizations/{organizationID}/teams/{teamID}/members/{accountID}", s.removeTeamMember)
	mux.HandleFunc("GET /v1/organizations/{organizationID}/policies", s.listAccessPolicies)
	mux.HandleFunc("POST /v1/organizations/{organizationID}/policies", s.createAccessPolicy)
	mux.HandleFunc("DELETE /v1/organizations/{organizationID}/policies/{policyID}", s.deleteAccessPolicy)
	mux.HandleFunc("GET /v1/tasks", s.listTasks)
	mux.HandleFunc("POST /v1/tasks", s.createTask)
	mux.HandleFunc("GET /v1/tasks/{taskID}", s.getTask)
	mux.HandleFunc("GET /v1/tasks/{taskID}/events", s.listEvents)
	mux.HandleFunc("POST /v1/tasks/{taskID}/events", s.appendEvent)
	mux.HandleFunc("GET /v1/tasks/{taskID}/branches", s.listBranches)
	mux.HandleFunc("POST /v1/tasks/{taskID}/branches", s.createBranch)
	mux.HandleFunc("GET /v1/tasks/{taskID}/branches/{branchID}/events", s.listBranchEvents)
	mux.HandleFunc("POST /v1/tasks/{taskID}/branches/{branchID}/merge", s.mergeBranch)
	mux.HandleFunc("GET /v1/tasks/{taskID}/conflicts", s.listConflicts)
	mux.HandleFunc("POST /v1/tasks/{taskID}/conflicts/{conflictID}/resolve", s.resolveConflict)
	mux.HandleFunc("POST /v1/tasks/{taskID}/branches/{branchID}/snapshots", s.rebuildSnapshot)
	mux.HandleFunc("GET /v1/tasks/{taskID}/branches/{branchID}/snapshots/latest", s.latestSnapshot)
	mux.HandleFunc("GET /v1/tasks/{taskID}/devices", s.listTaskDevices)
	mux.HandleFunc("GET /v1/tasks/{taskID}/participants", s.listParticipants)
	mux.HandleFunc("DELETE /v1/tasks/{taskID}/participants/{participantID}", s.leaveParticipant)
	mux.HandleFunc("GET /v1/tasks/{taskID}/permissions", s.listTaskPermissions)
	mux.HandleFunc("PUT /v1/tasks/{taskID}/permissions/{accountID}", s.putTaskPermission)
	mux.HandleFunc("DELETE /v1/tasks/{taskID}/permissions/{accountID}", s.deleteTaskPermission)
	mux.HandleFunc("GET /v1/tasks/{taskID}/agents", s.listTaskAgents)
	mux.HandleFunc("POST /v1/tasks/{taskID}/agents", s.bindTaskAgent)
	mux.HandleFunc("GET /v1/tasks/{taskID}/presence", s.listPresence)
	mux.HandleFunc("GET /v1/tasks/{taskID}/realtime", s.realtime)
	mux.HandleFunc("GET /v1/tasks/{taskID}/keys", s.getTaskKeySet)
	mux.HandleFunc("POST /v1/tasks/{taskID}/keys", s.putTaskKeySet)
	mux.HandleFunc("GET /v1/tasks/{taskID}/artifacts", s.listArtifacts)
	mux.HandleFunc("POST /v1/tasks/{taskID}/contents", s.uploadContent)
	mux.HandleFunc("POST /v1/tasks/{taskID}/storage-jobs/{jobID}/delete", s.deleteStorageJob)
	mux.HandleFunc("POST /v1/tasks/{taskID}/storage-jobs/{jobID}/hold", s.holdStorageJob)
	mux.HandleFunc("GET /v1/storage/config", s.archiveConfig)
	mux.HandleFunc("GET /v1/tasks/{taskID}/storage-jobs", s.listStorageJobs)
	mux.HandleFunc("GET /v1/tasks/{taskID}/storage-jobs/{jobID}", s.getStorageJob)
	mux.HandleFunc("GET /v1/tasks/{taskID}/storage-jobs/{jobID}/content", s.downloadStorageJob)
	mux.HandleFunc("POST /v1/tasks/{taskID}/storage-jobs/{jobID}/retry", s.retryStorageJob)
	mux.HandleFunc("POST /v1/tasks/{taskID}/storage-backfill", s.backfillStorage)
	mux.HandleFunc("POST /v1/tasks/{taskID}/artifact-uploads", s.createArtifactUpload)
	mux.HandleFunc("GET /v1/artifact-uploads/{uploadID}", s.getArtifactUpload)
	mux.HandleFunc("PUT /v1/artifact-uploads/{uploadID}/chunks/{chunkIndex}", s.putArtifactChunk)
	mux.HandleFunc("POST /v1/artifact-uploads/{uploadID}/complete", s.completeArtifactUpload)
	mux.HandleFunc("GET /v1/tasks/{taskID}/artifacts/{artifactID}/versions", s.listArtifactVersions)
	mux.HandleFunc("PUT /v1/tasks/{taskID}/artifacts/{artifactID}", s.putArtifact)
	mux.HandleFunc("GET /v1/tasks/{taskID}/artifacts/{artifactID}", s.getArtifact)
	mux.HandleFunc("GET /v1/tasks/{taskID}/artifacts/{artifactID}/permissions", s.listArtifactPermissions)
	mux.HandleFunc("PUT /v1/tasks/{taskID}/artifacts/{artifactID}/permissions/{accountID}", s.putArtifactPermission)
	mux.HandleFunc("DELETE /v1/tasks/{taskID}/artifacts/{artifactID}/permissions/{accountID}", s.deleteArtifactPermission)
	mux.HandleFunc("POST /v1/tasks/{taskID}/invitations", s.createInvitation)
	mux.HandleFunc("POST /v1/tasks/{taskID}/qr-invitations", s.createQRInvitation)
	mux.HandleFunc("DELETE /v1/invitations/{invitationID}", s.revokeInvitation)
	mux.HandleFunc("POST /v1/invitations/accept", s.acceptInvitation)
	mux.HandleFunc("GET /v1/tasks/{taskID}/continuity-package", s.continuityPackage)
	mux.HandleFunc("POST /v1/sync/hello", s.syncHello)
	mux.HandleFunc("POST /v1/sync/push", s.syncPush)
	mux.HandleFunc("POST /v1/sync/pull", s.syncPull)
	mux.HandleFunc("POST /v1/sync/ack", s.syncAck)
	mux.HandleFunc("POST /v1/reconcile", s.reconcileTask)
	mux.HandleFunc("GET /v1/oauth/clients", s.listOAuthClients)
	mux.HandleFunc("POST /v1/oauth/clients", s.createOAuthClient)
	mux.HandleFunc("DELETE /v1/oauth/clients/{clientID}", s.revokeOAuthClient)
	mux.HandleFunc("POST /v1/oauth/token", s.issueOAuthToken)
	mux.HandleFunc("GET /v1/webhooks", s.listWebhookEndpoints)
	mux.HandleFunc("POST /v1/webhooks", s.createWebhookEndpoint)
	mux.HandleFunc("DELETE /v1/webhooks/{webhookID}", s.revokeWebhookEndpoint)
	mux.HandleFunc("GET /v1/usage", s.listUsage)
	mux.HandleFunc("GET /v1/billing/accounts", s.listBillingAccounts)
	mux.HandleFunc("POST /v1/billing/accounts", s.createBillingAccount)
	s.registerBillingRoutes(mux)
	s.registerTreasuryRoutes(mux)
	s.registerPortalRoutes(mux)
	mux.HandleFunc("GET /v1/marketplace/listings", s.listMarketplaceListings)
	mux.HandleFunc("POST /v1/marketplace/listings", s.createMarketplaceListing)
	mux.HandleFunc("POST /v1/marketplace/listings/{listingID}/install", s.installMarketplaceListing)
	mux.HandleFunc("GET /v1/marketplace/installations", s.listMarketplaceInstallations)
	mux.HandleFunc("GET /v1/marketplace/transactions", s.listMarketplaceTransactions)
	mux.HandleFunc("POST /v1/marketplace/transactions", s.createMarketplaceTransaction)
	mux.HandleFunc("POST /v1/marketplace/transactions/{transactionID}/authorize", s.authorizeMarketplaceTransaction)
	mux.HandleFunc("POST /v1/marketplace/transactions/{transactionID}/complete", s.completeMarketplaceTransaction)
	mux.HandleFunc("POST /v1/marketplace/transactions/{transactionID}/cancel", s.cancelMarketplaceTransaction)
	mux.HandleFunc("GET /.well-known/agent-card.json", s.standardAgentCard)
	mux.HandleFunc("GET /.well-known/agent-continuity.json", s.protocolDiscovery)
	mux.HandleFunc("GET /v1/protocol/schemas/{name}", s.protocolSchema)
	mux.HandleFunc("GET /v1/protocol/openapi.yaml", s.protocolOpenAPI)
	mux.HandleFunc("POST /v1/a2a", s.a2a)
	mux.HandleFunc("POST /v1/mcp", s.mcp)
	mux.HandleFunc("POST /agentcontinuity.v1.ContinuityService/ListTasks", s.connectListTasks)
	mux.HandleFunc("POST /agentcontinuity.v1.ContinuityService/GetTask", s.connectGetTask)
	mux.HandleFunc("POST /agentcontinuity.v1.ContinuityService/AppendEvent", s.connectAppendEvent)
	mux.HandleFunc("POST /agentcontinuity.v1.ContinuityService/GetContinuityPackage", s.connectGetContinuityPackage)
	mux.HandleFunc("GET /app.js", s.staticAsset)
	mux.HandleFunc("GET /workflows.js", s.staticAsset)
	mux.HandleFunc("GET /archive.js", s.staticAsset)
	mux.HandleFunc("GET /billing.js", s.staticAsset)
	mux.HandleFunc("GET /refunds.js", s.staticAsset)
	mux.HandleFunc("GET /treasury.js", s.staticAsset)
	mux.HandleFunc("GET /styles.css", s.staticAsset)
	mux.HandleFunc("GET /", s.index)
	return otelhttp.NewHandler(s.instrument(s.limiter.Handler(mux)), "agent-continuity.http", otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string { return r.Method + " " + r.Pattern }))
}

func (s *Server) instrument(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID, _ := protocol.NewID("req")
		r = r.WithContext(context.WithValue(r.Context(), requestIDContextKey{}, requestID))
		w.Header().Set("X-Request-ID", requestID)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		s.requests.Add(1)
		start := time.Now()
		recorder := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		if recorder.status >= 500 {
			s.errors.Add(1)
		}
		traceID := trace.SpanContextFromContext(r.Context()).TraceID().String()
		s.logger.Info("request", "request_id", requestID, "trace_id", traceID, "method", r.Method, "path", r.URL.Path, "status", recorder.status, "duration_ms", time.Since(start).Milliseconds())
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (s *Server) live(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "live", "protocol_version": protocol.ProtocolVersion})
}
func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.store.Ping(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "store_unavailable", "persistence is not ready")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}
func (s *Server) metrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	s.metricSet.WritePrometheus(w, s.requests.Load(), s.errors.Load())
}

func (s *Server) createAccount(w http.ResponseWriter, r *http.Request) {
	if !secureEqual(r.Header.Get("X-Bootstrap-Token"), s.bootstrapToken) {
		writeError(w, http.StatusUnauthorized, "invalid_bootstrap_token", "invalid bootstrap token")
		return
	}
	var input struct {
		Name string `json:"name"`
	}
	if !s.decodeJSON(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Name) > 120 {
		writeError(w, http.StatusBadRequest, "invalid_name", "name must contain 1..120 characters")
		return
	}
	id, err := protocol.NewID("acct")
	if err != nil {
		writeInternal(w, err)
		return
	}
	account := protocol.Account{ID: id, Name: input.Name, CreatedAt: time.Now().UTC()}
	if err := s.store.CreateAccount(r.Context(), account); err != nil {
		writeStorageError(w, err)
		return
	}
	s.audit(r, auth.Claims{AccountID: account.ID, Subject: "bootstrap"}, "account.created", "account", account.ID, nil)
	writeJSON(w, http.StatusCreated, account)
}

func (s *Server) issueToken(w http.ResponseWriter, r *http.Request) {
	if !secureEqual(r.Header.Get("X-Bootstrap-Token"), s.bootstrapToken) {
		writeError(w, http.StatusUnauthorized, "invalid_bootstrap_token", "invalid bootstrap token")
		return
	}
	var input struct {
		AccountID  string `json:"account_id"`
		Subject    string `json:"subject"`
		TTLSeconds int64  `json:"ttl_seconds"`
	}
	if !s.decodeJSON(w, r, &input) {
		return
	}
	if _, err := s.store.GetAccount(r.Context(), input.AccountID); err != nil {
		writeStorageError(w, err)
		return
	}
	ttl, err := auth.ParseTTLSeconds(input.TTLSeconds)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_ttl", err.Error())
		return
	}
	token, err := s.tokens.Issue(input.AccountID, input.Subject, ttl)
	if err != nil {
		writeInternal(w, err)
		return
	}
	refreshToken, err := auth.NewOpaqueToken("rft")
	if err != nil {
		writeInternal(w, err)
		return
	}
	sessionID, _ := protocol.NewID("session")
	familyID, _ := protocol.NewID("family")
	now := time.Now().UTC()
	session := protocol.AuthSession{ID: sessionID, FamilyID: familyID, AccountID: input.AccountID, Subject: input.Subject, RefreshTokenHash: auth.HashOpaque(refreshToken), CreatedAt: now, ExpiresAt: now.Add(30 * 24 * time.Hour)}
	if err := s.store.CreateAuthSession(r.Context(), session); err != nil {
		writeStorageError(w, err)
		return
	}
	s.audit(r, auth.Claims{AccountID: input.AccountID, Subject: input.Subject}, "auth.session.created", "session", session.ID, nil)
	writeJSON(w, http.StatusOK, map[string]any{"access_token": token, "refresh_token": refreshToken, "token_type": "Bearer", "expires_in": int64(ttl.Seconds()), "refresh_expires_in": int64((30 * 24 * time.Hour).Seconds())})
}

func (s *Server) registerDevice(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeScope(w, r, "devices:write")
	if !ok {
		return
	}
	var input struct {
		DeviceID            string `json:"device_id"`
		Name                string `json:"name"`
		PublicKey           string `json:"public_key"`
		EncryptionPublicKey string `json:"encryption_public_key"`
	}
	if !s.decodeJSON(w, r, &input) {
		return
	}
	if input.DeviceID == "" {
		input.DeviceID, _ = protocol.NewID("device")
	}
	key, err := base64.RawURLEncoding.DecodeString(input.PublicKey)
	if err != nil || len(key) != 32 {
		writeError(w, http.StatusBadRequest, "invalid_public_key", "public_key must be an Ed25519 base64url key")
		return
	}
	if input.EncryptionPublicKey != "" {
		encryptionKey, err := base64.RawURLEncoding.DecodeString(input.EncryptionPublicKey)
		if err != nil || len(encryptionKey) != 32 {
			writeError(w, http.StatusBadRequest, "invalid_encryption_public_key", "encryption_public_key must be an X25519 base64url key")
			return
		}
	}
	if strings.TrimSpace(input.Name) == "" {
		writeError(w, http.StatusBadRequest, "invalid_name", "device name is required")
		return
	}
	device := protocol.Device{ID: input.DeviceID, AccountID: claims.AccountID, Name: input.Name, PublicKey: input.PublicKey, EncryptionPublicKey: input.EncryptionPublicKey, CreatedAt: time.Now().UTC()}
	if err := s.store.RegisterDevice(r.Context(), device); err != nil {
		writeStorageError(w, err)
		return
	}
	s.audit(r, claims, "device.registered", "device", device.ID, nil)
	writeJSON(w, http.StatusCreated, device)
}

func (s *Server) listDevices(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeScope(w, r, "devices:read")
	if !ok {
		return
	}
	items, err := s.store.ListDevices(r.Context(), claims.AccountID, r.URL.Query().Get("include_revoked") == "true")
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": items})
}

func (s *Server) revokeDevice(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeScope(w, r, "devices:write")
	if !ok {
		return
	}
	if err := s.store.RevokeDevice(r.Context(), claims.AccountID, r.PathValue("deviceID")); err != nil {
		writeStorageError(w, err)
		return
	}
	s.audit(r, claims, "device.revoked", "device", r.PathValue("deviceID"), nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) createTask(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeScope(w, r, "tasks:write")
	if !ok {
		return
	}
	var input struct {
		Title          string          `json:"title"`
		Objective      string          `json:"objective"`
		OriginDevice   string          `json:"origin_device"`
		OrganizationID string          `json:"organization_id"`
		TeamID         string          `json:"team_id"`
		Metadata       json.RawMessage `json:"metadata"`
	}
	if !s.decodeJSON(w, r, &input) {
		return
	}
	input.Title = strings.TrimSpace(input.Title)
	input.Objective = strings.TrimSpace(input.Objective)
	if input.Title == "" || len(input.Title) > 200 || input.Objective == "" {
		writeError(w, http.StatusBadRequest, "invalid_task", "title and objective are required; title is limited to 200 characters")
		return
	}
	if input.OriginDevice != "" {
		device, err := s.store.GetDevice(r.Context(), input.OriginDevice)
		if err != nil || device.AccountID != claims.AccountID || device.RevokedAt != nil {
			writeError(w, http.StatusBadRequest, "invalid_origin_device", "origin device is not active for this account")
			return
		}
	}
	if input.TeamID != "" && input.OrganizationID == "" {
		writeError(w, http.StatusBadRequest, "organization_required", "team tasks must belong to an organization")
		return
	}
	if input.OrganizationID != "" {
		organization, err := s.store.GetOrganization(r.Context(), input.OrganizationID)
		if err != nil {
			writeStorageError(w, err)
			return
		}
		if s.dataResidency != "" && strings.ToLower(strings.TrimSpace(organization.DataResidency)) != s.dataResidency {
			writeError(w, http.StatusConflict, "residency_mismatch", "organization data cannot be created in this deployment")
			return
		}
		allowed, err := s.organizationAllows(r, claims, input.OrganizationID, "task.create", "task", map[string]any{"team_id": input.TeamID})
		if err != nil || !allowed {
			writeError(w, http.StatusForbidden, "forbidden", "organization policy denied task creation")
			return
		}
		if input.TeamID != "" {
			team, err := s.store.GetTeam(r.Context(), input.TeamID)
			if err != nil || team.OrganizationID != input.OrganizationID {
				writeError(w, http.StatusBadRequest, "invalid_team", "team does not belong to the organization")
				return
			}
		}
	}
	id, err := protocol.NewID("task")
	if err != nil {
		writeInternal(w, err)
		return
	}
	now := time.Now().UTC()
	task := protocol.Task{ID: id, AccountID: claims.AccountID, OrganizationID: input.OrganizationID, TeamID: input.TeamID, Title: input.Title, Objective: input.Objective, Status: "active", OriginDevice: input.OriginDevice, HeadEventIDs: []string{}, Metadata: input.Metadata, CreatedAt: now, UpdatedAt: now}
	if err := s.store.CreateTask(r.Context(), task); err != nil {
		writeStorageError(w, err)
		return
	}
	s.audit(r, claims, "task.created", "task", task.ID, nil)
	writeJSON(w, http.StatusCreated, task)
}
func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeScope(w, r, "tasks:read")
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := s.store.ListTasks(r.Context(), claims.AccountID, limit)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	visible := make([]protocol.Task, 0, len(items))
	for _, item := range items {
		allowed, err := s.taskAccessAllowed(r, claims, item.ID, protocol.PermissionRead)
		if err != nil {
			writeStorageError(w, err)
			return
		}
		if allowed {
			visible = append(visible, item)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": visible, "lazy_payloads": true})
}
func (s *Server) getTask(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeTask(w, r, protocol.PermissionRead)
	if !ok {
		return
	}
	_ = claims
	task, err := s.store.GetTask(r.Context(), r.PathValue("taskID"))
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) appendEvent(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeTask(w, r, protocol.PermissionWrite)
	if !ok {
		return
	}
	var value protocol.Event
	if !s.decodeJSON(w, r, &value) {
		return
	}
	if value.TaskID != r.PathValue("taskID") || value.AccountID != claims.AccountID {
		writeError(w, http.StatusBadRequest, "identity_mismatch", "path task and token account must match the event")
		return
	}
	device, err := s.store.GetDevice(r.Context(), value.DeviceID)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	if device.AccountID != claims.AccountID || device.RevokedAt != nil {
		writeError(w, http.StatusForbidden, "device_forbidden", "event device is not active for this account")
		return
	}
	if err := protocol.ValidateEvent(value, time.Now()); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_event", err.Error())
		return
	}
	if err := protocol.VerifyEventSignature(value, device.PublicKey); err != nil {
		writeError(w, http.StatusUnauthorized, "invalid_event_signature", err.Error())
		return
	}
	result, err := s.store.AppendEvent(r.Context(), value)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	status := http.StatusCreated
	if result.Duplicate {
		status = http.StatusOK
		s.metricSet.EventDuplicates.Add(1)
	} else {
		s.metricSet.EventsAccepted.Add(1)
		s.audit(r, claims, "event.appended", "event", result.Event.EventID, mustJSON(map[string]any{"task_id": value.TaskID, "event_type": value.Type}))
		s.createMentionNotifications(r, claims, result.Event)
		s.publish(protocol.RealtimeMessage{Type: "event.available", TaskID: value.TaskID, ServerSequence: result.Event.ServerSequence, ResourceID: result.Event.EventID, SentAt: time.Now().UTC()})
	}
	writeJSON(w, status, result)
}
func (s *Server) listEvents(w http.ResponseWriter, r *http.Request) {
	_, ok := s.authorizeTask(w, r, protocol.PermissionRead)
	if !ok {
		return
	}
	after, _ := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	include := r.URL.Query().Get("include_payload") != "false"
	items, err := s.store.ListEvents(r.Context(), r.PathValue("taskID"), after, limit, include)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": items, "next_cursor": eventCursor(items, after)})
}

func (s *Server) putArtifact(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeTask(w, r, protocol.PermissionWrite)
	if !ok {
		return
	}
	taskID := r.PathValue("taskID")
	artifactID := r.PathValue("artifactID")
	if artifactID == "" || strings.ContainsAny(artifactID, `/\\`) {
		writeError(w, http.StatusBadRequest, "invalid_artifact_id", "artifact ID is invalid")
		return
	}
	if _, err := s.store.GetArtifact(r.Context(), taskID, artifactID); err == nil {
		if !s.authorizeArtifactAfterTask(w, r, claims, artifactID, protocol.PermissionWrite) {
			return
		}
	} else if !errors.Is(err, storage.ErrNotFound) {
		writeStorageError(w, err)
		return
	}
	digest := r.Header.Get("X-Content-SHA256")
	unlock, err := s.artifacts.LockContent(r.Context(), digest)
	if err != nil {
		writeError(w, 400, "content_lock_failed", "Invalid content digest or staging lock unavailable")
		return
	}
	defer unlock()
	ref, size, err := s.artifacts.Put(http.MaxBytesReader(w, r.Body, s.maxBodyBytes), digest)
	if err != nil {
		if errors.Is(err, artifactstore.ErrHashMismatch) {
			writeError(w, http.StatusUnprocessableEntity, "hash_mismatch", err.Error())
		} else if errors.Is(err, artifactstore.ErrStagingFull) {
			w.Header().Set("Retry-After", "30")
			writeError(w, 507, "staging_full", "上传暂存空间不足，请联系管理员扩容 / Staging capacity exhausted")
		} else {
			writeError(w, http.StatusBadRequest, "artifact_rejected", err.Error())
		}
		return
	}
	version := r.Header.Get("X-Artifact-Version")
	if version == "" {
		version, _ = protocol.NewID("version")
	}
	contentType := r.Header.Get("Content-Type")
	if contentType == "" {
		contentType = mime.TypeByExtension(filepath.Ext(r.Header.Get("X-Filename")))
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	value := protocol.Artifact{ID: artifactID, TaskID: taskID, AccountID: claims.AccountID, VersionID: version, ParentVersionID: r.Header.Get("X-Parent-Version"), ContentType: contentType, Filename: safeFilename(r.Header.Get("X-Filename")), Size: size, ContentHash: digest, ProducerType: r.Header.Get("X-Producer-Type"), ProducerID: r.Header.Get("X-Producer-ID"), DeviceID: r.Header.Get("X-Device-ID"), StorageRef: ref, CreatedAt: time.Now().UTC()}
	if err := s.store.PutArtifact(r.Context(), value); err != nil {
		writeStorageError(w, err)
		return
	}
	s.audit(r, claims, "artifact.uploaded", "artifact", value.ID, mustJSON(map[string]any{"task_id": taskID, "size": value.Size, "content_hash": value.ContentHash}))
	s.recordUsage(r, claims, "artifact.bytes", value.Size, "byte", value.ID, value.VersionID, mustJSON(map[string]string{"task_id": taskID, "content_hash": value.ContentHash}))
	s.metricSet.ArtifactUploads.Add(1)
	s.metricSet.ArtifactUploadBytes.Add(uint64(value.Size))
	s.publish(protocol.RealtimeMessage{Type: "artifact.available", TaskID: taskID, ResourceID: value.ID, SentAt: time.Now().UTC()})
	writeJSON(w, http.StatusCreated, value)
}
func (s *Server) listArtifacts(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeTask(w, r, protocol.PermissionRead)
	if !ok {
		return
	}
	items, err := s.store.ListArtifacts(r.Context(), r.PathValue("taskID"))
	if err != nil {
		writeStorageError(w, err)
		return
	}
	visible := make([]protocol.Artifact, 0, len(items))
	task, _ := s.store.GetTask(r.Context(), r.PathValue("taskID"))
	for _, item := range items {
		permissions, err := s.store.ListArtifactPermissions(r.Context(), item.TaskID, item.ID)
		if err != nil {
			writeStorageError(w, err)
			return
		}
		if task.AccountID == claims.AccountID || len(permissions) == 0 || artifactPermissionAllows(permissions, claims.AccountID, protocol.PermissionRead) {
			visible = append(visible, item)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"artifacts": visible, "blobs_included": false})
}
func (s *Server) getArtifact(w http.ResponseWriter, r *http.Request) {
	_, ok := s.authorizeArtifact(w, r, protocol.PermissionRead)
	if !ok {
		return
	}
	value, err := s.store.GetArtifact(r.Context(), r.PathValue("taskID"), r.PathValue("artifactID"))
	if err != nil {
		writeStorageError(w, err)
		return
	}
	file, temporary, err := s.openArtifactContent(r, value)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "artifact_unavailable", "附件暂不可用；请检查归档状态 / Artifact temporarily unavailable")
		return
	}
	defer file.Close()
	if temporary {
		defer os.Remove(file.Name())
	}
	s.metricSet.ArtifactDownloads.Add(1)
	s.metricSet.ArtifactDownloadBytes.Add(uint64(value.Size))
	w.Header().Set("Content-Type", value.ContentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, value.Filename))
	w.Header().Set("ETag", `"`+value.ContentHash+`"`)
	w.Header().Set("Cache-Control", "private, immutable")
	http.ServeContent(w, r, value.Filename, value.CreatedAt, file)
}

func (s *Server) createInvitation(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeTask(w, r, protocol.PermissionAdmin)
	if !ok {
		return
	}
	var input struct {
		Permission protocol.Permission `json:"permission"`
		TTLSeconds int64               `json:"ttl_seconds"`
	}
	if !s.decodeJSON(w, r, &input) {
		return
	}
	if input.Permission != protocol.PermissionRead && input.Permission != protocol.PermissionWrite {
		writeError(w, http.StatusBadRequest, "invalid_permission", "invitation permission must be read or write")
		return
	}
	if input.TTLSeconds == 0 {
		input.TTLSeconds = 900
	}
	if input.TTLSeconds < 60 || input.TTLSeconds > 86400 {
		writeError(w, http.StatusBadRequest, "invalid_ttl", "invitation ttl must be 60..86400 seconds")
		return
	}
	id, _ := protocol.NewID("invite")
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		writeInternal(w, err)
		return
	}
	secret := base64.RawURLEncoding.EncodeToString(tokenBytes)
	token := id + "." + secret
	hash := sha256.Sum256([]byte(token))
	now := time.Now().UTC()
	invitation := protocol.Invitation{ID: id, TaskID: r.PathValue("taskID"), InviterAccount: claims.AccountID, Permission: input.Permission, TokenHash: hex.EncodeToString(hash[:]), ExpiresAt: now.Add(time.Duration(input.TTLSeconds) * time.Second), CreatedAt: now}
	if err := s.store.CreateInvitation(r.Context(), invitation); err != nil {
		writeStorageError(w, err)
		return
	}
	s.metricSet.InvitationsCreated.Add(1)
	s.audit(r, claims, "invitation.created", "invitation", invitation.ID, mustJSON(map[string]any{"task_id": invitation.TaskID, "permission": invitation.Permission, "expires_at": invitation.ExpiresAt}))
	writeJSON(w, http.StatusCreated, map[string]any{"invitation": invitation, "token": token})
}
func (s *Server) acceptInvitation(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeScope(w, r, "invitations:accept")
	if !ok {
		return
	}
	var input struct {
		Token string `json:"token"`
	}
	if !s.decodeJSON(w, r, &input) {
		return
	}
	value, err := s.store.AcceptInvitation(r.Context(), input.Token, claims.AccountID)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	s.metricSet.InvitationsAccepted.Add(1)
	s.audit(r, claims, "invitation.accepted", "invitation", value.ID, nil)
	notificationID, _ := protocol.NewID("notification")
	_ = s.store.CreateNotification(r.Context(), protocol.Notification{ID: notificationID, AccountID: value.InviterAccount, TaskID: value.TaskID, Kind: "invitation.accepted", ActorID: claims.Subject, Subject: "A collaborator joined your task", Details: mustJSON(map[string]string{"invitation_id": value.ID, "account_id": claims.AccountID}), CreatedAt: time.Now().UTC()})
	s.publish(protocol.RealtimeMessage{Type: "participant.joined", TaskID: value.TaskID, ResourceID: claims.AccountID, SentAt: time.Now().UTC()})
	writeJSON(w, http.StatusOK, value)
}
func (s *Server) revokeInvitation(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeScope(w, r, "invitations:write")
	if !ok {
		return
	}
	if err := s.store.RevokeInvitation(r.Context(), claims.AccountID, r.PathValue("invitationID")); err != nil {
		writeStorageError(w, err)
		return
	}
	s.audit(r, claims, "invitation.revoked", "invitation", r.PathValue("invitationID"), nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) continuityPackage(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeTask(w, r, protocol.PermissionRead)
	if !ok {
		return
	}
	target := r.URL.Query().Get("agent")
	value, err := s.continuityForAccount(r, claims, r.PathValue("taskID"), target)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	s.metricSet.TaskResumeRequests.Add(1)
	if value.TargetAgent != "generic" {
		s.metricSet.CrossAgentResumeRequests.Add(1)
	}
	writeJSON(w, http.StatusOK, value)
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) (auth.Claims, bool) {
	if r.Header.Get("Authorization") == "" {
		return s.portalSessionClaims(w, r)
	}
	token, err := auth.Bearer(r.Header.Get("Authorization"))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", err.Error())
		return auth.Claims{}, false
	}
	claims, tokenErr := s.tokens.Parse(token)
	if tokenErr == nil {
		return claims, true
	}
	if strings.HasPrefix(token, "ack_") {
		key, keyErr := s.store.AuthenticateAPIKey(r.Context(), auth.HashOpaque(token))
		if keyErr == nil {
			return auth.Claims{AccountID: key.AccountID, Subject: "api-key:" + key.ID, Scopes: key.Scopes, APIKeyID: key.ID}, true
		}
	}
	writeError(w, http.StatusUnauthorized, "unauthorized", "invalid or expired bearer credential")
	return auth.Claims{}, false
}
func (s *Server) authorizeScope(w http.ResponseWriter, r *http.Request, required string) (auth.Claims, bool) {
	claims, ok := s.authorize(w, r)
	if !ok {
		return auth.Claims{}, false
	}
	if !auth.HasScope(claims, required) {
		writeError(w, http.StatusForbidden, "insufficient_scope", "required scope: "+required)
		return auth.Claims{}, false
	}
	return claims, true
}
func (s *Server) authorizeTask(w http.ResponseWriter, r *http.Request, permission protocol.Permission) (auth.Claims, bool) {
	requiredScope := "tasks:read"
	if permission == protocol.PermissionWrite {
		requiredScope = "tasks:write"
	} else if permission == protocol.PermissionAdmin {
		requiredScope = "tasks:admin"
	}
	claims, ok := s.authorizeScope(w, r, requiredScope)
	if !ok {
		return auth.Claims{}, false
	}
	allowed, err := s.taskAccessAllowed(r, claims, r.PathValue("taskID"), permission)
	if err != nil {
		writeStorageError(w, err)
		return auth.Claims{}, false
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "forbidden", "task permission denied")
		return auth.Claims{}, false
	}
	return claims, true
}

func (s *Server) taskAccessAllowed(r *http.Request, claims auth.Claims, taskID string, permission protocol.Permission) (bool, error) {
	task, err := s.store.GetTask(r.Context(), taskID)
	if err != nil {
		return false, err
	}
	if task.OrganizationID != "" {
		action := "task.read"
		if permission == protocol.PermissionWrite {
			action = "task.write"
		} else if permission == protocol.PermissionAdmin {
			action = "task.admin"
		}
		teamEligible := true
		if task.TeamID != "" {
			member, memberErr := s.store.GetOrganizationMember(r.Context(), task.OrganizationID, claims.AccountID)
			teamEligible = memberErr == nil && (member.Role == "owner" || member.Role == "admin")
			if !teamEligible && memberErr == nil {
				teamMember, teamErr := s.store.GetTeamMember(r.Context(), task.TeamID, claims.AccountID)
				teamEligible = teamErr == nil && (permission == protocol.PermissionRead || teamMember.Role == "lead" || teamMember.Role == "member")
			}
		}
		if teamEligible {
			allowed, policyErr := s.organizationAllows(r, claims, task.OrganizationID, action, "task", map[string]any{"task_id": task.ID, "team_id": task.TeamID, "owner_account_id": task.AccountID})
			if policyErr == nil {
				return allowed, nil
			}
			if !errors.Is(policyErr, storage.ErrUnauthorized) && !errors.Is(policyErr, storage.ErrNotFound) {
				return false, policyErr
			}
		}
	}
	// Tenant membership is authoritative; a historical creator ACL must not
	// restore access after removal or a policy denial.
	if task.OrganizationID != "" {
		return false, nil
	}
	if err := s.store.CheckPermission(r.Context(), task.ID, claims.AccountID, permission); err != nil {
		if errors.Is(err, storage.ErrUnauthorized) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (s *Server) authorizeArtifact(w http.ResponseWriter, r *http.Request, permission protocol.Permission) (auth.Claims, bool) {
	claims, ok := s.authorizeTask(w, r, permission)
	if !ok {
		return auth.Claims{}, false
	}
	if !s.authorizeArtifactAfterTask(w, r, claims, r.PathValue("artifactID"), permission) {
		return auth.Claims{}, false
	}
	return claims, true
}

func (s *Server) authorizeArtifactAfterTask(w http.ResponseWriter, r *http.Request, claims auth.Claims, artifactID string, permission protocol.Permission) bool {
	if task, err := s.store.GetTask(r.Context(), r.PathValue("taskID")); err == nil && task.AccountID == claims.AccountID {
		return true
	}
	permissions, err := s.store.ListArtifactPermissions(r.Context(), r.PathValue("taskID"), artifactID)
	if err != nil {
		writeStorageError(w, err)
		return false
	}
	if len(permissions) > 0 && !artifactPermissionAllows(permissions, claims.AccountID, permission) {
		writeError(w, http.StatusForbidden, "artifact_forbidden", "artifact-level permission denied")
		return false
	}
	return true
}

func artifactPermissionAllows(values []protocol.ArtifactPermission, accountID string, required protocol.Permission) bool {
	for _, value := range values {
		if value.AccountID == accountID && permissionAtLeast(value.Permission, required) {
			return true
		}
	}
	return false
}
func (s *Server) decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, s.maxBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_json", "request must contain one JSON object")
		return false
	}
	return true
}
func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	s.serveStatic(w, "index.html", "text/html; charset=utf-8")
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}
func writeInternal(w http.ResponseWriter, err error) {
	slog.Error("internal error", "error", err)
	writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
}
func writeStorageError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, archive.ErrQueueFull):
		w.Header().Set("Retry-After", "30")
		writeError(w, 503, "archive_queue_full", "归档积压达到上限，请稍后重试 / Archive backlog limit reached")
	case errors.Is(err, storage.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
	case errors.Is(err, storage.ErrAlreadyExists):
		writeError(w, http.StatusConflict, "already_exists", "resource already exists")
	case errors.Is(err, storage.ErrUnauthorized), errors.Is(err, storage.ErrDeviceRevoked):
		writeError(w, http.StatusForbidden, "forbidden", err.Error())
	case errors.Is(err, storage.ErrSequenceReplay):
		writeError(w, http.StatusConflict, "sequence_replay", err.Error())
	case errors.Is(err, storage.ErrInviteInvalid):
		writeError(w, http.StatusGone, "invitation_invalid", err.Error())
	case errors.Is(err, storage.ErrRefreshInvalid), errors.Is(err, storage.ErrTokenReuse):
		writeError(w, http.StatusUnauthorized, "refresh_invalid", err.Error())
	default:
		writeInternal(w, err)
	}
}
func secureEqual(a, b string) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
func safeFilename(value string) string {
	value = filepath.Base(strings.TrimSpace(value))
	if value == "" || value == "." {
		return "artifact.bin"
	}
	if len(value) > 240 {
		return value[:240]
	}
	return value
}
func eventCursor(events []protocol.Event, fallback uint64) uint64 {
	if len(events) == 0 {
		return fallback
	}
	return events[len(events)-1].ServerSequence
}
