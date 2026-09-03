package httpapi

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

func TestOrganizationTeamAndDenyPolicyEnforcement(t *testing.T) {
	f := newFixture(t)
	owner, ownerToken := f.account("organization-owner")
	member, memberToken := f.account("organization-member")
	status, data := f.request(http.MethodPost, "/v1/organizations", ownerToken, map[string]any{"name": "Continuity Lab", "slug": "continuity-lab", "plan": "team", "key_provider": "platform"}, nil)
	if status != http.StatusCreated {
		t.Fatalf("create organization: %d %s", status, data)
	}
	organization := decode[protocol.Organization](t, data)
	status, data = f.request(http.MethodPut, "/v1/organizations/"+organization.ID+"/members/"+member.ID, ownerToken, map[string]string{"role": "member"}, nil)
	if status != http.StatusOK {
		t.Fatalf("add member: %d %s", status, data)
	}
	status, data = f.request(http.MethodGet, "/v1/organizations/"+organization.ID, memberToken, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("member cannot read organization: %d %s", status, data)
	}
	status, data = f.request(http.MethodPost, "/v1/organizations/"+organization.ID+"/teams", ownerToken, map[string]string{"name": "Runtime", "slug": "runtime"}, nil)
	if status != http.StatusCreated {
		t.Fatalf("create team: %d %s", status, data)
	}
	team := decode[protocol.Team](t, data)
	status, data = f.request(http.MethodPut, "/v1/organizations/"+organization.ID+"/teams/"+team.ID+"/members/"+member.ID, ownerToken, map[string]string{"role": "viewer"}, nil)
	if status != http.StatusOK {
		t.Fatalf("add team viewer: %d %s", status, data)
	}
	status, data = f.request(http.MethodPost, "/v1/tasks", ownerToken, map[string]string{"title": "Team task", "objective": "enforce team access", "organization_id": organization.ID, "team_id": team.ID}, nil)
	if status != http.StatusCreated {
		t.Fatalf("create team task: %d %s", status, data)
	}
	task := decode[protocol.Task](t, data)
	status, data = f.request(http.MethodGet, "/v1/tasks/"+task.ID, memberToken, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("team viewer cannot read: %d %s", status, data)
	}
	status, _ = f.request(http.MethodPost, "/v1/tasks/"+task.ID+"/events", memberToken, map[string]string{}, nil)
	if status != http.StatusForbidden {
		t.Fatalf("team viewer write expected 403, got %d", status)
	}
	policy := map[string]any{"name": "deny one reader", "effect": "deny", "subjects": []string{"account:" + member.ID}, "actions": []string{"task.read"}, "resource_type": "task", "priority": 100}
	status, data = f.request(http.MethodPost, "/v1/organizations/"+organization.ID+"/policies", ownerToken, policy, nil)
	if status != http.StatusCreated {
		t.Fatalf("create deny policy: %d %s", status, data)
	}
	status, _ = f.request(http.MethodGet, "/v1/tasks/"+task.ID, memberToken, nil, nil)
	if status != http.StatusForbidden {
		t.Fatalf("explicit deny expected 403, got %d", status)
	}
	status, data = f.request(http.MethodGet, "/v1/tasks", memberToken, nil, nil)
	if status != http.StatusOK || bytes.Contains(data, []byte(task.ID)) {
		t.Fatalf("denied task leaked through index: %d %s", status, data)
	}
	_ = owner
}

func TestDeploymentDataResidencyIsEnforced(t *testing.T) {
	f := newFixtureWithOptions(t, WithDataResidency("eu-central"))
	_, token := f.account("residency-owner")
	status, data := f.request(http.MethodPost, "/v1/organizations", token, map[string]any{"name": "EU Org", "slug": "eu-org", "plan": "enterprise", "key_provider": "byok"}, nil)
	if status != http.StatusCreated {
		t.Fatalf("create deployment-local organization: %d %s", status, data)
	}
	organization := decode[protocol.Organization](t, data)
	if organization.DataResidency != "eu-central" || organization.KeyProvider != "byok" {
		t.Fatalf("residency or BYOK contract lost: %+v", organization)
	}
	status, _ = f.request(http.MethodPost, "/v1/organizations", token, map[string]any{"name": "US Org", "slug": "us-org", "data_residency": "us-east", "key_provider": "platform"}, nil)
	if status != http.StatusConflict {
		t.Fatalf("cross-residency organization expected 409, got %d", status)
	}
}

