package integration

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-continuity/agent-continuity-master/internal/archive"
	"github.com/agent-continuity/agent-continuity-master/internal/artifact"
	"github.com/agent-continuity/agent-continuity-master/internal/auth"
	"github.com/agent-continuity/agent-continuity-master/internal/billing"
	"github.com/agent-continuity/agent-continuity-master/internal/httpapi"
	"github.com/agent-continuity/agent-continuity-master/internal/objectstore"
	"github.com/agent-continuity/agent-continuity-master/internal/payments"
	pgstore "github.com/agent-continuity/agent-continuity-master/internal/storage/postgres"
	"github.com/agent-continuity/agent-continuity-master/pkg/client"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
	"github.com/jackc/pgx/v5"
	natsserver "github.com/nats-io/nats-server/v2/server"
)

func TestArchiveJetStreamTransaction(t *testing.T) {
	dsn := os.Getenv("CONTINUITY_ARCHIVE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("dedicated archive test database not configured")
	}
	u, err := url.Parse(dsn)
	if err != nil || !strings.HasPrefix(u.Path, "/continuity_archive_test_") {
		t.Fatal("refusing archive test against a non-dedicated database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	store, err := pgstore.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	sql, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer sql.Close(context.Background())
	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir(), JetStreamMaxMemory: 8 << 20, JetStreamMaxStore: 16 << 20, NoLog: true, NoSigs: true}
	start := func() *natsserver.Server {
		s, err := natsserver.NewServer(opts)
		if err != nil {
			t.Fatal(err)
		}
		go s.Start()
		if !s.ReadyForConnections(5 * time.Second) {
			t.Fatal("NATS did not become ready")
		}
		return s
	}
	ns := start()
	defer func() { ns.Shutdown(); ns.WaitForShutdown() }()
	config, err := archive.Parse(fmt.Sprintf(`{"enabled":true,"default_target":"archive-test","targets":[{"id":"archive-test","provider":"filesystem","root":%q}],"broker":{"url":%q,"allow_insecure":true,"max_bytes":8388608},"concurrency":2,"max_attempts":2,"timeout_seconds":5,"poll_seconds":1}`, t.TempDir(), ns.ClientURL()))
	if err != nil {
		t.Fatal(err)
	}
	q := store.ArchiveQueue()
	config.Lifecycle.AllowDelete = true // test-owned objects only
	config.DefaultTarget = fmt.Sprintf("archive-test-%d", time.Now().UnixNano())
	config.Targets[0].ID = config.DefaultTarget
	if err = q.Configure(ctx, config); err != nil {
		t.Fatal(err)
	}
	local, err := artifact.New(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := archive.New(config, q, store, local, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	worker.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	newID := func(prefix string) string {
		id, err := protocol.NewID(prefix)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	now := time.Now().UTC()
	account := protocol.Account{ID: newID("acct"), Name: "archive test", CreatedAt: now}
	if err = store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	org := protocol.Organization{ID: newID("org"), Name: "归档测试租户", Slug: newID("archive"), OwnerAccountID: account.ID, Plan: "free", KeyProvider: "platform", CreatedAt: now, UpdatedAt: now}
	if err = store.CreateOrganization(ctx, org, protocol.OrganizationMember{OrganizationID: org.ID, AccountID: account.ID, Role: "owner", Status: "active", JoinedAt: now}); err != nil {
		t.Fatal(err)
	}
	task := protocol.Task{ID: newID("task"), AccountID: account.ID, OrganizationID: org.ID, Title: "archive E2E", Status: "active", HeadEventIDs: []string{}, CreatedAt: now, UpdatedAt: now}
	if err = store.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	tokens, _ := auth.NewManager(strings.Repeat("a", 32))
	token, _ := tokens.Issue(account.ID, "archive-test", time.Hour)
	api := httptest.NewServer(httpapi.New(store, local, tokens, "test-bootstrap", 1<<20, worker.Logger, httpapi.WithArchive(worker)).Handler())
	defer api.Close()
	sdk := client.New(api.URL, token)
	payload := []byte("# 中文对话\n用户：请分析图片。\n助手：已保存文件。")
	a, err := sdk.UploadContent(ctx, task.ID, "conversation", "v1", "对话.md", "text/markdown", payload, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = sdk.UploadContent(ctx, task.ID, "conversation", "v1", "对话.md", "text/markdown", payload, ""); err != nil {
		t.Fatal(err)
	}
	if _, err = sdk.UploadContent(ctx, task.ID, "conversation", "v1", "对话.md", "text/markdown", []byte("changed"), ""); err == nil {
		t.Fatal("immutable version was overwritten")
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	device := protocol.Device{ID: newID("device"), AccountID: account.ID, Name: "SDK", PublicKey: base64.RawURLEncoding.EncodeToString(pub), EncryptionPublicKey: base64.RawURLEncoding.EncodeToString(make([]byte, 32)), CreatedAt: now}
	if err = store.RegisterDevice(ctx, device); err != nil {
		t.Fatal(err)
	}
	eventPayload := json.RawMessage(`{"role":"assistant","text":"来自 SDK 的对话"}`)
	e := protocol.Event{EventID: newID("event"), TaskID: task.ID, BranchID: "main", AccountID: account.ID, DeviceID: device.ID, ParentEventIDs: []string{}, LogicalTime: protocol.HLC{WallTimeMS: now.UnixMilli(), Node: device.ID}, DeviceSequence: 1, Type: "progress.updated", SchemaVersion: protocol.SchemaVersion, ContentType: "application/json", ContentDigest: protocol.Digest(eventPayload), Payload: eventPayload, CreatedAtClient: now, IdempotencyKey: newID("message")}
	if err = protocol.SignEvent(&e, priv); err != nil {
		t.Fatal(err)
	}
	if _, err = sdk.AppendEvent(ctx, e); err != nil {
		t.Fatal(err)
	}
	jobs, err := q.List(ctx, task.ID, 0, 100)
	if err != nil || len(jobs) != 2 {
		t.Fatalf("transactional outbox: %d %v", len(jobs), err)
	}
	for _, j := range jobs {
		if j.State != "queued" || j.OrganizationID != org.ID {
			t.Fatalf("wrong initial state or tenant: %+v", j)
		}
	}
	// Publish, stop NATS BEFORE any consumer runs, restart with the same file store.
	b, err := archive.ConnectBroker(config.Broker, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err = worker.RelayOnce(ctx, b); err != nil {
		t.Fatal(err)
	}
	b.Close()
	brokerURL, _ := url.Parse(ns.ClientURL())
	opts.Port, _ = strconv.Atoi(brokerURL.Port())
	ns.Shutdown()
	ns.WaitForShutdown()
	ns = start()
	workerCtx, stopWorker := context.WithCancel(ctx)
	done := make(chan struct{})
	secondWorker, err := archive.New(config, q, store, local, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	secondWorker.Logger = worker.Logger
	worker.Config.Concurrency = 1
	secondWorker.Config.Concurrency = 1
	var workers sync.WaitGroup
	workers.Go(func() { worker.Run(workerCtx) })
	workers.Go(func() { secondWorker.Run(workerCtx) })
	go func() { workers.Wait(); close(done) }()
	defer func() { stopWorker(); <-done }()
	deadline := time.Now().Add(20 * time.Second)
	for {
		jobs, err = q.List(ctx, task.ID, 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		stored := 0
		for _, j := range jobs {
			if j.State == "stored" {
				stored++
			}
		}
		if stored == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("did not drain after broker restart: %+v", jobs)
		}
		time.Sleep(100 * time.Millisecond)
	}
	stopWorker()
	<-done
	done = make(chan struct{})
	close(done)
	var artifactJob archive.Job
	for _, j := range jobs {
		if j.Kind == "artifact" {
			artifactJob = j
		}
	}
	f, err := worker.Download(ctx, artifactJob)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(f)
	f.Close()
	os.Remove(f.Name())
	if !bytes.Equal(got, payload) {
		t.Fatal("archive bytes differ")
	}
	// Cache loss must not lose an already archived attachment.
	cache, err := local.Open(a.StorageRef)
	if err != nil {
		t.Fatal(err)
	}
	cacheName := cache.Name()
	cache.Close()
	if err = os.Remove(cacheName); err != nil {
		t.Fatal(err)
	}
	var restored bytes.Buffer
	if _, err = sdk.DownloadArtifact(ctx, task.ID, a.ID, &restored, 0); err != nil || !bytes.Equal(restored.Bytes(), payload) {
		t.Fatalf("remote fallback: %v", err)
	}
	// Cross-account access must not leak job metadata or archived bytes.
	other := protocol.Account{ID: newID("acct"), Name: "other", CreatedAt: now}
	if err = store.CreateAccount(ctx, other); err != nil {
		t.Fatal(err)
	}
	otherToken, _ := tokens.Issue(other.ID, "other", time.Hour)
	otherSDK := client.New(api.URL, otherToken)
	if _, err = otherSDK.ListStorageJobs(ctx, task.ID, 0); err == nil {
		t.Fatal("cross-tenant job disclosure")
	}
	var count int
	err = sql.QueryRow(ctx, `SELECT count(*) FROM usage_records WHERE metric='storage.archive.bytes' AND organization_id=$1`, org.ID).Scan(&count)
	if err != nil || count != 2 {
		t.Fatalf("archive ledger: %d %v", count, err)
	}
	if worked, err := worker.ProcessJob(ctx, artifactJob.ID); err != nil || worked {
		t.Fatalf("duplicate delivery must be a no-op: %v", err)
	}
	// Archive, retained byte-hours and verified origin reads enter the SAME invoice
	// pipeline as existing usage, followed by idempotent simulated payment receipts.
	if _, err = sql.Exec(ctx, `UPDATE archive_jobs SET stored_at=now()-interval '2 hours' WHERE job_id=$1`, artifactJob.ID); err != nil {
		t.Fatal(err)
	}
	from, to := time.Now().Add(-3*time.Hour), time.Now()
	ba := protocol.BillingAccount{ID: newID("billing"), AccountID: account.ID, OrganizationID: org.ID, Currency: "CNY", Status: "active", Plan: "pro", CreatedAt: now, Limits: json.RawMessage(`{}`)}
	if err = store.PutBillingAccount(ctx, ba); err != nil {
		t.Fatal(err)
	}
	book := billing.DemoPriceBook()
	if err = book.ValidateArchiveRates(); err != nil {
		t.Fatal(err)
	}
	for i := range book.Currencies["CNY"] {
		book.Currencies["CNY"][i].UnitPriceMinor = 1
	}
	bs := billing.New(store, payments.NewRegistry(payments.Demo{}), book, api.URL, []byte(strings.Repeat("b", 32)))
	bill, err := bs.Issue(ctx, ba, from, to, newID("invoice"))
	if err != nil {
		t.Fatal(err)
	}
	meters := map[string]int64{}
	for _, line := range bill.Invoice.Lines {
		meters[line.Metric] = line.Quantity
	}
	if meters["storage.archive.bytes"] <= 0 || meters["storage.download.bytes"] != int64(len(payload)) || meters["storage.retained.byte_hours"] < int64(2*len(payload)) {
		t.Fatalf("missing storage bill meters: %+v", meters)
	}
	bill, paymentID, err := bs.CreatePayment(ctx, account.ID, bill.Invoice.ID, "demo", newID("checkout"))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		bill, err = bs.Simulate(ctx, account.ID, bill.Invoice.ID, paymentID, "succeeded")
		if err != nil {
			t.Fatal(err)
		}
	}
	if bill.Invoice.Status != "paid" || len(bill.Receipts) != 1 {
		t.Fatal("archive invoice failed to settle exactly once")
	}
	if _, err = sql.Exec(ctx, `UPDATE archive_jobs SET stored_at=now() WHERE job_id=$1`, artifactJob.ID); err != nil {
		t.Fatal(err)
	}
	// Opt-in cache eviction verifies remote content and never races archive-only
	// deletion. A subsequent SDK download must transparently fall back to cloud.
	cachePayload := []byte("unique cache lifecycle test")
	cached, err := sdk.UploadContent(ctx, task.ID, "cache-candidate", "v1", "cache.txt", "text/plain", cachePayload, "")
	if err != nil {
		t.Fatal(err)
	}
	cj, err := q.FindArtifact(ctx, task.ID, cached.ID, "v1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = worker.ProcessJob(ctx, cj.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = sql.Exec(ctx, `UPDATE archive_jobs SET stored_at=now()-interval '2 hours' WHERE job_id=$1`, cj.ID); err != nil {
		t.Fatal(err)
	}
	worker.Config.Lifecycle.CacheTTLSeconds = 3600
	if n, err := worker.EvictCacheOnce(ctx); err != nil || n != 1 {
		t.Fatalf("cache eviction %d: %v", n, err)
	}
	if _, err = local.Open(cached.StorageRef); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cache not evicted: %v", err)
	}
	if err = q.RequestDelete(ctx, cj.ID, cached.ContentHash, account.ID, "test deletion of archive copy"); !errors.Is(err, archive.ErrLeaseLost) {
		t.Fatal("last durable copy deletion was allowed")
	}
	restored.Reset()
	if _, err = sdk.DownloadArtifact(ctx, task.ID, cached.ID, &restored, 0); err != nil || !bytes.Equal(restored.Bytes(), cachePayload) {
		t.Fatal("post-eviction readback failed", err)
	}
	// Lease fencing: a worker which timed out cannot acknowledge a newer attempt.
	ref, _, err := local.Put(bytes.NewReader(payload), protocol.Digest(payload))
	if err != nil {
		t.Fatal(err)
	}
	failed := a
	failed.ID = "failure"
	failed.VersionID = "v1"
	failed.StorageRef = ref
	if err = store.PutArtifact(ctx, failed); err != nil {
		t.Fatal(err)
	}
	j, err := q.FindArtifact(ctx, task.ID, failed.ID, "v1")
	if err != nil {
		t.Fatal(err)
	}
	old, err := q.ClaimJob(ctx, j.ID, 2, time.Minute)
	if err != nil || old == nil {
		t.Fatal(err)
	}
	if _, err = sql.Exec(ctx, `UPDATE archive_jobs SET lease_until=now()-interval '1 second' WHERE job_id=$1`, j.ID); err != nil {
		t.Fatal(err)
	}
	newLease, err := q.ClaimJob(ctx, j.ID, 2, time.Minute)
	if err != nil || newLease == nil {
		t.Fatal(err)
	}
	if err = q.Complete(ctx, *old, archive.Receipt{}); !errors.Is(err, archive.ErrLeaseLost) {
		t.Fatalf("stale lease accepted: %v", err)
	}
	if err = q.Fail(ctx, *newLease, "provider_unavailable", 2, time.Second); err != nil {
		t.Fatal(err)
	}
	j, _ = q.Get(ctx, j.ID)
	if j.State != "dead" {
		t.Fatalf("expected dead letter: %+v", j)
	}
	if err = q.Retry(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	if worked, err := worker.ProcessJob(ctx, j.ID); !worked || err != nil {
		t.Fatalf("dead letter replay: %v", err)
	}
	j, _ = q.Get(ctx, j.ID)
	if err = q.SetHold(ctx, j.ID, true, account.ID, "test legal retention lock"); err != nil {
		t.Fatal(err)
	}
	if err = q.RequestDelete(ctx, j.ID, j.ContentHash, account.ID, "test owned copy deletion"); !errors.Is(err, archive.ErrLeaseLost) {
		t.Fatal("legal hold bypassed")
	}
	if err = q.SetHold(ctx, j.ID, false, account.ID, "test release retention lock"); err != nil {
		t.Fatal(err)
	}
	if _, err = sql.Exec(ctx, `UPDATE archive_jobs SET retain_until=now()+interval '1 day' WHERE job_id=$1`, j.ID); err != nil {
		t.Fatal(err)
	}
	if err = q.RequestDelete(ctx, j.ID, j.ContentHash, account.ID, "test owned copy deletion"); !errors.Is(err, archive.ErrLeaseLost) {
		t.Fatal("minimum retention bypassed")
	}
	if _, err = sql.Exec(ctx, `UPDATE archive_jobs SET retain_until=now()-interval '1 day' WHERE job_id=$1`, j.ID); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = sdk.Do(ctx, "POST", fmt.Sprintf("/v1/tasks/%s/storage-jobs/%d/delete", task.ID, j.ID), map[string]string{"confirm_hash": j.ContentHash, "reason": "delete integration test owned copy"}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = worker.ProcessJob(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	j, _ = q.Get(ctx, j.ID)
	if j.State != "deleted" || j.DeletedAt == nil {
		t.Fatal("delete receipt missing")
	}
	if _, err = worker.Providers[j.TargetID].Get(ctx, j.ObjectKey); err == nil {
		t.Fatal("deleted test object remains")
	}
	if err = sql.QueryRow(ctx, `SELECT count(*) FROM archive_action_audit WHERE job_id=$1 AND action='delete.completed'`, j.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("deletion audit missing")
	}
	metrics := httptest.NewRecorder()
	worker.HealthHandler().ServeHTTP(metrics, httptest.NewRequest("GET", "/metrics", nil))
	if metrics.Code != 200 || !strings.Contains(metrics.Body.String(), "continuity_archive_retained_bytes") {
		t.Fatal("worker metrics unavailable")
	}
	// Recover from total broker data loss using the database outbox, not only a
	// server restart on the same disk. This replaces only the test-owned NATS store.
	recovery, err := sdk.UploadContent(ctx, task.ID, "broker-recovery", "v1", "recovery.txt", "text/plain", []byte("recover a lost broker"), "")
	if err != nil {
		t.Fatal(err)
	}
	rj, err := q.FindArtifact(ctx, task.ID, recovery.ID, "v1")
	if err != nil {
		t.Fatal(err)
	}
	if err = q.MarkDispatched(ctx, rj); err != nil {
		t.Fatal(err)
	}
	ns.Shutdown()
	ns.WaitForShutdown()
	opts.StoreDir = t.TempDir()
	ns = start()
	if _, err = sql.Exec(ctx, `UPDATE archive_jobs SET dispatched_at=now()-interval '31 seconds' WHERE job_id=$1`, rj.ID); err != nil {
		t.Fatal(err)
	}
	recoveryCtx, stopRecovery := context.WithCancel(ctx)
	recoveryDone := make(chan struct{})
	go func() { defer close(recoveryDone); worker.Run(recoveryCtx) }()
	_, waitErr := sdk.WaitStorageJob(ctx, task.ID, rj.ID, 15*time.Second)
	stopRecovery()
	<-recoveryDone
	if waitErr != nil {
		t.Fatal("broker disaster recovery", waitErr)
	}
	// Queue backpressure must roll back the source and its outbox atomically.
	_, err = sql.Exec(ctx, `UPDATE archive_control SET max_pending=1+(SELECT count(*) FROM archive_jobs WHERE state NOT IN ('stored','deleted')) WHERE singleton`)
	if err != nil {
		t.Fatal(err)
	}
	pending := failed
	pending.ID = "pending"
	if err = store.PutArtifact(ctx, pending); err != nil {
		t.Fatal(err)
	}
	rejected := failed
	rejected.ID = "rejected"
	if err = store.PutArtifact(ctx, rejected); !errors.Is(err, archive.ErrQueueFull) {
		t.Fatalf("expected backlog guard, got %v", err)
	}
	err = sql.QueryRow(ctx, `SELECT count(*) FROM artifact_versions WHERE task_id=$1 AND artifact_id='rejected'`, task.ID).Scan(&count)
	if err != nil || count != 0 {
		t.Fatal("source committed without durable outbox")
	}
	// Stable destination IDs cannot be repointed during credential rotation.
	changed := config
	changed.Targets = append([]objectstore.Target(nil), config.Targets...)
	changed.Targets[0].Root = t.TempDir()
	if err = q.Configure(ctx, changed); err == nil {
		t.Fatal("target destination silently changed")
	}
	_ = http.MethodGet
	t.Log("PASS: SDK content + signed conversation → transactional outbox → file-backed JetStream restart → two concurrent consumers → verified object download; tenant authorization, idempotent ledger, fencing, dead-letter replay and rollback")
}
