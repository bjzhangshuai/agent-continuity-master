package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/agent-continuity/agent-continuity-master/internal/billing"
	"github.com/agent-continuity/agent-continuity-master/internal/operations"
	"math"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/agent-continuity/agent-continuity-master/internal/auth"
	accesspolicy "github.com/agent-continuity/agent-continuity-master/internal/policy"
	qrrender "github.com/agent-continuity/agent-continuity-master/internal/qrcode"
	"github.com/agent-continuity/agent-continuity-master/internal/storage"
	"github.com/agent-continuity/agent-continuity-master/internal/tenancy"
	webhookdispatcher "github.com/agent-continuity/agent-continuity-master/internal/webhook"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

var slugPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

func (s *Server) createOrganization(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeScope(w, r, "organizations:write")
	if !ok {
		return
	}
	var input struct {
		ParentID       string          `json:"parent_id"`
		OwnerAccountID string          `json:"owner_account_id"`
		Name           string          `json:"name"`
		Slug           string          `json:"slug"`
		Plan           string          `json:"plan"`
		DataResidency  string          `json:"data_residency"`
		KeyProvider    string          `json:"key_provider"`
		Settings       json.RawMessage `json:"settings"`
	}
	if !s.decodeJSON(w, r, &input) {
		return
	}
	if input.OwnerAccountID == "" {
		input.OwnerAccountID = claims.AccountID
	}
	if input.ParentID != "" {
		parent, err := s.store.GetOrganization(r.Context(), input.ParentID)
		if err != nil {
			writeStorageError(w, err)
			return
		}
		if s.tenantRole(r, claims, parent.ID) != "owner" && !s.platformAllows(r, claims, parent.ID, "delegate") {
			writeStorageError(w, storage.ErrUnauthorized)
			return
		}
		for depth := 1; ; depth++ {
			if depth >= tenancy.MaxDepth {
				writeError(w, 400, "tenant_depth", "Tenant hierarchy exceeds maximum depth")
				return
			}
			if parent.ParentID == "" {
				break
			}
			parent, err = s.store.GetOrganization(r.Context(), parent.ParentID)
			if err != nil {
				writeStorageError(w, err)
				return
			}
		}
	}
	if input.OwnerAccountID != claims.AccountID && !s.platformAllows(r, claims, input.ParentID, "delegate") {
		writeStorageError(w, storage.ErrUnauthorized)
		return
	}
	if _, err := s.store.GetAccount(r.Context(), input.OwnerAccountID); err != nil {
		writeStorageError(w, err)
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	input.Slug = strings.ToLower(strings.TrimSpace(input.Slug))
	if input.Plan == "" {
		input.Plan = "free"
	}
	if input.KeyProvider == "" {
		input.KeyProvider = "platform"
	}
	input.DataResidency = strings.ToLower(strings.TrimSpace(input.DataResidency))
	if s.dataResidency != "" {
		if input.DataResidency == "" {
			input.DataResidency = s.dataResidency
		}
		if input.DataResidency != s.dataResidency {
			writeError(w, http.StatusConflict, "residency_mismatch", "organization residency must match this deployment")
			return
		}
	}
	if input.Name == "" || len(input.Name) > 120 || !slugPattern.MatchString(input.Slug) || !slices.Contains([]string{"free", "pro", "team", "enterprise"}, input.Plan) || !slices.Contains([]string{"platform", "byok", "kms", "hsm"}, input.KeyProvider) {
		writeError(w, http.StatusBadRequest, "invalid_organization", "name, DNS-style slug, plan, and key provider must be valid")
		return
	}
	id, _ := protocol.NewID("org")
	now := time.Now().UTC()
	value := protocol.Organization{ID: id, ParentID: input.ParentID, Name: input.Name, Slug: input.Slug, OwnerAccountID: input.OwnerAccountID, Plan: input.Plan, DataResidency: input.DataResidency, KeyProvider: input.KeyProvider, Settings: input.Settings, CreatedAt: now, UpdatedAt: now}
	owner := protocol.OrganizationMember{OrganizationID: id, AccountID: input.OwnerAccountID, Role: "owner", Status: "active", JoinedAt: now}
	if err := s.store.CreateOrganization(r.Context(), value, owner); err != nil {
		writeStorageError(w, err)
		return
	}
	s.audit(r, claims, "organization.created", "organization", id, nil)
	writeJSON(w, http.StatusCreated, value)
}

func (s *Server) listOrganizations(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeScope(w, r, "organizations:read")
	if !ok {
		return
	}
	items, err := s.store.ListOrganizations(r.Context(), claims.AccountID)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"organizations": items})
}

