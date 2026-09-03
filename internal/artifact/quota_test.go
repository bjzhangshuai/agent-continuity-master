package artifact

import (
	"errors"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
	"strings"
	"testing"
)

func TestStagingQuotaPreservesExisting(t *testing.T) {
	s, err := New(t.TempDir(), 8)
	if err != nil {
		t.Fatal(err)
	}
	s.SetQuota(12)
	ref, _, err := s.Put(strings.NewReader("12345678"), protocol.Digest([]byte("12345678")))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Put(strings.NewReader("new"), protocol.Digest([]byte("new"))); !errors.Is(err, ErrStagingFull) {
		t.Fatalf("expected backpressure: %v", err)
	}
	if err = s.Verify(ref, protocol.Digest([]byte("12345678"))); err != nil {
		t.Fatal(err)
	}
}
