// Package archive owns durable asynchronous content archival. Repository holds
// transactional outbox/receipts; JetStream is the broker, separate from providers.
package archive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/agent-continuity/agent-continuity-master/internal/objectstore"
)

var ErrLeaseLost = errors.New("archive lease lost")
var ErrNotFound = errors.New("archive job not found")
var ErrQueueFull = errors.New("archive outbox capacity exhausted")

type Config struct {
	Broker              BrokerConfig         `json:"broker"`
	Concurrency         int                  `json:"concurrency"`
	Enabled             bool                 `json:"enabled"`
	DefaultTarget       string               `json:"default_target"`
	OrganizationTargets map[string]string    `json:"organization_targets,omitempty"`
	Targets             []objectstore.Target `json:"targets"`
	PollSeconds         int                  `json:"poll_seconds"`
	TimeoutSeconds      int                  `json:"timeout_seconds"`
	MaxAttempts         int                  `json:"max_attempts"`
	MaxPendingJobs      int                  `json:"max_pending_jobs"`
	MaxStagingBytes     int64                `json:"max_staging_bytes"`
	Lifecycle           LifecycleConfig      `json:"lifecycle"`
}

type LifecycleConfig struct {
	AllowDelete          bool `json:"allow_delete"`
	MinimumRetentionDays int  `json:"minimum_retention_days"`
	CacheTTLSeconds      int  `json:"cache_ttl_seconds"`
}

func Parse(raw string) (Config, error) {
	c := Config{PollSeconds: 2, TimeoutSeconds: 60, MaxAttempts: 6, MaxPendingJobs: 10000, MaxStagingBytes: 512 << 20, Concurrency: 1, Broker: BrokerConfig{Stream: "CONTINUITY_ARCHIVE", Subject: "continuity.archive.jobs", Consumer: "archive-workers", Replicas: 1, MaxBytes: 64 << 20, MaxMessages: 100000}}
	if strings.TrimSpace(raw) == "" {
		return c, nil
	}
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return c, fmt.Errorf("invalid archive config: %w", err)
	}
	if !c.Enabled {
		return c, nil
	}
	if err := c.Broker.Validate(); err != nil {
		return c, err
	}
	if c.Concurrency < 1 || c.Concurrency > 32 {
		return c, errors.New("archive concurrency must be 1..32")
	}
	if c.Lifecycle.MinimumRetentionDays < 0 || c.Lifecycle.MinimumRetentionDays > 36500 || (c.Lifecycle.CacheTTLSeconds != 0 && c.Lifecycle.CacheTTLSeconds < 3600) {
		return c, errors.New("invalid lifecycle policy; automatic cache eviction requires at least one hour")
	}
	if c.PollSeconds < 1 || c.PollSeconds > 60 || c.TimeoutSeconds < 5 || c.TimeoutSeconds > 300 || c.MaxAttempts < 1 || c.MaxAttempts > 20 || c.MaxPendingJobs < 1 || c.MaxPendingJobs > 1000000 || c.MaxStagingBytes < 1<<20 {
		return c, errors.New("archive worker limits are out of range")
	}
	targets := map[string]bool{}
	for _, t := range c.Targets {
		if targets[t.ID] {
			return c, errors.New("duplicate archive target ID")
		}
		if err := t.Validate(); err != nil {
			return c, err
		}
		targets[t.ID] = true
	}
	if !targets[c.DefaultTarget] {
		return c, errors.New("archive default_target does not exist")
	}
	for org, id := range c.OrganizationTargets {
		if org == "" || !targets[id] {
			return c, errors.New("invalid organization target mapping")
		}
	}
	return c, nil
}

type Job struct {
	ID             int64      `json:"id"`
	TaskID         string     `json:"task_id"`
	OrganizationID string     `json:"organization_id,omitempty"`
	AccountID      string     `json:"account_id"`
	Kind           string     `json:"kind"`
	ResourceID     string     `json:"resource_id"`
	VersionID      string     `json:"version_id,omitempty"`
	TargetID       string     `json:"target_id"`
	State          string     `json:"state"`
	Attempts       int        `json:"attempts"`
	LeaseVersion   int64      `json:"-"`
	SourceRef      string     `json:"-"`
	ContentType    string     `json:"content_type"`
	ContentHash    string     `json:"content_hash,omitempty"`
	Size           int64      `json:"size"`
	ObjectKey      string     `json:"object_key,omitempty"`
	ErrorCode      string     `json:"error_code,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	NextAttempt    time.Time  `json:"next_attempt"`
	Operation      string     `json:"operation"`
	StoredAt       *time.Time `json:"stored_at,omitempty"`
	DeletedAt      *time.Time `json:"deleted_at,omitempty"`
	RetainUntil    *time.Time `json:"retain_until,omitempty"`
	LegalHold      bool       `json:"legal_hold"`
}

type Receipt struct {
	Key, Hash     string
	Size          int64
	RetentionDays int
}
type Repository interface {
	Configure(context.Context, Config) error
	ClaimJob(context.Context, int64, int, time.Duration) (*Job, error)
	PendingDispatch(context.Context, int) ([]Job, error)
	MarkDispatched(context.Context, Job) error
	Complete(context.Context, Job, Receipt) error
	Fail(context.Context, Job, string, int, time.Duration) error
	Get(context.Context, int64) (Job, error)
	List(context.Context, string, int64, int) ([]Job, error)
	Retry(context.Context, int64) error
	Backfill(context.Context, string, int) (int64, error)
	FindArtifact(context.Context, string, string, string) (Job, error)
	RequestDelete(context.Context, int64, string, string, string) error
	CompleteDelete(context.Context, Job) error
	SetHold(context.Context, int64, bool, string, string) error
	RecordDownload(context.Context, Job, string) error
	Stats(context.Context) (Stats, error)
	Ping(context.Context) error
	CacheCandidates(context.Context, time.Duration, int, []string) ([]Job, error)
	PrepareCacheEviction(context.Context, string) (bool, error)
	CompleteCacheEviction(context.Context, string) error
}

type Stats struct {
	States               map[string]int64 `json:"states"`
	RetainedBytes        int64            `json:"retained_bytes"`
	OldestPendingSeconds float64          `json:"oldest_pending_seconds"`
}