func (s *Server) getOrganization(w http.ResponseWriter, r *http.Request) {
	_, ok := s.authorizeOrganization(w, r, "organization.read", "organization", nil)
	if !ok {
		return
	}
	value, err := s.store.GetOrganization(r.Context(), r.PathValue("organizationID"))
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func (s *Server) updateOrganization(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeOrganization(w, r, "organization.write", "organization", nil)
	if !ok {
		return
	}
	current, err := s.store.GetOrganization(r.Context(), r.PathValue("organizationID"))
	if err != nil {
		writeStorageError(w, err)
		return
	}
	var input struct {
		Name          string          `json:"name"`
		Slug          string          `json:"slug"`
		Plan          string          `json:"plan"`
		DataResidency string          `json:"data_residency"`
		KeyProvider   string          `json:"key_provider"`
		Settings      json.RawMessage `json:"settings"`
	}
	if !s.decodeJSON(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	input.Slug = strings.ToLower(strings.TrimSpace(input.Slug))
	input.DataResidency = strings.ToLower(strings.TrimSpace(input.DataResidency))
	if s.dataResidency != "" && input.DataResidency != s.dataResidency {
		writeError(w, http.StatusConflict, "residency_mismatch", "organization residency must match this deployment")
		return
	}
	if input.Name == "" || len(input.Name) > 120 || !slugPattern.MatchString(input.Slug) || !slices.Contains([]string{"free", "pro", "team", "enterprise"}, input.Plan) || !slices.Contains([]string{"platform", "byok", "kms", "hsm"}, input.KeyProvider) {
		writeError(w, http.StatusBadRequest, "invalid_organization", "name, DNS-style slug, plan, and key provider must be valid")
		return
	}
	current.Name, current.Slug, current.Plan = input.Name, input.Slug, input.Plan
	current.DataResidency, current.KeyProvider, current.Settings = input.DataResidency, input.KeyProvider, input.Settings
	current.UpdatedAt = time.Now().UTC()
	if err := s.store.UpdateOrganization(r.Context(), current); err != nil {
		writeStorageError(w, err)
		return
	}
	s.audit(r, claims, "organization.updated", "organization", current.ID, nil)
	writeJSON(w, http.StatusOK, current)
}

func (s *Server) deleteOrganization(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeOrganization(w, r, "organization.delete", "organization", nil)
	if !ok {
		return
	}
	value, err := s.store.GetOrganization(r.Context(), r.PathValue("organizationID"))
	if err != nil {
		writeStorageError(w, err)
		return
	}
	if value.OwnerAccountID != claims.AccountID {
		writeError(w, http.StatusForbidden, "owner_required", "only the organization owner can delete it")
		return
	}
	if err := s.store.DeleteOrganization(r.Context(), value.ID, claims.AccountID); err != nil {
		writeStorageError(w, err)
		return
	}
	s.audit(r, claims, "organization.deleted", "organization", value.ID, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listOrganizationMembers(w http.ResponseWriter, r *http.Request) {
	_, ok := s.authorizeOrganization(w, r, "member.read", "organization", nil)
	if !ok {
		return
	}
	items, err := s.store.ListOrganizationMembers(r.Context(), r.PathValue("organizationID"))
	if err != nil {
		writeStorageError(w, err)
		return
	}
	names := map[string]string{}
	for _, m := range items {
		if a, err := s.store.GetAccount(r.Context(), m.AccountID); err == nil {
			names[m.AccountID] = a.Name
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"members": items, "account_names": names})
}

func (s *Server) putOrganizationMember(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeOrganization(w, r, "member.write", "organization", nil)
	if !ok {
		return
	}
	var input struct {
		Role string `json:"role"`
	}
	if !s.decodeJSON(w, r, &input) {
		return
	}
	if !slices.Contains([]string{"admin", "member", "viewer", "billing"}, input.Role) {
		writeError(w, http.StatusBadRequest, "invalid_role", "role must be admin, member, viewer, or billing")
		return
	}
	accountID := r.PathValue("accountID")
	org, orgErr := s.store.GetOrganization(r.Context(), r.PathValue("organizationID"))
	if orgErr != nil {
		writeStorageError(w, orgErr)
		return
	}
	if accountID == org.OwnerAccountID {
		writeError(w, 403, "owner_immutable", "Owner cannot be changed through membership roles")
		return
	}
	oldMember, _ := s.store.GetOrganizationMember(r.Context(), org.ID, accountID)
	if (input.Role == "billing" || oldMember.Role == "billing") && s.tenantRole(r, claims, org.ID) != "owner" {
		writeError(w, 403, "billing_grant_owner_required", "Only the tenant owner can change payment authority")
		return
	}
	if _, err := s.store.GetAccount(r.Context(), accountID); err != nil {
		writeStorageError(w, err)
		return
	}
	value := protocol.OrganizationMember{OrganizationID: r.PathValue("organizationID"), AccountID: accountID, Role: input.Role, Status: "active", JoinedAt: time.Now().UTC()}
	if err := s.store.PutOrganizationMember(r.Context(), value); err != nil {
		writeStorageError(w, err)
		return
	}
	s.audit(r, claims, "organization.member.put", "account", accountID, mustJSON(map[string]string{"organization_id": value.OrganizationID, "role": value.Role}))
	writeJSON(w, http.StatusOK, value)
}

func (s *Server) removeOrganizationMember(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeOrganization(w, r, "member.write", "organization", nil)
	if !ok {
		return
	}
	organization, err := s.store.GetOrganization(r.Context(), r.PathValue("organizationID"))
	if err != nil {
		writeStorageError(w, err)
		return
	}
	accountID := r.PathValue("accountID")
	if accountID == organization.OwnerAccountID {
		writeError(w, http.StatusConflict, "owner_immutable", "the owner cannot be removed")
		return
	}
	member, _ := s.store.GetOrganizationMember(r.Context(), organization.ID, accountID)
	if member.Role == "billing" && s.tenantRole(r, claims, organization.ID) != "owner" {
		writeError(w, 403, "billing_grant_owner_required", "Only the owner can revoke payment authority")
		return
	}
	if err := s.store.RemoveOrganizationMember(r.Context(), organization.ID, accountID, time.Now().UTC()); err != nil {
		writeStorageError(w, err)
		return
	}
	s.audit(r, claims, "organization.member.removed", "account", accountID, mustJSON(map[string]string{"organization_id": organization.ID}))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) createTeam(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeOrganization(w, r, "team.create", "team", nil)
	if !ok {
		return
	}
	var input struct {
		Name     string          `json:"name"`
		Slug     string          `json:"slug"`
		Settings json.RawMessage `json:"settings"`
	}
	if !s.decodeJSON(w, r, &input) {
		return
	}
	input.Name, input.Slug = strings.TrimSpace(input.Name), strings.ToLower(strings.TrimSpace(input.Slug))
	if input.Name == "" || len(input.Name) > 120 || !slugPattern.MatchString(input.Slug) {
		writeError(w, http.StatusBadRequest, "invalid_team", "team name and DNS-style slug are required")
		return
	}
	id, _ := protocol.NewID("team")
	value := protocol.Team{ID: id, OrganizationID: r.PathValue("organizationID"), Name: input.Name, Slug: input.Slug, Settings: input.Settings, CreatedAt: time.Now().UTC()}
	if err := s.store.CreateTeam(r.Context(), value); err != nil {
		writeStorageError(w, err)
		return
	}
	s.audit(r, claims, "team.created", "team", id, nil)
	writeJSON(w, http.StatusCreated, value)
}

func (s *Server) listTeams(w http.ResponseWriter, r *http.Request) {
	_, ok := s.authorizeOrganization(w, r, "team.read", "team", nil)
	if !ok {
		return
	}
	items, err := s.store.ListTeams(r.Context(), r.PathValue("organizationID"))
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"teams": items})
}

func (s *Server) updateTeam(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeOrganization(w, r, "team.write", "team", map[string]any{"team_id": r.PathValue("teamID")})
	if !ok {
		return
	}
	value, err := s.store.GetTeam(r.Context(), r.PathValue("teamID"))
	if err != nil || value.OrganizationID != r.PathValue("organizationID") {
		writeStorageError(w, storage.ErrNotFound)
		return
	}
	var input struct {
		Name     string          `json:"name"`
		Slug     string          `json:"slug"`
		Settings json.RawMessage `json:"settings"`
	}
	if !s.decodeJSON(w, r, &input) {
		return
	}
	input.Name, input.Slug = strings.TrimSpace(input.Name), strings.ToLower(strings.TrimSpace(input.Slug))
	if input.Name == "" || !slugPattern.MatchString(input.Slug) {
		writeError(w, http.StatusBadRequest, "invalid_team", "team name and DNS-style slug are required")
		return
	}
	value.Name, value.Slug, value.Settings = input.Name, input.Slug, input.Settings
	if err := s.store.UpdateTeam(r.Context(), value); err != nil {
		writeStorageError(w, err)
		return
	}
	s.audit(r, claims, "team.updated", "team", value.ID, nil)
	writeJSON(w, http.StatusOK, value)
}

func (s *Server) deleteTeam(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeOrganization(w, r, "team.delete", "team", map[string]any{"team_id": r.PathValue("teamID")})
	if !ok {
		return
	}
	if err := s.store.DeleteTeam(r.Context(), r.PathValue("organizationID"), r.PathValue("teamID")); err != nil {
		writeStorageError(w, err)
		return
	}
	s.audit(r, claims, "team.deleted", "team", r.PathValue("teamID"), nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listTeamMembers(w http.ResponseWriter, r *http.Request) {
	_, ok := s.authorizeOrganization(w, r, "team.read", "team", map[string]any{"team_id": r.PathValue("teamID")})
	if !ok || !s.verifyTeamOrganization(w, r) {
		return
	}
	items, err := s.store.ListTeamMembers(r.Context(), r.PathValue("teamID"))
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"members": items})
}

func (s *Server) putTeamMember(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeOrganization(w, r, "team.write", "team", map[string]any{"team_id": r.PathValue("teamID")})
	if !ok || !s.verifyTeamOrganization(w, r) {
		return
	}
	var input struct {
		Role string `json:"role"`
	}
	if !s.decodeJSON(w, r, &input) {
		return
	}
	if !slices.Contains([]string{"lead", "member", "viewer"}, input.Role) {
		writeError(w, http.StatusBadRequest, "invalid_role", "team role must be lead, member, or viewer")
		return
	}
	accountID := r.PathValue("accountID")
	member, err := s.store.GetOrganizationMember(r.Context(), r.PathValue("organizationID"), accountID)
	if err != nil || member.Status != "active" {
		writeError(w, http.StatusConflict, "organization_membership_required", "team members must be active organization members")
		return
	}
	value := protocol.TeamMember{TeamID: r.PathValue("teamID"), AccountID: accountID, Role: input.Role, Status: "active", JoinedAt: time.Now().UTC()}
	if err := s.store.PutTeamMember(r.Context(), value); err != nil {
		writeStorageError(w, err)
		return
	}
	s.audit(r, claims, "team.member.put", "account", accountID, mustJSON(map[string]string{"team_id": value.TeamID, "role": value.Role}))
	writeJSON(w, http.StatusOK, value)
}

func (s *Server) removeTeamMember(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeOrganization(w, r, "team.write", "team", map[string]any{"team_id": r.PathValue("teamID")})
	if !ok || !s.verifyTeamOrganization(w, r) {
		return
	}
	if err := s.store.RemoveTeamMember(r.Context(), r.PathValue("teamID"), r.PathValue("accountID"), time.Now().UTC()); err != nil {
		writeStorageError(w, err)
		return
	}
	s.audit(r, claims, "team.member.removed", "account", r.PathValue("accountID"), mustJSON(map[string]string{"team_id": r.PathValue("teamID")}))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) verifyTeamOrganization(w http.ResponseWriter, r *http.Request) bool {
	team, err := s.store.GetTeam(r.Context(), r.PathValue("teamID"))
	if err != nil || team.OrganizationID != r.PathValue("organizationID") {
		writeStorageError(w, storage.ErrNotFound)
		return false
	}
	return true
}

func (s *Server) createAccessPolicy(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeOrganization(w, r, "policy.write", "policy", nil)
	if !ok {
		return
	}
	var input struct {
		Name         string                     `json:"name"`
		Effect       string                     `json:"effect"`
		Subjects     []string                   `json:"subjects"`
		Actions      []string                   `json:"actions"`
		ResourceType string                     `json:"resource_type"`
		Conditions   []protocol.PolicyCondition `json:"conditions"`
		Priority     int                        `json:"priority"`
	}
	if !s.decodeJSON(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Name) > 120 || !slices.Contains([]string{"allow", "deny"}, input.Effect) || len(input.Subjects) == 0 || len(input.Actions) == 0 || strings.TrimSpace(input.ResourceType) == "" || !validPolicyConditions(input.Conditions) {
		writeError(w, http.StatusBadRequest, "invalid_policy", "policy fields or condition operators are invalid")
		return
	}
	id, _ := protocol.NewID("policy")
	value := protocol.AccessPolicy{ID: id, OrganizationID: r.PathValue("organizationID"), Name: input.Name, Effect: input.Effect, Subjects: uniqueStrings(input.Subjects), Actions: uniqueStrings(input.Actions), ResourceType: input.ResourceType, Conditions: input.Conditions, Priority: input.Priority, CreatedBy: claims.AccountID, CreatedAt: time.Now().UTC()}
	if err := s.store.CreateAccessPolicy(r.Context(), value); err != nil {
		writeStorageError(w, err)
		return
	}
	s.audit(r, claims, "policy.created", "policy", id, nil)
	writeJSON(w, http.StatusCreated, value)
}

func (s *Server) listAccessPolicies(w http.ResponseWriter, r *http.Request) {
	_, ok := s.authorizeOrganization(w, r, "policy.read", "policy", nil)
	if !ok {
		return
	}
	items, err := s.store.ListAccessPolicies(r.Context(), r.PathValue("organizationID"))
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"policies": items})
}

