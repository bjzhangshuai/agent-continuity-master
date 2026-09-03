// Package p2p provides capability-scoped, hash-verified direct Artifact transfer.
// The cloud control plane is not in the byte path; peers exchange the short-lived
// endpoint and bearer capability through an already-authorized Task channel.
package p2p

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const DefaultMaxBytes int64 = 1 << 30

type Offer struct {
	Digest    string    `json:"content_digest"`
	Token     string    `json:"capability_token"`
	ExpiresAt time.Time `json:"expires_at"`
}

type Sender struct {
	path      string
	digest    string
	token     string
	expiresAt time.Time
	maxBytes  int64
}

func NewSender(path, digest string, ttl time.Duration, maxBytes int64) (*Sender, Offer, error) {
	if ttl < time.Minute || ttl > 24*time.Hour {
		return nil, Offer{}, errors.New("peer capability TTL must be 1 minute..24 hours")
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	actual, size, err := digestFile(path, maxBytes)
	if err != nil {
		return nil, Offer{}, err
	}
	if size > maxBytes || actual != digest {
		return nil, Offer{}, errors.New("peer source does not match the declared SHA-256 digest")
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, Offer{}, err
	}
	token := "p2p_" + base64.RawURLEncoding.EncodeToString(secret)
	expiresAt := time.Now().UTC().Add(ttl)
	sender := &Sender{path: path, digest: digest, token: token, expiresAt: expiresAt, maxBytes: maxBytes}
	return sender, Offer{Digest: digest, Token: token, ExpiresAt: expiresAt}, nil
}

func (s *Sender) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if time.Now().After(s.expiresAt) {
		http.Error(w, "capability expired", http.StatusGone)
		return
	}
	wantedPath := "/v1/artifacts/" + strings.TrimPrefix(s.digest, "sha256:")
	if r.URL.Path != wantedPath {
		http.NotFound(w, r)
		return
	}
	provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if len(provided) != len(s.token) || subtle.ConstantTimeCompare([]byte(provided), []byte(s.token)) != 1 {
		w.Header().Set("WWW-Authenticate", `Bearer realm="artifact-peer"`)
		http.Error(w, "invalid capability", http.StatusUnauthorized)
		return
	}
	actual, size, err := digestFile(s.path, s.maxBytes)
	if err != nil || actual != s.digest {
		http.Error(w, "source integrity check failed", http.StatusConflict)
		return
	}
	file, err := os.Open(s.path)
	if err != nil {
		http.Error(w, "source unavailable", http.StatusServiceUnavailable)
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-SHA256", s.digest)
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	http.ServeContent(w, r, filepath.Base(s.path), s.expiresAt, file)
}

type Receiver struct {
	Client   *http.Client
	MaxBytes int64
}

func (r Receiver) Download(ctx context.Context, endpoint, token, digest, destination string) error {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("peer endpoint must be an absolute URL without credentials, query, or fragment")
	}
	host := parsed.Hostname()
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopback(host)) {
		return errors.New("peer endpoint must use HTTPS; HTTP is allowed only on loopback")
	}
	if !validDigest(digest) || !strings.HasPrefix(token, "p2p_") {
		return errors.New("peer token or content digest is invalid")
	}
	maxBytes := r.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o750); err != nil {
		return err
	}
	partial := destination + ".part"
	file, err := os.OpenFile(partial, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() > maxBytes {
		if err := file.Truncate(0); err != nil {
			return err
		}
		info, _ = file.Stat()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	if info.Size() > 0 {
		request.Header.Set("Range", "bytes="+strconv.FormatInt(info.Size(), 10)+"-")
	}
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute, Transport: &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 10 * time.Second}).DialContext, TLSHandshakeTimeout: 10 * time.Second}}
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("peer returned HTTP %d", response.StatusCode)
	}
	if info.Size() > 0 && response.StatusCode != http.StatusPartialContent {
		if err := file.Truncate(0); err != nil {
			return err
		}
		info, _ = file.Stat()
	}
	if header := response.Header.Get("X-Content-SHA256"); header != "" && header != digest {
		return errors.New("peer advertised a different content digest")
	}
	if _, err := file.Seek(info.Size(), io.SeekStart); err != nil {
		return err
	}
	written, err := io.Copy(file, io.LimitReader(response.Body, maxBytes-info.Size()+1))
	if err != nil {
		return err
	}
	if info.Size()+written > maxBytes {
		return errors.New("peer artifact exceeds configured maximum")
	}
	if err := file.Sync(); err != nil {
		return err
	}
	actual, _, err := digestFile(partial, maxBytes)
	if err != nil {
		return err
	}
	if actual != digest {
		// A complete response with the wrong digest cannot be resumed safely;
		// retaining it would poison every subsequent Range request. Interrupted
		// transfers still retain their partial file for normal resume.
		_ = file.Close()
		_ = os.Remove(partial)
		return errors.New("peer artifact hash mismatch")
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(partial, destination)
}

func digestFile(path string, maxBytes int64) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, io.LimitReader(file, maxBytes+1))
	if err != nil {
		return "", size, err
	}
	if size > maxBytes {
		return "", size, errors.New("artifact exceeds configured maximum")
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), size, nil
}

func validDigest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+64 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
