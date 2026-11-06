package httpapi

import (
	"context"
	"encoding/json"
	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/agent-continuity/agent-continuity-master/internal/kms"
	"github.com/agent-continuity/agent-continuity-master/internal/operations"
	"github.com/agent-continuity/agent-continuity-master/internal/orchestration"
	"github.com/agent-continuity/agent-continuity-master/internal/secrets"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestMCPModernMetadataNegotiationAndLegacy(t *testing.T) {
	f := newFixture(t)
	_, token := f.account("mcp-user")
	params := map[string]any{"_meta": map[string]any{"io.modelcontextprotocol/protocolVersion": "2026-07-28", "io.modelcontextprotocol/clientInfo": map[string]string{"name": "contract-client", "version": "1"}, "io.modelcontextprotocol/clientCapabilities": map[string]any{}}}
	headers := map[string]string{"MCP-Protocol-Version": "2026-07-28", "Mcp-Method": "server/discover", "Accept": "application/json, text/event-stream"}
	call := func(method string, p any, h map[string]string) (int, rpcResponse) {
		status, data := f.request("POST", "/v1/mcp", token, map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": p}, h)
		return status, decode[rpcResponse](t, data)
	}
	status, out := call("server/discover", params, headers)
	if status != 200 || out.Error != nil || !strings.Contains(string(operations.Encode(out.Result)), `"resultType":"complete"`) {
		t.Fatalf("discover: %d %+v", status, out)
	}
	headers["Mcp-Method"] = "tools/list"
	status, out = call("server/discover", params, headers)
	if status != 400 || out.Error == nil || out.Error.Code != -32020 {
		t.Fatal("header-body mismatch accepted")
	}
	headers["Mcp-Method"] = "tools/list"
	status, out = call("tools/list", params, headers)
	if status != 200 || out.Error != nil {
		t.Fatal("modern tools", status, out)
	}
	headers["Origin"] = "https://evil.example"
	status, _ = call("tools/list", params, headers)
	if status != 403 {
		t.Fatal("cross-origin accepted")
	}
	delete(headers, "Origin")
	headers["Mcp-Method"] = "tools/call"
	headers["Mcp-Name"] = "task.list"
	params["name"] = "task.list"
	params["arguments"] = map[string]any{"limit": 0}
	status, out = call("tools/call", params, headers)
	if out.Error == nil || out.Error.Code != -32602 {
		t.Fatal("invalid schema accepted")
	}
	delete(params, "name")
	delete(params, "arguments")
	status, out = call("initialize", map[string]any{"protocolVersion": "2025-11-25"}, nil)
	if status != 200 || out.Error != nil || !strings.Contains(string(operations.Encode(out.Result)), "2025-11-25") {
		t.Fatal("legacy handshake failed")
	}
	status, raw := f.request("POST", "/v1/mcp", token, []byte(`{"jsonrpc":`), nil)
	if status != 400 || decode[rpcResponse](t, raw).Error.Code != -32700 {
		t.Fatal("parse error incorrect")
	}
	for _, invalid := range []string{`[]`, `null`, `123`, `{"jsonrpc":2,"id":1,"method":"ping"}`} {
		status, raw = f.request("POST", "/v1/mcp", token, []byte(invalid), nil)
		if status != 400 || decode[rpcResponse](t, raw).Error.Code != -32600 {
			t.Fatal("valid JSON but invalid RPC must return invalid request", invalid, string(raw))
		}
	}
	status, raw = f.request("POST", "/v1/mcp", token, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}, nil)
	if status != 202 || len(raw) != 0 {
		t.Fatal("notification response", status, string(raw))
	}
}
func TestA2AStandardTaskLifecycleHistoryAndIsolation(t *testing.T) {
	f := newFixtureWithOptions(t, WithOrchestration(map[string]orchestration.Executor{"default": orchestration.DemoAgent{Name: "test"}}))
	_, token := f.account("a2a-owner")
	_, other := f.account("a2a-other")
	rpc := func(token, method string, p any) rpcResponse {
		status, raw := f.request("POST", "/a2a/v1", token, map[string]any{"jsonrpc": "2.0", "id": "request", "method": method, "params": p}, map[string]string{"A2A-Version": "1.0"})
		if status != 200 {
			t.Fatalf("HTTP %d %s", status, raw)
		}
		return decode[rpcResponse](t, raw)
	}
	q := map[string]any{"message": map[string]any{"messageId": "m-1", "role": "ROLE_USER", "parts": []any{map[string]string{"text": "test handoff"}}, "contextId": "conversation-1"}, "configuration": map[string]any{"returnImmediately": true, "historyLength": 0}}
	out := rpc(token, "SendMessage", q)
	if out.Error != nil {
		t.Fatal(out.Error)
	}
	result := out.Result.(map[string]any)
	task := result["task"].(map[string]any)
	id := task["id"].(string)
	if _, ok := task["history"]; ok {
		t.Fatal("historyLength zero ignored")
	}
	duplicate := rpc(token, "SendMessage", q)
	if duplicate.Result.(map[string]any)["task"].(map[string]any)["id"] != id {
		t.Fatal("duplicate task")
	}
	if e := f.api.orchestrator.Tick(context.Background()); e != nil {
		t.Fatal(e)
	}
	out = rpc(token, "GetTask", map[string]any{"id": id, "historyLength": 1})
	task = out.Result.(map[string]any)
	if task["status"].(map[string]any)["state"] != "TASK_STATE_COMPLETED" || len(task["history"].([]any)) != 1 {
		t.Fatal("task result", task)
	}
	out = rpc(other, "GetTask", map[string]string{"id": id})
	if out.Error == nil || out.Error.Code != -32001 {
		t.Fatal("cross-tenant task leak")
	}
	out = rpc(token, "CancelTask", map[string]string{"id": id})
	if out.Error == nil || out.Error.Code != -32002 {
		t.Fatal("terminal task cancellation")
	}
	out = rpc(token, "ListTasks", map[string]any{"contextId": "conversation-1", "pageSize": 1})
	tasks := out.Result.(map[string]any)["tasks"].([]any)
	if len(tasks) != 1 {
		t.Fatal("task list")
	}
	if _, ok := tasks[0].(map[string]any)["artifacts"]; ok {
		t.Fatal("includeArtifacts default ignored")
	}
	out = rpc(token, "SendStreamingMessage", q)
	if out.Error == nil || out.Error.Code != -32004 {
		t.Fatal("unadvertised streaming capability")
	}
	status, raw := f.request("GET", "/.well-known/agent-card.json", "", nil, nil)
	if status != 200 || !strings.Contains(string(raw), `"protocolVersion":"1.0"`) {
		t.Fatal("agent card", string(raw))
	}
}
func TestTenantKMSVaultRewrapAndNoPlaintextAtRest(t *testing.T) {
	manager, e := kms.Build(`{"current":"aws-v1","simulation":true,"keys":[{"id":"aws-v1","cloud":"aws","key_id":"key1"},{"id":"azure-v2","cloud":"azure","key_id":"key2"}]}`, []byte(strings.Repeat("k", 32)))
	if e != nil {
		t.Fatal(e)
	}
	cipher, e := secrets.New([]byte(strings.Repeat("k", 32)))
	if e != nil {
		t.Fatal(e)
	}
	cipher.WithEnvelope(manager)
	f := newFixtureWithOptions(t, WithSecretCipher(cipher), WithKMS(manager))
	account, token := f.account("vault-owner")
	_, other := f.account("vault-other")
	status, raw := f.request("PUT", "/v1/security/secrets/agent-token", token, map[string]any{"value": "private-sample", "expected_version": 0}, nil)
	if status != 200 {
		t.Fatal(status, string(raw))
	}
	o, e := f.api.store.GetOperation(context.Background(), "vault", operations.ID("vault", account.ID, "agent-token"))
	if e != nil || strings.Contains(string(o.Data), "private-sample") {
		t.Fatal("plaintext persisted")
	}
	status, _ = f.request("GET", "/v1/security/secrets/agent-token", other, nil, nil)
	if status != 404 {
		t.Fatal("vault tenant leak")
	}
	status, _ = f.request("PUT", "/v1/security/secrets/agent-token", token, map[string]any{"value": "overwrite", "expected_version": 0}, nil)
	if status != 409 {
		t.Fatal("stale vault write")
	}
	manager.Config.Current = "azure-v2"
	status, raw = f.request("POST", "/v1/security/secrets/agent-token/rewrap", token, nil, nil)
	if status != 200 {
		t.Fatal(status, string(raw))
	}
	delete(manager.Wrappers, "aws-v1")
	status, raw = f.request("GET", "/v1/security/secrets/agent-token", token, nil, nil)
	if status != 200 || !strings.Contains(string(raw), "private-sample") {
		t.Fatal("rewrapped value lost")
	}
	delete(manager.Wrappers, "azure-v2")
	status, _ = f.request("GET", "/v1/security/secrets/agent-token", token, nil, nil)
	if status != 503 {
		t.Fatal("revocation not enforced")
	}
	readOnly, e := f.api.tokens.IssueScoped(account.ID, "restricted", []string{"tasks:read"}, "", 3600000000000)
	if e != nil {
		t.Fatal(e)
	}
	status, _ = f.request(http.MethodGet, "/v1/security/secrets/agent-token", readOnly, nil, nil)
	if status != 403 {
		t.Fatal("task token can read secrets")
	}
}

type bearerRoundTripper struct{ token string }

func (b bearerRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	clone := r.Clone(r.Context())
	clone.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(clone)
}
func TestA2AOfficialSDKInteroperability(t *testing.T) {
	f := newFixtureWithOptions(t, WithOrchestration(map[string]orchestration.Executor{"default": orchestration.DemoAgent{Name: "official-sdk"}}))
	_, token := f.account("official-sdk-client")
	status, raw := f.request("GET", "/.well-known/agent-card.json", "", nil, nil)
	if status != 200 {
		t.Fatal(status)
	}
	var card a2a.AgentCard
	if e := json.Unmarshal(raw, &card); e != nil {
		t.Fatal("official AgentCard decoder", e)
	}
	if len(card.SecurityRequirements) == 0 {
		t.Fatal("security requirements not interoperable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go f.api.orchestrator.Run(ctx, f.api.logger)
	official, e := a2aclient.NewFromCard(ctx, &card, a2aclient.WithJSONRPCTransport(&http.Client{Transport: bearerRoundTripper{token: token}}))
	if e != nil {
		t.Fatal(e)
	}
	result, e := official.SendMessage(ctx, &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("official SDK roundtrip"))})
	if e != nil {
		t.Fatal("official SendMessage", e)
	}
	task, ok := result.(*a2a.Task)
	if !ok || task.Status.State != a2a.TaskStateCompleted {
		t.Fatalf("official task result: %+v", result)
	}
	got, e := official.GetTask(ctx, &a2a.GetTaskRequest{ID: task.ID})
	if e != nil || got.ID != task.ID {
		t.Fatal("official GetTask", e)
	}
	listed, e := official.ListTasks(ctx, &a2a.ListTasksRequest{PageSize: 1})
	if e != nil || len(listed.Tasks) != 1 {
		t.Fatal("official ListTasks", e)
	}
}

func TestContinuityReportsBoundedWithoutDestroyingFullOutput(t *testing.T) {
	f := newFixture(t)
	owner, token := f.account("report-owner")
	status, raw := f.request("POST", "/v1/tasks", token, map[string]string{"title": "large report", "objective": "bounded portable context"}, nil)
	if status != 201 {
		t.Fatal(status, string(raw))
	}
	task := decode[protocol.Task](t, raw)
	text := strings.Repeat("中文", 10000)
	run := orchestration.Run{ID: "bounded-report", AccountID: owner.ID, Status: "completed", Request: orchestration.Request{TaskID: task.ID, Steps: []orchestration.Step{{ID: "write", Agent: "writer"}}}, Steps: []orchestration.StepState{{ID: "write", Status: "completed", Output: text}}, Updated: time.Now().UTC()}
	err := f.api.store.SaveOperation(context.Background(), operations.Record{Kind: "run", ID: run.ID, Owner: owner.ID, Status: run.Status, Updated: run.Updated, Resource: task.ID, Data: operations.Encode(run)}, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	status, raw = f.request("GET", "/v1/tasks/"+task.ID+"/continuity-package", token, nil, nil)
	if status != 200 {
		t.Fatal(status, string(raw))
	}
	pack := decode[protocol.ContinuityPackage](t, raw)
	if len(pack.ExecutionReports) != 1 {
		t.Fatal("report missing")
	}
	report := pack.ExecutionReports[0]
	if !report.OutputTruncated || len(report.Output) > 16<<10 || !utf8.ValidString(report.Output) {
		t.Fatal("unbounded or corrupt portable output")
	}
	original, err := f.api.store.GetOperation(context.Background(), "run", run.ID)
	if err != nil || !strings.Contains(string(original.Data), text) {
		t.Fatal("full output lost", err)
	}
}