func (s *Server) deleteAccessPolicy(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeOrganization(w, r, "policy.write", "policy", nil)
	if !ok {
		return
	}
	if err := s.store.DeleteAccessPolicy(r.Context(), r.PathValue("organizationID"), r.PathValue("policyID")); err != nil {
		writeStorageError(w, err)
		return
	}
	s.audit(r, claims, "policy.deleted", "policy", r.PathValue("policyID"), nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listArtifactPermissions(w http.ResponseWriter, r *http.Request) {
	_, ok := s.authorizeTask(w, r, protocol.PermissionAdmin)
	if !ok {
		return
	}
	items, err := s.store.ListArtifactPermissions(r.Context(), r.PathValue("taskID"), r.PathValue("artifactID"))
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"permissions": items})
}

func (s *Server) putArtifactPermission(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeTask(w, r, protocol.PermissionAdmin)
	if !ok {
		return
	}
	var input struct {
		Permission protocol.Permission `json:"permission"`
	}
	if !s.decodeJSON(w, r, &input) {
		return
	}
	if !slices.Contains([]protocol.Permission{protocol.PermissionRead, protocol.PermissionWrite, protocol.PermissionAdmin}, input.Permission) {
		writeError(w, http.StatusBadRequest, "invalid_permission", "permission must be read, write, or admin")
		return
	}
	if _, err := s.store.GetAccount(r.Context(), r.PathValue("accountID")); err != nil {
		writeStorageError(w, err)
		return
	}
	value := protocol.ArtifactPermission{TaskID: r.PathValue("taskID"), ArtifactID: r.PathValue("artifactID"), AccountID: r.PathValue("accountID"), Permission: input.Permission, GrantedBy: claims.AccountID, CreatedAt: time.Now().UTC()}
	if err := s.store.PutArtifactPermission(r.Context(), value); err != nil {
		writeStorageError(w, err)
		return
	}
	s.audit(r, claims, "artifact.permission.put", "artifact", value.ArtifactID, mustJSON(map[string]any{"account_id": value.AccountID, "permission": value.Permission}))
	writeJSON(w, http.StatusOK, value)
}