func TestArtifactACLFiltersDownloadsAndIndexes(t *testing.T) {
	f := newFixture(t)
	owner, ownerToken := f.account("artifact-owner")
	status, data := f.request(http.MethodPost, "/v1/tasks", ownerToken, map[string]string{"title": "Restricted artifact", "objective": "test ACL"}, nil)
	if status != http.StatusCreated {
		t.Fatal(string(data))
	}
	task := decode[protocol.Task](t, data)
	content := []byte("restricted bytes")
	status, data = f.request(http.MethodPut, "/v1/tasks/"+task.ID+"/artifacts/restricted", ownerToken, content, map[string]string{"X-Content-SHA256": protocol.Digest(content), "X-Filename": "secret.txt"})
	if status != http.StatusCreated {
		t.Fatalf("upload: %d %s", status, data)
	}
	reader, readerToken := f.account("artifact-reader")
	_, otherToken := f.account("artifact-other")
	for _, targetToken := range []string{readerToken, otherToken} {
		status, data = f.request(http.MethodPost, "/v1/tasks/"+task.ID+"/invitations", ownerToken, map[string]any{"permission": "read", "ttl_seconds": 600}, nil)
		invite := decode[struct {
			Token string `json:"token"`
		}](t, data)
		status, data = f.request(http.MethodPost, "/v1/invitations/accept", targetToken, map[string]string{"token": invite.Token}, nil)
		if status != http.StatusOK {
			t.Fatalf("accept invite: %d %s", status, data)
		}
	}
	status, data = f.request(http.MethodPut, "/v1/tasks/"+task.ID+"/artifacts/restricted/permissions/"+reader.ID, ownerToken, map[string]string{"permission": "read"}, nil)
	if status != http.StatusOK {
		t.Fatalf("put artifact ACL: %d %s", status, data)
	}
	status, data = f.request(http.MethodGet, "/v1/tasks/"+task.ID+"/artifacts/restricted", readerToken, nil, nil)
	if status != http.StatusOK || !bytes.Equal(data, content) {
		t.Fatalf("authorized artifact read: %d %q", status, data)
	}
	status, _ = f.request(http.MethodGet, "/v1/tasks/"+task.ID+"/artifacts/restricted", otherToken, nil, nil)
	if status != http.StatusForbidden {
		t.Fatalf("artifact ACL expected 403, got %d", status)
	}
	status, data = f.request(http.MethodGet, "/v1/tasks/"+task.ID+"/artifacts", otherToken, nil, nil)
	if status != http.StatusOK || bytes.Contains(data, []byte("restricted")) {
		t.Fatalf("restricted artifact leaked through index: %d %s", status, data)
	}
	status, data = f.request(http.MethodGet, "/v1/tasks/"+task.ID+"/continuity-package?agent=generic", otherToken, nil, nil)
	if status != http.StatusOK || bytes.Contains(data, []byte("restricted")) {
		t.Fatalf("restricted artifact leaked through continuity package: %d %s", status, data)
	}
	status, data = f.request(http.MethodGet, "/v1/tasks/"+task.ID+"/artifacts/restricted", ownerToken, nil, nil)
	if status != http.StatusOK || !bytes.Equal(data, content) {
		t.Fatalf("owner lost access after ACL: %d %q", status, data)
	}
	_ = owner
}

