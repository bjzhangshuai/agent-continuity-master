package httpapi

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	artifactstore "github.com/agent-continuity/agent-continuity-master/internal/artifact"
	"github.com/agent-continuity/agent-continuity-master/internal/auth"
	"github.com/agent-continuity/agent-continuity-master/internal/storage/memory"
	"github.com/agent-continuity/agent-continuity-master/pkg/e2ee"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

type apiFixture struct {
	t         *testing.T
	server    *httptest.Server
	api       *Server
	bootstrap string
}

func newFixture(t *testing.T) *apiFixture {
	return newFixtureWithOptions(t)
}

func newFixtureWithOptions(t *testing.T, options ...Option) *apiFixture {
	t.Helper()
	store := memory.New()
	artifacts, err := artifactstore.New(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := auth.NewManager(strings.Repeat("j", 32))
	if err != nil {
		t.Fatal(err)
	}
	api := New(store, artifacts, tokens, strings.Repeat("b", 24), 1<<20, slog.New(slog.NewTextHandler(io.Discard, nil)), options...)
	server := httptest.NewServer(api.Handler())
	t.Cleanup(server.Close)
	return &apiFixture{t: t, server: server, api: api, bootstrap: strings.Repeat("b", 24)}
}

func (f *apiFixture) request(method, path, token string, body any, headers map[string]string) (int, []byte) {
	f.t.Helper()
	var reader io.Reader
	if body != nil {
		switch value := body.(type) {
		case []byte:
			reader = bytes.NewReader(value)
		default:
			data, err := json.Marshal(body)
			if err != nil {
				f.t.Fatal(err)
			}
			reader = bytes.NewReader(data)
		}
	}
	req, err := http.NewRequest(method, f.server.URL+path, reader)
	if err != nil {
		f.t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		f.t.Fatal(err)
	}
	return resp.StatusCode, data
}
func decode[T any](t *testing.T, data []byte) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatalf("decode %s: %v", data, err)
	}
	return value
}
func (f *apiFixture) account(name string) (protocol.Account, string) {
	f.t.Helper()
	status, data := f.request(http.MethodPost, "/v1/accounts", "", map[string]string{"name": name}, map[string]string{"X-Bootstrap-Token": f.bootstrap})
	if status != http.StatusCreated {
		f.t.Fatalf("create account: %d %s", status, data)
	}
	account := decode[protocol.Account](f.t, data)
	status, data = f.request(http.MethodPost, "/v1/auth/token", "", map[string]any{"account_id": account.ID, "subject": "e2e", "ttl_seconds": 900}, map[string]string{"X-Bootstrap-Token": f.bootstrap})
	if status != http.StatusOK {
		f.t.Fatalf("issue token: %d %s", status, data)
	}
	response := decode[map[string]any](f.t, data)
	return account, response["access_token"].(string)
}

func TestLocalizedWebConsoleAndAgentCatalog(t *testing.T) {
	f := newFixture(t)
	status, index := f.request(http.MethodGet, "/", "", nil, nil)
	if status != http.StatusOK || !bytes.Contains(index, []byte("Agent Continuity 管理控制台")) || !bytes.Contains(index, []byte(`data-zh="运营总览"`)) || !bytes.Contains(index, []byte(`id="language-toggle"`)) {
		t.Fatalf("localized console missing: %d %s", status, index)
	}
	for _, id := range []string{"home-scale-badge", "attention-tasks", "operations-scenarios", "task-search", "task-health-filter", "task-provider-filter", "billing-summary", "billing-range", "billing-load-status", "billing-usage-detail", "device-count-badge"} {
		if !bytes.Contains(index, []byte(`id="`+id+`"`)) {
			t.Fatalf("operations console element %s missing", id)
		}
	}
	status, script := f.request(http.MethodGet, "/app.js", "", nil, nil)
	if status != http.StatusOK {
		t.Fatalf("console script: %d %s", status, script)
	}
	for _, provider := range []string{"codex", "claude", "codebuddy", "gemini", "cursor", "opencode"} {
		if !bytes.Contains(script, []byte(`provider: "`+provider+`"`)) {
			t.Fatalf("provider %s missing from console catalog", provider)
		}
	}
	for _, capability := range []string{"renderOperations", "renderTaskList", "providerRoute", "deviceName", "devicePlatform", "origin_device_name", "loadBilling", "panelLoaders", "activatePanel", "formatUsageQuantity", "resume_success_rate", "cross_agent_handoffs"} {
		if !bytes.Contains(script, []byte(capability)) {
			t.Fatalf("operations capability %s missing from console", capability)
		}
	}
}

func (f *apiFixture) device(token string, account protocol.Account, name string) (protocol.Device, ed25519.PrivateKey) {
	f.t.Helper()
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		f.t.Fatal(err)
	}
	status, data := f.request(http.MethodPost, "/v1/devices", token, map[string]string{"name": name, "public_key": base64.RawURLEncoding.EncodeToString(pub)}, nil)
	if status != http.StatusCreated {
		f.t.Fatalf("register device: %d %s", status, data)
	}
	return decode[protocol.Device](f.t, data), private
}

func (f *apiFixture) encryptedDevice(token, name string) (protocol.Device, ed25519.PrivateKey, e2ee.DeviceKeyPair) {
	f.t.Helper()
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		f.t.Fatal(err)
	}
	pair, err := e2ee.GenerateDeviceKeyPair()
	if err != nil {
		f.t.Fatal(err)
	}
	status, data := f.request(http.MethodPost, "/v1/devices", token, map[string]string{"name": name, "public_key": base64.RawURLEncoding.EncodeToString(pub), "encryption_public_key": pair.PublicKey}, nil)
	if status != http.StatusCreated {
		f.t.Fatalf("register encrypted device: %d %s", status, data)
	}
	return decode[protocol.Device](f.t, data), private, pair
}

