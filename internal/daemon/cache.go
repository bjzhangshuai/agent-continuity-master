package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type ArtifactCache struct {
	root      string
	maxObject int64
}

func OpenArtifactCache(root string, maxObjectBytes int64) (*ArtifactCache, error) {
	if maxObjectBytes <= 0 {
		return nil, errors.New("positive cache object limit is required")
	}
	absolute, err := filepath.Abs(filepath.Join(root, "artifact-cache", "sha256"))
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, err
	}
	return &ArtifactCache{root: absolute, maxObject: maxObjectBytes}, nil
}

func (c *ArtifactCache) Put(expected string, reader io.Reader) (string, int64, error) {
	hexHash, err := digestHex(expected)
	if err != nil {
		return "", 0, err
	}
	tmp, err := os.CreateTemp(c.root, ".cache-*")
	if err != nil {
		return "", 0, err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(tmp, hash), io.LimitReader(reader, c.maxObject+1))
	if err != nil {
		_ = tmp.Close()
		return "", size, err
	}
	if size > c.maxObject {
		_ = tmp.Close()
		return "", size, errors.New("artifact exceeds cache object limit")
	}
	if hex.EncodeToString(hash.Sum(nil)) != hexHash {
		_ = tmp.Close()
		return "", size, errors.New("cached artifact hash mismatch")
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", size, err
	}
	if err := tmp.Close(); err != nil {
		return "", size, err
	}
	directory := filepath.Join(c.root, hexHash[:2])
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", size, err
	}
	destination := filepath.Join(directory, hexHash)
	if _, err := os.Stat(destination); err == nil {
		return destination, size, nil
	}
	if err := os.Rename(name, destination); err != nil {
		return "", size, err
	}
	if err := os.Chmod(destination, 0o600); err != nil {
		return "", size, err
	}
	return destination, size, syncDirectory(directory)
}

func (c *ArtifactCache) Open(expected string) (*os.File, error) {
	hexHash, err := digestHex(expected)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(c.root, hexHash[:2], hexHash)
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		_ = file.Close()
		return nil, err
	}
	if hex.EncodeToString(hash.Sum(nil)) != hexHash {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, errors.New("cached artifact failed integrity verification")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = file.Close()
		return nil, err
	}
	now := time.Now()
	_ = os.Chtimes(path, now, now)
	return file, nil
}

func (c *ArtifactCache) Prune(maxBytes int64) (int, error) {
	if maxBytes < 0 {
		return 0, errors.New("cache size cannot be negative")
	}
	type entry struct {
		path    string
		size    int64
		modTime time.Time
	}
	entries := make([]entry, 0)
	var total int64
	err := filepath.WalkDir(c.root, func(path string, item os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if item.IsDir() || strings.HasPrefix(item.Name(), ".") {
			return nil
		}
		info, err := item.Info()
		if err != nil {
			return err
		}
		entries = append(entries, entry{path: path, size: info.Size(), modTime: info.ModTime()})
		total += info.Size()
		return nil
	})
	if err != nil {
		return 0, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].modTime.Before(entries[j].modTime) })
	removed := 0
	for _, value := range entries {
		if total <= maxBytes {
			break
		}
		if err := os.Remove(value.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return removed, err
		}
		total -= value.size
		removed++
	}
	return removed, nil
}

func digestHex(value string) (string, error) {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+64 {
		return "", errors.New("invalid sha256 digest")
	}
	hexHash := strings.TrimPrefix(value, "sha256:")
	if _, err := hex.DecodeString(hexHash); err != nil {
		return "", errors.New("invalid sha256 digest")
	}
	return hexHash, nil
}