func (s *Server) deleteArtifactPermission(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeTask(w, r, protocol.PermissionAdmin)
	if !ok {
		return
	}
	if err := s.store.DeleteArtifactPermission(r.Context(), r.PathValue("taskID"), r.PathValue("artifactID"), r.PathValue("accountID")); err != nil {
		writeStorageError(w, err)
		return
	}
	s.audit(r, claims, "artifact.permission.deleted", "artifact", r.PathValue("artifactID"), mustJSON(map[string]string{"account_id": r.PathValue("accountID")}))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) createOAuthClient(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeScope(w, r, "oauth:write")
	if !ok {
		return
	}
	var input struct {
		Name         string   `json:"name"`
		Scopes       []string `json:"scopes"`
		RedirectURIs []string `json:"redirect_uris"`
	}
	if !s.decodeJSON(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	input.Scopes = uniqueStrings(input.Scopes)
	if input.Name == "" || len(input.Name) > 120 || len(input.Scopes) == 0 || slices.Contains(input.Scopes, "*") {
		writeError(w, http.StatusBadRequest, "invalid_oauth_client", "name and explicit non-wildcard scopes are required")
		return
	}
	for _, scope := range input.Scopes {
		if _, supported := allowedDelegatedScopes[scope]; !supported {
			writeError(w, http.StatusBadRequest, "invalid_scope", "unsupported OAuth scope: "+scope)
			return
		}
		if !auth.HasScope(claims, scope) {
			writeError(w, http.StatusForbidden, "scope_escalation", "cannot grant a scope the caller does not hold")
			return
		}
	}
	clientID, _ := protocol.NewID("client")
	clientSecret, err := auth.NewOpaqueToken("ocs")
	if err != nil {
		writeInternal(w, err)
		return
	}
	value := protocol.OAuthClient{ID: clientID, AccountID: claims.AccountID, Name: input.Name, SecretHash: auth.HashOpaque(clientSecret), Scopes: input.Scopes, RedirectURIs: uniqueStrings(input.RedirectURIs), CreatedAt: time.Now().UTC()}
	if err := s.store.CreateOAuthClient(r.Context(), value); err != nil {
		writeStorageError(w, err)
		return
	}
	s.audit(r, claims, "oauth.client.created", "oauth_client", clientID, nil)
	writeJSON(w, http.StatusCreated, map[string]any{"client": value, "client_secret": clientSecret})
}

func (s *Server) listOAuthClients(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeScope(w, r, "oauth:read")
	if !ok {
		return
	}
	items, err := s.store.ListOAuthClients(r.Context(), claims.AccountID)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"clients": items})
}

func (s *Server) revokeOAuthClient(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeScope(w, r, "oauth:write")
	if !ok {
		return
	}
	if err := s.store.RevokeOAuthClient(r.Context(), claims.AccountID, r.PathValue("clientID")); err != nil {
		writeStorageError(w, err)
		return
	}
	s.audit(r, claims, "oauth.client.revoked", "oauth_client", r.PathValue("clientID"), nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) issueOAuthToken(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid OAuth form")
		return
	}
	clientID, clientSecret := r.Form.Get("client_id"), r.Form.Get("client_secret")
	if basicID, basicSecret, ok := r.BasicAuth(); ok {
		clientID, clientSecret = basicID, basicSecret
	}
	if r.Form.Get("grant_type") != "client_credentials" || clientID == "" || clientSecret == "" {
		writeError(w, http.StatusBadRequest, "unsupported_grant_type", "client_credentials grant is required")
		return
	}
	client, err := s.store.AuthenticateOAuthClient(r.Context(), auth.HashOpaque(clientSecret))
	if err != nil || client.ID != clientID {
		writeError(w, http.StatusUnauthorized, "invalid_client", "OAuth client authentication failed")
		return
	}
	requested := strings.Fields(r.Form.Get("scope"))
	if len(requested) == 0 {
		requested = client.Scopes
	}
	for _, scope := range requested {
		if !slices.Contains(client.Scopes, scope) {
			writeError(w, http.StatusBadRequest, "invalid_scope", "requested scope is not granted to this client")
			return
		}
	}
	token, err := s.tokens.IssueScoped(client.AccountID, "oauth-client:"+client.ID, requested, client.ID, time.Hour)
	if err != nil {
		writeInternal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"access_token": token, "token_type": "Bearer", "expires_in": 3600, "scope": strings.Join(requested, " ")})
}

