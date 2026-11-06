package httpapi

import (
	"encoding/json"
	"github.com/agent-continuity/agent-continuity-master/internal/kms"
	"github.com/agent-continuity/agent-continuity-master/internal/operations"
	"net/http"
	"regexp"
	"time"
)

func WithKMS(m *kms.Manager) Option { return func(s *Server) { s.keyManager = m } }

type vaultValue struct {
	Ciphertext []byte    `json:"ciphertext"`
	Updated    time.Time `json:"updated_at"`
}

var secretName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,80}$`)

func (s *Server) registerVaultRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/security/kms", s.kmsStatus)
	mux.HandleFunc("PUT /v1/security/secrets/{name}", s.putSecret)
	mux.HandleFunc("GET /v1/security/secrets/{name}", s.getSecret)
	mux.HandleFunc("POST /v1/security/secrets/{name}/rewrap", s.rewrapSecret)
}
func (s *Server) kmsStatus(w http.ResponseWriter, r *http.Request) {
	c, ok := s.authorizeScope(w, r, "secrets:read")
	if !ok {
		return
	}
	state := map[string]any{"enabled": s.keyManager != nil, "scope": "server-managed secrets; client E2EE private keys stay on devices"}
	if s.keyManager != nil {
		state["current"] = s.keyManager.KeyFor(c.AccountID + ":")
		state["simulation"] = s.keyManager.Config.Simulation
	}
	writeJSON(w, 200, state)
}
func (s *Server) putSecret(w http.ResponseWriter, r *http.Request)    { s.mutateSecret(w, r, false) }
func (s *Server) rewrapSecret(w http.ResponseWriter, r *http.Request) { s.mutateSecret(w, r, true) }
func (s *Server) mutateSecret(w http.ResponseWriter, r *http.Request, rewrap bool) {
	c, ok := s.authorizeScope(w, r, "secrets:write")
	if !ok {
		return
	}
	name := r.PathValue("name")
	if !secretName.MatchString(name) {
		writeError(w, 400, "invalid_name", "secret name is invalid")
		return
	}
	id := operations.ID("vault", c.AccountID, name)
	o, e := s.store.GetOperation(r.Context(), "vault", id)
	if e != nil && e != operations.ErrNotFound {
		workflowError(w, e)
		return
	}
	scope := c.AccountID + ":" + id
	var plain []byte
	if rewrap {
		if e != nil {
			workflowError(w, e)
			return
		}
		var v vaultValue
		if e = json.Unmarshal(o.Data, &v); e != nil {
			workflowError(w, e)
			return
		}
		plain, e = s.secretCipher.Decrypt(v.Ciphertext, scope)
		if e != nil {
			writeError(w, 503, "kms_unavailable", "key unavailable or revoked")
			return
		}
	} else {
		var q struct {
			Value           string `json:"value"`
			ExpectedVersion int64  `json:"expected_version"`
		}
		if !s.decodeJSON(w, r, &q) {
			return
		}
		if len(q.Value) > 64<<10 || q.ExpectedVersion != o.Version {
			writeError(w, 409, "version_conflict", "value exceeds limit or expected_version does not match")
			return
		}
		plain = []byte(q.Value)
	}
	defer clear(plain)
	encrypted, e := s.secretCipher.Encrypt(plain, scope)
	if e != nil {
		writeError(w, 503, "kms_unavailable", "KMS encryption unavailable; no plaintext saved")
		return
	}
	o.Kind = "vault"
	o.ID = id
	o.Owner = c.AccountID
	o.Status = "active"
	o.Due = time.Now()
	o.Data = operations.Encode(vaultValue{Ciphertext: encrypted, Updated: o.Due})
	if e = s.store.SaveOperation(r.Context(), o, o.Version, nil); e != nil {
		workflowError(w, e)
		return
	}
	s.audit(r, c, "vault.updated", "secret", id, nil)
	writeJSON(w, 200, map[string]any{"name": name, "version": o.Version + 1, "rewrapped": rewrap})
}
func (s *Server) getSecret(w http.ResponseWriter, r *http.Request) {
	c, ok := s.authorizeScope(w, r, "secrets:read")
	if !ok {
		return
	}
	name := r.PathValue("name")
	if !secretName.MatchString(name) {
		writeError(w, 404, "not_found", "secret not found")
		return
	}
	id := operations.ID("vault", c.AccountID, name)
	o, e := s.store.GetOperation(r.Context(), "vault", id)
	if e != nil || o.Owner != c.AccountID {
		writeError(w, 404, "not_found", "secret not found")
		return
	}
	var v vaultValue
	if e = json.Unmarshal(o.Data, &v); e != nil {
		workflowError(w, e)
		return
	}
	plain, e := s.secretCipher.Decrypt(v.Ciphertext, c.AccountID+":"+id)
	if e != nil {
		writeError(w, 503, "kms_unavailable", "key unavailable or revoked")
		return
	}
	defer clear(plain)
	s.audit(r, c, "vault.read", "secret", id, nil)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"name": name, "version": o.Version, "value": string(plain)})
}
