package kms

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Credentials struct {
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	SessionToken    string `json:"session_token"`
	BearerToken     string `json:"bearer_token"`
}
type Cloud struct {
	Key    Key
	Client *http.Client
	Now    func() time.Time
}

func (c Cloud) Wrap(ctx context.Context, p []byte, scope string) (string, error) {
	return c.call(ctx, "Encrypt", base64.StdEncoding.EncodeToString(p), scope)
}
func (c Cloud) Unwrap(ctx context.Context, v, scope string) ([]byte, error) {
	out, e := c.call(ctx, "Decrypt", v, scope)
	if e != nil {
		return nil, e
	}
	return base64.StdEncoding.DecodeString(out)
}
func hash(s string) string { v := sha256.Sum256([]byte(s)); return hex.EncodeToString(v[:]) }
func mac(key []byte, s string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(s))
	return h.Sum(nil)
}
func percent(s string) string { return strings.ReplaceAll(url.QueryEscape(s), "+", "%20") }
func (c Cloud) call(ctx context.Context, action, value, scope string) (string, error) {
	raw, e := os.ReadFile(c.Key.CredentialsFile)
	if e != nil {
		return "", errors.New("KMS credentials unavailable")
	}
	var cred Credentials
	if json.Unmarshal(raw, &cred) != nil {
		return "", errors.New("invalid KMS credentials")
	}
	clear(raw)
	now := time.Now().UTC()
	if c.Now != nil {
		now = c.Now().UTC()
	}
	contextValue := hash(scope)
	body := map[string]any{"KeyId": c.Key.KeyID, "EncryptionContext": map[string]string{"ac_context": contextValue}}
	field := "Plaintext"
	if action == "Decrypt" {
		field = "CiphertextBlob"
	}
	body[field] = value
	endpoint := strings.TrimRight(c.Key.Endpoint, "/") + "/"
	headers := http.Header{}
	headers.Set("Content-Type", "application/json")
	var encoded []byte
	switch c.Key.Cloud {
	case "aws":
		headers.Set("Content-Type", "application/x-amz-json-1.1")
		headers.Set("X-Amz-Target", "TrentService."+action)
		headers.Set("X-Amz-Date", now.Format("20060102T150405Z"))
		if cred.SessionToken != "" {
			headers.Set("X-Amz-Security-Token", cred.SessionToken)
		}
		encoded, _ = json.Marshal(body)
		u, _ := url.Parse(endpoint)
		date := now.Format("20060102")
		canonicalHeaders := "content-type:" + headers.Get("Content-Type") + "\nhost:" + u.Host + "\nx-amz-date:" + headers.Get("X-Amz-Date") + "\n"
		signed := "content-type;host;x-amz-date"
		if cred.SessionToken != "" {
			canonicalHeaders += "x-amz-security-token:" + cred.SessionToken + "\n"
			signed += ";x-amz-security-token"
		}
		canonicalHeaders += "x-amz-target:" + headers.Get("X-Amz-Target") + "\n"
		signed += ";x-amz-target"
		scopeKey := date + "/" + c.Key.Region + "/kms/aws4_request"
		canonical := "POST\n" + u.EscapedPath() + "\n\n" + canonicalHeaders + "\n" + signed + "\n" + hash(string(encoded))
		signKey := mac(mac(mac(mac([]byte("AWS4"+cred.SecretAccessKey), date), c.Key.Region), "kms"), "aws4_request")
		signature := hex.EncodeToString(mac(signKey, "AWS4-HMAC-SHA256\n"+headers.Get("X-Amz-Date")+"\n"+scopeKey+"\n"+hash(canonical)))
		headers.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+cred.AccessKeyID+"/"+scopeKey+", SignedHeaders="+signed+", Signature="+signature)
	case "tencentcloud":
		ec, _ := json.Marshal(body["EncryptionContext"])
		body["EncryptionContext"] = string(ec)
		if action == "Decrypt" {
			delete(body, "KeyId")
		}
		encoded, _ = json.Marshal(body)
		headers.Set("X-TC-Action", action)
		headers.Set("X-TC-Version", "2019-01-18")
		headers.Set("X-TC-Region", c.Key.Region)
		timestamp := strconv.FormatInt(now.Unix(), 10)
		headers.Set("X-TC-Timestamp", timestamp)
		if cred.SessionToken != "" {
			headers.Set("X-TC-Token", cred.SessionToken)
		}
		u, _ := url.Parse(endpoint)
		signed := "content-type;host;x-tc-action"
		canonical := "POST\n" + u.EscapedPath() + "\n\ncontent-type:application/json\nhost:" + u.Host + "\nx-tc-action:" + strings.ToLower(action) + "\n\n" + signed + "\n" + hash(string(encoded))
		date := now.Format("2006-01-02")
		scopeKey := date + "/kms/tc3_request"
		signKey := mac(mac(mac([]byte("TC3"+cred.SecretAccessKey), date), "kms"), "tc3_request")
		signature := hex.EncodeToString(mac(signKey, "TC3-HMAC-SHA256\n"+timestamp+"\n"+scopeKey+"\n"+hash(canonical)))
		headers.Set("Authorization", "TC3-HMAC-SHA256 Credential="+cred.AccessKeyID+"/"+scopeKey+", SignedHeaders="+signed+", Signature="+signature)
	case "alicloud":
		nonce := make([]byte, 16)
		if _, e = rand.Read(nonce); e != nil {
			return "", e
		}
		ec, _ := json.Marshal(body["EncryptionContext"])
		v := url.Values{"Action": {action}, "Format": {"JSON"}, "Version": {"2016-01-20"}, "AccessKeyId": {cred.AccessKeyID}, "SignatureMethod": {"HMAC-SHA1"}, "SignatureVersion": {"1.0"}, "SignatureNonce": {hex.EncodeToString(nonce)}, "Timestamp": {now.Format("2006-01-02T15:04:05Z")}, "EncryptionContext": {string(ec)}, field: {value}}
		if action == "Encrypt" {
			v.Set("KeyId", c.Key.KeyID)
		}
		if cred.SessionToken != "" {
			v.Set("SecurityToken", cred.SessionToken)
		}
		canonical := strings.ReplaceAll(v.Encode(), "+", "%20")
		h := hmac.New(sha1.New, []byte(cred.SecretAccessKey+"&"))
		h.Write([]byte("POST&%2F&" + percent(canonical)))
		v.Set("Signature", base64.StdEncoding.EncodeToString(h.Sum(nil)))
		encoded = []byte(v.Encode())
		headers.Set("Content-Type", "application/x-www-form-urlencoded")
	case "azure":
		endpoint = strings.TrimRight(c.Key.Endpoint, "/") + "/keys/" + c.Key.KeyID + "/" + strings.ToLower(action) + "?api-version=2025-07-01"
		v, e := base64.StdEncoding.DecodeString(value)
		if e != nil {
			return "", errors.New("invalid Azure wrapped key")
		}
		encoded, _ = json.Marshal(map[string]string{"alg": "RSA-OAEP-256", "value": base64.RawURLEncoding.EncodeToString(v)})
		headers.Set("Authorization", "Bearer "+cred.BearerToken)
	default:
		return "", errors.New("unsupported KMS cloud")
	}
	if c.Key.Cloud == "azure" && cred.BearerToken == "" || c.Key.Cloud != "azure" && (cred.AccessKeyID == "" || cred.SecretAccessKey == "") {
		return "", errors.New("missing KMS authentication")
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if e != nil {
		return "", e
	}
	req.Header = headers
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	resp, e := client.Do(req)
	if e != nil {
		return "", errors.New("KMS transport unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("KMS HTTP %d", resp.StatusCode)
	}
	var result map[string]json.RawMessage
	if json.NewDecoder(io.LimitReader(resp.Body, 128<<10)).Decode(&result) != nil {
		return "", errors.New("invalid KMS response")
	}
	if c.Key.Cloud == "tencentcloud" {
		if json.Unmarshal(result["Response"], &result) != nil {
			return "", errors.New("invalid Tencent KMS response")
		}
	}
	output := "CiphertextBlob"
	if action == "Decrypt" {
		output = "Plaintext"
	}
	if c.Key.Cloud == "azure" {
		output = "value"
	}
	var out string
	if json.Unmarshal(result[output], &out) != nil || out == "" {
		return "", errors.New("KMS returned no key result")
	}
	if c.Key.Cloud == "azure" {
		b, e := base64.RawURLEncoding.DecodeString(out)
		if e != nil {
			return "", e
		}
		out = base64.StdEncoding.EncodeToString(b)
	}
	return out, nil
}