func (s *Server) createWebhookEndpoint(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeScope(w, r, "webhooks:write")
	if !ok {
		return
	}
	var input struct {
		URL    string   `json:"url"`
		Events []string `json:"events"`
	}
	if !s.decodeJSON(w, r, &input) {
		return
	}
	input.URL = strings.TrimSpace(input.URL)
	input.Events = uniqueStrings(input.Events)
	if err := webhookdispatcher.ValidateEndpointURL(input.URL); err != nil || len(input.Events) == 0 || len(input.Events) > 100 {
		message := "one to 100 event names and a public HTTPS URL are required"
		if err != nil {
			message = err.Error()
		}
		writeError(w, http.StatusBadRequest, "invalid_webhook", message)
		return
	}
	id, _ := protocol.NewID("webhook")
	secret, err := auth.NewOpaqueToken("whsec")
	if err != nil {
		writeInternal(w, err)
		return
	}
	encryptedSecret, err := s.secretCipher.Encrypt([]byte(secret), claims.AccountID+":"+id)
	if err != nil {
		writeInternal(w, err)
		return
	}
	value := protocol.WebhookEndpoint{ID: id, AccountID: claims.AccountID, URL: input.URL, Events: input.Events, SecretHash: auth.HashOpaque(secret), CreatedAt: time.Now().UTC()}
	if err := s.store.CreateWebhookEndpoint(r.Context(), value, encryptedSecret); err != nil {
		writeStorageError(w, err)
		return
	}
	s.audit(r, claims, "webhook.created", "webhook", id, nil)
	value.Secret = secret
	writeJSON(w, http.StatusCreated, value)
}

func (s *Server) listWebhookEndpoints(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeScope(w, r, "webhooks:read")
	if !ok {
		return
	}
	items, err := s.store.ListWebhookEndpoints(r.Context(), claims.AccountID)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"webhooks": items})
}

