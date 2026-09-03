package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/agent-continuity/agent-continuity-master/internal/auth"
	"github.com/agent-continuity/agent-continuity-master/internal/storage"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

const (
	scimUserSchema  = "urn:ietf:params:scim:schemas:core:2.0:User"
	scimGroupSchema = "urn:ietf:params:scim:schemas:core:2.0:Group"
	scimListSchema  = "urn:ietf:params:scim:api:messages:2.0:ListResponse"
	scimPatchSchema = "urn:ietf:params:scim:api:messages:2.0:PatchOp"
)

type scimEmail struct {
	Value   string `json:"value"`
	Primary bool   `json:"primary,omitempty"`
}

type scimUser struct {
	Schemas     []string       `json:"schemas"`
	ID          string         `json:"id,omitempty"`
	ExternalID  string         `json:"externalId,omitempty"`
	UserName    string         `json:"userName"`
	DisplayName string         `json:"displayName,omitempty"`
	Active      bool           `json:"active"`
	Emails      []scimEmail    `json:"emails,omitempty"`
	Meta        map[string]any `json:"meta,omitempty"`
}

type scimMember struct {
	Value   string `json:"value"`
	Display string `json:"display,omitempty"`
}

type scimGroup struct {
	Schemas     []string       `json:"schemas"`
	ID          string         `json:"id,omitempty"`
	ExternalID  string         `json:"externalId,omitempty"`
	DisplayName string         `json:"displayName"`
	Members     []scimMember   `json:"members,omitempty"`
	Meta        map[string]any `json:"meta,omitempty"`
}

func (s *Server) exchangeSSO(w http.ResponseWriter, r *http.Request) {
	if s.oidcVerifier == nil {
		writeError(w, http.StatusNotFound, "sso_disabled", "SSO is not configured")
		return
	}
	var input struct {
		IDToken string `json:"id_token"`
	}
	if !s.decodeJSON(w, r, &input) {
		return
	}
	claims, err := s.oidcVerifier.Verify(input.IDToken)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid_id_token", err.Error())
		return
	}
	provider := "oidc:" + strings.TrimRight(claims.Issuer, "/")
	identity, err := s.store.GetExternalIdentity(r.Context(), provider, claims.Subject)
	if errors.Is(err, storage.ErrNotFound) {
		if !s.oidcAutoProvision {
			writeError(w, http.StatusForbidden, "sso_not_provisioned", "identity is not provisioned")
			return
		}
		accountID, _ := protocol.NewID("account")
		name := claims.Name
		if name == "" {
			name = claims.Email
		}
		if name == "" {
			name = claims.Subject
		}
		now := time.Now().UTC()
		account := protocol.Account{ID: accountID, Name: name, CreatedAt: now}
		if err := s.store.CreateAccount(r.Context(), account); err != nil {
			writeStorageError(w, err)
			return
		}
		identityID, _ := protocol.NewID("identity")
		identity = protocol.ExternalIdentity{ID: identityID, Provider: provider, Subject: claims.Subject, AccountID: accountID, Email: claims.Email, CreatedAt: now}
		if err := s.store.PutExternalIdentity(r.Context(), identity); err != nil {
			writeStorageError(w, err)
			return
		}
		if s.oidcOrganizationID != "" {
			member := protocol.OrganizationMember{OrganizationID: s.oidcOrganizationID, AccountID: accountID, Role: "member", Status: "active", JoinedAt: now}
			if err := s.store.PutOrganizationMember(r.Context(), member); err != nil {
				writeStorageError(w, err)
				return
			}
		}
	} else if err != nil {
		writeStorageError(w, err)
		return
	}
	response, err := s.newLoginSession(r.Context(), identity.AccountID, provider+"#"+claims.Subject)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	s.audit(r, auth.Claims{AccountID: identity.AccountID, Subject: claims.Subject}, "auth.sso.exchanged", "external_identity", identity.ID, nil)
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) newLoginSession(ctx context.Context, accountID, subject string) (map[string]any, error) {
	ttl := 15 * time.Minute
	accessToken, err := s.tokens.Issue(accountID, subject, ttl)
	if err != nil {
		return nil, err
	}
	refreshToken, err := auth.NewOpaqueToken("rft")
	if err != nil {
		return nil, err
	}
	sessionID, _ := protocol.NewID("session")
	familyID, _ := protocol.NewID("family")
	now := time.Now().UTC()
	session := protocol.AuthSession{ID: sessionID, FamilyID: familyID, AccountID: accountID, Subject: subject, RefreshTokenHash: auth.HashOpaque(refreshToken), CreatedAt: now, ExpiresAt: now.Add(30 * 24 * time.Hour)}
	if err := s.store.CreateAuthSession(ctx, session); err != nil {
		return nil, err
	}
	return map[string]any{"access_token": accessToken, "refresh_token": refreshToken, "token_type": "Bearer", "expires_in": int64(ttl.Seconds()), "refresh_expires_in": int64((30 * 24 * time.Hour).Seconds())}, nil
}

