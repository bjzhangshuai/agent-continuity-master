package kms

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnvelopeTenantBindingRotationRevocationAndTampering(t *testing.T) {
	cfg := `{"current":"v1","simulation":true,"tenant_keys":{"tenant-b":"v2"},"keys":[{"id":"v1","cloud":"aws","key_id":"key1"},{"id":"v2","cloud":"azure","key_id":"key2"}]}`
	m, e := Build(cfg, bytes.Repeat([]byte{1}, 32))
	if e != nil {
		t.Fatal(e)
	}
	cipher, e := m.Encrypt([]byte("private server credential"), "tenant-a:credential")
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(cipher, []byte("private server credential")) {
		t.Fatal("plaintext stored")
	}
	if _, e = m.Decrypt(cipher, "tenant-b:credential"); e == nil {
		t.Fatal("cross-tenant decrypt")
	}
	other, e := m.Encrypt([]byte("other"), "tenant-b:credential")
	if e != nil || !bytes.Contains(other, []byte(`"key":"v2"`)) {
		t.Fatal("tenant key policy not applied")
	}
	rotated, e := Build(strings.Replace(cfg, `"current":"v1"`, `"current":"v2"`, 1), bytes.Repeat([]byte{1}, 32))
	if e != nil {
		t.Fatal(e)
	}
	plain, e := rotated.Decrypt(cipher, "tenant-a:credential")
	if e != nil || string(plain) != "private server credential" {
		t.Fatal("old key unreadable", e)
	}
	newCipher, e := rotated.Encrypt(plain, "tenant-a:credential")
	if e != nil || !bytes.Contains(newCipher, []byte(`"key":"v2"`)) {
		t.Fatal("rotation failed")
	}
	delete(rotated.Wrappers, "v1")
	if _, e = rotated.Decrypt(cipher, "tenant-a:credential"); e == nil {
		t.Fatal("revoked key accepted")
	}
	var v envelope
	_ = json.Unmarshal(newCipher[1:], &v)
	v.Data[len(v.Data)-1] ^= 1
	b, _ := json.Marshal(v)
	if _, e = rotated.Decrypt(append([]byte{2}, b...), "tenant-a:credential"); e == nil {
		t.Fatal("tampering accepted")
	}
}
func TestFourCloudWireAdapters(t *testing.T) {
	for _, cloud := range []string{"aws", "alicloud", "tencentcloud", "azure"} {
		t.Run(cloud, func(t *testing.T) {
			key := bytes.Repeat([]byte{7}, 32)
			var encryptSeen, decryptSeen bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var action, value string
				var b map[string]any
				ec := ""
				_ = json.NewDecoder(r.Body).Decode(&b)
				switch cloud {
				case "aws":
					action = strings.TrimPrefix(r.Header.Get("X-Amz-Target"), "TrentService.")
					if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=test/") {
						t.Error("missing SigV4")
					}
					ec = b["EncryptionContext"].(map[string]any)["ac_context"].(string)
				case "tencentcloud":
					action = r.Header.Get("X-TC-Action")
					if !strings.HasPrefix(r.Header.Get("Authorization"), "TC3-HMAC-SHA256 Credential=test/") || r.Header.Get("X-TC-Version") != "2019-01-18" {
						t.Error("missing TC3/version")
					}
					var v map[string]string
					_ = json.Unmarshal([]byte(b["EncryptionContext"].(string)), &v)
					ec = v["ac_context"]
				case "alicloud":
					t.Fatal("form request must use separate handler")
				case "azure":
					if r.Header.Get("Authorization") != "Bearer azure-token" || !strings.Contains(r.URL.Path, "/keys/key/version/") || b["alg"] != "RSA-OAEP-256" {
						t.Error("invalid Key Vault request")
					}
					action = "Encrypt"
					if strings.HasSuffix(r.URL.Path, "/decrypt") {
						action = "Decrypt"
					}
				}
				if cloud != "azure" && ec != hash("tenant-a:secret") {
					t.Error("missing tenant binding")
				}
				field := "CiphertextBlob"
				result := base64.StdEncoding.EncodeToString([]byte("wrapped-data-key"))
				if action == "Encrypt" {
					encryptSeen = true
					value, _ = b["Plaintext"].(string)
					if cloud != "azure" && value != base64.StdEncoding.EncodeToString(key) {
						t.Error("plaintext mapping")
					}
				} else {
					decryptSeen = true
					field = "Plaintext"
					result = base64.StdEncoding.EncodeToString(key)
				}
				out := map[string]string{field: result}
				if cloud == "azure" {
					if action == "Encrypt" {
						result = base64.RawURLEncoding.EncodeToString([]byte("wrapped-data-key"))
					} else {
						result = base64.RawURLEncoding.EncodeToString(key)
					}
					out = map[string]string{"value": result}
				}
				w.Header().Set("Content-Type", "application/json")
				if cloud == "tencentcloud" {
					_ = json.NewEncoder(w).Encode(map[string]any{"Response": out})
				} else {
					_ = json.NewEncoder(w).Encode(out)
				}
			}))
			defer server.Close()
			if cloud == "alicloud" {
				server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if e := r.ParseForm(); e != nil {
						t.Error(e)
					}
					if r.Form.Get("Signature") == "" || r.Form.Get("Version") != "2016-01-20" || r.Form.Get("AccessKeyId") != "test" || r.Form.Get("EncryptionContext") != "{\"ac_context\":\""+hash("tenant-a:secret")+"\"}" {
						t.Error("invalid Alibaba RPC request")
					}
					out := map[string]string{"CiphertextBlob": "opaque-alicloud-cipher"}
					if r.Form.Get("Action") == "Decrypt" {
						decryptSeen = true
						out = map[string]string{"Plaintext": base64.StdEncoding.EncodeToString(key)}
					} else {
						encryptSeen = true
						if r.Form.Get("Plaintext") != base64.StdEncoding.EncodeToString(key) {
							t.Error("plaintext mapping")
						}
					}
					_ = json.NewEncoder(w).Encode(out)
				})
			}
			cred := filepath.Join(t.TempDir(), "credentials.json")
			_ = os.WriteFile(cred, []byte(`{"access_key_id":"test","secret_access_key":"test-secret","session_token":"session","bearer_token":"azure-token"}`), 0600)
			provider := Cloud{Key: Key{Cloud: cloud, KeyID: "key/version", Region: "test-region", Endpoint: server.URL, CredentialsFile: cred}, Client: server.Client()}
			wrapped, e := provider.Wrap(context.Background(), key, "tenant-a:secret")
			if e != nil {
				t.Fatal(e)
			}
			plain, e := provider.Unwrap(context.Background(), wrapped, "tenant-a:secret")
			if e != nil || !bytes.Equal(plain, key) || !encryptSeen || !decryptSeen {
				t.Fatalf("wire roundtrip failed %v", e)
			}
		})
	}
}
func TestKMSFailsClosed(t *testing.T) {
	for _, raw := range []string{`{"current":"v1","keys":[{"id":"v1","cloud":"aws","key_id":"x","endpoint":"http://127.0.0.1"}]}`, `{"current":"v1","simulation":true,"keys":[{"id":"v1","cloud":"aws","key_id":"x","disabled":true}]}`} {
		if _, e := Build(raw, make([]byte, 32)); e == nil {
			t.Fatal("unsafe config accepted")
		}
	}
}