func TestCrossDeviceLazySyncAndSignedEvent(t *testing.T) {
	f := newFixture(t)
	account, token := f.account("A")
	deviceA, private := f.device(token, account, "A")
	_, _ = f.device(token, account, "B")
	status, data := f.request(http.MethodPost, "/v1/tasks", token, map[string]string{"title": "Portable task", "objective": "continue elsewhere", "origin_device": deviceA.ID}, nil)
	if status != http.StatusCreated {
		t.Fatalf("create task: %d %s", status, data)
	}
	task := decode[protocol.Task](t, data)
	payload := json.RawMessage(`{"text":"work at 60%"}`)
	event := protocol.Event{EventID: "event_a", TaskID: task.ID, BranchID: "main", AccountID: account.ID, DeviceID: deviceA.ID, ParentEventIDs: []string{}, LogicalTime: protocol.HLC{WallTimeMS: time.Now().UnixMilli(), Node: deviceA.ID}, DeviceSequence: 1, Type: "progress.updated", SchemaVersion: protocol.SchemaVersion, ContentType: "application/json", ContentDigest: protocol.Digest(payload), Payload: payload, CreatedAtClient: time.Now().UTC(), IdempotencyKey: "a-1"}
	if err := protocol.SignEvent(&event, private); err != nil {
		t.Fatal(err)
	}
	status, data = f.request(http.MethodPost, "/v1/tasks/"+task.ID+"/events", token, event, nil)
	if status != http.StatusCreated {
		t.Fatalf("append: %d %s", status, data)
	}
	status, index := f.request(http.MethodGet, "/v1/tasks", token, nil, nil)
	if status != http.StatusOK || bytes.Contains(index, []byte("work at 60%")) {
		t.Fatalf("task index was not lazy: %d %s", status, index)
	}
	status, data = f.request(http.MethodGet, "/v1/tasks/"+task.ID+"/events?include_payload=false", token, nil, nil)
	if status != http.StatusOK || bytes.Contains(data, []byte("work at 60%")) {
		t.Fatalf("manifest-only event pull leaked payload: %d %s", status, data)
	}
	status, data = f.request(http.MethodGet, "/v1/tasks/"+task.ID+"/continuity-package?agent=codex", token, nil, nil)
	if status != http.StatusOK || !bytes.Contains(data, []byte("work at 60%")) {
		t.Fatalf("rehydrate package missing payload: %d %s", status, data)
	}
}

func TestArtifactHashInvitationAndRevocation(t *testing.T) {
	f := newFixture(t)
	owner, ownerToken := f.account("owner")
	device, _ := f.device(ownerToken, owner, "owner-device")
	status, data := f.request(http.MethodPost, "/v1/tasks", ownerToken, map[string]string{"title": "Share", "objective": "collaborate", "origin_device": device.ID}, nil)
	if status != http.StatusCreated {
		t.Fatal(string(data))
	}
	task := decode[protocol.Task](t, data)
	content := []byte("artifact payload")
	digest := protocol.Digest(content)
	headers := map[string]string{"X-Content-SHA256": digest, "X-Filename": "result.txt", "Content-Type": "text/plain"}
	status, data = f.request(http.MethodPut, "/v1/tasks/"+task.ID+"/artifacts/art_1", ownerToken, content, headers)
	if status != http.StatusCreated {
		t.Fatalf("upload: %d %s", status, data)
	}
	status, data = f.request(http.MethodPost, "/v1/tasks/"+task.ID+"/invitations", ownerToken, map[string]any{"permission": "read", "ttl_seconds": 600}, nil)
	if status != http.StatusCreated {
		t.Fatalf("invite: %d %s", status, data)
	}
	inviteResponse := decode[map[string]json.RawMessage](t, data)
	var inviteToken string
	if err := json.Unmarshal(inviteResponse["token"], &inviteToken); err != nil {
		t.Fatal(err)
	}
	_, guestToken := f.account("guest")
	status, data = f.request(http.MethodPost, "/v1/invitations/accept", guestToken, map[string]string{"token": inviteToken}, nil)
	if status != http.StatusOK {
		t.Fatalf("accept: %d %s", status, data)
	}
	status, data = f.request(http.MethodGet, "/v1/tasks/"+task.ID+"/artifacts/art_1", guestToken, nil, nil)
	if status != http.StatusOK || !bytes.Equal(data, content) {
		t.Fatalf("download: %d %q", status, data)
	}
	status, _ = f.request(http.MethodPost, "/v1/tasks/"+task.ID+"/events", guestToken, map[string]string{}, nil)
	if status != http.StatusForbidden {
		t.Fatalf("read-only guest should not write, got %d", status)
	}
	status, data = f.request(http.MethodPost, "/v1/invitations/accept", guestToken, map[string]string{"token": inviteToken}, nil)
	if status != http.StatusGone {
		t.Fatalf("invite replay expected 410, got %d %s", status, data)
	}
}

