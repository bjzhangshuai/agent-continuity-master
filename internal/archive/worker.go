package archive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/agent-continuity/agent-continuity-master/internal/artifact"
	"github.com/agent-continuity/agent-continuity-master/internal/objectstore"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

type EventSource interface {
	GetEvent(context.Context, string, string) (protocol.Event, error)
}
type Worker struct {
	Config    Config
	Queue     Repository
	Events    EventSource
	Artifacts *artifact.Service
	Providers map[string]objectstore.Provider
	Targets   map[string]objectstore.Target
	TempDir   string
	Logger    *slog.Logger
	connected atomic.Bool
}

func New(c Config, q Repository, events EventSource, artifacts *artifact.Service, tempDir string) (*Worker, error) {
	w := &Worker{Config: c, Queue: q, Events: events, Artifacts: artifacts, TempDir: tempDir, Providers: map[string]objectstore.Provider{}, Targets: map[string]objectstore.Target{}, Logger: slog.Default()}
	for _, target := range c.Targets {
		p, err := objectstore.New(target)
		if err != nil {
			return nil, fmt.Errorf("initialize archive target %s: %w", target.ID, err)
		}
		w.Providers[target.ID] = p
		w.Targets[target.ID] = target
	}
	return w, nil
}

func (w *Worker) ProcessJob(ctx context.Context, id int64) (bool, error) {
	lease := time.Duration(2*w.Config.TimeoutSeconds+15) * time.Second
	j, err := w.Queue.ClaimJob(ctx, id, w.Config.MaxAttempts, lease)
	if err != nil || j == nil {
		return false, err
	}
	operation, cancel := context.WithTimeout(ctx, time.Duration(w.Config.TimeoutSeconds)*time.Second)
	defer cancel()
	var receipt Receipt
	var code string
	if j.Operation == "delete" {
		if !w.Config.Lifecycle.AllowDelete {
			code = "delete_disabled"
		} else if p := w.Providers[j.TargetID]; p == nil {
			code = "target_unconfigured"
		} else if err := p.Delete(operation, j.ObjectKey); err != nil {
			code = "delete_failed"
		}
	} else {
		receipt, code = w.upload(operation, *j)
	}
	if code != "" {
		// A stopped worker leaves its lease for recovery; never acknowledge with a cancelled context.
		if ctx.Err() != nil {
			return true, ctx.Err()
		}
		delay := time.Duration((1<<min(j.Attempts, 9))+int(j.ID%5)) * time.Second
		return true, w.Queue.Fail(ctx, *j, code, w.Config.MaxAttempts, delay)
	}
	if j.Operation == "delete" {
		return true, w.Queue.CompleteDelete(ctx, *j)
	}
	receipt.RetentionDays = w.Config.Lifecycle.MinimumRetentionDays
	return true, w.Queue.Complete(ctx, *j, receipt)
}

func (w *Worker) upload(ctx context.Context, j Job) (Receipt, string) {
	p, ok := w.Providers[j.TargetID]
	if !ok {
		return Receipt{}, "target_unconfigured"
	}
	var reader io.Reader
	var size int64
	var digest string
	if j.Kind == "event" {
		e, err := w.Events.GetEvent(ctx, j.TaskID, j.ResourceID)
		if err != nil {
			return Receipt{}, "source_unavailable"
		}
		data, err := json.Marshal(e)
		if err != nil {
			return Receipt{}, "source_invalid"
		}
		size = int64(len(data))
		reader = bytes.NewReader(data)
		digest = hashBytes(data)
	} else {
		f, err := w.Artifacts.Open(j.SourceRef)
		if err != nil {
			return Receipt{}, "source_unavailable"
		}
		defer f.Close()
		h := sha256.New()
		n, err := io.Copy(h, io.LimitReader(f, j.Size+1))
		digest = "sha256:" + hex.EncodeToString(h.Sum(nil))
		if err != nil || n != j.Size || digest != j.ContentHash {
			return Receipt{}, "integrity_mismatch"
		}
		if _, err = f.Seek(0, io.SeekStart); err != nil {
			return Receipt{}, "source_unavailable"
		}
		reader = f
		size = n
	}
	owner := j.OrganizationID
	if owner == "" {
		owner = j.AccountID
	}
	relative := fmt.Sprintf("tenants/%s/tasks/%s/%s/%d/%s", hashSegment(owner), hashSegment(j.TaskID), j.Kind, j.ID, strings.TrimPrefix(digest, "sha256:"))
	key, err := w.Targets[j.TargetID].Key(relative)
	if err != nil {
		return Receipt{}, "target_invalid"
	}
	if err = p.Put(ctx, objectstore.Object{Key: key, Size: size, ContentType: j.ContentType, SHA256: digest}, reader); err != nil {
		return Receipt{}, "provider_unavailable"
	}
	return Receipt{Key: key, Hash: digest, Size: size}, ""
}

// Download does not expose signed cloud URLs or credentials. It bounds bytes,
// verifies SHA-256, and returns a seekable temporary file for authenticated HTTP.
// Callers MUST close and remove the returned file.
func (w *Worker) Download(ctx context.Context, j Job) (*os.File, error) {
	if j.State != "stored" {
		return nil, errors.New("archive is not stored yet")
	}
	release, err := w.Artifacts.ReserveTemporary(j.Size + 1)
	if err != nil {
		return nil, err
	}
	defer release()
	p, ok := w.Providers[j.TargetID]
	if !ok {
		return nil, errors.New("archive target unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(w.Config.TimeoutSeconds)*time.Second)
	defer cancel()
	body, err := p.Get(ctx, j.ObjectKey)
	if err != nil {
		return nil, errors.New("archive download failed")
	}
	defer body.Close()
	f, err := os.CreateTemp(w.TempDir, ".archive-download-*")
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			f.Close()
			os.Remove(f.Name())
		}
	}()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(body, j.Size+1))
	if err != nil {
		return nil, errors.New("archive download failed")
	}
	if n != j.Size || "sha256:"+hex.EncodeToString(h.Sum(nil)) != j.ContentHash {
		return nil, artifact.ErrHashMismatch
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	success = true
	return f, nil
}
func hashBytes(b []byte) string   { s := sha256.Sum256(b); return "sha256:" + hex.EncodeToString(s[:]) }
func hashSegment(s string) string { return strings.TrimPrefix(hashBytes([]byte(s)), "sha256:") }
