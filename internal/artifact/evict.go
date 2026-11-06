package artifact

import (
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func contentKey(hash string) (string, error) {
	h := strings.TrimPrefix(hash, "sha256:")
	b, err := hex.DecodeString(h)
	if err != nil || len(b) != 32 || h == hash || strings.ToLower(h) != h {
		return "", errors.New("invalid content digest")
	}
	return "sha256/" + h[:2] + "/" + h, nil
}

// Evict removes only the exact local CAS file, never uploads, directories, or
// remote objects. Caller must hold LockContent and a durable archive receipt.
func (s *Service) Evict(hash, ref string) error {
	key, err := contentKey(hash)
	if err != nil {
		return err
	}
	if s.remote != nil || ref != key {
		return errors.New("eviction requires matching local content reference")
	}
	root, err := os.OpenRoot(s.root)
	if err != nil {
		return err
	}
	defer root.Close()
	err = root.Remove(key)
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
