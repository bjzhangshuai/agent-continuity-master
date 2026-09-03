package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

type Claims struct {
	AccountID string   `json:"account_id"`
	Subject   string   `json:"sub"`
	Scopes    []string `json:"scopes,omitempty"`
	APIKeyID  string   `json:"api_key_id,omitempty"`
	IssuedAt  int64    `json:"iat"`
	ExpiresAt int64    `json:"exp"`
	TokenID   string   `json:"jti"`
}

type Manager struct {
	secret []byte
	now    func() time.Time
}

func NewManager(secret string) (*Manager, error) {
	if len(secret) < 32 {
		return nil, errors.New("JWT secret must be at least 32 characters")
	}
	return &Manager{secret: []byte(secret), now: time.Now}, nil
}

func (m *Manager) Issue(accountID, subject string, ttl time.Duration) (string, error) {
	return m.IssueScoped(accountID, subject, []string{"*"}, "", ttl)
}

func (m *Manager) IssueScoped(accountID, subject string, scopes []string, apiKeyID string, ttl time.Duration) (string, error) {
	if accountID == "" || subject == "" {
		return "", errors.New("account and subject are required")
	}
	if ttl <= 0 || ttl > time.Hour {
		return "", errors.New("token ttl must be between zero and one hour")
	}
	id, err := protocol.NewID("tok")
	if err != nil {
		return "", err
	}
	now := m.now()
	claims := Claims{AccountID: accountID, Subject: subject, Scopes: append([]string(nil), scopes...), APIKeyID: apiKeyID, IssuedAt: now.Unix(), ExpiresAt: now.Add(ttl).Unix(), TokenID: id}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payloadBytes, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(payloadBytes)
	unsigned := header + "." + payload
	return unsigned + "." + m.sign(unsigned), nil
}

func NewOpaqueToken(prefix string) (string, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", err
	}
	return prefix + "_" + base64.RawURLEncoding.EncodeToString(secret), nil
}

func HashOpaque(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func HasScope(claims Claims, required string) bool {
	for _, scope := range claims.Scopes {
		if scope == "*" || scope == required {
			return true
		}
		if strings.HasSuffix(scope, ":*") && strings.HasPrefix(required, strings.TrimSuffix(scope, "*")) {
			return true
		}
	}
	return false
}

func (m *Manager) Parse(token string) (Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Claims{}, errors.New("malformed token")
	}
	if !hmac.Equal([]byte(parts[2]), []byte(m.sign(parts[0]+"."+parts[1]))) {
		return Claims{}, errors.New("invalid token signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Claims{}, errors.New("invalid token payload encoding")
	}
	var claims Claims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return Claims{}, errors.New("invalid token claims")
	}
	now := m.now().Unix()
	if claims.AccountID == "" || claims.Subject == "" || claims.TokenID == "" {
		return Claims{}, errors.New("incomplete token claims")
	}
	if claims.ExpiresAt <= now {
		return Claims{}, errors.New("token expired")
	}
	if claims.IssuedAt > now+60 {
		return Claims{}, errors.New("token issued in the future")
	}
	if claims.ExpiresAt-claims.IssuedAt > int64(time.Hour.Seconds()) {
		return Claims{}, errors.New("token lifetime exceeds maximum")
	}
	return claims, nil
}

func (m *Manager) sign(value string) string {
	mac := hmac.New(sha256.New, m.secret)
	_, _ = mac.Write([]byte(value))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func Bearer(header string) (string, error) {
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return "", errors.New("Bearer token required")
	}
	return parts[1], nil
}

func ParseTTLSeconds(value int64) (time.Duration, error) {
	if value == 0 {
		value = 900
	}
	if value < 60 || value > 3600 {
		return 0, fmt.Errorf("ttl_seconds %s must be between 60 and 3600", strconv.FormatInt(value, 10))
	}
	return time.Duration(value) * time.Second, nil
}
