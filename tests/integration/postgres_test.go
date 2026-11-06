package integration

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/agent-continuity/agent-continuity-master/internal/auth"
	"github.com/agent-continuity/agent-continuity-master/internal/metering"
	"github.com/agent-continuity/agent-continuity-master/internal/reconcile"
	"github.com/agent-continuity/agent-continuity-master/internal/storage"
	pgstore "github.com/agent-continuity/agent-continuity-master/internal/storage/postgres"
	"github.com/agent-continuity/agent-continuity-master/pkg/e2ee"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

func TestPostgresEventTransaction(t *testing.T) {
	databaseURL := os.Getenv("CONTINUITY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("CONTINUITY_TEST_DATABASE_URL is not set")
	}
	dbURL, parseErr := url.Parse(databaseURL)
	if parseErr != nil || !(strings.HasPrefix(dbURL.Path, "/continuity_test_") || strings.HasPrefix(dbURL.Path, "/continuity_archive_test_") || dbURL.Path == "/continuity_test") {
		t.Fatal("refusing integration test against an unrecognized database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := pgstore.Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migration is not idempotent: %v", err)
	}
	suffix := time.Now().Format("150405.000000000")
	account := protocol.Account{ID: "acct_" + suffix, Name: "integration", CreatedAt: time.Now().UTC()}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	encryptionKeys, err := e2ee.GenerateDeviceKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	device := protocol.Device{ID: "device_" + suffix, AccountID: account.ID, Name: "integration", PublicKey: base64.RawURLEncoding.EncodeToString(publicKey), EncryptionPublicKey: encryptionKeys.PublicKey, CreatedAt: time.Now().UTC()}
	if err := store.RegisterDevice(ctx, device); err != nil {
		t.Fatal(err)
	}
	task := protocol.Task{ID: "task_" + suffix, AccountID: account.ID, Title: "integration", Objective: "verify transaction", Status: "active", HeadEventIDs: []string{}, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := store.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	guest := protocol.Account{ID: "guest_" + suffix, Name: "permission guest", CreatedAt: time.Now().UTC()}
	if err := store.CreateAccount(ctx, guest); err != nil {
		t.Fatal(err)
	}
	grantedAt := time.Now().UTC()
	if err := store.PutTaskPermission(ctx, protocol.TaskPermission{TaskID: task.ID, AccountID: guest.ID, Permission: protocol.PermissionRead, CreatedAt: grantedAt}); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckPermission(ctx, task.ID, guest.ID, protocol.PermissionRead); err != nil {
		t.Fatalf("task permission grant was not effective: %v", err)
	}
	permissions, err := store.ListTaskPermissions(ctx, task.ID)
	if err != nil || len(permissions) != 2 {
		t.Fatalf("task permission persistence failed: %+v %v", permissions, err)
	}
	if err := store.DeleteTaskPermission(ctx, task.ID, guest.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckPermission(ctx, task.ID, guest.ID, protocol.PermissionRead); !errors.Is(err, storage.ErrUnauthorized) {
		t.Fatalf("task permission revoke was not immediate: %v", err)
	}
	if err := store.DeleteTaskPermission(ctx, task.ID, account.ID, time.Now().UTC()); !errors.Is(err, storage.ErrUnauthorized) {
		t.Fatalf("task owner permission must be preserved: %v", err)
	}
	payload := json.RawMessage(`{"ok":true}`)
	value := protocol.Event{EventID: "event_" + suffix, TaskID: task.ID, BranchID: "main", AccountID: account.ID, DeviceID: device.ID, ParentEventIDs: []string{}, LogicalTime: protocol.HLC{WallTimeMS: time.Now().UnixMilli(), Node: device.ID}, DeviceSequence: 1, Type: "progress.updated", SchemaVersion: protocol.SchemaVersion, ContentType: "application/json", ContentDigest: protocol.Digest(payload), Payload: payload, CreatedAtClient: time.Now().UTC(), IdempotencyKey: "integration-" + suffix}
	if err := protocol.SignEvent(&value, privateKey); err != nil {
		t.Fatal(err)
	}
	first, err := store.AppendEvent(ctx, value)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.AppendEvent(ctx, value)
	if err != nil {
		t.Fatal(err)
	}
	if first.Duplicate || !second.Duplicate || first.Event.ServerSequence != second.Event.ServerSequence {
		t.Fatalf("idempotency failed: first=%+v second=%+v", first, second)
	}
	otherTask := task
	otherTask.ID, otherTask.AccountID = "other_"+suffix, guest.ID
	if err := store.CreateTask(ctx, otherTask); err != nil {
		t.Fatal(err)
	}
	otherDevice := device
	otherDevice.ID, otherDevice.AccountID = "otherdevice_"+suffix, guest.ID
	if err := store.RegisterDevice(ctx, otherDevice); err != nil {
		t.Fatal(err)
	}
	crossTenant := value
	crossTenant.TaskID, crossTenant.AccountID, crossTenant.DeviceID = otherTask.ID, guest.ID, otherDevice.ID
	if _, err := store.AppendEvent(ctx, crossTenant); !errors.Is(err, storage.ErrSequenceReplay) {
		t.Fatalf("cross-tenant event collision was not rejected: %v", err)
	}
	// A ledger write failure must roll back the event and its cursor/head changes.
	usage, err := store.ListUsage(ctx, account.ID, time.Time{}, time.Now().Add(time.Minute), 100)
	if err != nil || len(usage) != 1 || usage[0].Quantity != 1 || usage[0].ResourceID != value.EventID {
		t.Fatalf("transactional event meter: %+v %v", usage, err)
	}
	failing := value
	failing.EventID, failing.IdempotencyKey, failing.DeviceSequence = "atomic_"+suffix, "atomic_"+suffix, 2
	failing.ReceivedAtServer = time.Now().UTC()
	collision := metering.AcceptedEvent(failing, account.ID, "")
	if err := store.RecordUsage(ctx, collision); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(ctx, failing); err == nil {
		t.Fatal("ledger failure did not abort event acceptance")
	}
	remaining, err := store.ListEvents(ctx, task.ID, 0, 100, true)
	if err != nil || len(remaining) != 1 || remaining[0].EventID != value.EventID {
		t.Fatalf("event leaked from failed transaction: %+v %v", remaining, err)
	}
	if current, err := store.GetTask(ctx, task.ID); err != nil || len(current.HeadEventIDs) != 1 || current.HeadEventIDs[0] != value.EventID {
		t.Fatalf("task heads changed after rollback: %+v %v", current, err)
	}
	cleanup, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = cleanup.Exec(ctx, `DELETE FROM usage_records WHERE usage_id=$1 AND account_id=$2`, collision.ID, account.ID)
	cleanup.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}

	refresh, _ := auth.NewOpaqueToken("rft")
	session := protocol.AuthSession{ID: "session_" + suffix, FamilyID: "family_" + suffix, AccountID: account.ID, Subject: "integration", RefreshTokenHash: auth.HashOpaque(refresh), CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Hour)}
	if err := store.CreateAuthSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	replacementToken, _ := auth.NewOpaqueToken("rft")
	replacement := protocol.AuthSession{ID: "replacement_" + suffix, RefreshTokenHash: auth.HashOpaque(replacementToken), CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Hour)}
	if _, err := store.RotateAuthSession(ctx, session.RefreshTokenHash, replacement); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RotateAuthSession(ctx, session.RefreshTokenHash, protocol.AuthSession{ID: "reuse_" + suffix, RefreshTokenHash: auth.HashOpaque("reuse"), CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Hour)}); !errors.Is(err, storage.ErrTokenReuse) {
		t.Fatalf("refresh reuse not detected: %v", err)
	}

	keySecret, _ := auth.NewOpaqueToken("ack")
	apiKey := protocol.APIKey{ID: "key_" + suffix, AccountID: account.ID, Name: "integration", Prefix: keySecret[:12], SecretHash: auth.HashOpaque(keySecret), Scopes: []string{"tasks:read"}, CreatedAt: time.Now().UTC()}
	if err := store.CreateAPIKey(ctx, apiKey); err != nil {
		t.Fatal(err)
	}
	if authenticated, err := store.AuthenticateAPIKey(ctx, apiKey.SecretHash); err != nil || authenticated.ID != apiKey.ID {
		t.Fatalf("API key authentication failed: %+v %v", authenticated, err)
	}

	taskKey, _ := e2ee.GenerateTaskKey()
	envelope, err := e2ee.WrapTaskKey(task.ID, 1, taskKey, device.EncryptionPublicKey, device.ID)
	if err != nil {
		t.Fatal(err)
	}
	keySet := protocol.TaskKeySet{TaskID: task.ID, Version: 1, Algorithm: e2ee.Algorithm, CreatedBy: device.ID, WrappedKeys: []protocol.WrappedTaskKey{envelope}, CreatedAt: time.Now().UTC()}
	if err := store.PutTaskKeySet(ctx, keySet); err != nil {
		t.Fatal(err)
	}
	if got, err := store.LatestTaskKeySet(ctx, task.ID); err != nil || got.Version != 1 {
		t.Fatalf("task key persistence failed: %+v %v", got, err)
	}

	branch := protocol.TaskBranch{TaskID: task.ID, ID: "branch_" + suffix, Name: "Integration branch", BaseEventID: value.EventID, Status: "active", CreatedBy: account.ID, CreatedAt: time.Now().UTC()}
	if err := store.CreateBranch(ctx, branch); err != nil {
		t.Fatal(err)
	}
	branches, err := store.ListBranches(ctx, task.ID)
	if err != nil || len(branches) != 2 {
		t.Fatalf("branch persistence failed: %+v %v", branches, err)
	}
	mainBranch, err := store.GetBranch(ctx, task.ID, "main")
	if err != nil {
		t.Fatal(err)
	}
	events, err := store.ListEvents(ctx, task.ID, 0, 100, true)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := reconcile.BuildSnapshot(task, mainBranch, events, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	if got, err := store.LatestSnapshot(ctx, task.ID, "main"); err != nil || got.StateDigest != snapshot.StateDigest {
		t.Fatalf("snapshot persistence failed: %+v %v", got, err)
	}

	upload := protocol.ArtifactUploadSession{ID: "upload_" + suffix, TaskID: task.ID, ArtifactID: "artifact_" + suffix, AccountID: account.ID, VersionID: "version_" + suffix, Filename: "integration.bin", ContentType: "application/octet-stream", TotalSize: 4, ChunkSize: 2, TotalChunks: 2, ContentHash: protocol.Digest([]byte("data")), Status: "open", CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Hour)}
	if err := store.CreateArtifactUpload(ctx, upload); err != nil {
		t.Fatal(err)
	}
	for index, content := range [][]byte{[]byte("da"), []byte("ta")} {
		chunk := protocol.ArtifactChunk{UploadID: upload.ID, Index: index, Offset: int64(index * 2), Size: 2, ContentHash: protocol.Digest(content), StorageRef: "test/chunk", CreatedAt: time.Now().UTC()}
		if err := store.PutArtifactChunk(ctx, chunk); err != nil {
			t.Fatal(err)
		}
	}
	artifact := protocol.Artifact{ID: upload.ArtifactID, TaskID: task.ID, AccountID: account.ID, VersionID: upload.VersionID, ContentType: upload.ContentType, Filename: upload.Filename, Size: upload.TotalSize, ContentHash: upload.ContentHash, StorageRef: "sha256/test", CreatedAt: time.Now().UTC()}
	if err := store.CompleteArtifactUpload(ctx, upload.ID, artifact, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if versions, err := store.ListArtifactVersions(ctx, task.ID, artifact.ID); err != nil || len(versions) != 1 {
		t.Fatalf("artifact upload persistence failed: %+v %v", versions, err)
	}

	billing := protocol.BillingAccount{ID: "billing_" + suffix, AccountID: account.ID, Plan: "developer", Status: "active", Currency: "USD", Limits: json.RawMessage(`{"api_calls":1000}`), CurrentPeriodStart: time.Now().UTC(), CurrentPeriodEnd: time.Now().UTC().AddDate(0, 1, 0), CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := store.PutBillingAccount(ctx, billing); err != nil {
		t.Fatal(err)
	}
	if values, err := store.ListBillingAccounts(ctx, account.ID); err != nil || len(values) != 1 || values[0].ID != billing.ID {
		t.Fatalf("billing account persistence failed: %+v %v", values, err)
	}
	seller := protocol.Account{ID: "seller_" + suffix, Name: "seller", CreatedAt: time.Now().UTC()}
	if err := store.CreateAccount(ctx, seller); err != nil {
		t.Fatal(err)
	}
	buyerAgent := protocol.AgentProfile{ID: "buyer_agent_" + suffix, AccountID: account.ID, Name: "buyer", Provider: "generic", Capabilities: []string{"buy"}, CreatedAt: time.Now().UTC()}
	sellerAgent := protocol.AgentProfile{ID: "seller_agent_" + suffix, AccountID: seller.ID, Name: "seller", Provider: "generic", Capabilities: []string{"sell"}, CreatedAt: time.Now().UTC()}
	if err := store.CreateAgent(ctx, buyerAgent); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateAgent(ctx, sellerAgent); err != nil {
		t.Fatal(err)
	}
	listing := protocol.MarketplaceListing{ID: "listing_" + suffix, PublisherID: seller.ID, Kind: "capability", Name: "integration service", Description: "transaction", Version: "1.0.0", Manifest: json.RawMessage(`{"protocol":"a2a"}`), PriceMinor: 500, Currency: "USD", CommissionBP: 500, Status: "published", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := store.CreateMarketplaceListing(ctx, listing); err != nil {
		t.Fatal(err)
	}
	transaction := protocol.MarketplaceTransaction{ID: "transaction_" + suffix, ListingID: listing.ID, BuyerAccountID: account.ID, SellerAccountID: seller.ID, BuyerAgentID: buyerAgent.ID, SellerAgentID: sellerAgent.ID, Quantity: 2, AmountMinor: 1000, CommissionMinor: 50, Currency: "USD", Status: "pending", IdempotencyKey: "integration-transaction-" + suffix, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := store.CreateMarketplaceTransaction(ctx, transaction); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionMarketplaceTransaction(ctx, transaction.ID, account.ID, "authorized", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if completed, err := store.TransitionMarketplaceTransaction(ctx, transaction.ID, seller.ID, "completed", time.Now().UTC()); err != nil || completed.Status != "completed" {
		t.Fatalf("marketplace transaction completion failed: %+v %v", completed, err)
	}
	if paid, err := store.HasCompletedMarketplaceTransaction(ctx, listing.ID, account.ID); err != nil || !paid {
		t.Fatalf("completed transaction not found: paid=%v err=%v", paid, err)
	}

	// Terminate the store's current PostgreSQL sessions from a separate backend.
	// pgxpool must transparently discard the dead connections and reconnect.
	admin, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid()`); err != nil {
		admin.Close(ctx)
		t.Fatal(err)
	}
	_ = admin.Close(ctx)
	recovered := false
	var recoveryErr error
	for started := time.Now(); time.Since(started) < 5*time.Second; {
		pingCtx, pingCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		err := store.Ping(pingCtx)
		recoveryErr = err
		pingCancel()
		if err == nil {
			recovered = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !recovered {
		t.Fatalf("PostgreSQL connection pool did not recover within 5s after backend termination: %v", recoveryErr)
	}
}