func TestRefreshRotationDetectsReuseAndRevokesFamily(t *testing.T) {
	f := newFixture(t)
	status, data := f.request(http.MethodPost, "/v1/accounts", "", map[string]string{"name": "refresh"}, map[string]string{"X-Bootstrap-Token": f.bootstrap})
	if status != http.StatusCreated {
		t.Fatal(string(data))
	}
	account := decode[protocol.Account](t, data)
	status, data = f.request(http.MethodPost, "/v1/auth/token", "", map[string]any{"account_id": account.ID, "subject": "rotation", "ttl_seconds": 900}, map[string]string{"X-Bootstrap-Token": f.bootstrap})
	issued := decode[map[string]any](t, data)
	oldRefresh := issued["refresh_token"].(string)
	status, data = f.request(http.MethodPost, "/v1/auth/refresh", "", map[string]string{"refresh_token": oldRefresh}, nil)
	if status != http.StatusOK {
		t.Fatalf("rotate: %d %s", status, data)
	}
	rotated := decode[map[string]any](t, data)
	newRefresh := rotated["refresh_token"].(string)
	status, data = f.request(http.MethodPost, "/v1/auth/refresh", "", map[string]string{"refresh_token": oldRefresh}, nil)
	if status != http.StatusUnauthorized || !bytes.Contains(data, []byte("refresh_reuse_detected")) {
		t.Fatalf("reuse was not detected: %d %s", status, data)
	}
	status, data = f.request(http.MethodPost, "/v1/auth/refresh", "", map[string]string{"refresh_token": newRefresh}, nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("replacement survived family revocation: %d %s", status, data)
	}
}

func TestAPIKeyScopesAuditAndRevocation(t *testing.T) {
	f := newFixture(t)
	_, ownerToken := f.account("key-owner")
	status, data := f.request(http.MethodPost, "/v1/api-keys", ownerToken, map[string]any{"name": "reader", "scopes": []string{"tasks:read", "audit:read"}}, nil)
	if status != http.StatusCreated {
		t.Fatalf("create key: %d %s", status, data)
	}
	response := decode[struct {
		APIKey protocol.APIKey `json:"api_key"`
		Secret string          `json:"secret"`
	}](t, data)
	status, data = f.request(http.MethodGet, "/v1/tasks", response.Secret, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("scoped read: %d %s", status, data)
	}
	status, data = f.request(http.MethodPost, "/v1/tasks", response.Secret, map[string]string{"title": "denied", "objective": "scope"}, nil)
	if status != http.StatusForbidden || !bytes.Contains(data, []byte("insufficient_scope")) {
		t.Fatalf("scope bypass: %d %s", status, data)
	}
	status, data = f.request(http.MethodGet, "/v1/audit/events", ownerToken, nil, nil)
	if status != http.StatusOK || !bytes.Contains(data, []byte("api_key.created")) {
		t.Fatalf("audit missing key creation: %d %s", status, data)
	}
	status, data = f.request(http.MethodDelete, "/v1/api-keys/"+response.APIKey.ID, ownerToken, nil, nil)
	if status != http.StatusNoContent {
		t.Fatalf("revoke key: %d %s", status, data)
	}
	status, _ = f.request(http.MethodGet, "/v1/tasks", response.Secret, nil, nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("revoked key remained valid: %d", status)
	}
}

func TestTaskKeyEnvelopeCanOnlyBeFetchedByRecipientDevice(t *testing.T) {
	f := newFixture(t)
	account, token := f.account("encrypted")
	deviceA, _, encryptionA := f.encryptedDevice(token, "encrypted-a")
	deviceB, _, encryptionB := f.encryptedDevice(token, "encrypted-b")
	status, data := f.request(http.MethodPost, "/v1/tasks", token, map[string]string{"title": "E2EE", "objective": "server cannot read", "origin_device": deviceA.ID}, nil)
	if status != http.StatusCreated {
		t.Fatal(string(data))
	}
	task := decode[protocol.Task](t, data)
	taskKey, err := e2ee.GenerateTaskKey()
	if err != nil {
		t.Fatal(err)
	}
	wrappedA, err := e2ee.WrapTaskKey(task.ID, 1, taskKey, encryptionA.PublicKey, deviceA.ID)
	if err != nil {
		t.Fatal(err)
	}
	wrappedB, err := e2ee.WrapTaskKey(task.ID, 1, taskKey, encryptionB.PublicKey, deviceB.ID)
	if err != nil {
		t.Fatal(err)
	}
	keySet := protocol.TaskKeySet{TaskID: task.ID, Version: 1, Algorithm: e2ee.Algorithm, CreatedBy: deviceA.ID, WrappedKeys: []protocol.WrappedTaskKey{wrappedA, wrappedB}}
	status, data = f.request(http.MethodPost, "/v1/tasks/"+task.ID+"/keys", token, keySet, nil)
	if status != http.StatusCreated {
		t.Fatalf("put key set: %d %s", status, data)
	}
	status, data = f.request(http.MethodGet, "/v1/tasks/"+task.ID+"/keys?device_id="+deviceB.ID, token, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("get key set: %d %s", status, data)
	}
	fetched := decode[protocol.TaskKeySet](t, data)
	if len(fetched.WrappedKeys) != 1 || fetched.WrappedKeys[0].DeviceID != deviceB.ID {
		t.Fatalf("unexpected envelope filtering: %+v", fetched.WrappedKeys)
	}
	unwrapped, err := e2ee.UnwrapTaskKey(task.ID, 1, fetched.WrappedKeys[0], encryptionB.PrivateKey)
	if err != nil || !bytes.Equal(unwrapped, taskKey) {
		t.Fatalf("recipient could not decrypt: key=%x err=%v", unwrapped, err)
	}
	_, outsiderToken := f.account("outsider")
	status, _ = f.request(http.MethodGet, "/v1/tasks/"+task.ID+"/keys?device_id="+deviceB.ID, outsiderToken, nil, nil)
	if status != http.StatusForbidden {
		t.Fatalf("outsider read key metadata: %d (owner=%s)", status, account.ID)
	}
}

