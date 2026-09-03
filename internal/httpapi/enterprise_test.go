package httpapi

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/agent-continuity/agent-continuity-master/internal/identity"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

func TestSSOExchangeAutoProvisionAndReuse(t *testing.T) {
	f := newFixture(t)
	_, ownerToken := f.account("sso-owner")
	status, data := f.request(http.MethodPost, "/v1/organizations", ownerToken, map[string]any{"name": "SSO Enterprise", "slug": "sso-enterprise", "plan": "enterprise"}, nil)
	if status != http.StatusCreated {
		t.Fatalf("organization: %d %s", status, data)
	}
	organization := decode[protocol.Organization](t, data)
	private, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	exponent := make([]byte, 4)
	binary.BigEndian.PutUint32(exponent, uint32(private.PublicKey.E))
	for len(exponent) > 1 && exponent[0] == 0 {
		exponent = exponent[1:]
	}
	jwks, _ := json.Marshal(map[string]any{"keys": []map[string]string{{"kid": "enterprise-key", "kty": "RSA", "use": "sig", "alg": "RS256", "n": base64.RawURLEncoding.EncodeToString(private.PublicKey.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(exponent)}}})
	verifier, err := identity.NewOIDCVerifier("https://idp.example", "continuity", jwks)
	if err != nil {
		t.Fatal(err)
	}
	f.api.oidcVerifier = verifier
	f.api.oidcAutoProvision = true
	f.api.oidcOrganizationID = organization.ID
	now := time.Now().UTC()
	idToken := signSSOIDToken(t, private, map[string]any{"iss": "https://idp.example", "sub": "employee-1", "aud": "continuity", "iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(), "email": "employee@example.com", "email_verified": true, "name": "Employee One"})

	var accountID string
	for attempt := 0; attempt < 2; attempt++ {
		status, data = f.request(http.MethodPost, "/v1/auth/sso/exchange", "", map[string]string{"id_token": idToken}, nil)
		if status != http.StatusOK {
			t.Fatalf("SSO exchange %d: %d %s", attempt, status, data)
		}
		response := decode[map[string]any](t, data)
		if response["access_token"] == "" || response["refresh_token"] == "" {
			t.Fatalf("SSO tokens missing: %s", data)
		}
		claims, err := f.api.tokens.Parse(response["access_token"].(string))
		if err != nil {
			t.Fatal(err)
		}
		if accountID == "" {
			accountID = claims.AccountID
		} else if claims.AccountID != accountID {
			t.Fatalf("SSO identity created duplicate accounts: %s != %s", claims.AccountID, accountID)
		}
	}
	member, err := f.api.store.GetOrganizationMember(t.Context(), organization.ID, accountID)
	if err != nil || member.Status != "active" || member.Role != "member" {
		t.Fatalf("auto-provisioned membership missing: %+v %v", member, err)
	}
	parts := strings.Split(idToken, ".")
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	signature[0] ^= 1
	broken := parts[0] + "." + parts[1] + "." + base64.RawURLEncoding.EncodeToString(signature)
	status, _ = f.request(http.MethodPost, "/v1/auth/sso/exchange", "", map[string]string{"id_token": broken}, nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("tampered SSO token expected 401, got %d", status)
	}
}

func signSSOIDToken(t *testing.T, private *rsa.PrivateKey, claims any) string {
	t.Helper()
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "enterprise-key", "typ": "JWT"})
	payload, _ := json.Marshal(claims)
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, private, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func TestSCIMUsersGroupsAndDeprovisioning(t *testing.T) {
	f := newFixture(t)
	_, ownerToken := f.account("enterprise-owner")
	status, data := f.request(http.MethodPost, "/v1/organizations", ownerToken, map[string]any{"name": "Enterprise", "slug": "enterprise", "plan": "enterprise", "key_provider": "byok"}, nil)
	if status != http.StatusCreated {
		t.Fatalf("organization: %d %s", status, data)
	}
	organization := decode[protocol.Organization](t, data)
	scimToken := strings.Repeat("s", 40)
	f.api.scimBearerToken = scimToken
	f.api.scimOrganizationID = organization.ID
	status, data = f.request(http.MethodPost, "/scim/v2/Users", scimToken, map[string]any{"schemas": []string{scimUserSchema}, "externalId": "worker-1", "userName": "worker@example.com", "displayName": "Worker One", "active": true, "emails": []map[string]any{{"value": "worker@example.com", "primary": true}}}, nil)
	if status != http.StatusCreated {
		t.Fatalf("SCIM user: %d %s", status, data)
	}
	user := decode[scimUser](t, data)
	status, data = f.request(http.MethodPost, "/scim/v2/Groups", scimToken, map[string]any{"schemas": []string{scimGroupSchema}, "displayName": "Runtime Team", "members": []map[string]string{{"value": user.ID}}}, nil)
	if status != http.StatusCreated {
		t.Fatalf("SCIM group: %d %s", status, data)
	}
	group := decode[scimGroup](t, data)
	if len(group.Members) != 1 || group.Members[0].Value != user.ID {
		t.Fatalf("SCIM group membership: %+v", group)
	}
	status, data = f.request(http.MethodGet, "/scim/v2/Users?filter=userName%20eq%20%22worker@example.com%22", scimToken, nil, nil)
	if status != http.StatusOK || !bytes.Contains(data, []byte(user.ID)) {
		t.Fatalf("SCIM filter: %d %s", status, data)
	}
	status, data = f.request(http.MethodPatch, "/scim/v2/Users/"+user.ID, scimToken, map[string]any{"schemas": []string{scimPatchSchema}, "Operations": []map[string]any{{"op": "replace", "path": "active", "value": false}}}, nil)
	if status != http.StatusOK || !bytes.Contains(data, []byte(`"active":false`)) {
		t.Fatalf("SCIM deactivate: %d %s", status, data)
	}
	status, _ = f.request(http.MethodGet, "/scim/v2/Users", "wrong-token", nil, nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("invalid SCIM token expected 401, got %d", status)
	}
}
