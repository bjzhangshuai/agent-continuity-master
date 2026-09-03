package httpapi

import (
	"github.com/agent-continuity/agent-continuity-master/internal/auth"
	"github.com/agent-continuity/agent-continuity-master/internal/tenancy"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
	"golang.org/x/crypto/bcrypt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const portalCookie = "ac_portal_session"

var usernamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._@-]{2,79}$`)

// Limits expensive password verification independently of HTTP concurrency.
var passwordSlots = make(chan struct{}, 2)
var dummyPasswordHash = []byte("$2a$10$7EqJtq98hPqEX7fNZaFWoO5rPuSbRMZXFFFD/XGYVT41FHMmK.iSm")

func WithPlatformAdmins(accounts string) Option {
	return func(s *Server) {
		s.platformAdmins = map[string]bool{}
		for _, id := range strings.Split(accounts, ",") {
			id = strings.TrimSpace(id)
			if id != "" {
				s.platformAdmins[id] = true
			}
		}
	}
}
func (s *Server) superAdmin(c auth.Claims) bool {
	return c.APIKeyID == "" && auth.HasScope(c, "*") && s.platformAdmins[c.AccountID]
}
func portalOriginOK(r *http.Request) bool {
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, e := url.Parse(origin)
		if e != nil || u.Host != r.Host || u.User != nil {
			return false
		}
		if r.TLS != nil && u.Scheme != "https" {
			return false
		}
	}
	return true
}
func (s *Server) portalCookieValue(r *http.Request, value string, age int) *http.Cookie {
	return &http.Cookie{Name: portalCookie, Value: value, Path: "/", HttpOnly: true, Secure: r.TLS != nil || strings.HasPrefix(s.billing.BaseURL, "https://"), SameSite: http.SameSiteStrictMode, MaxAge: age}
}
func (s *Server) portalSessionClaims(w http.ResponseWriter, r *http.Request) (auth.Claims, bool) {
	cookie, err := r.Cookie(portalCookie)
	if err != nil || !strings.HasPrefix(cookie.Value, "pss_") {
		writeError(w, 401, "login_required", "请登录账号，或连接有效访问凭据 / Login required")
		return auth.Claims{}, false
	}
	if !portalOriginOK(r) || (r.Method != "GET" && r.Method != "HEAD" && r.Header.Get("X-Portal-Request") != "1") {
		writeError(w, 403, "csrf_denied", "Same-origin portal request required")
		return auth.Claims{}, false
	}
	session, err := s.store.GetPortalSession(r.Context(), auth.HashOpaque(cookie.Value))
	if err != nil {
		http.SetCookie(w, s.portalCookieValue(r, "", -1))
		writeError(w, 401, "session_expired", "登录已失效，请重新登录 / Session expired")
		return auth.Claims{}, false
	}
	return auth.Claims{AccountID: session.AccountID, Subject: "portal:" + session.Username, Scopes: []string{"*"}}, true
}
func (s *Server) portalLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if r.TLS == nil && !strings.HasPrefix(s.billing.BaseURL, "https://") && host != "localhost" && (net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback()) {
		writeError(w, 403, "https_required", "Account login requires HTTPS outside loopback")
		return
	}
	if !portalOriginOK(r) || r.Header.Get("X-Portal-Request") != "1" {
		writeError(w, 403, "csrf_denied", "Same-origin portal request required")
		return
	}
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !s.decodeJSON(w, r, &in) {
		return
	}
	in.Username = strings.ToLower(strings.TrimSpace(in.Username))
	if !usernamePattern.MatchString(in.Username) || len(in.Password) > 72 {
		writeError(w, 401, "login_failed", "账号或密码不正确 / Invalid login")
		return
	}
	if ok, _ := s.limiter.allow("login-user:"+in.Username, 10); !ok {
		writeError(w, 429, "login_limited", "Too many login attempts; retry in a minute")
		return
	}
	select {
	case passwordSlots <- struct{}{}:
		defer func() { <-passwordSlots }()
	default:
		writeError(w, 429, "login_busy", "Login busy; retry shortly")
		return
	}
	login, err := s.store.GetPortalLogin(r.Context(), in.Username)
	hash := dummyPasswordHash
	if err == nil {
		hash = []byte(login.PasswordHash)
	}
	mismatch := bcrypt.CompareHashAndPassword(hash, []byte(in.Password))
	if err != nil || mismatch != nil {
		writeError(w, 401, "login_failed", "账号或密码不正确 / Invalid login")
		return
	}
	raw, err := auth.NewOpaqueToken("pss")
	if err != nil {
		writeInternal(w, err)
		return
	}
	now := time.Now().UTC()
	session := tenancy.Session{Hash: auth.HashOpaque(raw), AccountID: login.AccountID, Username: login.Username, CreatedAt: now, LastSeen: now, ExpiresAt: now.Add(8 * time.Hour)}
	if err = s.store.CreatePortalSession(r.Context(), session); err != nil {
		writeStorageError(w, err)
		return
	}
	// Login replaces the browser's previous identity, never upgrades its session.
	if old, e := r.Cookie(portalCookie); e == nil {
		_ = s.store.DeletePortalSession(r.Context(), auth.HashOpaque(old.Value))
	}
	http.SetCookie(w, s.portalCookieValue(r, raw, 8*3600))
	writeJSON(w, 200, map[string]any{"account_id": login.AccountID, "username": login.Username, "display_name": login.DisplayName, "expires_at": session.ExpiresAt})
}
func (s *Server) portalLogout(w http.ResponseWriter, r *http.Request) {
	if !portalOriginOK(r) || r.Header.Get("X-Portal-Request") != "1" {
		writeError(w, 403, "csrf_denied", "Same-origin portal request required")
		return
	}
	if c, e := r.Cookie(portalCookie); e == nil {
		if err := s.store.DeletePortalSession(r.Context(), auth.HashOpaque(c.Value)); err != nil {
			writeStorageError(w, err)
			return
		}
	}
	http.SetCookie(w, s.portalCookieValue(r, "", -1))
	w.WriteHeader(204)
}
func (s *Server) portalCreateUser(w http.ResponseWriter, r *http.Request) {
	c, ok := s.authorizeScope(w, r, "*")
	if !ok || c.APIKeyID != "" {
		if ok {
			writeError(w, 403, "interactive_account_required", "Use an interactive account")
		}
		return
	}
	var in struct {
		Username    string `json:"username"`
		Password    string `json:"password"`
		DisplayName string `json:"display_name"`
		TenantID    string `json:"tenant_id"`
		Role        string `json:"role"`
		Self        bool   `json:"self"`
	}
	if !s.decodeJSON(w, r, &in) {
		return
	}
	in.Username = strings.ToLower(strings.TrimSpace(in.Username))
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	if !usernamePattern.MatchString(in.Username) || len(in.Password) < 12 || len(in.Password) > 72 || in.DisplayName == "" || len(in.DisplayName) > 120 {
		writeError(w, 400, "invalid_user", "用户名 3–80 字符，密码 12–72 字节，显示名必填 / Invalid account fields")
		return
	}
	if !in.Self {
		if in.TenantID != "" {
			m, e := s.store.GetOrganizationMember(r.Context(), in.TenantID, c.AccountID)
			if !s.superAdmin(c) && (e != nil || m.Role != "owner") {
				writeError(w, 403, "owner_required", "Tenant owner required")
				return
			}
			if _, e = s.store.GetOrganization(r.Context(), in.TenantID); e != nil {
				writeStorageError(w, e)
				return
			}
			if !validTenantRole(in.Role) {
				writeError(w, 400, "invalid_role", "admin, member, viewer or billing required")
				return
			}
		} else if !s.superAdmin(c) {
			writeError(w, 403, "super_admin_required", "Platform super administrator required")
			return
		}
	}
	if _, err := s.store.GetPortalLogin(r.Context(), in.Username); err == nil {
		writeError(w, 409, "login_exists", "Login already exists")
		return
	}
	select {
	case passwordSlots <- struct{}{}:
		defer func() { <-passwordSlots }()
	default:
		writeError(w, 429, "login_busy", "Retry shortly")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), 10)
	if err != nil {
		writeInternal(w, err)
		return
	}
	accountID := c.AccountID
	var account *protocol.Account
	var member *protocol.OrganizationMember
	if !in.Self {
		accountID, err = protocol.NewID("acct")
		if err != nil {
			writeInternal(w, err)
			return
		}
		account = &protocol.Account{ID: accountID, Name: in.DisplayName, CreatedAt: time.Now().UTC()}
	}
	if !in.Self && in.TenantID != "" {
		member = &protocol.OrganizationMember{OrganizationID: in.TenantID, AccountID: accountID, Role: in.Role, Status: "active", JoinedAt: time.Now().UTC()}
	}
	if err = s.store.ProvisionPortalUser(r.Context(), account, tenancy.Login{Username: in.Username, AccountID: accountID, DisplayName: in.DisplayName, PasswordHash: string(hash)}, member); err != nil {
		writeStorageError(w, err)
		return
	}
	s.audit(r, c, "portal.account.created", "account", accountID, nil)
	writeJSON(w, 201, map[string]string{"account_id": accountID, "username": in.Username, "display_name": in.DisplayName})
}
func validTenantRole(role string) bool {
	switch role {
	case "admin", "member", "viewer", "billing":
		return true
	}
	return false
}
