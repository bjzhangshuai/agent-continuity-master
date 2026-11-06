// Package operations persists versioned workflow aggregates. External effects are
// performed only after a durable intent; stable effect IDs make retries safe.
package operations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
	"time"
)

var ErrNotFound = errors.New("operation not found")
var ErrConflict = errors.New("operation changed; reload and retry")
var ErrInvalid = errors.New("invalid operation")

type Record struct {
	Updated  time.Time              `json:"updated_at"`
	Resource string                 `json:"resource,omitempty"`
	Usage    []protocol.UsageRecord `json:"-"`
	ID       string                 `json:"id"`
	Kind     string                 `json:"kind"`
	Owner    string                 `json:"owner"`
	Peer     string                 `json:"peer,omitempty"`
	Status   string                 `json:"status"`
	Version  int64                  `json:"version"`
	Due      time.Time              `json:"due"`
	Data     json.RawMessage        `json:"data"`
}

// BillGuard prevents a concurrent refund changing money while it is reserved.
type BillGuard struct {
	ID      string
	Version int64
}
type RunQuery struct {
	ContextID, Status, TaskID, BeforeID string
	After, Before                       time.Time
	Limit                               int
}
type Repository interface {
	QueryRuns(context.Context, string, RunQuery) ([]Record, int, error)
	GetOperation(context.Context, string, string) (Record, error)
	ListOperations(context.Context, string, string, string, int) ([]Record, error)
	DueOperations(context.Context, string, time.Time, int) ([]Record, error)
	SaveOperation(context.Context, Record, int64, []BillGuard) error
}

func ID(kind, owner, key string) string {
	h := sha256.Sum256([]byte(kind + "\x00" + owner + "\x00" + key))
	return kind + "_" + hex.EncodeToString(h[:20])
}
func Encode(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
func Clone(r Record) Record { r.Data = append(json.RawMessage(nil), r.Data...); return r }
