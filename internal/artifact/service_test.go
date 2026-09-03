package artifact

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

func TestContentAddressedRoundTrip(t *testing.T) {
	service, err := New(t.TempDir(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	content := "portable task artifact"
	digest := protocol.Digest([]byte(content))
	ref, size, err := service.Put(strings.NewReader(content), digest)
	if err != nil {
		t.Fatal(err)
	}
	if size != int64(len(content)) {
		t.Fatalf("unexpected size %d", size)
	}
	file, err := service.Open(ref)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	got, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Fatalf("got %q", got)
	}
	if err := service.Verify(ref, digest); err != nil {
		t.Fatal(err)
	}
}

func TestRejectsTamperingAndTraversal(t *testing.T) {
	root := t.TempDir()
	service, _ := New(root, 1024)
	if _, _, err := service.Put(strings.NewReader("bad"), protocol.Digest([]byte("good"))); !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("expected hash mismatch, got %v", err)
	}
	if _, err := service.Open("../../etc/passwd"); err == nil {
		t.Fatal("expected traversal rejection")
	}
	digest := protocol.Digest([]byte("immutable"))
	ref, _, err := service.Put(strings.NewReader("immutable"), digest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(ref)), []byte("tampered!"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := service.Verify(ref, digest); !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("stored tampering was not detected: %v", err)
	}
}