func TestAuthenticationEndpointsAreRateLimited(t *testing.T) {
	f := newFixture(t)
	var status int
	for i := 0; i < 21; i++ {
		status, _ = f.request(http.MethodPost, "/v1/auth/refresh", "", map[string]string{"refresh_token": "invalid"}, nil)
	}
	if status != http.StatusTooManyRequests {
		t.Fatalf("expected rate limit, got %d", status)
	}
}

func TestCrossTenantEnumerationDeviceSpoofingAndSignatureFailure(t *testing.T) {
	f := newFixture(t)
	owner, ownerToken := f.account("security-owner")
	ownerDevice, ownerPrivate := f.device(ownerToken, owner, "security-owner-device")
	status, data := f.request(http.MethodPost, "/v1/tasks", ownerToken, map[string]string{"title": "Private task", "objective": "must not leak", "origin_device": ownerDevice.ID}, nil)
	if status != http.StatusCreated {
		t.Fatal(string(data))
	}
	task := decode[protocol.Task](t, data)
	outsider, outsiderToken := f.account("security-outsider")
	outsiderDevice, outsiderPrivate := f.device(outsiderToken, outsider, "security-outsider-device")
	status, data = f.request(http.MethodGet, "/v1/tasks", outsiderToken, nil, nil)
	if status != http.StatusOK || bytes.Contains(data, []byte(task.ID)) || bytes.Contains(data, []byte(task.Title)) {
		t.Fatalf("cross-tenant task leaked through enumeration: %d %s", status, data)
	}
	status, data = f.request(http.MethodGet, "/v1/tasks/"+task.ID, outsiderToken, nil, nil)
	if status != http.StatusForbidden || bytes.Contains(data, []byte(task.Title)) || bytes.Contains(data, []byte(task.Objective)) {
		t.Fatalf("cross-tenant direct read leaked task data: %d %s", status, data)
	}
	payload := json.RawMessage(`{"secure":true}`)
	invalidSignature := protocol.Event{EventID: "event_bad_signature", TaskID: task.ID, BranchID: "main", AccountID: owner.ID, DeviceID: ownerDevice.ID, LogicalTime: protocol.HLC{WallTimeMS: time.Now().UnixMilli(), Node: ownerDevice.ID}, DeviceSequence: 1, Type: "progress.updated", SchemaVersion: protocol.SchemaVersion, ContentType: "application/json", ContentDigest: protocol.Digest(payload), Payload: payload, CreatedAtClient: time.Now().UTC(), IdempotencyKey: "bad-signature"}
	if err := protocol.SignEvent(&invalidSignature, outsiderPrivate); err != nil {
		t.Fatal(err)
	}
	status, data = f.request(http.MethodPost, "/v1/tasks/"+task.ID+"/events", ownerToken, invalidSignature, nil)
	if status != http.StatusUnauthorized || !bytes.Contains(data, []byte("invalid_event_signature")) {
		t.Fatalf("invalid signature accepted: %d %s", status, data)
	}
	spoofedDevice := invalidSignature
	spoofedDevice.EventID = "event_spoofed_device"
	spoofedDevice.DeviceID = outsiderDevice.ID
	spoofedDevice.LogicalTime.Node = outsiderDevice.ID
	spoofedDevice.IdempotencyKey = "spoofed-device"
	if err := protocol.SignEvent(&spoofedDevice, outsiderPrivate); err != nil {
		t.Fatal(err)
	}
	status, data = f.request(http.MethodPost, "/v1/tasks/"+task.ID+"/events", ownerToken, spoofedDevice, nil)
	if status != http.StatusForbidden || !bytes.Contains(data, []byte("device_forbidden")) {
		t.Fatalf("cross-account device spoofing accepted: %d %s", status, data)
	}
	valid := invalidSignature
	valid.EventID, valid.IdempotencyKey = "event_security_valid", "security-valid"
	if err := protocol.SignEvent(&valid, ownerPrivate); err != nil {
		t.Fatal(err)
	}
	status, data = f.request(http.MethodPost, "/v1/tasks/"+task.ID+"/events", ownerToken, valid, nil)
	if status != http.StatusCreated {
		t.Fatalf("valid signed event rejected after attacks: %d %s", status, data)
	}
}