func TestSignedQRInvitationOneTimeAcceptance(t *testing.T) {
	f := newFixture(t)
	owner, ownerToken := f.account("qr-owner")
	device, private := f.device(ownerToken, owner, "qr-device")
	status, data := f.request(http.MethodPost, "/v1/tasks", ownerToken, map[string]string{"title": "QR handoff", "objective": "signed local invitation"}, nil)
	task := decode[protocol.Task](t, data)
	if status != http.StatusCreated {
		t.Fatal(string(data))
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	invitationID, _ := protocol.NewID("invite")
	token := "qri_" + base64.RawURLEncoding.EncodeToString(random)
	value := protocol.QRInvitation{Version: 1, InvitationID: invitationID, TaskID: task.ID, InviterAccountID: owner.ID, InviterDeviceID: device.ID, Permission: protocol.PermissionWrite, ExpiresAt: time.Now().UTC().Add(10 * time.Minute), Nonce: "qr-nonce-0000000001", TokenDigest: protocol.Digest([]byte(token)), KeyExchangeReference: json.RawMessage(`{"wrapped_key":"opaque"}`), Token: token}
	if err := protocol.SignQRInvitation(&value, private); err != nil {
		t.Fatal(err)
	}
	status, data = f.request(http.MethodPost, "/v1/tasks/"+task.ID+"/qr-invitations", ownerToken, value, nil)
	if status != http.StatusCreated {
		t.Fatalf("create QR invitation: %d %s", status, data)
	}
	rendered := decode[protocol.QRInvitationRender](t, data)
	if rendered.Invitation.InvitationID != invitationID || rendered.Content != "agentcontinuity://invite?token="+token || !strings.Contains(rendered.SVG, "<svg") || strings.Contains(rendered.SVG, "<script") {
		t.Fatalf("invalid rendered QR invitation: %#v", rendered)
	}
	_, guestToken := f.account("qr-guest")
	status, data = f.request(http.MethodPost, "/v1/invitations/accept", guestToken, map[string]string{"token": token}, nil)
	if status != http.StatusOK || !bytes.Contains(data, []byte("wrapped_key")) {
		t.Fatalf("accept QR invitation: %d %s", status, data)
	}
	status, _ = f.request(http.MethodPost, "/v1/invitations/accept", guestToken, map[string]string{"token": token}, nil)
	if status != http.StatusGone {
		t.Fatalf("QR replay expected 410, got %d", status)
	}
	status, _ = f.request(http.MethodPost, "/v1/tasks/"+task.ID+"/qr-invitations", ownerToken, value, nil)
	if status != http.StatusConflict {
		t.Fatalf("device nonce replay expected 409, got %d", status)
	}
}

func TestTaskPermissionAdministrationAndImmediateRevocation(t *testing.T) {
	f := newFixture(t)
	owner, ownerToken := f.account("permission-owner")
	guest, guestToken := f.account("permission-guest")
	status, data := f.request(http.MethodPost, "/v1/tasks", ownerToken, map[string]string{"title": "Permission lifecycle", "objective": "grant and revoke"}, nil)
	if status != http.StatusCreated {
		t.Fatal(string(data))
	}
	task := decode[protocol.Task](t, data)
	status, data = f.request(http.MethodPut, "/v1/tasks/"+task.ID+"/permissions/"+guest.ID, ownerToken, map[string]string{"permission": "write"}, nil)
	if status != http.StatusOK || !bytes.Contains(data, []byte(`"permission":"write"`)) {
		t.Fatalf("put task permission: %d %s", status, data)
	}
	status, data = f.request(http.MethodGet, "/v1/tasks/"+task.ID+"/permissions", ownerToken, nil, nil)
	if status != http.StatusOK || !bytes.Contains(data, []byte(guest.ID)) {
		t.Fatalf("list task permissions: %d %s", status, data)
	}
	status, _ = f.request(http.MethodGet, "/v1/tasks/"+task.ID, guestToken, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("granted collaborator expected access, got %d", status)
	}
	status, data = f.request(http.MethodDelete, "/v1/tasks/"+task.ID+"/permissions/"+guest.ID, ownerToken, nil, nil)
	if status != http.StatusNoContent {
		t.Fatalf("delete task permission: %d %s", status, data)
	}
	status, _ = f.request(http.MethodGet, "/v1/tasks/"+task.ID, guestToken, nil, nil)
	if status != http.StatusForbidden {
		t.Fatalf("revoked collaborator expected 403, got %d", status)
	}
	status, data = f.request(http.MethodDelete, "/v1/tasks/"+task.ID+"/permissions/"+owner.ID, ownerToken, nil, nil)
	if status != http.StatusForbidden {
		t.Fatalf("owner permission deletion expected 403, got %d: %s", status, data)
	}
	status, data = f.request(http.MethodGet, "/v1/tasks/"+task.ID+"/participants", ownerToken, nil, nil)
	if status != http.StatusOK || !bytes.Contains(data, []byte(`"status":"left"`)) {
		t.Fatalf("revoked participant did not leave: %d %s", status, data)
	}
}

func TestOAuthMCPA2AMarketplaceAndUsage(t *testing.T) {
	f := newFixture(t)
	account, token := f.account("protocol-owner")
	status, data := f.request(http.MethodPost, "/v1/tasks", token, map[string]string{"title": "Protocol task", "objective": "MCP and A2A"}, nil)
	task := decode[protocol.Task](t, data)
	if status != http.StatusCreated {
		t.Fatal(string(data))
	}
	status, data = f.request(http.MethodPost, "/v1/oauth/clients", token, map[string]any{"name": "agent client", "scopes": []string{"tasks:read", "marketplace:read"}}, nil)
	if status != http.StatusCreated {
		t.Fatalf("create OAuth client: %d %s", status, data)
	}
	created := decode[struct {
		Client       protocol.OAuthClient `json:"client"`
		ClientSecret string               `json:"client_secret"`
	}](t, data)
	form := url.Values{"grant_type": {"client_credentials"}, "scope": {"tasks:read"}}
	req, _ := http.NewRequest(http.MethodPost, f.server.URL+"/v1/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(created.Client.ID, created.ClientSecret)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	oauthData, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("issue OAuth token: %d %s", response.StatusCode, oauthData)
	}
	oauthToken := decode[map[string]any](t, oauthData)["access_token"].(string)
	status, data = f.request(http.MethodPost, "/v1/mcp", oauthToken, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "task.list", "arguments": map[string]any{"limit": 10}}}, nil)
	if status != http.StatusOK || !bytes.Contains(data, []byte(task.ID)) {
		t.Fatalf("MCP task.list: %d %s", status, data)
	}
	status, data = f.request(http.MethodPost, "/v1/a2a", oauthToken, map[string]any{"jsonrpc": "2.0", "id": "a2a-1", "method": "tasks/get", "params": map[string]string{"id": task.ID}}, nil)
	if status != http.StatusOK || !bytes.Contains(data, []byte("continuity_package")) {
		t.Fatalf("A2A tasks/get: %d %s", status, data)
	}
	status, data = f.request(http.MethodPost, "/agentcontinuity.v1.ContinuityService/GetTask", oauthToken, map[string]string{"task_id": task.ID}, map[string]string{"Connect-Protocol-Version": "1"})
	if status != http.StatusOK || !bytes.Contains(data, []byte(task.ID)) {
		t.Fatalf("Connect GetTask: %d %s", status, data)
	}
	listingInput := map[string]any{"kind": "adapter", "name": "Test Adapter", "description": "A signed adapter listing", "version": "1.0.0", "capabilities": []string{"capture"}, "manifest": map[string]any{"entrypoint": "adapter"}, "status": "published"}
	status, data = f.request(http.MethodPost, "/v1/marketplace/listings", token, listingInput, nil)
	if status != http.StatusCreated {
		t.Fatalf("create marketplace listing: %d %s", status, data)
	}
	listing := decode[protocol.MarketplaceListing](t, data)
	status, data = f.request(http.MethodPost, "/v1/marketplace/listings/"+listing.ID+"/install", token, nil, nil)
	if status != http.StatusCreated {
		t.Fatalf("install listing: %d %s", status, data)
	}
	status, data = f.request(http.MethodGet, "/v1/usage", token, nil, nil)
	if status != http.StatusOK || !bytes.Contains(data, []byte("api.mutation")) {
		t.Fatalf("usage records: %d %s", status, data)
	}
	_ = account
}

