package daemon

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

func TestArtifactCacheVerifiesAndPrunes(t *testing.T) {
	cache, err := OpenArtifactCache(t.TempDir(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	content := "cached artifact"
	digest := protocol.Digest([]byte(content))
	path, _, err := cache.Put(digest, strings.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	file, err := cache.Open(digest)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(file)
	_ = file.Close()
	if string(got) != content {
		t.Fatalf("got %q", got)
	}
	if err := os.WriteFile(path, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Open(digest); err == nil {
		t.Fatal("tampered cache entry was accepted")
	}
	for _, value := range []string{"one", "two"} {
		if _, _, err := cache.Put(protocol.Digest([]byte(value)), strings.NewReader(value)); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := cache.Prune(0)
	if err != nil || removed != 2 {
		t.Fatalf("removed=%d err=%v", removed, err)
	}
}
