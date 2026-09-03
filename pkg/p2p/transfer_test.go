package p2p

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDirectTransferResumeAndIntegrity(t *testing.T) {
	content := []byte(strings.Repeat("portable-artifact-", 8192))
	source := filepath.Join(t.TempDir(), "source.bin")
	if err := os.WriteFile(source, content, 0o600); err != nil {
		t.Fatal(err)
	}
	digest, _, err := digestFile(source, DefaultMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	sender, offer, err := NewSender(source, digest, time.Minute, DefaultMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(sender)
	defer server.Close()
	endpoint := server.URL + "/v1/artifacts/" + strings.TrimPrefix(digest, "sha256:")
	destination := filepath.Join(t.TempDir(), "download.bin")
	if err := os.WriteFile(destination+".part", content[:4096], 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (Receiver{}).Download(context.Background(), endpoint, offer.Token, digest, destination); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(destination)
	if err != nil || string(got) != string(content) {
		t.Fatalf("direct transfer mismatch: bytes=%d err=%v", len(got), err)
	}
}

func TestDirectTransferRejectsCapabilityAndTampering(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source.bin")
	if err := os.WriteFile(source, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	digest, _, _ := digestFile(source, DefaultMaxBytes)
	sender, offer, err := NewSender(source, digest, time.Minute, DefaultMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(sender)
	defer server.Close()
	endpoint := server.URL + "/v1/artifacts/" + strings.TrimPrefix(digest, "sha256:")
	if err := (Receiver{}).Download(context.Background(), endpoint, offer.Token+"bad", digest, filepath.Join(t.TempDir(), "bad.bin")); err == nil {
		t.Fatal("invalid capability was accepted")
	}
	if err := os.WriteFile(source, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (Receiver{}).Download(context.Background(), endpoint, offer.Token, digest, filepath.Join(t.TempDir(), "tampered.bin")); err == nil {
		t.Fatal("tampered source was accepted")
	}
	if err := (Receiver{}).Download(context.Background(), "http://example.com/v1/artifacts/x", offer.Token, digest, filepath.Join(t.TempDir(), "remote.bin")); err == nil {
		t.Fatal("cleartext non-loopback endpoint was accepted")
	}
}

func TestReceiverDiscardsCompleteHashMismatch(t *testing.T) {
	digest := "sha256:" + strings.Repeat("0", 64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("untrusted complete response"))
	}))
	defer server.Close()
	destination := filepath.Join(t.TempDir(), "artifact.bin")
	err := (Receiver{}).Download(context.Background(), server.URL+"/v1/artifacts/"+strings.TrimPrefix(digest, "sha256:"), "p2p_test", digest, destination)
	if err == nil {
		t.Fatal("hash mismatch was accepted")
	}
	if _, statErr := os.Stat(destination + ".part"); !os.IsNotExist(statErr) {
		t.Fatalf("poisoned partial file was retained: %v", statErr)
	}
}