func (s *Server) revokeWebhookEndpoint(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeScope(w, r, "webhooks:write")
	if !ok {
		return
	}
	if err := s.store.RevokeWebhookEndpoint(r.Context(), claims.AccountID, r.PathValue("webhookID"), time.Now().UTC()); err != nil {
		writeStorageError(w, err)
		return
	}
	s.audit(r, claims, "webhook.revoked", "webhook", r.PathValue("webhookID"), nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listUsage(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeScope(w, r, "usage:read")
	if !ok {
		return
	}
	from, err := parseOptionalTime(r.URL.Query().Get("from"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_time", "from must be RFC3339")
		return
	}
	to, err := parseOptionalTime(r.URL.Query().Get("to"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_time", "to must be RFC3339")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := s.store.ListUsage(r.Context(), claims.AccountID, from, to, limit)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"usage": items})
}

func (s *Server) createBillingAccount(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeScope(w, r, "billing:write")
	if !ok {
		return
	}
	var input struct {
		OrganizationID string `json:"organization_id"`
		Plan           string `json:"plan"`
		Currency       string `json:"currency"`
	}
	if !s.decodeJSON(w, r, &input) {
		return
	}
	input.Currency = strings.ToUpper(strings.TrimSpace(input.Currency))
	if input.Currency == "" {
		input.Currency = "USD"
	}
	if !slices.Contains([]string{"free", "pro", "team", "enterprise", "developer"}, input.Plan) || !regexp.MustCompile(`^[A-Z]{3}$`).MatchString(input.Currency) {
		writeError(w, http.StatusBadRequest, "invalid_billing_account", "plan and ISO-style currency code are required")
		return
	}
	if input.OrganizationID != "" {
		organization, err := s.store.GetOrganization(r.Context(), input.OrganizationID)
		if err != nil || organization.OwnerAccountID != claims.AccountID || !slices.Contains([]string{"team", "enterprise"}, input.Plan) {
			writeError(w, http.StatusForbidden, "billing_owner_required", "organization owner and a team or enterprise plan are required")
			return
		}
	} else if input.Plan == "team" || input.Plan == "enterprise" {
		writeError(w, http.StatusBadRequest, "organization_required", "team and enterprise billing accounts require an organization")
		return
	}
	existing, err := s.store.ListBillingAccounts(r.Context(), claims.AccountID)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	for _, value := range existing {
		if value.OrganizationID == input.OrganizationID {
			writeError(w, http.StatusConflict, "billing_account_exists", "a billing account already exists for this scope")
			return
		}
	}
	now := time.Now().UTC()
	id, _ := protocol.NewID("billing")
	value := protocol.BillingAccount{ID: id, AccountID: claims.AccountID, OrganizationID: input.OrganizationID, Plan: input.Plan, Status: "active", Currency: input.Currency, Limits: billingLimits(input.Plan), CurrentPeriodStart: now, CurrentPeriodEnd: now.AddDate(0, 1, 0), CreatedAt: now, UpdatedAt: now}
	if err := s.store.PutBillingAccount(r.Context(), value); err != nil {
		writeStorageError(w, err)
		return
	}
	s.audit(r, claims, "billing.account.created", "billing_account", value.ID, mustJSON(map[string]any{"plan": value.Plan, "organization_id": value.OrganizationID}))
	writeJSON(w, http.StatusCreated, value)
}

func (s *Server) listBillingAccounts(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeScope(w, r, "billing:read")
	if !ok {
		return
	}
	items, err := s.store.ListBillingAccounts(r.Context(), claims.AccountID)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"billing_accounts": items})
}

func billingLimits(plan string) json.RawMessage {
	limits := map[string]any{"devices": 3, "artifact_bytes": int64(1 << 30), "retained_tasks": 100, "monthly_cross_agent_resumes": 20}
	switch plan {
	case "pro":
		limits = map[string]any{"devices": 20, "artifact_bytes": int64(100 << 30), "retained_tasks": 10000, "monthly_cross_agent_resumes": 1000}
	case "developer":
		limits = map[string]any{"api_calls": 1000000, "synchronized_events": 1000000, "artifact_bytes": int64(100 << 30), "egress_bytes": int64(1 << 40)}
	case "team":
		limits = map[string]any{"members": 100, "devices": 500, "artifact_bytes": int64(1 << 40), "retained_tasks": 100000}
	case "enterprise":
		limits = map[string]any{"members": -1, "devices": -1, "artifact_bytes": -1, "retained_tasks": -1, "custom_policy": true}
	}
	return mustJSON(limits)
}

func (s *Server) createMarketplaceListing(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeScope(w, r, "marketplace:write")
	if !ok {
		return
	}
	var input struct {
		Kind         string          `json:"kind"`
		Name         string          `json:"name"`
		Description  string          `json:"description"`
		Version      string          `json:"version"`
		Capabilities []string        `json:"capabilities"`
		Manifest     json.RawMessage `json:"manifest"`
		PriceMinor   int64           `json:"price_minor"`
		Currency     string          `json:"currency"`
		Commission   int             `json:"commission_basis_points"`
		Status       string          `json:"status"`
	}
	if !s.decodeJSON(w, r, &input) {
		return
	}
	input.Name, input.Description, input.Version = strings.TrimSpace(input.Name), strings.TrimSpace(input.Description), strings.TrimSpace(input.Version)
	if input.Status == "" {
		input.Status = "draft"
	}
	input.Currency = strings.ToUpper(strings.TrimSpace(input.Currency))
	if input.Currency == "" {
		input.Currency = "USD"
	}
	if !slices.Contains([]string{"agent", "capability", "adapter", "task"}, input.Kind) || input.Name == "" || input.Description == "" || input.Version == "" || len(input.Manifest) == 0 || !json.Valid(input.Manifest) || input.PriceMinor < 0 || !regexp.MustCompile(`^[A-Z]{3}$`).MatchString(input.Currency) || input.Commission < 0 || input.Commission > 3000 || !slices.Contains([]string{"draft", "published"}, input.Status) {
		writeError(w, http.StatusBadRequest, "invalid_listing", "listing kind, name, description, version, manifest, and status are required")
		return
	}
	id, _ := protocol.NewID("listing")
	now := time.Now().UTC()
	value := protocol.MarketplaceListing{ID: id, PublisherID: claims.AccountID, Kind: input.Kind, Name: input.Name, Description: input.Description, Version: input.Version, Capabilities: uniqueStrings(input.Capabilities), Manifest: input.Manifest, PriceMinor: input.PriceMinor, Currency: input.Currency, CommissionBP: input.Commission, Status: input.Status, CreatedAt: now, UpdatedAt: now}
	if err := s.store.CreateMarketplaceListing(r.Context(), value); err != nil {
		writeStorageError(w, err)
		return
	}
	s.audit(r, claims, "marketplace.listing.created", "marketplace_listing", id, nil)
	writeJSON(w, http.StatusCreated, value)
}

func (s *Server) listMarketplaceListings(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := s.store.ListMarketplaceListings(r.Context(), r.URL.Query().Get("kind"), r.URL.Query().Get("q"), limit)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"listings": items})
}

func (s *Server) installMarketplaceListing(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeScope(w, r, "marketplace:install")
	if !ok {
		return
	}
	listing, err := s.store.GetMarketplaceListing(r.Context(), r.PathValue("listingID"))
	if err != nil || listing.Status != "published" {
		writeStorageError(w, storage.ErrNotFound)
		return
	}
	if listing.PriceMinor > 0 {
		paid, err := s.store.HasCompletedMarketplaceTransaction(r.Context(), listing.ID, claims.AccountID)
		if err != nil {
			writeStorageError(w, err)
			return
		}
		if !paid {
			writeError(w, http.StatusPaymentRequired, "completed_transaction_required", "paid listings require a completed marketplace transaction")
			return
		}
	}
	id, _ := protocol.NewID("installation")
	value := protocol.MarketplaceInstallation{ID: id, ListingID: listing.ID, AccountID: claims.AccountID, Version: listing.Version, Status: "active", InstalledAt: time.Now().UTC()}
	if err := s.store.InstallMarketplaceListing(r.Context(), value); err != nil {
		writeStorageError(w, err)
		return
	}
	s.audit(r, claims, "marketplace.listing.installed", "marketplace_listing", listing.ID, nil)
	writeJSON(w, http.StatusCreated, value)
}

func (s *Server) listMarketplaceInstallations(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeScope(w, r, "marketplace:read")
	if !ok {
		return
	}
	items, err := s.store.ListMarketplaceInstallations(r.Context(), claims.AccountID)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"installations": items})
}

