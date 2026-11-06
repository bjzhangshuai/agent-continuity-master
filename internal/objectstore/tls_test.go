package objectstore

import (
	"context"
	"encoding/pem"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestS3PrivateCA(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "DELETE" || !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256") {
			t.Error("expected signed S3 delete")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	defer server.Close()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	base := Target{ID: "private-s3", Provider: "s3", Endpoint: server.URL, Bucket: "private", Region: "us-east-1", AccessKey: "test", SecretKey: "test-secret", PathStyle: true}
	for _, tc := range []struct {
		name      string
		ca        string
		endpoint  string
		wantError bool
	}{
		{"trusted", ca, server.URL, false},
		{"untrusted", "", server.URL, true},
		{"hostname-still-verified", ca, strings.Replace(server.URL, "127.0.0.1", "localhost", 1), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := base
			target.CAFile, target.Endpoint = tc.ca, tc.endpoint
			p, err := New(target)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			err = p.Delete(ctx, "continuity/test")
			if (err != nil) != tc.wantError {
				t.Fatalf("TLS error=%v, want error=%v", err, tc.wantError)
			}
		})
	}
	rotated := base
	rotated.CAFile = ca
	if base.Identity() != rotated.Identity() {
		t.Fatal("CA rotation changed physical target identity")
	}
	for _, file := range []string{filepath.Join(t.TempDir(), "missing.pem"), filepath.Join(t.TempDir(), "invalid.pem")} {
		if strings.HasSuffix(file, "invalid.pem") {
			if err := os.WriteFile(file, []byte("not a certificate"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		rotated.CAFile = file
		if _, err := New(rotated); err == nil {
			t.Fatal("invalid private CA was silently ignored")
		}
	}
}
