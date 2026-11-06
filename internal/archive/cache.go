package archive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"time"
)

// EvictCacheOnce is bounded and opt-in. Cloud reads for integrity maintenance are
// not tenant downloads. It never removes the original event records in Postgres.
func (w *Worker) EvictCacheOnce(ctx context.Context) (int, error) {
	if w.Config.Lifecycle.CacheTTLSeconds == 0 {
		return 0, nil
	}
	ids := make([]string, 0, len(w.Targets))
	for id := range w.Targets {
		ids = append(ids, id)
	}
	jobs, err := w.Queue.CacheCandidates(ctx, time.Duration(w.Config.Lifecycle.CacheTTLSeconds)*time.Second, 10, ids)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, j := range jobs {
		err := func() error {
			op, cancel := context.WithTimeout(ctx, time.Duration(w.Config.TimeoutSeconds)*time.Second)
			defer cancel()
			unlock, err := w.Artifacts.LockContent(op, j.ContentHash)
			if err != nil {
				return err
			}
			defer unlock()
			p := w.Providers[j.TargetID]
			if p == nil {
				return errors.New("cache target unavailable")
			}
			safe, err := w.Queue.PrepareCacheEviction(op, j.SourceRef)
			if err != nil || !safe {
				return err
			}
			body, err := p.Get(op, j.ObjectKey)
			if err != nil {
				return errors.New("cache remote read unavailable")
			}
			defer body.Close()
			h := sha256.New()
			n, err := io.Copy(h, io.LimitReader(body, j.Size+1))
			if err != nil || n != j.Size || "sha256:"+hex.EncodeToString(h.Sum(nil)) != j.ContentHash {
				return errors.New("cache remote integrity check failed")
			}
			if err = w.Artifacts.Evict(j.ContentHash, j.SourceRef); err != nil {
				return err
			}
			if err = w.Queue.CompleteCacheEviction(op, j.SourceRef); err != nil {
				return err
			}
			count++
			return nil
		}()
		if err != nil {
			return count, err
		}
	}
	return count, nil
}
