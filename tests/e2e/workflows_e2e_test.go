package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/agent-continuity/agent-continuity-master/internal/commerce"
	"github.com/agent-continuity/agent-continuity-master/internal/operations"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
	"os"
	"testing"
	"time"
)

func TestWorkflowCommerceKMSJourney(t *testing.T) {
	base, bootstrap, merchant := os.Getenv("CONTINUITY_E2E_URL"), os.Getenv("CONTINUITY_E2E_BOOTSTRAP_TOKEN"), os.Getenv("CONTINUITY_E2E_TREASURY_TOKEN")
	if os.Getenv("CONTINUITY_E2E_WORKFLOWS") != "1" {
		t.Skip("requires isolated server with demo workflows and KMS")
	}
	if base != "http://127.0.0.1:18081" || bootstrap == "" || merchant == "" {
		t.Fatal("workflow acceptance requires isolated local :18081 server and merchant credential")
	}
	_, buyer := createAccount(t, base, bootstrap, fmt.Sprintf("workflow buyer %d", time.Now().UnixNano()))
	_, seller := createAccount(t, base, bootstrap, fmt.Sprintf("workflow seller %d", time.Now().UnixNano()))
	call := func(method, path, token string, body any, key string, status int) []byte {
		t.Helper()
		var raw []byte
		if body != nil {
			raw, _ = json.Marshal(body)
		}
		return request(t, method, base+path, token, raw, map[string]string{"Content-Type": "application/json", "Idempotency-Key": key}, status)
	}
	var shared protocol.Task
	_ = json.Unmarshal(call("POST", "/v1/tasks", buyer, map[string]string{"title": "DAG cross-device handoff", "objective": "share orchestrator results"}, "", 201), &shared)

	var run struct {
		ID     string `json:"run_id"`
		Status string `json:"status"`
		Steps  []struct {
			Status string `json:"status"`
			Output string `json:"output"`
		} `json:"steps"`
	}
	raw := call("POST", "/v1/orchestration/runs", buyer, map[string]any{"task_id": shared.ID, "steps": []any{map[string]any{"id": "research", "agent": "researcher", "input": "Prepare a continuity plan", "cost_limit": 1}, map[string]any{"id": "review", "agent": "reviewer", "needs": []string{"research"}, "input": "Review the plan", "cost_limit": 1}, map[string]any{"id": "write", "agent": "writer", "needs": []string{"review"}, "input": "Publish a summary", "cost_limit": 1}}, "budget": 3, "require_approval": true}, "e2e-orchestration", 201)
	_ = json.Unmarshal(raw, &run)
	if run.Status != "approval_required" {
		t.Fatal("approval missing")
	}
	call("GET", "/v1/orchestration/runs/"+run.ID, seller, nil, "", 404)
	call("POST", "/v1/orchestration/runs/"+run.ID+"/approve", buyer, nil, "", 200)
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		raw = call("GET", "/v1/orchestration/runs/"+run.ID, buyer, nil, "", 200)
		_ = json.Unmarshal(raw, &run)
		if run.Status == "completed" {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if run.Status != "completed" || len(run.Steps) != 3 || !bytes.Contains([]byte(run.Steps[2].Output), []byte("Prepare a continuity plan")) {
		t.Fatal("DAG did not propagate results", string(raw))
	}
	resumed := call("GET", "/v1/tasks/"+shared.ID+"/continuity-package?agent=codebuddy", buyer, nil, "", 200)
	var portable protocol.ContinuityPackage
	if json.Unmarshal(resumed, &portable) != nil || len(portable.ExecutionReports) != 3 || !bytes.Contains([]byte(portable.ExecutionReports[2].Output), []byte("Prepare a continuity plan")) {
		t.Fatal("cross-device execution reports missing", string(resumed))
	}

	call("PUT", "/v1/security/secrets/e2e-agent", buyer, map[string]any{"value": "synthetic-test-value", "expected_version": 0}, "", 200)
	call("GET", "/v1/security/secrets/e2e-agent", seller, nil, "", 404)
	call("POST", "/v1/security/secrets/e2e-agent/rewrap", buyer, nil, "", 200)
	raw = call("GET", "/v1/security/secrets/e2e-agent", buyer, nil, "", 200)
	if !bytes.Contains(raw, []byte("synthetic-test-value")) {
		t.Fatal("KMS roundtrip")
	}
	var ba protocol.BillingAccount
	_ = json.Unmarshal(call("POST", "/v1/billing/accounts", buyer, map[string]string{"plan": "pro", "currency": "CNY"}, "", 201), &ba)
	var listing protocol.MarketplaceListing
	_ = json.Unmarshal(call("POST", "/v1/marketplace/listings", seller, map[string]any{"kind": "capability", "name": "E2E review service", "version": "1.0.0", "description": "synthetic review", "manifest": map[string]string{"protocol": "a2a/1.0"}, "price_minor": 10000, "currency": "CNY", "commission_basis_points": 1000, "status": "published"}, "", 201), &listing)
	var tx protocol.MarketplaceTransaction
	_ = json.Unmarshal(call("POST", "/v1/marketplace/transactions", buyer, map[string]any{"listing_id": listing.ID, "quantity": 1, "idempotency_key": "e2e-market-transaction"}, "", 201), &tx)
	call("POST", "/v1/marketplace/transactions/"+tx.ID+"/authorize", buyer, nil, "", 200)
	call("POST", "/v1/marketplace/transactions/"+tx.ID+"/complete", seller, nil, "", 402)
	var checkout struct {
		Bill      protocol.BillingDocument `json:"bill"`
		PaymentID string                   `json:"payment_id"`
		Link      string                   `json:"payment_link"`
	}
	_ = json.Unmarshal(call("POST", "/v1/marketplace/transactions/"+tx.ID+"/checkout", buyer, map[string]string{"billing_account_id": ba.ID, "provider": "demo"}, "e2e-market-payment", 201), &checkout)
	if checkout.Link == "" || checkout.PaymentID == "" {
		t.Fatal("checkout link missing")
	}
	call("POST", "/v1/billing/invoices/"+checkout.Bill.Invoice.ID+"/payments/"+checkout.PaymentID+"/simulate", buyer, map[string]string{"outcome": "succeeded"}, "", 200)
	call("POST", "/v1/marketplace/transactions/"+tx.ID+"/complete", seller, nil, "", 200)
	call("POST", "/v1/marketplace/listings/"+listing.ID+"/install", buyer, nil, "", 201)
	call("POST", "/v1/marketplace/transactions/"+tx.ID+"/settlement", buyer, nil, "", 201)
	settlement := "/v1/commerce/settlements/" + tx.ID
	raw = call("POST", settlement+"/reconcile", seller, nil, "", 200)
	var ledger commerce.Settlement
	_ = json.Unmarshal(raw, &ledger)
	if ledger.Available != 9000 {
		t.Fatal("seller split", string(raw))
	}
	call("POST", "/v1/commerce/recipients", seller, map[string]string{"provider_account_token": "e2e-sim-bank", "kind": "bank"}, "", 201)
	call("POST", settlement+"/withdraw", seller, map[string]any{"amount_minor": 6000, "destination": "e2e-sim-bank"}, "e2e-seller-payout", 202)
	raw = call("POST", settlement+"/reconcile", seller, nil, "", 200)
	_ = json.Unmarshal(raw, &ledger)
	if len(ledger.Payouts) != 1 || ledger.Payouts[0].Result.Status != "succeeded" || ledger.Available != 3000 {
		t.Fatal("payout not confirmed", string(raw))
	}
	var fiscal struct {
		Effect struct {
			ID string `json:"idempotency_key"`
		} `json:"request"`
	}
	_ = json.Unmarshal(call("POST", "/v1/billing/invoices/"+checkout.Bill.Invoice.ID+"/fiscal", buyer, map[string]string{"buyer_name": "E2E模拟公司", "tax_id": "91310000TEST"}, "", 202), &fiscal)
	call("POST", "/v1/commerce/fiscal/"+fiscal.Effect.ID+"/reconcile", buyer, nil, "", 200)
	document := call("GET", "/v1/commerce/fiscal/"+fiscal.Effect.ID+"/document", buyer, nil, "", 200)
	if !bytes.Contains(document, []byte(`"legally_valid_tax_invoice":false`)) {
		t.Fatal("simulation watermark missing")
	}
	var bill protocol.BillingDocument
	_ = json.Unmarshal(call("POST", "/v1/billing/invoices/"+checkout.Bill.Invoice.ID+"/refunds", buyer, map[string]any{"payment_id": checkout.PaymentID, "amount_minor": 5000, "reason": "E2E partial refund"}, "e2e-market-refund", 200), &bill)
	if len(bill.Refunds) != 1 {
		t.Fatal("refund request", bill)
	}
	rid := bill.Refunds[0].ID
	op := "/v1/treasury/receivables/" + bill.Invoice.ID + "/refunds/" + rid
	call("POST", op+"/decision", merchant, map[string]string{"decision": "approved", "reason": "simulated acceptance"}, "", 200)
	call("POST", op+"/submit", merchant, nil, "", 200)
	call("POST", op+"/simulate", merchant, map[string]string{"outcome": "succeeded"}, "", 200)
	raw = call("POST", settlement+"/reconcile", seller, nil, "", 200)
	_ = json.Unmarshal(raw, &ledger)
	if ledger.Refunded != 5000 || ledger.Debt != 1500 || len(ledger.Reversals) != 1 {
		t.Fatal("refund split reversal", string(raw))
	}
	call("POST", "/v1/commerce/fiscal/"+fiscal.Effect.ID+"/reconcile", buyer, nil, "", 200)
	raw = call("GET", "/v1/billing/invoices/"+checkout.Bill.Invoice.ID+"/fiscal", buyer, nil, "", 200)
	if !bytes.Contains(raw, []byte(rid)) {
		t.Fatal("credit invoice missing")
	}
	var fiscalRows struct {
		Items []operations.Record `json:"items"`
	}
	_ = json.Unmarshal(raw, &fiscalRows)
	issuedCredit := false
	for _, row := range fiscalRows.Items {
		var credit commerce.Fiscal
		_ = json.Unmarshal(row.Data, &credit)
		if credit.RefundID != rid {
			continue
		}
		call("POST", "/v1/commerce/fiscal/"+row.ID+"/reconcile", buyer, nil, "", 200)
		document = call("GET", "/v1/commerce/fiscal/"+row.ID+"/document", buyer, nil, "", 200)
		if !bytes.Contains(document, []byte(`"legally_valid_tax_invoice":false`)) {
			t.Fatal("credit missing simulation watermark")
		}
		issuedCredit = true
	}
	if !issuedCredit {
		t.Fatal("refund credit was not issued and downloaded")
	}
	call("GET", "/v1/commerce/fiscal/"+fiscal.Effect.ID, seller, nil, "", 404)
	t.Log("PASS: DAG approval/execution/dependency results, tenant-bound KMS, marketplace checkout/payment, split/withdrawal, partial refund/debt/reversal, simulated fiscal/credit documents; no money moved")
}

func TestStandardProtocolJourney(t *testing.T) {
	if os.Getenv("CONTINUITY_E2E_WORKFLOWS") != "1" {
		t.Skip("isolated protocol fixture required")
	}
	base, bootstrap := os.Getenv("CONTINUITY_E2E_URL"), os.Getenv("CONTINUITY_E2E_BOOTSTRAP_TOKEN")
	_, token := createAccount(t, base, bootstrap, "standards acceptance")
	meta := map[string]any{"io.modelcontextprotocol/protocolVersion": "2026-07-28", "io.modelcontextprotocol/clientInfo": map[string]string{"name": "independent-http-e2e", "version": "1"}, "io.modelcontextprotocol/clientCapabilities": map[string]any{}}
	rpc := func(path, method string, params any, headers map[string]string) map[string]any {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "rpc-e2e", "method": method, "params": params})
		headers["Content-Type"] = "application/json"
		raw := request(t, "POST", base+path, token, b, headers, 200)
		var result map[string]any
		_ = json.Unmarshal(raw, &result)
		if result["error"] != nil {
			t.Fatalf("RPC %s: %s", method, raw)
		}
		return result["result"].(map[string]any)
	}
	discovered := rpc("/v1/mcp", "server/discover", map[string]any{"_meta": meta}, map[string]string{"MCP-Protocol-Version": "2026-07-28", "Mcp-Method": "server/discover"})
	if discovered["resultType"] != "complete" {
		t.Fatal("modern discovery")
	}
	rpc("/v1/mcp", "tools/call", map[string]any{"_meta": meta, "name": "task.list", "arguments": map[string]int{"limit": 5}}, map[string]string{"MCP-Protocol-Version": "2026-07-28", "Mcp-Method": "tools/call", "Mcp-Name": "task.list"})
	result := rpc("/a2a/v1", "SendMessage", map[string]any{"message": map[string]any{"messageId": "a2a-e2e-message", "role": "ROLE_USER", "parts": []any{map[string]string{"text": "Return a continuity summary"}}}}, map[string]string{"A2A-Version": "1.0"})
	task := result["task"].(map[string]any)
	if task["status"].(map[string]any)["state"] != "TASK_STATE_COMPLETED" {
		t.Fatal("blocking A2A returned early")
	}
	result = rpc("/a2a/v1", "ListTasks", map[string]any{"pageSize": 10, "includeArtifacts": true}, map[string]string{"A2A-Version": "1.0"})
	if result["totalSize"].(float64) != 1 {
		t.Fatal("A2A task listing")
	}
	t.Log("PASS: MCP 2026-07-28 discovery and tools over HTTP; A2A 1.0 blocking SendMessage and task listing")
}