func TestBranchMergeConflictResolutionAndSnapshotRebuild(t *testing.T) {
	f := newFixture(t)
	account, token := f.account("dag")
	device, private := f.device(token, account, "dag-device")
	status, data := f.request(http.MethodPost, "/v1/tasks", token, map[string]string{"title": "DAG", "objective": "merge", "origin_device": device.ID}, nil)
	if status != http.StatusCreated {
		t.Fatal(string(data))
	}
	task := decode[protocol.Task](t, data)
	sequence := uint64(0)
	sign := func(id, branch, eventType string, parents []string, payload json.RawMessage) protocol.Event {
		sequence++
		value := protocol.Event{EventID: id, TaskID: task.ID, BranchID: branch, AccountID: account.ID, DeviceID: device.ID, ParentEventIDs: parents, LogicalTime: protocol.HLC{WallTimeMS: time.Now().UnixMilli(), Counter: uint32(sequence), Node: device.ID}, DeviceSequence: sequence, Type: eventType, SchemaVersion: protocol.SchemaVersion, ContentType: "application/json", ContentDigest: protocol.Digest(payload), Payload: payload, CreatedAtClient: time.Now().UTC(), IdempotencyKey: id}
		if err := protocol.SignEvent(&value, private); err != nil {
			t.Fatal(err)
		}
		return value
	}
	base := sign("event_base", "main", "human.message.created", nil, json.RawMessage(`{"text":"base"}`))
	status, data = f.request(http.MethodPost, "/v1/tasks/"+task.ID+"/events", token, base, nil)
	if status != http.StatusCreated {
		t.Fatalf("base: %d %s", status, data)
	}
	status, data = f.request(http.MethodPost, "/v1/tasks/"+task.ID+"/branches", token, map[string]string{"branch_id": "alternative", "name": "Alternative", "base_event_id": base.EventID}, nil)
	if status != http.StatusCreated {
		t.Fatalf("branch: %d %s", status, data)
	}
	alternative := sign("event_alt", "alternative", "agent.message.created", []string{base.EventID}, json.RawMessage(`{"text":"alternative"}`))
	status, data = f.request(http.MethodPost, "/v1/tasks/"+task.ID+"/events", token, alternative, nil)
	if status != http.StatusCreated {
		t.Fatalf("alternative: %d %s", status, data)
	}
	main := sign("event_main", "main", "agent.message.created", []string{base.EventID}, json.RawMessage(`{"text":"main"}`))
	status, data = f.request(http.MethodPost, "/v1/tasks/"+task.ID+"/events", token, main, nil)
	if status != http.StatusCreated {
		t.Fatalf("main: %d %s", status, data)
	}
	decision := json.RawMessage(`{"choice":"combine","provenance":["event_alt","event_main"]}`)
	merge := sign("event_merge", "main", "task.merged", []string{alternative.EventID, main.EventID}, decision)
	status, data = f.request(http.MethodPost, "/v1/tasks/"+task.ID+"/branches/alternative/merge", token, map[string]any{"target_branch_id": "main", "decision": decision, "merge_event": merge}, nil)
	if status != http.StatusOK {
		t.Fatalf("merge: %d %s", status, data)
	}
	status, data = f.request(http.MethodGet, "/v1/tasks/"+task.ID+"/branches", token, nil, nil)
	if status != http.StatusOK || !bytes.Contains(data, []byte(`"status":"merged"`)) {
		t.Fatalf("merged branch state missing: %d %s", status, data)
	}
	status, data = f.request(http.MethodPost, "/v1/tasks/"+task.ID+"/branches/main/snapshots", token, map[string]any{}, nil)
	if status != http.StatusCreated {
		t.Fatalf("snapshot: %d %s", status, data)
	}
	snapshot := decode[protocol.TaskSnapshot](t, data)
	if snapshot.EventCount != 4 || snapshot.State.LastEventID != merge.EventID {
		t.Fatalf("snapshot did not include merged ancestry: %+v", snapshot)
	}
	status, data = f.request(http.MethodGet, "/v1/tasks/"+task.ID+"/branches/main/snapshots/latest", token, nil, nil)
	if status != http.StatusOK || !bytes.Contains(data, []byte(snapshot.StateDigest)) {
		t.Fatalf("latest snapshot: %d %s", status, data)
	}
}

func TestSemanticConflictRequiresExplicitResolution(t *testing.T) {
	f := newFixture(t)
	account, token := f.account("conflicts")
	device, private := f.device(token, account, "conflict-device")
	status, data := f.request(http.MethodPost, "/v1/tasks", token, map[string]string{"title": "Conflict", "objective": "resolve", "origin_device": device.ID}, nil)
	task := decode[protocol.Task](t, data)
	sign := func(id string, sequence uint64) protocol.Event {
		payload := json.RawMessage(`{"percent":50}`)
		value := protocol.Event{EventID: id, TaskID: task.ID, BranchID: "main", AccountID: account.ID, DeviceID: device.ID, ParentEventIDs: []string{}, LogicalTime: protocol.HLC{WallTimeMS: time.Now().UnixMilli(), Counter: uint32(sequence), Node: device.ID}, DeviceSequence: sequence, Type: "progress.updated", SchemaVersion: protocol.SchemaVersion, ContentType: "application/json", ContentDigest: protocol.Digest(payload), Payload: payload, CreatedAtClient: time.Now().UTC(), IdempotencyKey: id}
		if err := protocol.SignEvent(&value, private); err != nil {
			t.Fatal(err)
		}
		return value
	}
	status, _ = f.request(http.MethodPost, "/v1/tasks/"+task.ID+"/events", token, sign("progress_a", 1), nil)
	if status != http.StatusCreated {
		t.Fatalf("first progress: %d", status)
	}
	status, data = f.request(http.MethodPost, "/v1/tasks/"+task.ID+"/events", token, sign("progress_b", 2), nil)
	result := decode[protocol.AppendResult](t, data)
	if status != http.StatusCreated || len(result.Conflicts) != 1 || result.Conflicts[0].Kind != "semantic_conflict" {
		t.Fatalf("semantic conflict: %d %+v", status, result)
	}
	conflictID := result.Conflicts[0].ID
	status, data = f.request(http.MethodPost, "/v1/tasks/"+task.ID+"/conflicts/"+conflictID+"/resolve", token, map[string]string{"strategy": "manual", "notes": "reviewed both updates"}, nil)
	if status != http.StatusOK {
		t.Fatalf("resolve: %d %s", status, data)
	}
	status, data = f.request(http.MethodGet, "/v1/tasks/"+task.ID+"/conflicts", token, nil, nil)
	if status != http.StatusOK || !bytes.Contains(data, []byte(`"conflicts":[]`)) {
		t.Fatalf("resolved conflict remained open: %d %s", status, data)
	}
}

