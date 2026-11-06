package objectstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// filesystem is a labelled local demonstration target, not a cloud emulator.
type filesystem struct {
	root     string
	maxBytes int64
	mu       sync.Mutex
}

func newFilesystem(root string, maxBytes int64) (Provider, error) {
	if maxBytes == 0 {
		maxBytes = 512 << 20
	}
	if maxBytes < 0 {
		return nil, errors.New("invalid filesystem target capacity")
	}
	if err := os.MkdirAll(root, 0750); err != nil {
		return nil, err
	}
	return &filesystem{root: root, maxBytes: maxBytes}, nil
}
func (p *filesystem) Put(ctx context.Context, o Object, r io.Reader) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	var used int64
	if err := filepath.WalkDir(p.root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return errors.New("symlink in demo target")
		}
		if !d.IsDir() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			used += info.Size()
		}
		if used+o.Size > p.maxBytes {
			return errors.New("local demo object target capacity exhausted")
		}
		return ctx.Err()
	}); err != nil {
		return err
	}
	if !safeKey(o.Key) {
		return errors.New("invalid object key")
	}
	dst := filepath.Join(p.root, filepath.FromSlash(o.Key))
	if err := os.MkdirAll(filepath.Dir(dst), 0750); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(dst), ".put-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(&contextReader{ctx, r}, o.Size+1))
	if err != nil {
		return err
	}
	if n != o.Size || hex.EncodeToString(h.Sum(nil)) != strings.TrimPrefix(o.SHA256, "sha256:") {
		return errors.New("object integrity mismatch")
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), dst); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(dst))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func (p *filesystem) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !safeKey(key) {
		return nil, errors.New("invalid object key")
	}
	return os.Open(filepath.Join(p.root, filepath.FromSlash(key)))
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (p *filesystem) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !safeKey(key) {
		return errors.New("invalid object key")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	root, err := os.OpenRoot(p.root)
	if err != nil {
		return err
	}
	defer root.Close()
	err = root.Remove(filepath.FromSlash(key))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	directory, err := root.Open(filepath.Dir(key))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func (r *contextReader) Read(b []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(b)
}
