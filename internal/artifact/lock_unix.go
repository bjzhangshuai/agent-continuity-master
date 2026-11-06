//go:build linux || darwin

package artifact

import (
	"context"
	"errors"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"time"
)

// LockContent spans publishing the CAS file AND committing its metadata. Locks
// are shared by API replicas and workers, and require a POSIX-locking RWX volume.
// Lock files must never be removed while participants can be running.
func (s *Service) LockContent(ctx context.Context, hash string) (func(), error) {
	key, err := contentKey(hash)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(s.root, ".locks")
	if err = os.MkdirAll(dir, 0750); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, filepath.Base(key)), os.O_CREATE|os.O_RDWR, 0640)
	if err != nil {
		return nil, err
	}
	for {
		err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN); _ = f.Close() }, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}