func TestResumableArtifactUploadAndVersionHistory(t *testing.T) {
	f := newFixture(t)
	account, token := f.account("artifact-transfer")
	device, _ := f.device(token, account, "artifact-device")
	status, data := f.request(http.MethodPost, "/v1/tasks", token, map[string]string{"title": "Artifact", "objective": "resume upload", "origin_device": device.ID}, nil)
	task := decode[protocol.Task](t, data)
	uploadVersion := func(content []byte, parent string) protocol.Artifact {
		status, data = f.request(http.MethodPost, "/v1/tasks/"+task.ID+"/artifact-uploads", token, map[string]any{"artifact_id": "large", "parent_version_id": parent, "filename": "large.bin", "content_type": "application/octet-stream", "total_size": len(content), "chunk_size": 4, "content_hash": protocol.Digest(content)}, nil)
		if status != http.StatusCreated {
			t.Fatalf("create upload: %d %s", status, data)
		}
		upload := decode[protocol.ArtifactUploadStatus](t, data)
		order := []int{0, upload.Session.TotalChunks - 1}
		if upload.Session.TotalChunks == 1 {
			order = []int{0}
		}
		for _, index := range order {
			start := index * 4
			end := min(start+4, len(content))
			chunk := content[start:end]
			status, data = f.request(http.MethodPut, "/v1/artifact-uploads/"+upload.Session.ID+"/chunks/"+strconv.Itoa(index), token, chunk, map[string]string{"X-Content-SHA256": protocol.Digest(chunk)})
			if status != http.StatusOK {
				t.Fatalf("put chunk %d: %d %s", index, status, data)
			}
		}
		if upload.Session.TotalChunks > 2 {
			status, data = f.request(http.MethodPost, "/v1/artifact-uploads/"+upload.Session.ID+"/complete", token, nil, nil)
			if status != http.StatusConflict || !bytes.Contains(data, []byte(`"missing_chunks":[1]`)) {
				t.Fatalf("interrupted upload status: %d %s", status, data)
			}
			chunk := content[4:8]
			status, data = f.request(http.MethodPut, "/v1/artifact-uploads/"+upload.Session.ID+"/chunks/1", token, chunk, map[string]string{"X-Content-SHA256": protocol.Digest(chunk)})
			if status != http.StatusOK {
				t.Fatalf("resume chunk: %d %s", status, data)
			}
		}
		status, data = f.request(http.MethodPost, "/v1/artifact-uploads/"+upload.Session.ID+"/complete", token, nil, nil)
		if status != http.StatusCreated {
			t.Fatalf("complete: %d %s", status, data)
		}
		return decode[protocol.Artifact](t, data)
	}
	firstContent := []byte("abcdefghij")
	first := uploadVersion(firstContent, "")
	status, data = f.request(http.MethodGet, "/v1/tasks/"+task.ID+"/artifacts/large", token, nil, nil)
	if status != http.StatusOK || !bytes.Equal(data, firstContent) {
		t.Fatalf("assembled artifact mismatch: %d %q", status, data)
	}
	second := uploadVersion([]byte("version-two"), first.VersionID)
	if second.ParentVersionID != first.VersionID {
		t.Fatalf("version provenance lost: %+v", second)
	}
	status, data = f.request(http.MethodGet, "/v1/tasks/"+task.ID+"/artifacts/large/versions", token, nil, nil)
	versions := decode[struct {
		Versions []protocol.Artifact `json:"versions"`
	}](t, data)
	if status != http.StatusOK || len(versions.Versions) != 2 {
		t.Fatalf("artifact history: %d %s", status, data)
	}
}

func TestAgentBindingParticipantMentionAndNotification(t *testing.T) {
	f := newFixture(t)
	owner, ownerToken := f.account("collaboration-owner")
	device, private := f.device(ownerToken, owner, "collaboration-device")
	status, data := f.request(http.MethodPost, "/v1/tasks", ownerToken, map[string]string{"title": "Room", "objective": "collaborate", "origin_device": device.ID}, nil)
	task := decode[protocol.Task](t, data)
	status, data = f.request(http.MethodPost, "/v1/agents", ownerToken, map[string]any{"name": "Reviewer", "provider": "generic", "capabilities": []string{"review", "rehydrate"}}, nil)
	if status != http.StatusCreated {
		t.Fatalf("create agent: %d %s", status, data)
	}
	agent := decode[protocol.AgentProfile](t, data)
	status, data = f.request(http.MethodPost, "/v1/tasks/"+task.ID+"/agents", ownerToken, map[string]any{"agent_id": agent.ID, "role": "reviewer", "resume_mode": "rehydrate", "capabilities": []string{"review"}}, nil)
	if status != http.StatusCreated {
		t.Fatalf("bind agent: %d %s", status, data)
	}
	status, data = f.request(http.MethodPost, "/v1/tasks/"+task.ID+"/invitations", ownerToken, map[string]any{"permission": "write", "ttl_seconds": 600}, nil)
	invite := decode[struct {
		Token string `json:"token"`
	}](t, data)
	guest, guestToken := f.account("collaboration-guest")
	status, data = f.request(http.MethodPost, "/v1/invitations/accept", guestToken, map[string]string{"token": invite.Token}, nil)
	if status != http.StatusOK {
		t.Fatalf("accept: %d %s", status, data)
	}
	status, data = f.request(http.MethodGet, "/v1/tasks/"+task.ID+"/participants", ownerToken, nil, nil)
	participants := decode[struct {
		Participants []protocol.TaskParticipant `json:"participants"`
	}](t, data)
	if status != http.StatusOK || len(participants.Participants) != 3 {
		t.Fatalf("participants: %d %s", status, data)
	}
	guestParticipantID := ""
	for _, participant := range participants.Participants {
		if participant.AccountID == guest.ID && participant.Kind == "human" {
			guestParticipantID = participant.ID
		}
	}
	if guestParticipantID == "" {
		t.Fatal("guest participant missing")
	}
	payload, _ := json.Marshal(map[string]any{"text": "please review", "mentions": []string{guestParticipantID}})
	event := protocol.Event{EventID: "mention_event", TaskID: task.ID, BranchID: "main", AccountID: owner.ID, DeviceID: device.ID, ParentEventIDs: []string{}, LogicalTime: protocol.HLC{WallTimeMS: time.Now().UnixMilli(), Node: device.ID}, DeviceSequence: 1, Type: "human.message.created", SchemaVersion: protocol.SchemaVersion, ContentType: "application/json", ContentDigest: protocol.Digest(payload), Payload: payload, CreatedAtClient: time.Now().UTC(), IdempotencyKey: "mention-event"}
	if err := protocol.SignEvent(&event, private); err != nil {
		t.Fatal(err)
	}
	status, data = f.request(http.MethodPost, "/v1/tasks/"+task.ID+"/events", ownerToken, event, nil)
	if status != http.StatusCreated {
		t.Fatalf("mention event: %d %s", status, data)
	}
	status, data = f.request(http.MethodGet, "/v1/notifications?unread_only=true", guestToken, nil, nil)
	if status != http.StatusOK || !bytes.Contains(data, []byte(`"kind":"mention"`)) {
		t.Fatalf("mention notification: %d %s", status, data)
	}
}

