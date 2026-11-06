package artifact

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSharedContentLockAndEvictionScope(t *testing.T) {
	s, _ := New(t.TempDir(), 1024)
	hash := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	unlock, err := s.LockContent(context.Background(), hash)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	other, _ := New(s.root, 1024)
	ctx, cancel := context.WithTimeout(context.Background(), 70*time.Millisecond)
	defer cancel()
	if release, err := other.LockContent(ctx, hash); !errors.Is(err, context.DeadlineExceeded) {
		if release != nil {
			release()
		}
		t.Fatal("lock was not shared", err)
	}
	for _, ref := range []string{"../outside", "sha256/aa/wrong", "uploads/user-file"} {
		if s.Evict(hash, ref) == nil {
			t.Fatal("unsafe eviction accepted")
		}
	}
}