func (s *Server) authorizeSCIM(w http.ResponseWriter, r *http.Request) (protocol.Organization, bool) {
	if len(s.scimBearerToken) < 32 || s.scimOrganizationID == "" {
		scimError(w, http.StatusNotFound, "SCIM is not configured")
		return protocol.Organization{}, false
	}
	provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !secureEqual(provided, s.scimBearerToken) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="scim"`)
		scimError(w, http.StatusUnauthorized, "invalid SCIM bearer token")
		return protocol.Organization{}, false
	}
	organization, err := s.store.GetOrganization(r.Context(), s.scimOrganizationID)
	if err != nil {
		scimError(w, http.StatusServiceUnavailable, "SCIM organization is unavailable")
		return protocol.Organization{}, false
	}
	return organization, true
}

func (s *Server) scimServiceProviderConfig(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authorizeSCIM(w, r); !ok {
		return
	}
	writeSCIM(w, http.StatusOK, map[string]any{"schemas": []string{"urn:ietf:params:scim:schemas:core:2.0:ServiceProviderConfig"}, "patch": map[string]bool{"supported": true}, "bulk": map[string]any{"supported": false, "maxOperations": 0, "maxPayloadSize": 0}, "filter": map[string]any{"supported": true, "maxResults": 200}, "changePassword": map[string]bool{"supported": false}, "sort": map[string]bool{"supported": false}, "etag": map[string]bool{"supported": false}, "authenticationSchemes": []map[string]any{{"type": "oauthbearertoken", "name": "Bearer Token", "primary": true}}})
}

func (s *Server) scimResourceTypes(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authorizeSCIM(w, r); !ok {
		return
	}
	writeSCIM(w, http.StatusOK, map[string]any{"schemas": []string{scimListSchema}, "totalResults": 2, "startIndex": 1, "itemsPerPage": 2, "Resources": []map[string]any{{"id": "User", "name": "User", "endpoint": "/Users", "schema": scimUserSchema}, {"id": "Group", "name": "Group", "endpoint": "/Groups", "schema": scimGroupSchema}}})
}

func (s *Server) scimSchemas(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authorizeSCIM(w, r); !ok {
		return
	}
	writeSCIM(w, http.StatusOK, map[string]any{"schemas": []string{scimListSchema}, "totalResults": 2, "startIndex": 1, "itemsPerPage": 2, "Resources": []map[string]any{{"id": scimUserSchema, "name": "User"}, {"id": scimGroupSchema, "name": "Group"}}})
}

func (s *Server) createSCIMUser(w http.ResponseWriter, r *http.Request) {
	organization, ok := s.authorizeSCIM(w, r)
	if !ok {
		return
	}
	var input scimUser
	if !decodeSCIM(w, r, &input) {
		return
	}
	if strings.TrimSpace(input.UserName) == "" || len(input.UserName) > 320 || len(input.DisplayName) > 200 {
		scimError(w, http.StatusBadRequest, "userName is required and fields exceed limits")
		return
	}
	subject := strings.TrimSpace(input.ExternalID)
	if subject == "" {
		subject = strings.ToLower(strings.TrimSpace(input.UserName))
	}
	provider := "scim:" + organization.ID
	if _, err := s.store.GetExternalIdentity(r.Context(), provider, subject); err == nil {
		scimError(w, http.StatusConflict, "externalId or userName already exists")
		return
	} else if !errors.Is(err, storage.ErrNotFound) {
		scimError(w, http.StatusInternalServerError, "identity lookup failed")
		return
	}
	now := time.Now().UTC()
	accountID, _ := protocol.NewID("account")
	identityID, _ := protocol.NewID("identity")
	name := strings.TrimSpace(input.DisplayName)
	if name == "" {
		name = strings.TrimSpace(input.UserName)
	}
	if err := s.store.CreateAccount(r.Context(), protocol.Account{ID: accountID, Name: name, CreatedAt: now}); err != nil {
		writeSCIMStorageError(w, err)
		return
	}
	identity := protocol.ExternalIdentity{ID: identityID, Provider: provider, Subject: subject, AccountID: accountID, Email: primarySCIMEmail(input), CreatedAt: now}
	if identity.Email == "" {
		identity.Email = strings.ToLower(strings.TrimSpace(input.UserName))
	}
	if err := s.store.PutExternalIdentity(r.Context(), identity); err != nil {
		writeSCIMStorageError(w, err)
		return
	}
	member := protocol.OrganizationMember{OrganizationID: organization.ID, AccountID: accountID, Role: "member", Status: "active", JoinedAt: now}
	if err := s.store.PutOrganizationMember(r.Context(), member); err != nil {
		writeSCIMStorageError(w, err)
		return
	}
	value, _ := s.scimUser(r.Context(), identity)
	s.audit(r, auth.Claims{AccountID: organization.OwnerAccountID, Subject: "scim"}, "scim.user.created", "external_identity", identity.ID, nil)
	writeSCIM(w, http.StatusCreated, value)
}

func (s *Server) listSCIMUsers(w http.ResponseWriter, r *http.Request) {
	organization, ok := s.authorizeSCIM(w, r)
	if !ok {
		return
	}
	identities, err := s.store.ListExternalIdentities(r.Context(), "scim:"+organization.ID, 1000)
	if err != nil {
		writeSCIMStorageError(w, err)
		return
	}
	resources := make([]scimUser, 0, len(identities))
	filter := strings.TrimSpace(r.URL.Query().Get("filter"))
	for _, identity := range identities {
		value, err := s.scimUser(r.Context(), identity)
		if err != nil {
			continue
		}
		if filter != "" && !matchSCIMUserFilter(value, filter) {
			continue
		}
		resources = append(resources, value)
	}
	start, count := scimPage(r, len(resources))
	page := resources[start:count]
	writeSCIM(w, http.StatusOK, map[string]any{"schemas": []string{scimListSchema}, "totalResults": len(resources), "startIndex": start + 1, "itemsPerPage": len(page), "Resources": page})
}

func (s *Server) getSCIMUser(w http.ResponseWriter, r *http.Request) {
	organization, ok := s.authorizeSCIM(w, r)
	if !ok {
		return
	}
	identity, err := s.scimIdentity(r.Context(), organization.ID, r.PathValue("identityID"))
	if err != nil {
		writeSCIMStorageError(w, err)
		return
	}
	value, err := s.scimUser(r.Context(), identity)
	if err != nil {
		writeSCIMStorageError(w, err)
		return
	}
	writeSCIM(w, http.StatusOK, value)
}

func (s *Server) replaceSCIMUser(w http.ResponseWriter, r *http.Request) {
	organization, ok := s.authorizeSCIM(w, r)
	if !ok {
		return
	}
	identity, err := s.scimIdentity(r.Context(), organization.ID, r.PathValue("identityID"))
	if err != nil {
		writeSCIMStorageError(w, err)
		return
	}
	var input scimUser
	if !decodeSCIM(w, r, &input) {
		return
	}
	if strings.TrimSpace(input.UserName) == "" {
		scimError(w, http.StatusBadRequest, "userName is required")
		return
	}
	if err := s.updateSCIMUser(r.Context(), organization.ID, &identity, input.DisplayName, primarySCIMEmail(input), input.Active); err != nil {
		writeSCIMStorageError(w, err)
		return
	}
	value, _ := s.scimUser(r.Context(), identity)
	writeSCIM(w, http.StatusOK, value)
}

func (s *Server) patchSCIMUser(w http.ResponseWriter, r *http.Request) {
	organization, ok := s.authorizeSCIM(w, r)
	if !ok {
		return
	}
	identity, err := s.scimIdentity(r.Context(), organization.ID, r.PathValue("identityID"))
	if err != nil {
		writeSCIMStorageError(w, err)
		return
	}
	var input struct {
		Schemas    []string `json:"schemas"`
		Operations []struct {
			Op    string          `json:"op"`
			Path  string          `json:"path"`
			Value json.RawMessage `json:"value"`
		} `json:"Operations"`
	}
	if !decodeSCIM(w, r, &input) || len(input.Operations) == 0 || !containsString(input.Schemas, scimPatchSchema) {
		if len(input.Operations) == 0 {
			scimError(w, http.StatusBadRequest, "PatchOp Operations are required")
		}
		return
	}
	value, err := s.scimUser(r.Context(), identity)
	if err != nil {
		writeSCIMStorageError(w, err)
		return
	}
	for _, operation := range input.Operations {
		switch strings.ToLower(operation.Path) {
		case "active":
			_ = json.Unmarshal(operation.Value, &value.Active)
		case "displayname":
			_ = json.Unmarshal(operation.Value, &value.DisplayName)
		case "emails":
			_ = json.Unmarshal(operation.Value, &value.Emails)
		default:
			scimError(w, http.StatusBadRequest, "unsupported User patch path")
			return
		}
	}
	if err := s.updateSCIMUser(r.Context(), organization.ID, &identity, value.DisplayName, primarySCIMEmail(value), value.Active); err != nil {
		writeSCIMStorageError(w, err)
		return
	}
	value, _ = s.scimUser(r.Context(), identity)
	writeSCIM(w, http.StatusOK, value)
}

func (s *Server) deleteSCIMUser(w http.ResponseWriter, r *http.Request) {
	organization, ok := s.authorizeSCIM(w, r)
	if !ok {
		return
	}
	identity, err := s.scimIdentity(r.Context(), organization.ID, r.PathValue("identityID"))
	if err != nil {
		writeSCIMStorageError(w, err)
		return
	}
	if err := s.store.RemoveOrganizationMember(r.Context(), organization.ID, identity.AccountID, time.Now().UTC()); err != nil && !errors.Is(err, storage.ErrNotFound) {
		writeSCIMStorageError(w, err)
		return
	}
	s.audit(r, auth.Claims{AccountID: organization.OwnerAccountID, Subject: "scim"}, "scim.user.deactivated", "external_identity", identity.ID, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) scimIdentity(ctx context.Context, organizationID, identityID string) (protocol.ExternalIdentity, error) {
	value, err := s.store.GetExternalIdentityByID(ctx, identityID)
	if err != nil || value.Provider != "scim:"+organizationID {
		return protocol.ExternalIdentity{}, storage.ErrNotFound
	}
	return value, nil
}

func (s *Server) scimUser(ctx context.Context, identity protocol.ExternalIdentity) (scimUser, error) {
	account, err := s.store.GetAccount(ctx, identity.AccountID)
	if err != nil {
		return scimUser{}, err
	}
	organizationID := strings.TrimPrefix(identity.Provider, "scim:")
	_, memberErr := s.store.GetOrganizationMember(ctx, organizationID, identity.AccountID)
	value := scimUser{Schemas: []string{scimUserSchema}, ID: identity.ID, ExternalID: identity.Subject, UserName: identity.Email, DisplayName: account.Name, Active: memberErr == nil, Meta: map[string]any{"resourceType": "User", "created": identity.CreatedAt}}
	if value.UserName == "" {
		value.UserName = identity.Subject
	}
	if identity.Email != "" {
		value.Emails = []scimEmail{{Value: identity.Email, Primary: true}}
	}
	return value, nil
}

func (s *Server) updateSCIMUser(ctx context.Context, organizationID string, identity *protocol.ExternalIdentity, name, email string, active bool) error {
	account, err := s.store.GetAccount(ctx, identity.AccountID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(name) != "" {
		account.Name = strings.TrimSpace(name)
		if err := s.store.UpdateAccount(ctx, account); err != nil {
			return err
		}
	}
	if strings.TrimSpace(email) != "" {
		identity.Email = strings.ToLower(strings.TrimSpace(email))
		if err := s.store.PutExternalIdentity(ctx, *identity); err != nil {
			return err
		}
	}
	if active {
		return s.store.PutOrganizationMember(ctx, protocol.OrganizationMember{OrganizationID: organizationID, AccountID: identity.AccountID, Role: "member", Status: "active", JoinedAt: time.Now().UTC()})
	}
	err = s.store.RemoveOrganizationMember(ctx, organizationID, identity.AccountID, time.Now().UTC())
	if errors.Is(err, storage.ErrNotFound) {
		return nil
	}
	return err
}

func (s *Server) createSCIMGroup(w http.ResponseWriter, r *http.Request) {
	organization, ok := s.authorizeSCIM(w, r)
	if !ok {
		return
	}
	var input scimGroup
	if !decodeSCIM(w, r, &input) {
		return
	}
	if strings.TrimSpace(input.DisplayName) == "" {
		scimError(w, http.StatusBadRequest, "displayName is required")
		return
	}
	id, _ := protocol.NewID("team")
	team := protocol.Team{ID: id, OrganizationID: organization.ID, Name: strings.TrimSpace(input.DisplayName), Slug: "scim-" + strings.ToLower(id[len(id)-12:]), CreatedAt: time.Now().UTC()}
	if err := s.store.CreateTeam(r.Context(), team); err != nil {
		writeSCIMStorageError(w, err)
		return
	}
	if err := s.replaceSCIMGroupMembers(r.Context(), organization.ID, team.ID, input.Members); err != nil {
		writeSCIMStorageError(w, err)
		return
	}
	value, _ := s.scimGroup(r.Context(), organization.ID, team)
	writeSCIM(w, http.StatusCreated, value)
}

func (s *Server) listSCIMGroups(w http.ResponseWriter, r *http.Request) {
	organization, ok := s.authorizeSCIM(w, r)
	if !ok {
		return
	}
	teams, err := s.store.ListTeams(r.Context(), organization.ID)
	if err != nil {
		writeSCIMStorageError(w, err)
		return
	}
	resources := make([]scimGroup, 0, len(teams))
	for _, team := range teams {
		value, err := s.scimGroup(r.Context(), organization.ID, team)
		if err == nil {
			resources = append(resources, value)
		}
	}
	start, count := scimPage(r, len(resources))
	page := resources[start:count]
	writeSCIM(w, http.StatusOK, map[string]any{"schemas": []string{scimListSchema}, "totalResults": len(resources), "startIndex": start + 1, "itemsPerPage": len(page), "Resources": page})
}

func (s *Server) getSCIMGroup(w http.ResponseWriter, r *http.Request) {
	organization, ok := s.authorizeSCIM(w, r)
	if !ok {
		return
	}
	team, err := s.store.GetTeam(r.Context(), r.PathValue("groupID"))
	if err != nil || team.OrganizationID != organization.ID {
		writeSCIMStorageError(w, storage.ErrNotFound)
		return
	}
	value, err := s.scimGroup(r.Context(), organization.ID, team)
	if err != nil {
		writeSCIMStorageError(w, err)
		return
	}
	writeSCIM(w, http.StatusOK, value)
}

func (s *Server) replaceSCIMGroup(w http.ResponseWriter, r *http.Request) {
	organization, ok := s.authorizeSCIM(w, r)
	if !ok {
		return
	}
	team, err := s.store.GetTeam(r.Context(), r.PathValue("groupID"))
	if err != nil || team.OrganizationID != organization.ID {
		writeSCIMStorageError(w, storage.ErrNotFound)
		return
	}
	var input scimGroup
	if !decodeSCIM(w, r, &input) {
		return
	}
	if strings.TrimSpace(input.DisplayName) == "" {
		scimError(w, http.StatusBadRequest, "displayName is required")
		return
	}
	team.Name = strings.TrimSpace(input.DisplayName)
	if err := s.store.UpdateTeam(r.Context(), team); err != nil {
		writeSCIMStorageError(w, err)
		return
	}
	if err := s.replaceSCIMGroupMembers(r.Context(), organization.ID, team.ID, input.Members); err != nil {
		writeSCIMStorageError(w, err)
		return
	}
	value, _ := s.scimGroup(r.Context(), organization.ID, team)
	writeSCIM(w, http.StatusOK, value)
}

func (s *Server) patchSCIMGroup(w http.ResponseWriter, r *http.Request) {
	organization, ok := s.authorizeSCIM(w, r)
	if !ok {
		return
	}
	team, err := s.store.GetTeam(r.Context(), r.PathValue("groupID"))
	if err != nil || team.OrganizationID != organization.ID {
		writeSCIMStorageError(w, storage.ErrNotFound)
		return
	}
	var input struct {
		Schemas    []string `json:"schemas"`
		Operations []struct {
			Op    string          `json:"op"`
			Path  string          `json:"path"`
			Value json.RawMessage `json:"value"`
		} `json:"Operations"`
	}
	if !decodeSCIM(w, r, &input) || len(input.Operations) == 0 || !containsString(input.Schemas, scimPatchSchema) {
		if len(input.Operations) == 0 {
			scimError(w, http.StatusBadRequest, "PatchOp Operations are required")
		}
		return
	}
	for _, operation := range input.Operations {
		op := strings.ToLower(operation.Op)
		path := strings.ToLower(operation.Path)
		if path == "displayname" {
			if json.Unmarshal(operation.Value, &team.Name) != nil || strings.TrimSpace(team.Name) == "" {
				scimError(w, http.StatusBadRequest, "invalid displayName")
				return
			}
			if err := s.store.UpdateTeam(r.Context(), team); err != nil {
				writeSCIMStorageError(w, err)
				return
			}
			continue
		}
		if !strings.HasPrefix(path, "members") {
			scimError(w, http.StatusBadRequest, "unsupported Group patch path")
			return
		}
		var members []scimMember
		if json.Unmarshal(operation.Value, &members) != nil {
			scimError(w, http.StatusBadRequest, "invalid members")
			return
		}
		for _, member := range members {
			identity, err := s.scimIdentity(r.Context(), organization.ID, member.Value)
			if err != nil {
				writeSCIMStorageError(w, err)
				return
			}
			if op == "remove" {
				err = s.store.RemoveTeamMember(r.Context(), team.ID, identity.AccountID, time.Now().UTC())
				if errors.Is(err, storage.ErrNotFound) {
					err = nil
				}
			} else {
				err = s.store.PutTeamMember(r.Context(), protocol.TeamMember{TeamID: team.ID, AccountID: identity.AccountID, Role: "member", Status: "active", JoinedAt: time.Now().UTC()})
			}
			if err != nil {
				writeSCIMStorageError(w, err)
				return
			}
		}
	}
	value, _ := s.scimGroup(r.Context(), organization.ID, team)
	writeSCIM(w, http.StatusOK, value)
}

func (s *Server) deleteSCIMGroup(w http.ResponseWriter, r *http.Request) {
	organization, ok := s.authorizeSCIM(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteTeam(r.Context(), organization.ID, r.PathValue("groupID")); err != nil {
		writeSCIMStorageError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) scimGroup(ctx context.Context, organizationID string, team protocol.Team) (scimGroup, error) {
	members, err := s.store.ListTeamMembers(ctx, team.ID)
	if err != nil {
		return scimGroup{}, err
	}
	identities, err := s.store.ListExternalIdentities(ctx, "scim:"+organizationID, 1000)
	if err != nil {
		return scimGroup{}, err
	}
	byAccount := make(map[string]protocol.ExternalIdentity, len(identities))
	for _, identity := range identities {
		byAccount[identity.AccountID] = identity
	}
	result := scimGroup{Schemas: []string{scimGroupSchema}, ID: team.ID, ExternalID: team.Slug, DisplayName: team.Name, Meta: map[string]any{"resourceType": "Group", "created": team.CreatedAt}}
	for _, member := range members {
		if identity, ok := byAccount[member.AccountID]; ok {
			account, _ := s.store.GetAccount(ctx, member.AccountID)
			result.Members = append(result.Members, scimMember{Value: identity.ID, Display: account.Name})
		}
	}
	return result, nil
}

func (s *Server) replaceSCIMGroupMembers(ctx context.Context, organizationID, teamID string, wanted []scimMember) error {
	current, err := s.store.ListTeamMembers(ctx, teamID)
	if err != nil {
		return err
	}
	for _, member := range current {
		if err := s.store.RemoveTeamMember(ctx, teamID, member.AccountID, time.Now().UTC()); err != nil {
			return err
		}
	}
	for _, member := range wanted {
		identity, err := s.scimIdentity(ctx, organizationID, member.Value)
		if err != nil {
			return err
		}
		if err := s.store.PutTeamMember(ctx, protocol.TeamMember{TeamID: teamID, AccountID: identity.AccountID, Role: "member", Status: "active", JoinedAt: time.Now().UTC()}); err != nil {
			return err
		}
	}
	return nil
}

func primarySCIMEmail(value scimUser) string {
	for _, email := range value.Emails {
		if email.Primary {
			return email.Value
		}
	}
	if len(value.Emails) > 0 {
		return value.Emails[0].Value
	}
	return ""
}
func matchSCIMUserFilter(value scimUser, filter string) bool {
	const prefix = `userName eq "`
	if strings.HasPrefix(filter, prefix) && strings.HasSuffix(filter, `"`) {
		expected := strings.TrimSuffix(strings.TrimPrefix(filter, prefix), `"`)
		return strings.EqualFold(value.UserName, expected)
	}
	return false
}
func scimPage(r *http.Request, total int) (int, int) {
	start, _ := strconv.Atoi(r.URL.Query().Get("startIndex"))
	if start < 1 {
		start = 1
	}
	count, _ := strconv.Atoi(r.URL.Query().Get("count"))
	if count <= 0 || count > 200 {
		count = 200
	}
	zero := start - 1
	if zero > total {
		zero = total
	}
	end := min(total, zero+count)
	return zero, end
}
func decodeSCIM(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := decoder.Decode(target); err != nil {
		scimError(w, http.StatusBadRequest, "invalid SCIM JSON")
		return false
	}
	return true
}
func writeSCIM(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/scim+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func scimError(w http.ResponseWriter, status int, detail string) {
	writeSCIM(w, status, map[string]any{"schemas": []string{"urn:ietf:params:scim:api:messages:2.0:Error"}, "status": strconv.Itoa(status), "detail": detail})
}
func writeSCIMStorageError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, storage.ErrNotFound):
		scimError(w, http.StatusNotFound, "resource not found")
	case errors.Is(err, storage.ErrAlreadyExists):
		scimError(w, http.StatusConflict, "resource already exists")
	case errors.Is(err, storage.ErrUnauthorized):
		scimError(w, http.StatusForbidden, "operation denied")
	default:
		scimError(w, http.StatusInternalServerError, "storage operation failed")
	}
}