func TestWebSocketWakeupHasDurableCursorFallback(t *testing.T) {
	f := newFixture(t)
	account, token := f.account("realtime")
	device, private := f.device(token, account, "realtime-device")
	status, data := f.request(http.MethodPost, "/v1/tasks", token, map[string]string{"title": "Realtime", "objective": "wake up", "origin_device": device.ID}, nil)
	task := decode[protocol.Task](t, data)
	status, data = f.request(http.MethodGet, "/v1/tasks/"+task.ID+"/participants", token, nil, nil)
	participants := decode[struct {
		Participants []protocol.TaskParticipant `json:"participants"`
	}](t, data)
	participantID := participants.Participants[0].ID
	address := strings.TrimPrefix(f.server.URL, "http://")
	connection, err := net.DialTimeout("tcp", address, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(5 * time.Second))
	request := "GET /v1/tasks/" + task.ID + "/realtime?participant_id=" + participantID + "&device_id=" + device.ID + " HTTP/1.1\r\nHost: " + address + "\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: MDEyMzQ1Njc4OWFiY2RlZg==\r\nAuthorization: Bearer " + token + "\r\n\r\n"
	if _, err := connection.Write([]byte(request)); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(connection)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("websocket status: %d", response.StatusCode)
	}
	connected := readWebSocketTestMessage(t, reader)
	if connected.Type != "connected" || !bytes.Contains(connected.Payload, []byte("/events")) {
		t.Fatalf("missing cursor fallback: %+v", connected)
	}
	payload := json.RawMessage(`{"text":"wake"}`)
	event := protocol.Event{EventID: "realtime_event", TaskID: task.ID, BranchID: "main", AccountID: account.ID, DeviceID: device.ID, ParentEventIDs: []string{}, LogicalTime: protocol.HLC{WallTimeMS: time.Now().UnixMilli(), Node: device.ID}, DeviceSequence: 1, Type: "human.message.created", SchemaVersion: protocol.SchemaVersion, ContentType: "application/json", ContentDigest: protocol.Digest(payload), Payload: payload, CreatedAtClient: time.Now().UTC(), IdempotencyKey: "realtime-event"}
	if err := protocol.SignEvent(&event, private); err != nil {
		t.Fatal(err)
	}
	status, data = f.request(http.MethodPost, "/v1/tasks/"+task.ID+"/events", token, event, nil)
	if status != http.StatusCreated {
		t.Fatalf("append: %d %s", status, data)
	}
	found := false
	for i := 0; i < 3; i++ {
		message := readWebSocketTestMessage(t, reader)
		if message.Type == "event.available" && message.ResourceID == event.EventID && message.ServerSequence > 0 {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("event availability wakeup not received")
	}
	status, data = f.request(http.MethodGet, "/v1/tasks/"+task.ID+"/events?after=0", token, nil, nil)
	if status != http.StatusOK || !bytes.Contains(data, []byte(event.EventID)) {
		t.Fatalf("durable cursor fallback failed: %d %s", status, data)
	}
}