func (s *Server) createMarketplaceTransaction(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeScope(w, r, "marketplace:transact")
	if !ok {
		return
	}
	var input struct {
		ListingID      string          `json:"listing_id"`
		BuyerAgentID   string          `json:"buyer_agent_id"`
		SellerAgentID  string          `json:"seller_agent_id"`
		TaskID         string          `json:"task_id"`
		Quantity       int64           `json:"quantity"`
		IdempotencyKey string          `json:"idempotency_key"`
		Metadata       json.RawMessage `json:"metadata"`
	}
	if !s.decodeJSON(w, r, &input) {
		return
	}
	if input.Quantity == 0 {
		input.Quantity = 1
	}
	if input.Quantity < 1 || input.Quantity > 1000 || len(input.IdempotencyKey) < 8 || len(input.IdempotencyKey) > 200 || len(input.Metadata) > 64<<10 || len(input.Metadata) > 0 && !json.Valid(input.Metadata) {
		writeError(w, http.StatusBadRequest, "invalid_transaction", "quantity, idempotency key, and metadata are invalid")
		return
	}
	listing, err := s.store.GetMarketplaceListing(r.Context(), input.ListingID)
	if err != nil || listing.Status != "published" || listing.PublisherID == claims.AccountID {
		writeError(w, http.StatusBadRequest, "invalid_listing", "a published listing owned by another account is required")
		return
	}
	if listing.PriceMinor > 0 && input.Quantity > math.MaxInt64/listing.PriceMinor {
		writeError(w, http.StatusBadRequest, "amount_overflow", "transaction amount exceeds supported range")
		return
	}
	if input.BuyerAgentID != "" {
		agent, err := s.store.GetAgent(r.Context(), input.BuyerAgentID)
		if err != nil || agent.AccountID != claims.AccountID || agent.RevokedAt != nil {
			writeError(w, http.StatusForbidden, "buyer_agent_forbidden", "buyer agent must be active and owned by the caller")
			return
		}
	}
	if input.SellerAgentID != "" {
		agent, err := s.store.GetAgent(r.Context(), input.SellerAgentID)
		if err != nil || agent.AccountID != listing.PublisherID || agent.RevokedAt != nil {
			writeError(w, http.StatusBadRequest, "seller_agent_invalid", "seller agent must be active and owned by the publisher")
			return
		}
	}
	if input.TaskID != "" {
		allowed, err := s.taskAccessAllowed(r, claims, input.TaskID, protocol.PermissionRead)
		if err != nil || !allowed {
			writeError(w, http.StatusForbidden, "task_forbidden", "transaction task must be readable by the buyer")
			return
		}
	}
	amount := listing.PriceMinor * input.Quantity
	now := time.Now().UTC()
	id, _ := protocol.NewID("transaction")
	value := protocol.MarketplaceTransaction{ID: id, ListingID: listing.ID, BuyerAccountID: claims.AccountID, SellerAccountID: listing.PublisherID, BuyerAgentID: input.BuyerAgentID, SellerAgentID: input.SellerAgentID, TaskID: input.TaskID, Quantity: input.Quantity, AmountMinor: amount, CommissionMinor: amount * int64(listing.CommissionBP) / 10000, Currency: listing.Currency, Status: "pending", IdempotencyKey: input.IdempotencyKey, Metadata: input.Metadata, CreatedAt: now, UpdatedAt: now}
	if err := s.store.CreateMarketplaceTransaction(r.Context(), value); err != nil {
		writeStorageError(w, err)
		return
	}
	s.audit(r, claims, "marketplace.transaction.created", "marketplace_transaction", value.ID, mustJSON(value))
	writeJSON(w, http.StatusCreated, value)
}

func (s *Server) listMarketplaceTransactions(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeScope(w, r, "marketplace:read")
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := s.store.ListMarketplaceTransactions(r.Context(), claims.AccountID, limit)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"transactions": items})
}

func (s *Server) authorizeMarketplaceTransaction(w http.ResponseWriter, r *http.Request) {
	s.transitionMarketplaceTransaction(w, r, "authorized")
}

func (s *Server) completeMarketplaceTransaction(w http.ResponseWriter, r *http.Request) {
	s.transitionMarketplaceTransaction(w, r, "completed")
}

func (s *Server) cancelMarketplaceTransaction(w http.ResponseWriter, r *http.Request) {
	s.transitionMarketplaceTransaction(w, r, "cancelled")
}

func (s *Server) transitionMarketplaceTransaction(w http.ResponseWriter, r *http.Request, status string) {
	claims, ok := s.authorizeScope(w, r, "marketplace:transact")
	if !ok {
		return
	}
	if status == "completed" {
		t, e := s.store.GetMarketplaceTransaction(r.Context(), r.PathValue("transactionID"))
		if e != nil || t.SellerAccountID != claims.AccountID {
			writeError(w, 409, "invalid_transaction_transition", "seller required")
			return
		}
		if t.AmountMinor > 0 {
			d, e := s.store.GetBill(r.Context(), t.BuyerAccountID, operations.ID("inv", t.BuyerAccountID, "marketplace:"+t.ID))
			if e != nil || !billing.PurchasePaid(d, t.ID) {
				writeError(w, 402, "payment_required", "verified marketplace payment required")
				return
			}
		}
	}

	value, err := s.store.TransitionMarketplaceTransaction(r.Context(), r.PathValue("transactionID"), claims.AccountID, status, time.Now().UTC())
	if err != nil {
		if errors.Is(err, storage.ErrUnauthorized) {
			writeError(w, http.StatusConflict, "invalid_transaction_transition", "actor or transaction state does not allow this transition")
			return
		}
		writeStorageError(w, err)
		return
	}
	s.audit(r, claims, "marketplace.transaction."+status, "marketplace_transaction", value.ID, mustJSON(value))
	writeJSON(w, http.StatusOK, value)
}

