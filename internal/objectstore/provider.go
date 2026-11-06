// Package objectstore contains server-side object storage adapters. Providers never
// receive tenant-selected endpoints or credentials; routing is operator managed.
package objectstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

type Object struct {
	Key         string
	Size        int64
	ContentType string
	SHA256      string
}

// Put must consume the stream before returning. Retrying the same key with the
// same bytes is safe. Implementations must not grant public access to objects.
type Provider interface {
	Put(context.Context, Object, io.Reader) error
	Get(context.Context, string) (io.ReadCloser, error)
	// Delete removes one current object key, never a prefix/bucket. Versioned
	// buckets retain noncurrent versions according to the operator's policy.
	Delete(context.Context, string) error
}

type Target struct {
	ID               string `json:"id"`
	Provider         string `json:"provider"`
	Endpoint         string `json:"endpoint,omitempty"`
	Bucket           string `json:"bucket,omitempty"`
	Region           string `json:"region,omitempty"`
	Prefix           string `json:"prefix,omitempty"`
	Root             string `json:"root,omitempty"`
	MaxBytes         int64  `json:"max_bytes,omitempty"`
	AccountName      string `json:"account_name,omitempty"`
	AccessKey        string `json:"access_key,omitempty"`
	SecretKey        string `json:"secret_key,omitempty"`
	SessionToken     string `json:"session_token,omitempty"`
	AccessKeyFile    string `json:"access_key_file,omitempty"`
	SecretKeyFile    string `json:"secret_key_file,omitempty"`
	SessionTokenFile string `json:"session_token_file,omitempty"`
	PathStyle        bool   `json:"path_style,omitempty"`
	AllowHTTP        bool   `json:"allow_http,omitempty"`
	// CAFile adds a private CA for S3-compatible stores, without disabling TLS verification.
	CAFile string `json:"ca_file,omitempty"`
}

var identifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

func (t Target) Validate() error {
	if t.CAFile != "" && ((t.Provider != "s3" && t.Provider != "aws") || !filepath.IsAbs(t.CAFile)) {
		return errors.New("ca_file requires an absolute file path and an S3-compatible provider")
	}
	if !identifier.MatchString(t.ID) {
		return errors.New("invalid object storage target ID")
	}
	if t.Prefix != "" && (!safeKey(t.Prefix) || strings.HasSuffix(t.Prefix, "/")) {
		return errors.New("object prefix must be a clean relative path without trailing slash")
	}
	if t.Provider == "filesystem" {
		if !filepath.IsAbs(t.Root) || filepath.Clean(t.Root) == "/" {
			return errors.New("filesystem target requires an absolute dedicated root")
		}
		return nil
	}
	switch t.Provider {
	case "aws", "s3", "alicloud", "tencentcloud", "azure":
	default:
		return errors.New("unknown object storage provider")
	}
	u, err := url.Parse(t.Endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("object endpoint must be an HTTP(S) origin without credentials, query or path")
	}
	if u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		if u.Scheme != "http" || !t.AllowHTTP || (u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback())) {
			return errors.New("HTTPS required; HTTP is only allowed for explicitly opted-in loopback emulators")
		}
	}
	if t.Bucket == "" || strings.ContainsAny(t.Bucket, `/\\?#`) {
		return errors.New("bucket/container is required")
	}
	if t.Provider == "azure" && t.AccountName == "" {
		return errors.New("Azure account_name is required")
	}
	if t.Provider != "azure" && t.AccessKey == "" && t.AccessKeyFile == "" {
		return errors.New("access key or access_key_file is required")
	}
	if t.SecretKey == "" && t.SecretKeyFile == "" {
		return errors.New("secret key or secret_key_file is required")
	}
	if (t.Provider == "aws" || t.Provider == "alicloud") && t.Region == "" {
		return errors.New("provider region is required")
	}
	return nil
}

// Identity excludes rotating secrets but includes the physical destination. A
// persisted target ID may never silently point at another bucket or prefix.
func (t Target) Identity() string {
	t.MaxBytes = 0 // Capacity changes do not change physical destination.
	t.AccessKey, t.SecretKey, t.SessionToken = "", "", ""
	t.AccessKeyFile, t.SecretKeyFile, t.SessionTokenFile = "", "", ""
	t.CAFile = "" // Trust bundle rotation does not move objects.
	b, _ := json.Marshal(t)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (t Target) Key(relative string) (string, error) {
	if !safeKey(relative) {
		return "", errors.New("invalid object key")
	}
	if t.Prefix == "" {
		return relative, nil
	}
	return t.Prefix + "/" + relative, nil
}

func safeKey(key string) bool {
	return key != "" && key != "." && !strings.HasPrefix(key, "/") && path.Clean(key) == key && !strings.ContainsAny(key, "\\\x00") && !strings.HasPrefix(key, "../")
}

func secret(value, file string) (string, error) {
	if value != "" && file != "" {
		return "", errors.New("secret value and secret file are mutually exclusive")
	}
	if file == "" {
		return value, nil
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return "", errors.New("cannot read object storage credential file")
	}
	v := strings.TrimRight(string(b), "\r\n")
	if v == "" {
		return "", errors.New("empty object storage credential file")
	}
	return v, nil
}

func New(t Target) (Provider, error) {
	if err := t.Validate(); err != nil {
		return nil, fmt.Errorf("target %s: %w", t.ID, err)
	}
	var err error
	if t.AccessKey, err = secret(t.AccessKey, t.AccessKeyFile); err != nil {
		return nil, err
	}
	if t.SecretKey, err = secret(t.SecretKey, t.SecretKeyFile); err != nil {
		return nil, err
	}
	if t.SessionToken, err = secret(t.SessionToken, t.SessionTokenFile); err != nil {
		return nil, err
	}
	switch t.Provider {
	case "filesystem":
		return newFilesystem(t.Root, t.MaxBytes)
	case "aws", "s3":
		return newS3(t)
	case "alicloud":
		return newOSS(t)
	case "tencentcloud":
		return newCOS(t)
	case "azure":
		return newAzure(t)
	default:
		return nil, errors.New("unknown object storage provider")
	}
}
