package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

func TestPlatformEndpoints(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tasks/task_1/qr-invitations":
			writeClientTestJSON(t, w, map[string]any{"invitation": map[string]any{"version": 1, "task_id": "task_1", "token": "invite"}, "qr_content": "agentcontinuity://invite?token=invite", "qr_svg": "<svg/>"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tasks/task_1/permissions":
			writeClientTestJSON(t, w, map[string]any{"permissions": []map[string]any{{"task_id": "task_1", "account_id": "acct_2", "permission": "read", "created_at": "2026-01-01T00:00:00Z"}}})
		case r.Method == http.MethodPut && r.URL.Path == "/v1/tasks/task_1/permissions/acct_2":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["permission"] != "write" {
				t.Errorf("unexpected permission request: body=%v err=%v", body, err)
			}
			writeClientTestJSON(t, w, map[string]any{"task_id": "task_1", "account_id": "acct_2", "permission": "write", "created_at": "2026-01-01T00:00:00Z"})
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tasks/task_1/permissions/acct_2":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/auth/sso/exchange":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["id_token"] != "oidc-token" {
				t.Errorf("unexpected SSO request: body=%v err=%v", body, err)
			}
			writeClientTestJSON(t, w, map[string]any{"access_token": "access", "refresh_token": "refresh", "token_type": "Bearer", "expires_in": 900, "refresh_expires_in": 3600})
		default:
			http.Error(w, `{"error":{"code":"not_found","message":"missing"}}`, http.StatusNotFound)
		}
	}))
	defer server.Close()

	api := New(server.URL, "access")
	ctx := context.Background()
	qr, err := api.CreateQRInvitation(ctx, protocol.QRInvitation{Version: 1, TaskID: "task_1", Token: "invite"})
	if err != nil || qr.Content == "" || qr.SVG != "<svg/>" {
		t.Fatalf("QR invitation: %+v %v", qr, err)
	}
	permissions, err := api.ListTaskPermissions(ctx, "task_1")
	if err != nil || len(permissions) != 1 || permissions[0].Permission != protocol.PermissionRead {
		t.Fatalf("list permissions: %+v %v", permissions, err)
	}
	permission, err := api.PutTaskPermission(ctx, "task_1", "acct_2", protocol.PermissionWrite)
	if err != nil || permission.Permission != protocol.PermissionWrite {
		t.Fatalf("put permission: %+v %v", permission, err)
	}
	if err := api.DeleteTaskPermission(ctx, "task_1", "acct_2"); err != nil {
		t.Fatal(err)
	}
	tokens, err := api.ExchangeSSO(ctx, "oidc-token")
	if err != nil || tokens.AccessToken != "access" || tokens.RefreshToken != "refresh" {
		t.Fatalf("SSO exchange: %+v %v", tokens, err)
	}
}

func writeClientTestJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Error(err)
	}
}
