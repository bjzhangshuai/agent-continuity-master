package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

const Version = "0.1.0"

func New(baseURL, token string) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), Token: token, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

type APIError struct {
	Status  int
	Code    string
	Message string
}

type TokenResponse struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	TokenType        string `json:"token_type"`
	ExpiresIn        int64  `json:"expires_in"`
	RefreshExpiresIn int64  `json:"refresh_expires_in"`
}

func (e *APIError) Error() string { return fmt.Sprintf("API %d %s: %s", e.Status, e.Code, e.Message) }

func (c *Client) Do(ctx context.Context, method, path string, body any, result any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reader)
	if err != nil {
		return err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var envelope struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(data, &envelope)
		return &APIError{Status: resp.StatusCode, Code: envelope.Error.Code, Message: envelope.Error.Message}
	}
	if result != nil && len(data) > 0 {
		if err := json.Unmarshal(data, result); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}
func (c *Client) Health(ctx context.Context) error {
	return c.Do(ctx, http.MethodGet, "/v1/health/ready", nil, nil)
}
func (c *Client) IssueToken(ctx context.Context, bootstrapToken, accountID, subject string) (TokenResponse, error) {
	var result TokenResponse
	data, err := json.Marshal(map[string]any{"account_id": accountID, "subject": subject, "ttl_seconds": 3600})
	if err != nil {
		return result, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/v1/auth/token", bytes.NewReader(data))
	if err != nil {
		return result, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Bootstrap-Token", bootstrapToken)
	return result, c.doRequest(req, &result)
}
func (c *Client) RefreshToken(ctx context.Context, refreshToken string) (TokenResponse, error) {
	var result TokenResponse
	err := c.Do(ctx, http.MethodPost, "/v1/auth/refresh", map[string]string{"refresh_token": refreshToken}, &result)
	return result, err
}
func (c *Client) Logout(ctx context.Context, refreshToken string) error {
	return c.Do(ctx, http.MethodPost, "/v1/auth/logout", map[string]string{"refresh_token": refreshToken}, nil)
}
func (c *Client) RegisterDevice(ctx context.Context, deviceID, name, publicKey string, encryptionPublicKeys ...string) (protocol.Device, error) {
	var result protocol.Device
	encryptionPublicKey := ""
	if len(encryptionPublicKeys) > 0 {
		encryptionPublicKey = encryptionPublicKeys[0]
	}
	err := c.Do(ctx, http.MethodPost, "/v1/devices", map[string]string{"device_id": deviceID, "name": name, "public_key": publicKey, "encryption_public_key": encryptionPublicKey}, &result)
	return result, err
}
func (c *Client) ListDevices(ctx context.Context, includeRevoked bool) ([]protocol.Device, error) {
	var result struct {
		Devices []protocol.Device `json:"devices"`
	}
	err := c.Do(ctx, http.MethodGet, "/v1/devices?include_revoked="+strconv.FormatBool(includeRevoked), nil, &result)
	return result.Devices, err
}
func (c *Client) ListTaskDevices(ctx context.Context, taskID string) ([]protocol.Device, error) {
	var result struct {
		Devices []protocol.Device `json:"devices"`
	}
	err := c.Do(ctx, http.MethodGet, "/v1/tasks/"+url.PathEscape(taskID)+"/devices", nil, &result)
	return result.Devices, err
}
func (c *Client) PutTaskKeySet(ctx context.Context, value protocol.TaskKeySet) error {
	return c.Do(ctx, http.MethodPost, "/v1/tasks/"+url.PathEscape(value.TaskID)+"/keys", value, nil)
}
func (c *Client) GetTaskKeySet(ctx context.Context, taskID, deviceID string, version uint32) (protocol.TaskKeySet, error) {
	var result protocol.TaskKeySet
	path := "/v1/tasks/" + url.PathEscape(taskID) + "/keys?device_id=" + url.QueryEscape(deviceID)
	if version > 0 {
		path += "&version=" + strconv.FormatUint(uint64(version), 10)
	}
	err := c.Do(ctx, http.MethodGet, path, nil, &result)
	return result, err
}
func (c *Client) ListTasks(ctx context.Context) ([]protocol.Task, error) {
	var result struct {
		Tasks []protocol.Task `json:"tasks"`
	}
	err := c.Do(ctx, http.MethodGet, "/v1/tasks", nil, &result)
	return result.Tasks, err
}
func (c *Client) ListAgents(ctx context.Context) ([]protocol.AgentProfile, error) {
	var result struct {
		Agents []protocol.AgentProfile `json:"agents"`
	}
	err := c.Do(ctx, http.MethodGet, "/v1/agents", nil, &result)
	return result.Agents, err
}
func (c *Client) CreateAgent(ctx context.Context, name, provider, model string, capabilities []string, card any) (protocol.AgentProfile, error) {
	var result protocol.AgentProfile
	err := c.Do(ctx, http.MethodPost, "/v1/agents", map[string]any{"name": name, "provider": provider, "model": model, "capabilities": capabilities, "card": card}, &result)
	return result, err
}
func (c *Client) RevokeAgent(ctx context.Context, agentID string) error {
	return c.Do(ctx, http.MethodDelete, "/v1/agents/"+url.PathEscape(agentID), nil, nil)
}
func (c *Client) BindTaskAgent(ctx context.Context, taskID, agentID, role, nativeSessionRef, resumeMode string, capabilities []string, metadata any) (protocol.TaskAgentBinding, protocol.TaskParticipant, error) {
	var result struct {
		Binding     protocol.TaskAgentBinding `json:"binding"`
		Participant protocol.TaskParticipant  `json:"participant"`
	}
	err := c.Do(ctx, http.MethodPost, "/v1/tasks/"+url.PathEscape(taskID)+"/agents", map[string]any{"agent_id": agentID, "role": role, "native_session_ref": nativeSessionRef, "resume_mode": resumeMode, "capabilities": capabilities, "metadata": metadata}, &result)
	return result.Binding, result.Participant, err
}
func (c *Client) ListTaskAgents(ctx context.Context, taskID string) ([]protocol.TaskAgentBinding, error) {
	var result struct {
		Bindings []protocol.TaskAgentBinding `json:"agent_bindings"`
	}
	err := c.Do(ctx, http.MethodGet, "/v1/tasks/"+url.PathEscape(taskID)+"/agents", nil, &result)
	return result.Bindings, err
}
func (c *Client) ListConflicts(ctx context.Context, taskID string, includeResolved bool) ([]protocol.Conflict, error) {
	var result struct {
		Conflicts []protocol.Conflict `json:"conflicts"`
	}
	path := "/v1/tasks/" + url.PathEscape(taskID) + "/conflicts?include_resolved=" + strconv.FormatBool(includeResolved)
	err := c.Do(ctx, http.MethodGet, path, nil, &result)
	return result.Conflicts, err
}
func (c *Client) ResolveConflict(ctx context.Context, taskID, conflictID, strategy, notes string) (protocol.ConflictResolution, error) {
	var result protocol.ConflictResolution
	path := "/v1/tasks/" + url.PathEscape(taskID) + "/conflicts/" + url.PathEscape(conflictID) + "/resolve"
	err := c.Do(ctx, http.MethodPost, path, map[string]string{"strategy": strategy, "notes": notes}, &result)
	return result, err
}
func (c *Client) CreateTask(ctx context.Context, title, objective, deviceID string) (protocol.Task, error) {
	var result protocol.Task
	err := c.Do(ctx, http.MethodPost, "/v1/tasks", map[string]string{"title": title, "objective": objective, "origin_device": deviceID}, &result)
	return result, err
}
func (c *Client) GetTask(ctx context.Context, id string) (protocol.Task, error) {
	var result protocol.Task
	err := c.Do(ctx, http.MethodGet, "/v1/tasks/"+url.PathEscape(id), nil, &result)
	return result, err
}
func (c *Client) AppendEvent(ctx context.Context, value protocol.Event) (protocol.AppendResult, error) {
	var result protocol.AppendResult
	err := c.Do(ctx, http.MethodPost, "/v1/tasks/"+url.PathEscape(value.TaskID)+"/events", value, &result)
	return result, err
}
func (c *Client) ListEvents(ctx context.Context, taskID string, after uint64, includePayload bool) ([]protocol.Event, error) {
	var result struct {
		Events []protocol.Event `json:"events"`
	}
	path := "/v1/tasks/" + url.PathEscape(taskID) + "/events?after=" + strconv.FormatUint(after, 10) + "&include_payload=" + strconv.FormatBool(includePayload)
	err := c.Do(ctx, http.MethodGet, path, nil, &result)
	return result.Events, err
}
func (c *Client) ContinuityPackage(ctx context.Context, taskID, agent string) (protocol.ContinuityPackage, error) {
	var result protocol.ContinuityPackage
	path := "/v1/tasks/" + url.PathEscape(taskID) + "/continuity-package?agent=" + url.QueryEscape(agent)
	err := c.Do(ctx, http.MethodGet, path, nil, &result)
	return result, err
}

func (c *Client) SyncHello(ctx context.Context, input protocol.SyncHelloRequest) (protocol.SyncHelloResponse, error) {
	var result protocol.SyncHelloResponse
	err := c.Do(ctx, http.MethodPost, "/v1/sync/hello", input, &result)
	return result, err
}

func (c *Client) SyncPush(ctx context.Context, input protocol.SyncPushRequest) (protocol.SyncPushResponse, error) {
	var result protocol.SyncPushResponse
	err := c.Do(ctx, http.MethodPost, "/v1/sync/push", input, &result)
	return result, err
}

func (c *Client) SyncPull(ctx context.Context, input protocol.SyncPullRequest) (protocol.SyncPullResponse, error) {
	var result protocol.SyncPullResponse
	err := c.Do(ctx, http.MethodPost, "/v1/sync/pull", input, &result)
	return result, err
}

func (c *Client) SyncAck(ctx context.Context, input protocol.SyncAckRequest) error {
	return c.Do(ctx, http.MethodPost, "/v1/sync/ack", input, nil)
}

func (c *Client) Reconcile(ctx context.Context, input protocol.ReconcileRequest) (protocol.ReconcileResult, error) {
	var result protocol.ReconcileResult
	err := c.Do(ctx, http.MethodPost, "/v1/reconcile", input, &result)
	return result, err
}

// MCP forwards one JSON-RPC message to the platform MCP endpoint. A nil
// response represents a JSON-RPC notification accepted without a reply.
func (c *Client) MCP(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
	var q struct {
		Method string `json:"method"`
		Params struct {
			Name string                     `json:"name"`
			Meta map[string]json.RawMessage `json:"_meta"`
		} `json:"params"`
	}
	if err := json.Unmarshal(input, &q); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+"/v1/mcp", bytes.NewReader(input))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	var version string
	_ = json.Unmarshal(q.Params.Meta["io.modelcontextprotocol/protocolVersion"], &version)
	if version != "" {
		req.Header.Set("MCP-Protocol-Version", version)
		req.Header.Set("Mcp-Method", q.Method)
		if q.Params.Name != "" {
			req.Header.Set("Mcp-Name", q.Params.Name)
		}
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == 202 {
		return nil, nil
	}
	var message struct {
		JSONRPC string `json:"jsonrpc"`
	}
	if json.Unmarshal(raw, &message) == nil && message.JSONRPC == "2.0" {
		return raw, nil
	}
	return nil, &APIError{Status: resp.StatusCode, Code: "mcp_transport_error", Message: "MCP transport/authentication rejected request"}
}

func (c *Client) CreateInvitation(ctx context.Context, taskID string, permission protocol.Permission, ttlSeconds int64) (string, error) {
	var result struct {
		Token string `json:"token"`
	}
	err := c.Do(ctx, http.MethodPost, "/v1/tasks/"+url.PathEscape(taskID)+"/invitations", map[string]any{"permission": permission, "ttl_seconds": ttlSeconds}, &result)
	return result.Token, err
}
func (c *Client) CreateQRInvitation(ctx context.Context, value protocol.QRInvitation) (protocol.QRInvitationRender, error) {
	var result protocol.QRInvitationRender
	err := c.Do(ctx, http.MethodPost, "/v1/tasks/"+url.PathEscape(value.TaskID)+"/qr-invitations", value, &result)
	return result, err
}
func (c *Client) ListTaskPermissions(ctx context.Context, taskID string) ([]protocol.TaskPermission, error) {
	var result struct {
		Permissions []protocol.TaskPermission `json:"permissions"`
	}
	err := c.Do(ctx, http.MethodGet, "/v1/tasks/"+url.PathEscape(taskID)+"/permissions", nil, &result)
	return result.Permissions, err
}
func (c *Client) PutTaskPermission(ctx context.Context, taskID, accountID string, permission protocol.Permission) (protocol.TaskPermission, error) {
	var result protocol.TaskPermission
	err := c.Do(ctx, http.MethodPut, "/v1/tasks/"+url.PathEscape(taskID)+"/permissions/"+url.PathEscape(accountID), map[string]any{"permission": permission}, &result)
	return result, err
}
func (c *Client) DeleteTaskPermission(ctx context.Context, taskID, accountID string) error {
	return c.Do(ctx, http.MethodDelete, "/v1/tasks/"+url.PathEscape(taskID)+"/permissions/"+url.PathEscape(accountID), nil, nil)
}
func (c *Client) ExchangeSSO(ctx context.Context, idToken string) (TokenResponse, error) {
	var result TokenResponse
	err := c.Do(ctx, http.MethodPost, "/v1/auth/sso/exchange", map[string]string{"id_token": idToken}, &result)
	return result, err
}
func (c *Client) AcceptInvitation(ctx context.Context, token string) error {
	return c.Do(ctx, http.MethodPost, "/v1/invitations/accept", map[string]string{"token": token}, nil)
}

func (c *Client) CreateOrganization(ctx context.Context, value protocol.Organization) (protocol.Organization, error) {
	var result protocol.Organization
	err := c.Do(ctx, http.MethodPost, "/v1/organizations", map[string]any{"name": value.Name, "slug": value.Slug, "plan": value.Plan, "data_residency": value.DataResidency, "key_provider": value.KeyProvider, "settings": value.Settings}, &result)
	return result, err
}

func (c *Client) ListOrganizations(ctx context.Context) ([]protocol.Organization, error) {
	var result struct {
		Organizations []protocol.Organization `json:"organizations"`
	}
	err := c.Do(ctx, http.MethodGet, "/v1/organizations", nil, &result)
	return result.Organizations, err
}

func (c *Client) PutOrganizationMember(ctx context.Context, organizationID, accountID, role string) (protocol.OrganizationMember, error) {
	var result protocol.OrganizationMember
	err := c.Do(ctx, http.MethodPut, "/v1/organizations/"+url.PathEscape(organizationID)+"/members/"+url.PathEscape(accountID), map[string]string{"role": role}, &result)
	return result, err
}

func (c *Client) CreateOAuthClient(ctx context.Context, name string, scopes []string) (protocol.OAuthClient, string, error) {
	var result struct {
		Client       protocol.OAuthClient `json:"client"`
		ClientSecret string               `json:"client_secret"`
	}
	err := c.Do(ctx, http.MethodPost, "/v1/oauth/clients", map[string]any{"name": name, "scopes": scopes}, &result)
	return result.Client, result.ClientSecret, err
}

func (c *Client) CreateWebhook(ctx context.Context, endpoint string, events []string) (protocol.WebhookEndpoint, error) {
	var result protocol.WebhookEndpoint
	err := c.Do(ctx, http.MethodPost, "/v1/webhooks", map[string]any{"url": endpoint, "events": events}, &result)
	return result, err
}

func (c *Client) ListUsage(ctx context.Context, from, to time.Time) ([]protocol.UsageRecord, error) {
	values := url.Values{}
	if !from.IsZero() {
		values.Set("from", from.UTC().Format(time.RFC3339))
	}
	if !to.IsZero() {
		values.Set("to", to.UTC().Format(time.RFC3339))
	}
	var result struct {
		Usage []protocol.UsageRecord `json:"usage"`
	}
	path := "/v1/usage"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	err := c.Do(ctx, http.MethodGet, path, nil, &result)
	return result.Usage, err
}

func (c *Client) ListBillingAccounts(ctx context.Context) ([]protocol.BillingAccount, error) {
	var result struct {
		BillingAccounts []protocol.BillingAccount `json:"billing_accounts"`
	}
	err := c.Do(ctx, http.MethodGet, "/v1/billing/accounts", nil, &result)
	return result.BillingAccounts, err
}

func (c *Client) CreateBillingAccount(ctx context.Context, organizationID, plan, currency string) (protocol.BillingAccount, error) {
	var result protocol.BillingAccount
	err := c.Do(ctx, http.MethodPost, "/v1/billing/accounts", map[string]string{"organization_id": organizationID, "plan": plan, "currency": currency}, &result)
	return result, err
}

func (c *Client) CreateMarketplaceTransaction(ctx context.Context, input any) (protocol.MarketplaceTransaction, error) {
	var result protocol.MarketplaceTransaction
	err := c.Do(ctx, http.MethodPost, "/v1/marketplace/transactions", input, &result)
	return result, err
}

func (c *Client) ListMarketplaceTransactions(ctx context.Context) ([]protocol.MarketplaceTransaction, error) {
	var result struct {
		Transactions []protocol.MarketplaceTransaction `json:"transactions"`
	}
	err := c.Do(ctx, http.MethodGet, "/v1/marketplace/transactions", nil, &result)
	return result.Transactions, err
}

func (c *Client) TransitionMarketplaceTransaction(ctx context.Context, transactionID, action string) (protocol.MarketplaceTransaction, error) {
	var result protocol.MarketplaceTransaction
	if action != "authorize" && action != "complete" && action != "cancel" {
		return result, errors.New("marketplace transaction action must be authorize, complete, or cancel")
	}
	path := "/v1/marketplace/transactions/" + url.PathEscape(transactionID) + "/" + action
	err := c.Do(ctx, http.MethodPost, path, nil, &result)
	return result, err
}

func (c *Client) ListArtifacts(ctx context.Context, taskID string) ([]protocol.Artifact, error) {
	var result struct {
		Artifacts []protocol.Artifact `json:"artifacts"`
	}
	err := c.Do(ctx, http.MethodGet, "/v1/tasks/"+url.PathEscape(taskID)+"/artifacts", nil, &result)
	return result.Artifacts, err
}

func (c *Client) CreateArtifactUpload(ctx context.Context, taskID string, input any) (protocol.ArtifactUploadStatus, error) {
	var result protocol.ArtifactUploadStatus
	err := c.Do(ctx, http.MethodPost, "/v1/tasks/"+url.PathEscape(taskID)+"/artifact-uploads", input, &result)
	return result, err
}

func (c *Client) GetArtifactUpload(ctx context.Context, uploadID string) (protocol.ArtifactUploadStatus, error) {
	var result protocol.ArtifactUploadStatus
	err := c.Do(ctx, http.MethodGet, "/v1/artifact-uploads/"+url.PathEscape(uploadID), nil, &result)
	return result, err
}

func (c *Client) PutArtifactChunk(ctx context.Context, uploadID string, index int, content []byte, digest string) (protocol.ArtifactChunk, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.BaseURL+"/v1/artifact-uploads/"+url.PathEscape(uploadID)+"/chunks/"+strconv.Itoa(index), bytes.NewReader(content))
	if err != nil {
		return protocol.ArtifactChunk{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-Content-SHA256", digest)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return protocol.ArtifactChunk{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return protocol.ArtifactChunk{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return protocol.ArtifactChunk{}, decodeAPIError(resp.StatusCode, data)
	}
	var result protocol.ArtifactChunk
	err = json.Unmarshal(data, &result)
	return result, err
}

func (c *Client) CompleteArtifactUpload(ctx context.Context, uploadID string) (protocol.Artifact, error) {
	var result protocol.Artifact
	err := c.Do(ctx, http.MethodPost, "/v1/artifact-uploads/"+url.PathEscape(uploadID)+"/complete", nil, &result)
	return result, err
}

func (c *Client) DownloadArtifact(ctx context.Context, taskID, artifactID string, writer io.Writer, offset int64) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/v1/tasks/"+url.PathEscape(taskID)+"/artifacts/"+url.PathEscape(artifactID), nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if offset > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(offset, 10)+"-")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return 0, decodeAPIError(resp.StatusCode, data)
	}
	if offset > 0 && resp.StatusCode != http.StatusPartialContent {
		return 0, errors.New("server ignored resumable download range")
	}
	return io.Copy(writer, resp.Body)
}

func decodeAPIError(status int, data []byte) error {
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(data, &envelope)
	return &APIError{Status: status, Code: envelope.Error.Code, Message: envelope.Error.Message}
}

func (c *Client) doRequest(req *http.Request, result any) error {
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return decodeAPIError(resp.StatusCode, data)
	}
	if result != nil && len(data) > 0 {
		return json.Unmarshal(data, result)
	}
	return nil
}

func IsTemporary(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status == http.StatusTooManyRequests || apiErr.Status >= 500
	}
	return true
}
