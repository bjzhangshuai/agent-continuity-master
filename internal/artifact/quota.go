package artifact

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

var ErrStagingFull = errors.New("artifact staging capacity exhausted")

// SetQuota enables a conservative, serialized capacity guard for durable staging.
// It includes temporary and abandoned chunks. No user data is evicted implicitly.
// Shared-volume multi-process capacity must additionally be bounded by a PVC quota.
func (s *Service) SetQuota(bytes int64) { s.quotaBytes = bytes }

// ReserveTemporary bounds concurrent cloud readback buffers on the staging PVC.
// Call the returned release function after the temporary file has been written.
func (s *Service) ReserveTemporary(bytes int64) (func(), error) { return s.reserveSpace(bytes) }
func (s *Service) reserveSpace(worstCase int64) (func(), error) {
	if s.quotaBytes == 0 {
		return func() {}, nil
	}
	s.quotaMu.Lock()
	var size int64
	err := filepath.WalkDir(s.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return errors.New("symlink in artifact staging root")
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		size += info.Size()
		if size+worstCase > s.quotaBytes {
			return ErrStagingFull
		}
		return nil
	})
	if err == nil && size+worstCase > s.quotaBytes {
		err = ErrStagingFull
	}
	if err != nil {
		s.quotaMu.Unlock()
		return nil, err
	}
	return s.quotaMu.Unlock, nil
}
