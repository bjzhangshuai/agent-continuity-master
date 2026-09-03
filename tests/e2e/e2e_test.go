package e2e

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/agent-continuity/agent-continuity-master/pkg/client"
	"github.com/agent-continuity/agent-continuity-master/pkg/e2ee"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

type deviceFixture struct {
	device     protocol.Device
	private    ed25519.PrivateKey
	encryption e2ee.DeviceKeyPair
	sequence   uint64
}

func TestFullContinuityJourney(t *testing.T) {
	baseURL := os.Getenv("CONTINUITY_E2E_URL")
	bootstrap := os.Getenv("CONTINUITY_E2E_BOOTSTRAP_TOKEN")
	if baseURL == "" || bootstrap == "" {
		t.Skip("CONTINUITY_E2E_URL and CONTINUITY_E2E_BOOTSTRAP_TOKEN are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	owner, ownerToken := createAccount(t, baseURL, bootstrap, "owner-"+time.Now().Format("150405"))
	api := client.New(baseURL, ownerToken)
	deviceA := registerDevice(t, ctx, api, owner, "device-a")
	deviceB := registerDevice(t, ctx, api, owner, "device-b")
	task, err := api.CreateTask(ctx, "Cross-agent continuity", "reach completion safely", deviceA.device.ID)
	if err != nil {
		t.Fatal(err)
	}
	agentBody, _ := json.Marshal(map[string]any{"name": "E2E reviewer", "provider": "claude", "model": "claude-code", "capabilities": []string{"event_capture", "rehydrate", "artifacts"}})
	agentRaw := request(t, http.MethodPost, baseURL+"/v1/agents", ownerToken, agentBody, map[string]string{"Content-Type": "application/json"}, http.StatusCreated)
	var agent protocol.AgentProfile
	if err := json.Unmarshal(agentRaw, &agent); err != nil {
		t.Fatal(err)
	}
	bindingBody, _ := json.Marshal(map[string]any{"agent_id": agent.ID, "role": "reviewer", "resume_mode": "rehydrate", "capabilities": []string{"event_capture", "rehydrate"}})
	request(t, http.MethodPost, baseURL+"/v1/tasks/"+task.ID+"/agents", ownerToken, bindingBody, map[string]string{"Content-Type": "application/json"}, http.StatusCreated)
	participantsRaw := request(t, http.MethodGet, baseURL+"/v1/tasks/"+task.ID+"/participants", ownerToken, nil, nil, http.StatusOK)
	var participants struct {
		Items []protocol.TaskParticipant `json:"participants"`
	}
	if err := json.Unmarshal(participantsRaw, &participants); err != nil {
		t.Fatal(err)
	}
	agentParticipantFound := false
	for _, participant := range participants.Items {
		if participant.AgentID == agent.ID && participant.Kind == "agent" && participant.AccountID == "" {
			agentParticipantFound = true
		}
	}
	if !agentParticipantFound {
		t.Fatalf("agent participant was not stored independently from the human owner: %+v", participants.Items)
	}
	first := newEvent(t, task, owner, deviceA, nil, "progress.updated", map[string]any{"progress": 60, "agent": "generic"})
	firstResult, err := api.AppendEvent(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	if len(firstResult.TaskHeads) != 1 {
		t.Fatalf("unexpected first heads: %v", firstResult.TaskHeads)
	}
	// Device B works offline. Delivery is deliberately reversed to verify causal gap preservation.
	parent := newEvent(t, task, owner, deviceB, []string{first.EventID}, "decision.recorded", map[string]any{"decision": "switch to Codex"})
	child := newEvent(t, task, owner, deviceB, []string{parent.EventID}, "progress.updated", map[string]any{"progress": 90, "agent": "codex"})
	childResult, err := api.AppendEvent(ctx, child)
	if err != nil {
		t.Fatal(err)
	}
	if len(childResult.Conflicts) == 0 || childResult.Conflicts[0].Kind != "missing_parent" {
		t.Fatalf("missing-parent conflict was not retained: %+v", childResult)
	}
	parentResult, err := api.AppendEvent(ctx, parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(parentResult.TaskHeads) != 1 || parentResult.TaskHeads[0] != child.EventID {
		t.Fatalf("reconciliation produced false head: %v", parentResult.TaskHeads)
	}
	if _, err := api.ResolveConflict(ctx, task.ID, childResult.Conflicts[0].ID, "manual", "parent arrived and causal chain was verified"); err != nil {
		t.Fatal(err)
	}
	duplicate, err := api.AppendEvent(ctx, parent)
	if err != nil || !duplicate.Duplicate {
		t.Fatalf("at-least-once duplicate was not idempotent: %+v %v", duplicate, err)
	}
	taskKey, err := e2ee.GenerateTaskKey()
	if err != nil {
		t.Fatal(err)
	}
	wrapperA, err := e2ee.WrapTaskKey(task.ID, 1, taskKey, deviceA.encryption.PublicKey, deviceA.device.ID)
	if err != nil {
		t.Fatal(err)
	}
	wrapperB, err := e2ee.WrapTaskKey(task.ID, 1, taskKey, deviceB.encryption.PublicKey, deviceB.device.ID)
	if err != nil {
		t.Fatal(err)
	}
	keySet, _ := json.Marshal(protocol.TaskKeySet{TaskID: task.ID, Version: 1, Algorithm: e2ee.Algorithm, CreatedBy: deviceA.device.ID, WrappedKeys: []protocol.WrappedTaskKey{wrapperA, wrapperB}})
	request(t, http.MethodPost, baseURL+"/v1/tasks/"+task.ID+"/keys", ownerToken, keySet, map[string]string{"Content-Type": "application/json"}, http.StatusCreated)
	pull, err := api.SyncPull(ctx, protocol.SyncPullRequest{DeviceID: deviceB.device.ID, Tasks: []protocol.SyncPullCursor{{TaskID: task.ID, After: 0, IncludePayload: true}}})
	if err != nil || len(pull.Batches) != 1 || len(pull.Batches[0].Events) != 3 {
		t.Fatalf("device B cursor pull incomplete: %+v %v", pull, err)
	}
	if err := api.SyncAck(ctx, protocol.SyncAckRequest{DeviceID: deviceB.device.ID, Tasks: []protocol.SyncAck{{TaskID: task.ID, Cursor: pull.Batches[0].NextCursor}}}); err != nil {
		t.Fatal(err)
	}
	reconciled, err := api.Reconcile(ctx, protocol.ReconcileRequest{TaskID: task.ID, EventCursor: 0})
	if err != nil || reconciled.ServerCursor != pull.Batches[0].NextCursor || len(reconciled.ServerHeadIDs) != 1 {
		t.Fatalf("reconciliation incomplete: %+v %v", reconciled, err)
	}
	content := []byte("portable artifact bytes")
	digest := protocol.Digest(content)
	uploadArtifact(t, baseURL, ownerToken, task.ID, "artifact_e2e", content, digest)
	manifests := request(t, http.MethodGet, baseURL+"/v1/tasks/"+task.ID+"/artifacts", ownerToken, nil, nil, http.StatusOK)
	if !bytes.Contains(manifests, []byte(digest)) || bytes.Contains(manifests, content) {
		t.Fatal("Artifact list did not remain manifest-only")
	}
	guest, guestToken := createAccount(t, baseURL, bootstrap, "guest-"+time.Now().Format("150405"))
	_ = guest
	invitationID, _ := protocol.NewID("invite")
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		t.Fatal(err)
	}
	invitation := "qri_" + base64.RawURLEncoding.EncodeToString(tokenBytes)
	qr := protocol.QRInvitation{Version: 1, InvitationID: invitationID, TaskID: task.ID, InviterAccountID: owner.ID, InviterDeviceID: deviceA.device.ID, Permission: protocol.PermissionRead, ExpiresAt: time.Now().UTC().Add(10 * time.Minute), Nonce: "e2e-" + invitationID, TokenDigest: protocol.Digest([]byte(invitation)), KeyExchangeReference: json.RawMessage(`{"task_key_version":1}`), Token: invitation}
	if err := protocol.SignQRInvitation(&qr, deviceA.private); err != nil {
		t.Fatal(err)
	}
	qrRaw, _ := json.Marshal(qr)
	request(t, http.MethodPost, baseURL+"/v1/tasks/"+task.ID+"/qr-invitations", ownerToken, qrRaw, map[string]string{"Content-Type": "application/json"}, http.StatusCreated)
	guestAPI := client.New(baseURL, guestToken)
	if err := guestAPI.AcceptInvitation(ctx, invitation); err != nil {
		t.Fatal(err)
	}
	request(t, http.MethodPost, baseURL+"/v1/tasks/"+task.ID+"/events", guestToken, []byte(`{}`), map[string]string{"Content-Type": "application/json"}, http.StatusForbidden)
	download := downloadArtifact(t, baseURL, guestToken, task.ID, "artifact_e2e")
	if protocol.Digest(download) != digest {
		t.Fatalf("artifact digest mismatch: got %s want %s", protocol.Digest(download), digest)
	}
	pack, err := guestAPI.ContinuityPackage(ctx, task.ID, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if pack.TargetAgent != "codex" || len(pack.Events) != 3 || len(pack.Artifacts) != 1 || len(pack.Branches) == 0 || len(pack.Snapshots) == 0 || pack.Snapshots[0].StateDigest == "" {
		t.Fatalf("incomplete continuity package: events=%d artifacts=%d branches=%d snapshots=%d target=%s", len(pack.Events), len(pack.Artifacts), len(pack.Branches), len(pack.Snapshots), pack.TargetAgent)
	}
	request(t, http.MethodPost, baseURL+"/v1/invitations/accept", guestToken, []byte(`{"token":"`+invitation+`"}`), map[string]string{"Content-Type": "application/json"}, http.StatusGone)
	request(t, http.MethodDelete, baseURL+"/v1/tasks/"+task.ID+"/permissions/"+guest.ID, ownerToken, nil, nil, http.StatusNoContent)
	request(t, http.MethodGet, baseURL+"/v1/tasks/"+task.ID, guestToken, nil, nil, http.StatusForbidden)
	request(t, http.MethodDelete, baseURL+"/v1/devices/"+deviceA.device.ID, ownerToken, nil, nil, http.StatusNoContent)
	revoked := newEvent(t, task, owner, deviceA, []string{child.EventID}, "task.completed", map[string]any{"done": true})
	data, _ := json.Marshal(revoked)
	request(t, http.MethodPost, baseURL+"/v1/tasks/"+task.ID+"/events", ownerToken, data, map[string]string{"Content-Type": "application/json"}, http.StatusForbidden)
}

func createAccount(t *testing.T, baseURL, bootstrap, name string) (protocol.Account, string) {
	data, _ := json.Marshal(map[string]string{"name": name})
	body := request(t, http.MethodPost, baseURL+"/v1/accounts", "", data, map[string]string{"Content-Type": "application/json", "X-Bootstrap-Token": bootstrap}, http.StatusCreated)
	var account protocol.Account
	if err := json.Unmarshal(body, &account); err != nil {
		t.Fatal(err)
	}
	data, _ = json.Marshal(map[string]any{"account_id": account.ID, "subject": "e2e", "ttl_seconds": 900})
	body = request(t, http.MethodPost, baseURL+"/v1/auth/token", "", data, map[string]string{"Content-Type": "application/json", "X-Bootstrap-Token": bootstrap}, http.StatusOK)
	var tokenResponse struct {
		Token string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &tokenResponse); err != nil {
		t.Fatal(err)
	}
	return account, tokenResponse.Token
}
func registerDevice(t *testing.T, ctx context.Context, api *client.Client, account protocol.Account, name string) *deviceFixture {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := e2ee.GenerateDeviceKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	device, err := api.RegisterDevice(ctx, "", name, base64.RawURLEncoding.EncodeToString(publicKey), pair.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if device.AccountID != account.ID {
		t.Fatalf("device account mismatch")
	}
	return &deviceFixture{device: device, private: privateKey, encryption: pair}
}
func newEvent(t *testing.T, task protocol.Task, account protocol.Account, device *deviceFixture, parents []string, eventType string, payloadValue any) protocol.Event {
	device.sequence++
	payload, err := json.Marshal(payloadValue)
	if err != nil {
		t.Fatal(err)
	}
	id, err := protocol.NewID("event")
	if err != nil {
		t.Fatal(err)
	}
	value := protocol.Event{EventID: id, TaskID: task.ID, BranchID: "main", AccountID: account.ID, DeviceID: device.device.ID, ParentEventIDs: parents, LogicalTime: protocol.HLC{WallTimeMS: time.Now().UnixMilli(), Counter: uint32(device.sequence), Node: device.device.ID}, DeviceSequence: device.sequence, Type: eventType, SchemaVersion: protocol.SchemaVersion, ContentType: "application/json", ContentDigest: protocol.Digest(payload), Payload: payload, CreatedAtClient: time.Now().UTC(), IdempotencyKey: id}
	if err := protocol.SignEvent(&value, device.private); err != nil {
		t.Fatal(err)
	}
	return value
}
func uploadArtifact(t *testing.T, baseURL, token, taskID, artifactID string, content []byte, digest string) {
	t.Helper()
	chunkSize := 8
	manifest, _ := json.Marshal(map[string]any{"artifact_id": artifactID, "filename": "artifact.bin", "content_type": "application/octet-stream", "total_size": len(content), "chunk_size": chunkSize, "content_hash": digest})
	created := request(t, http.MethodPost, fmt.Sprintf("%s/v1/tasks/%s/artifact-uploads", baseURL, taskID), token, manifest, map[string]string{"Content-Type": "application/json"}, http.StatusCreated)
	var status protocol.ArtifactUploadStatus
	if err := json.Unmarshal(created, &status); err != nil {
		t.Fatal(err)
	}
	order := []int{status.Session.TotalChunks - 1, 0}
	if status.Session.TotalChunks == 1 {
		order = []int{0}
	}
	for _, index := range order {
		start := index * chunkSize
		end := min(start+chunkSize, len(content))
		chunk := content[start:end]
		request(t, http.MethodPut, fmt.Sprintf("%s/v1/artifact-uploads/%s/chunks/%d", baseURL, status.Session.ID, index), token, chunk, map[string]string{"X-Content-SHA256": protocol.Digest(chunk)}, http.StatusOK)
	}
	if status.Session.TotalChunks > 2 {
		progress := request(t, http.MethodGet, fmt.Sprintf("%s/v1/artifact-uploads/%s", baseURL, status.Session.ID), token, nil, nil, http.StatusOK)
		if !bytes.Contains(progress, []byte(`"missing_chunks":[1]`)) {
			t.Fatalf("resumable upload did not report the interrupted middle chunk: %s", progress)
		}
		for index := 1; index < status.Session.TotalChunks-1; index++ {
			start := index * chunkSize
			end := min(start+chunkSize, len(content))
			chunk := content[start:end]
			request(t, http.MethodPut, fmt.Sprintf("%s/v1/artifact-uploads/%s/chunks/%d", baseURL, status.Session.ID, index), token, chunk, map[string]string{"X-Content-SHA256": protocol.Digest(chunk)}, http.StatusOK)
		}
	}
	request(t, http.MethodPost, fmt.Sprintf("%s/v1/artifact-uploads/%s/complete", baseURL, status.Session.ID), token, nil, nil, http.StatusCreated)
}
func downloadArtifact(t *testing.T, baseURL, token, taskID, artifactID string) []byte {
	return request(t, http.MethodGet, fmt.Sprintf("%s/v1/tasks/%s/artifacts/%s", baseURL, taskID, artifactID), token, nil, nil, http.StatusOK)
}
func request(t *testing.T, method, url, token string, body []byte, headers map[string]string, want int) []byte {
	t.Helper()
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != want {
		t.Fatalf("%s %s: got %d want %d: %s", method, url, resp.StatusCode, want, strings.TrimSpace(string(data)))
	}
	return data
}