func TestSyncProtocolProofNegotiationPullAckAndReplay(t *testing.T) {
	f := newFixture(t)
	account, token := f.account("sync")
	deviceA, privateA := f.device(token, account, "sync-a")
	deviceB, privateB := f.device(token, account, "sync-b")
	status, data := f.request(http.MethodPost, "/v1/tasks", token, map[string]string{"title": "Sync", "objective": "offline convergence", "origin_device": deviceA.ID}, nil)
	if status != http.StatusCreated {
		t.Fatal(string(data))
	}
	task := decode[protocol.Task](t, data)
	hello := protocol.SyncHelloRequest{ProtocolVersions: []string{protocol.ProtocolVersion}, Capabilities: append([]string(nil), protocol.ServerSyncCapabilities...), ClientVersion: "test", DeviceID: deviceB.ID, Nonce: "nonce-0000000000000001", Timestamp: time.Now().UTC()}
	if err := protocol.SignSyncHello(&hello, privateB); err != nil {
		t.Fatal(err)
	}
	status, data = f.request(http.MethodPost, "/v1/sync/hello", token, hello, nil)
	if status != http.StatusOK {
		t.Fatalf("hello: %d %s", status, data)
	}
	negotiated := decode[protocol.SyncHelloResponse](t, data)
	if negotiated.ProtocolVersion != protocol.ProtocolVersion || len(negotiated.Tasks) != 1 || negotiated.Tasks[0].Task.ID != task.ID {
		t.Fatalf("negotiation incomplete: %+v", negotiated)
	}
	status, data = f.request(http.MethodPost, "/v1/sync/hello", token, hello, nil)
	if status != http.StatusConflict || !bytes.Contains(data, []byte("hello_replay")) {
		t.Fatalf("hello replay accepted: %d %s", status, data)
	}
	payload := json.RawMessage(`{"text":"offline event"}`)
	event := protocol.Event{EventID: "sync_event", TaskID: task.ID, BranchID: "main", AccountID: account.ID, DeviceID: deviceA.ID, LogicalTime: protocol.HLC{WallTimeMS: time.Now().UnixMilli(), Node: deviceA.ID}, DeviceSequence: 1, Type: "human.message.created", SchemaVersion: protocol.SchemaVersion, ContentType: "application/json", ContentDigest: protocol.Digest(payload), Payload: payload, CreatedAtClient: time.Now().UTC(), IdempotencyKey: "sync-event"}
	if err := protocol.SignEvent(&event, privateA); err != nil {
		t.Fatal(err)
	}
	status, data = f.request(http.MethodPost, "/v1/sync/push", token, protocol.SyncPushRequest{DeviceID: deviceA.ID, Events: []protocol.Event{event}}, nil)
	push := decode[protocol.SyncPushResponse](t, data)
	if status != http.StatusOK || len(push.Acknowledgements) != 1 || !push.Acknowledgements[0].Accepted || push.Acknowledgements[0].ServerSequence == 0 {
		t.Fatalf("batch push: %d %+v", status, push)
	}
	status, data = f.request(http.MethodPost, "/v1/sync/push", token, protocol.SyncPushRequest{DeviceID: deviceA.ID, Events: []protocol.Event{event}}, nil)
	duplicate := decode[protocol.SyncPushResponse](t, data)
	if status != http.StatusOK || !duplicate.Acknowledgements[0].Accepted || !duplicate.Acknowledgements[0].Duplicate {
		t.Fatalf("idempotent batch retry: %d %+v", status, duplicate)
	}
	hello.Nonce = "nonce-0000000000000002"
	hello.Timestamp = time.Now().UTC()
	hello.Tasks = []protocol.SyncTaskState{{TaskID: task.ID, EventCursor: 0}}
	if err := protocol.SignSyncHello(&hello, privateB); err != nil {
		t.Fatal(err)
	}
	status, data = f.request(http.MethodPost, "/v1/sync/hello", token, hello, nil)
	negotiated = decode[protocol.SyncHelloResponse](t, data)
	if status != http.StatusOK || len(negotiated.Tasks[0].MissingEventIDs) != 1 || negotiated.Tasks[0].MissingEventIDs[0] != event.EventID {
		t.Fatalf("missing discovery: %d %+v", status, negotiated)
	}
	status, data = f.request(http.MethodPost, "/v1/sync/pull", token, protocol.SyncPullRequest{DeviceID: deviceB.ID, Tasks: []protocol.SyncPullCursor{{TaskID: task.ID, IncludePayload: true}}}, nil)
	pull := decode[protocol.SyncPullResponse](t, data)
	if status != http.StatusOK || len(pull.Batches) != 1 || len(pull.Batches[0].Events) != 1 || len(pull.Devices) != 1 || pull.Devices[0].ID != deviceA.ID {
		t.Fatalf("cursor pull: %d %+v", status, pull)
	}
	cursor := pull.Batches[0].NextCursor
	status, data = f.request(http.MethodPost, "/v1/sync/ack", token, protocol.SyncAckRequest{DeviceID: deviceB.ID, Tasks: []protocol.SyncAck{{TaskID: task.ID, Cursor: cursor}}}, nil)
	if status != http.StatusOK {
		t.Fatalf("ack: %d %s", status, data)
	}
	hello.Nonce = "nonce-0000000000000003"
	hello.Timestamp = time.Now().UTC()
	hello.Tasks[0].EventCursor = cursor
	hello.Tasks[0].HeadEventIDs = pull.Batches[0].Task.HeadEventIDs
	if err := protocol.SignSyncHello(&hello, privateB); err != nil {
		t.Fatal(err)
	}
	status, data = f.request(http.MethodPost, "/v1/sync/hello", token, hello, nil)
	negotiated = decode[protocol.SyncHelloResponse](t, data)
	if status != http.StatusOK || negotiated.Tasks[0].AcknowledgedCursor != cursor || len(negotiated.Tasks[0].MissingEventIDs) != 0 {
		t.Fatalf("ack was not durable: %d %+v", status, negotiated)
	}
	status, data = f.request(http.MethodPost, "/v1/reconcile", token, protocol.ReconcileRequest{TaskID: task.ID, EventCursor: 0}, nil)
	reconciled := decode[protocol.ReconcileResult](t, data)
	if status != http.StatusOK || reconciled.ServerCursor != cursor || len(reconciled.MissingEvents) != 1 {
		t.Fatalf("reconcile: %d %+v", status, reconciled)
	}
}

func readWebSocketTestMessage(t *testing.T, reader *bufio.Reader) protocol.RealtimeMessage {
	t.Helper()
	first, err := reader.ReadByte()
	if err != nil {
		t.Fatal(err)
	}
	second, err := reader.ReadByte()
	if err != nil {
		t.Fatal(err)
	}
	if first&0x0f != 1 || second&0x80 != 0 {
		t.Fatalf("unexpected websocket frame: %x %x", first, second)
	}
	length := int(second & 0x7f)
	if length == 126 {
		lengthBytes := make([]byte, 2)
		if _, err := io.ReadFull(reader, lengthBytes); err != nil {
			t.Fatal(err)
		}
		length = int(lengthBytes[0])<<8 | int(lengthBytes[1])
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(reader, payload); err != nil {
		t.Fatal(err)
	}
	return decode[protocol.RealtimeMessage](t, payload)
}