func TestBillingAndAgentToAgentMarketplaceTransaction(t *testing.T) {
	f := newFixture(t)
	_, sellerToken := f.account("marketplace-seller")
	_, buyerToken := f.account("marketplace-buyer")
	status, data := f.request(http.MethodPost, "/v1/billing/accounts", buyerToken, map[string]any{"plan": "developer", "currency": "usd"}, nil)
	if status != http.StatusCreated {
		t.Fatalf("create billing account: %d %s", status, data)
	}
	billing := decode[protocol.BillingAccount](t, data)
	if billing.Plan != "developer" || billing.Currency != "USD" || !bytes.Contains(billing.Limits, []byte("api_calls")) {
		t.Fatalf("unexpected billing account: %+v", billing)
	}
	status, data = f.request(http.MethodPost, "/v1/agents", sellerToken, map[string]any{"name": "Seller Agent", "provider": "generic", "capabilities": []string{"review"}}, nil)
	if status != http.StatusCreated {
		t.Fatalf("seller agent: %d %s", status, data)
	}
	sellerAgent := decode[protocol.AgentProfile](t, data)
	status, data = f.request(http.MethodPost, "/v1/agents", buyerToken, map[string]any{"name": "Buyer Agent", "provider": "codex", "capabilities": []string{"execute"}}, nil)
	if status != http.StatusCreated {
		t.Fatalf("buyer agent: %d %s", status, data)
	}
	buyerAgent := decode[protocol.AgentProfile](t, data)
	listingInput := map[string]any{"kind": "capability", "name": "Review service", "description": "Agent-to-agent review", "version": "1.0.0", "manifest": map[string]any{"protocol": "a2a"}, "price_minor": 1250, "currency": "USD", "commission_basis_points": 500, "status": "published"}
	status, data = f.request(http.MethodPost, "/v1/marketplace/listings", sellerToken, listingInput, nil)
	if status != http.StatusCreated {
		t.Fatalf("paid listing: %d %s", status, data)
	}
	listing := decode[protocol.MarketplaceListing](t, data)
	status, _ = f.request(http.MethodPost, "/v1/marketplace/listings/"+listing.ID+"/install", buyerToken, nil, nil)
	if status != http.StatusPaymentRequired {
		t.Fatalf("unpaid install expected 402, got %d", status)
	}
	status, data = f.request(http.MethodPost, "/v1/marketplace/transactions", buyerToken, map[string]any{"listing_id": listing.ID, "buyer_agent_id": buyerAgent.ID, "seller_agent_id": sellerAgent.ID, "quantity": 2, "idempotency_key": "purchase-review-0001"}, nil)
	if status != http.StatusCreated {
		t.Fatalf("create transaction: %d %s", status, data)
	}
	transaction := decode[protocol.MarketplaceTransaction](t, data)
	if transaction.AmountMinor != 2500 || transaction.CommissionMinor != 125 || transaction.Status != "pending" {
		t.Fatalf("unexpected transaction economics: %+v", transaction)
	}
	status, _ = f.request(http.MethodPost, "/v1/marketplace/transactions/"+transaction.ID+"/authorize", sellerToken, nil, nil)
	if status != http.StatusConflict {
		t.Fatalf("seller authorized buyer transaction: %d", status)
	}
	status, data = f.request(http.MethodPost, "/v1/marketplace/transactions/"+transaction.ID+"/authorize", buyerToken, nil, nil)
	if status != http.StatusOK || decode[protocol.MarketplaceTransaction](t, data).Status != "authorized" {
		t.Fatalf("buyer authorize: %d %s", status, data)
	}
	status, _ = f.request(http.MethodPost, "/v1/marketplace/transactions/"+transaction.ID+"/complete", buyerToken, nil, nil)
	if status != http.StatusConflict {
		t.Fatalf("buyer completed seller transaction: %d", status)
	}
	status, data = f.request(http.MethodPost, "/v1/marketplace/transactions/"+transaction.ID+"/complete", sellerToken, nil, nil)
	if status != http.StatusOK || decode[protocol.MarketplaceTransaction](t, data).Status != "completed" {
		t.Fatalf("seller complete: %d %s", status, data)
	}
	status, data = f.request(http.MethodPost, "/v1/marketplace/listings/"+listing.ID+"/install", buyerToken, nil, nil)
	if status != http.StatusCreated {
		t.Fatalf("paid install after completion: %d %s", status, data)
	}
	status, data = f.request(http.MethodGet, "/v1/marketplace/transactions", buyerToken, nil, nil)
	if status != http.StatusOK || !bytes.Contains(data, []byte(transaction.ID)) {
		t.Fatalf("list buyer transactions: %d %s", status, data)
	}
}

func TestDelegatedOAuthClientCannotEscalateScopes(t *testing.T) {
	f := newFixture(t)
	_, ownerToken := f.account("oauth-scope-owner")
	status, data := f.request(http.MethodPost, "/v1/api-keys", ownerToken, map[string]any{"name": "oauth-manager", "scopes": []string{"oauth:write"}}, nil)
	if status != http.StatusCreated {
		t.Fatalf("create delegated key: %d %s", status, data)
	}
	key := decode[struct {
		Secret string `json:"secret"`
	}](t, data)
	status, data = f.request(http.MethodPost, "/v1/oauth/clients", key.Secret, map[string]any{"name": "escalated", "scopes": []string{"tasks:admin"}}, nil)
	if status != http.StatusForbidden || !bytes.Contains(data, []byte("scope_escalation")) {
		t.Fatalf("delegated OAuth scope escalation was accepted: %d %s", status, data)
	}
}
