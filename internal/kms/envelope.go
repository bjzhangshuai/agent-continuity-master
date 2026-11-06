// Package kms provides envelope encryption for server-managed secrets, not
// client-owned E2EE task keys. Cloud calls wrap a random 256-bit data key only.
package kms

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"
)

type Wrapper interface {
	Wrap(context.Context, []byte, string) (string, error)
	Unwrap(context.Context, string, string) ([]byte, error)
}
type Key struct {
	ID              string `json:"id"`
	Cloud           string `json:"cloud"`
	Endpoint        string `json:"endpoint"`
	Region          string `json:"region"`
	KeyID           string `json:"key_id"`
	CredentialsFile string `json:"credentials_file"`
	Disabled        bool   `json:"disabled"`
}
type Config struct {
	TenantKeys map[string]string `json:"tenant_keys,omitempty"`
	Current    string            `json:"current"`
	Simulation bool              `json:"simulation"`
	Keys       []Key             `json:"keys"`
}
type Manager struct {
	Config   Config
	Wrappers map[string]Wrapper
}
type envelope struct {
	Version int    `json:"v"`
	Key     string `json:"key"`
	Wrapped string `json:"wrapped_key"`
	Nonce   []byte `json:"nonce"`
	Data    []byte `json:"ciphertext"`
}

func Build(raw string, master []byte) (*Manager, error) {
	if raw == "" {
		return nil, nil
	}
	var cfg Config
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&cfg); err != nil {
		return nil, err
	}
	if cfg.Simulation && len(master) != 32 {
		return nil, errors.New("simulated KMS needs a stable 32-byte master key")
	}
	if cfg.Current == "" || len(cfg.Keys) == 0 || len(cfg.Keys) > 32 {
		return nil, errors.New("KMS current and 1..32 keys required")
	}
	m := &Manager{Config: cfg, Wrappers: map[string]Wrapper{}}
	seen := map[string]bool{}
	for _, k := range cfg.Keys {
		if k.ID == "" || seen[k.ID] || k.KeyID == "" {
			return nil, errors.New("KMS key IDs must be nonempty and unique")
		}
		seen[k.ID] = true
		if !strings.Contains("|aws|alicloud|tencentcloud|azure|", "|"+k.Cloud+"|") {
			return nil, errors.New("unsupported KMS cloud")
		}
		if k.Disabled {
			continue
		}
		if cfg.Simulation {
			key := sha256.Sum256(append(append([]byte(nil), master...), []byte("kms:"+k.ID+":"+k.KeyID)...))
			m.Wrappers[k.ID] = Local{Key: key[:]}
			continue
		}
		u, err := url.Parse(k.Endpoint)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || k.CredentialsFile == "" {
			return nil, errors.New("live KMS requires HTTPS endpoint and mounted credential file")
		}
		if k.Cloud != "azure" && k.Region == "" {
			return nil, errors.New("KMS region required")
		}
		if k.Cloud == "azure" && (strings.Contains(k.KeyID, "..") || strings.ContainsAny(k.KeyID, "?#") || len(strings.Split(k.KeyID, "/")) != 2) {
			return nil, errors.New("Azure key_id must be key-name/version")
		}
		if _, err = os.Stat(k.CredentialsFile); err != nil {
			return nil, fmt.Errorf("KMS credential file unavailable for %s", k.ID)
		}
		m.Wrappers[k.ID] = Cloud{Key: k}
	}
	for _, id := range cfg.TenantKeys {
		if m.Wrappers[id] == nil {
			return nil, errors.New("tenant KMS key unavailable")
		}
	}
	if m.Wrappers[cfg.Current] == nil {
		return nil, errors.New("current KMS key is missing or disabled")
	}
	return m, nil
}
func aead(key []byte) (cipher.AEAD, error) {
	b, e := aes.NewCipher(key)
	if e != nil {
		return nil, e
	}
	return cipher.NewGCM(b)
}
func (m *Manager) KeyFor(scope string) string {
	owner, _, _ := strings.Cut(scope, ":")
	if id := m.Config.TenantKeys[owner]; id != "" {
		return id
	}
	return m.Config.Current
}
func (m *Manager) Encrypt(plain []byte, scope string) ([]byte, error) {
	if scope == "" || len(plain) > 1<<20 {
		return nil, errors.New("KMS context required and plaintext limited to 1 MiB")
	}
	key := make([]byte, 32)
	if _, e := io.ReadFull(rand.Reader, key); e != nil {
		return nil, e
	}
	defer clear(key)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	wrapped, e := m.Wrappers[m.KeyFor(scope)].Wrap(ctx, key, scope)
	if e != nil {
		return nil, e
	}
	a, e := aead(key)
	if e != nil {
		return nil, e
	}
	v := envelope{Version: 2, Key: m.KeyFor(scope), Wrapped: wrapped, Nonce: make([]byte, a.NonceSize())}
	if _, e = io.ReadFull(rand.Reader, v.Nonce); e != nil {
		return nil, e
	}
	v.Data = a.Seal(nil, v.Nonce, plain, []byte("2:"+v.Key+":"+scope))
	b, e := json.Marshal(v)
	return append([]byte{2}, b...), e
}
func (m *Manager) Decrypt(raw []byte, scope string) ([]byte, error) {
	if scope == "" || len(raw) < 2 || len(raw) > 2<<20 || raw[0] != 2 {
		return nil, errors.New("invalid KMS envelope")
	}
	var v envelope
	if json.Unmarshal(raw[1:], &v) != nil || v.Version != 2 {
		return nil, errors.New("invalid KMS envelope")
	}
	w := m.Wrappers[v.Key]
	if w == nil {
		return nil, errors.New("KMS key revoked or unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	key, e := w.Unwrap(ctx, v.Wrapped, scope)
	if e != nil {
		return nil, e
	}
	defer clear(key)
	if len(key) != 32 {
		return nil, errors.New("invalid KMS data key")
	}
	a, e := aead(key)
	if e != nil {
		return nil, e
	}
	if len(v.Nonce) != a.NonceSize() {
		return nil, errors.New("invalid nonce")
	}
	return a.Open(nil, v.Nonce, v.Data, []byte("2:"+v.Key+":"+scope))
}

type Local struct{ Key []byte }

func (l Local) Wrap(_ context.Context, p []byte, scope string) (string, error) {
	a, e := aead(l.Key)
	if e != nil {
		return "", e
	}
	nonce := make([]byte, a.NonceSize())
	if _, e = rand.Read(nonce); e != nil {
		return "", e
	}
	return base64.StdEncoding.EncodeToString(a.Seal(nonce, nonce, p, []byte(scope))), nil
}
func (l Local) Unwrap(_ context.Context, v, scope string) ([]byte, error) {
	a, e := aead(l.Key)
	if e != nil {
		return nil, e
	}
	b, e := base64.StdEncoding.DecodeString(v)
	if e != nil || len(b) < a.NonceSize() {
		return nil, errors.New("invalid wrapped key")
	}
	return a.Open(nil, b[:a.NonceSize()], b[a.NonceSize():], []byte(scope))
}
