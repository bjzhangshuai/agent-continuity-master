package objectstore

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"hash/crc64"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestNativeProviderContracts(t *testing.T) {
	for _, kind := range []string{"aws", "s3", "alicloud", "tencentcloud", "azure"} {
		t.Run(kind, func(t *testing.T) {
			content := []byte("# 对话归档\n图像与附件 opaque bytes \x00\x01")
			var mu sync.Mutex
			var stored []byte
			authorized := false
			puts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				auth := r.Header.Get("Authorization")
				expected := map[string]string{"aws": "AWS4-HMAC-SHA256", "s3": "AWS4-HMAC-SHA256", "alicloud": "OSS4-HMAC-SHA256", "tencentcloud": "q-sign-algorithm=sha1", "azure": "SharedKey"}[kind]
				if strings.Contains(auth, expected) {
					authorized = true
				} else {
					t.Errorf("missing native %s authorization", kind)
				}
				w.Header().Set("ETag", `"archive-test"`)
				w.Header().Set("Last-Modified", "Thu, 03 Sep 2026 00:00:00 GMT")
				w.Header().Set("x-ms-request-id", "test")
				w.Header().Set("x-ms-version", "2025-05-05")
				if r.Method == "PUT" {
					b, _ := io.ReadAll(r.Body)
					if strings.Contains(r.Header.Get("Content-Encoding"), "aws-chunked") || strings.HasPrefix(r.Header.Get("X-Amz-Content-Sha256"), "STREAMING-") {
						chunks := bufio.NewReader(bytes.NewReader(b))
						var decoded bytes.Buffer
						for {
							line, err := chunks.ReadString('\n')
							if err != nil {
								t.Error(err)
								break
							}
							size, err := strconv.ParseInt(strings.Split(strings.TrimSpace(line), ";")[0], 16, 64)
							if err != nil {
								t.Error(err)
								break
							}
							if size == 0 {
								break
							}
							if _, err = io.CopyN(&decoded, chunks, size); err != nil {
								t.Error(err)
								break
							}
							_, _ = chunks.ReadString('\n')
						}
						b = decoded.Bytes()
					}
					if r.URL.Query().Get("comp") != "blocklist" {
						stored = b
					}
					puts++
					w.Header().Set("x-cos-hash-crc64ecma", strconv.FormatUint(crc64.Checksum(b, crc64.MakeTable(crc64.ECMA)), 10))
					if kind == "azure" {
						w.WriteHeader(201)
					} else {
						w.WriteHeader(200)
					}
					return
				}
				if r.Method == "GET" {
					w.Write(stored)
					return
				}
				if r.Method == "HEAD" {
					return
				}
				t.Errorf("unexpected HTTP method %s", r.Method)
			}))
			defer server.Close()
			target := Target{ID: "native-test", Provider: kind, Endpoint: server.URL, Bucket: "archive-test", Region: "us-east-1", AccountName: "testaccount", AccessKey: "test-access", SecretKey: "test-secret", PathStyle: true, AllowHTTP: true}
			if kind == "azure" {
				target.SecretKey = base64.StdEncoding.EncodeToString([]byte("test-only-account-key"))
			}
			p, err := New(target)
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(content)
			object := Object{Key: "tenants/test/message.md", Size: int64(len(content)), ContentType: "text/markdown", SHA256: "sha256:" + hex.EncodeToString(sum[:])}
			if err = p.Put(context.Background(), object, bytes.NewReader(content)); err != nil {
				t.Fatal(err)
			}
			body, err := p.Get(context.Background(), object.Key)
			if err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(body)
			body.Close()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, content) {
				t.Fatalf("round trip differs: %q", got)
			}
			mu.Lock()
			defer mu.Unlock()
			if !authorized || puts == 0 {
				t.Fatal("native provider did not send authenticated upload")
			}
		})
	}
}

func TestTargetIdentityAndSafety(t *testing.T) {
	a := Target{ID: "store-v1", Provider: "filesystem", Root: t.TempDir()}
	b := a
	b.SecretKey = "rotated"
	if a.Identity() != b.Identity() {
		t.Fatal("credential rotation changes physical identity")
	}
	b.Root = t.TempDir()
	if a.Identity() == b.Identity() {
		t.Fatal("destination change must change identity")
	}
	for _, key := range []string{"../secret", "/abs", "a/../b", "a\\b", "a\x00b"} {
		if _, err := a.Key(key); err == nil {
			t.Errorf("unsafe key accepted: %q", key)
		}
	}
	bad := Target{ID: "remote", Provider: "s3", Endpoint: "http://example.com", Bucket: "private", AccessKey: "a", SecretKey: "b", AllowHTTP: true}
	if err := bad.Validate(); err == nil {
		t.Fatal("non-loopback HTTP accepted")
	}
}
func TestFilesystemIntegrityAndRetry(t *testing.T) {
	p, err := New(Target{ID: "local", Provider: "filesystem", Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("data"))
	o := Object{Key: "t/task/blob", Size: 4, SHA256: "sha256:" + hex.EncodeToString(sum[:])}
	for range 2 {
		if err := p.Put(context.Background(), o, strings.NewReader("data")); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.Put(context.Background(), o, strings.NewReader("bad!")); err == nil {
		t.Fatal("corrupt content accepted")
	}
	f, err := p.Get(context.Background(), o.Key)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	b, _ := io.ReadAll(f)
	if string(b) != "data" {
		t.Fatal("failed upload corrupted existing object")
	}
}