func (s *Server) createQRInvitation(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeTask(w, r, protocol.PermissionAdmin)
	if !ok {
		return
	}
	var input protocol.QRInvitation
	if !s.decodeJSON(w, r, &input) {
		return
	}
	if input.Version != 1 || input.TaskID != r.PathValue("taskID") || input.InviterAccountID != claims.AccountID || input.InvitationID == "" || input.InviterDeviceID == "" || input.Nonce == "" || len(input.Nonce) > 200 || input.Token == "" || len(input.Token) < 32 || !slices.Contains([]protocol.Permission{protocol.PermissionRead, protocol.PermissionWrite}, input.Permission) || input.ExpiresAt.Before(time.Now().Add(time.Minute)) || input.ExpiresAt.After(time.Now().Add(24*time.Hour)) {
		writeError(w, http.StatusBadRequest, "invalid_qr_invitation", "signed invitation fields, permission, token entropy, and expiration are invalid")
		return
	}
	if input.TokenDigest != protocol.Digest([]byte(input.Token)) {
		writeError(w, http.StatusBadRequest, "invalid_token_digest", "token digest does not match the one-time token")
		return
	}
	device, err := s.store.GetDevice(r.Context(), input.InviterDeviceID)
	if err != nil || device.AccountID != claims.AccountID || device.RevokedAt != nil || protocol.VerifyQRInvitation(input, device.PublicKey) != nil {
		writeError(w, http.StatusUnauthorized, "invalid_qr_signature", "QR invitation signature or device is invalid")
		return
	}
	hash := sha256.Sum256([]byte(input.Token))
	value := protocol.Invitation{ID: input.InvitationID, TaskID: input.TaskID, InviterAccount: claims.AccountID, Permission: input.Permission, TokenHash: hex.EncodeToString(hash[:]), InviterDeviceID: input.InviterDeviceID, Nonce: input.Nonce, TokenDigest: input.TokenDigest, KeyExchangeReference: input.KeyExchangeReference, Signature: input.Signature, ExpiresAt: input.ExpiresAt.UTC(), CreatedAt: time.Now().UTC()}
	if err := s.store.CreateInvitation(r.Context(), value); err != nil {
		writeStorageError(w, err)
		return
	}
	s.metricSet.InvitationsCreated.Add(1)
	s.audit(r, claims, "qr_invitation.created", "invitation", value.ID, mustJSON(map[string]string{"task_id": value.TaskID, "device_id": value.InviterDeviceID}))
	content := "agentcontinuity://invite?token=" + input.Token
	svg, err := qrrender.SVG(content)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "qr_render_failed", "invitation was stored but QR rendering failed")
		return
	}
	writeJSON(w, http.StatusCreated, protocol.QRInvitationRender{Invitation: input, Content: content, SVG: svg})
}

func (s *Server) listTaskPermissions(w http.ResponseWriter, r *http.Request) {
	_, ok := s.authorizeTask(w, r, protocol.PermissionAdmin)
	if !ok {
		return
	}
	items, err := s.store.ListTaskPermissions(r.Context(), r.PathValue("taskID"))
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"permissions": items})
}

func (s *Server) putTaskPermission(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeTask(w, r, protocol.PermissionAdmin)
	if !ok {
		return
	}
	var input struct {
		Permission protocol.Permission `json:"permission"`
	}
	if !s.decodeJSON(w, r, &input) {
		return
	}
	if !slices.Contains([]protocol.Permission{protocol.PermissionRead, protocol.PermissionWrite, protocol.PermissionAdmin}, input.Permission) || r.PathValue("accountID") == "" {
		writeError(w, http.StatusBadRequest, "invalid_permission", "account and read, write, or admin permission are required")
		return
	}
	value := protocol.TaskPermission{TaskID: r.PathValue("taskID"), AccountID: r.PathValue("accountID"), Permission: input.Permission, CreatedAt: time.Now().UTC()}
	if err := s.store.PutTaskPermission(r.Context(), value); err != nil {
		writeStorageError(w, err)
		return
	}
	s.audit(r, claims, "task.permission.put", "task", value.TaskID, mustJSON(map[string]any{"account_id": value.AccountID, "permission": value.Permission}))
	s.publish(protocol.RealtimeMessage{Type: "permission.updated", TaskID: value.TaskID, ResourceID: value.AccountID, SentAt: value.CreatedAt})
	writeJSON(w, http.StatusOK, value)
}

func (s *Server) deleteTaskPermission(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.authorizeTask(w, r, protocol.PermissionAdmin)
	if !ok {
		return
	}
	removedAt := time.Now().UTC()
	if err := s.store.DeleteTaskPermission(r.Context(), r.PathValue("taskID"), r.PathValue("accountID"), removedAt); err != nil {
		writeStorageError(w, err)
		return
	}
	s.audit(r, claims, "task.permission.deleted", "task", r.PathValue("taskID"), mustJSON(map[string]string{"account_id": r.PathValue("accountID")}))
	s.publish(protocol.RealtimeMessage{Type: "permission.revoked", TaskID: r.PathValue("taskID"), ResourceID: r.PathValue("accountID"), SentAt: removedAt})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) authorizeOrganization(w http.ResponseWriter, r *http.Request, action, resourceType string, attributes map[string]any) (auth.Claims, bool) {
	requiredScope := "organizations:read"
	if !strings.HasSuffix(action, ".read") {
		requiredScope = "organizations:write"
	}
	claims, ok := s.authorizeScope(w, r, requiredScope)
	if !ok {
		return auth.Claims{}, false
	}
	allowed, err := s.organizationAllows(r, claims, r.PathValue("organizationID"), action, resourceType, attributes)
	if err != nil {
		writeStorageError(w, err)
		return auth.Claims{}, false
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "forbidden", "organization policy denied this action")
		return auth.Claims{}, false
	}
	return claims, true
}

func (s *Server) organizationAllows(r *http.Request, claims auth.Claims, organizationID, action, resourceType string, attributes map[string]any) (bool, error) {
	member, err := s.store.GetOrganizationMember(r.Context(), organizationID, claims.AccountID)
	if err != nil || member.Status != "active" {
		return false, storage.ErrUnauthorized
	}
	policies, err := s.store.ListAccessPolicies(r.Context(), organizationID)
	if err != nil {
		return false, err
	}
	decision, err := accesspolicy.Evaluate(accesspolicy.Context{SubjectID: claims.AccountID, Roles: []string{member.Role}, Action: action, ResourceType: resourceType, Attributes: attributes, Now: time.Now().UTC()}, policies)
	if err != nil {
		return false, err
	}
	return decision.Allowed, nil
}

func validPolicyConditions(values []protocol.PolicyCondition) bool {
	for _, value := range values {
		if strings.TrimSpace(value.Key) == "" || !slices.Contains([]string{"exists", "equals", "not_equals", "in", "contains", "before", "after"}, value.Operator) || len(value.Value) == 0 || !json.Valid(value.Value) {
			return false
		}
	}
	return true
}

func parseOptionalTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339, value)
}

func permissionAtLeast(actual, required protocol.Permission) bool {
	rank := func(value protocol.Permission) int {
		switch value {
		case protocol.PermissionRead:
			return 1
		case protocol.PermissionWrite:
			return 2
		case protocol.PermissionAdmin:
			return 3
		default:
			return 0
		}
	}
	return rank(actual) >= rank(required)
}

var _ = errors.Is
