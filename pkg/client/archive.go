package client

import (
	"context"
	"encoding/base64"
	"fmt"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
	"net/url"
	"time"
)

type StorageJob struct {
	ID          int64  `json:"id"`
	TaskID      string `json:"task_id"`
	Kind        string `json:"kind"`
	ResourceID  string `json:"resource_id"`
	VersionID   string `json:"version_id"`
	TargetID    string `json:"target_id"`
	State       string `json:"state"`
	Attempts    int    `json:"attempts"`
	Size        int64  `json:"size"`
	ContentHash string `json:"content_hash"`
	ErrorCode   string `json:"error_code"`
	Operation   string `json:"operation"`
	LegalHold   bool   `json:"legal_hold"`
}

// WaitStorageJob waits for a durable receipt, not merely HTTP upload acceptance.
// It never retries a dead letter automatically or hides authentication errors.
func (c *Client) WaitStorageJob(ctx context.Context, taskID string, id int64, timeout time.Duration) (StorageJob, error) {
	if timeout <= 0 {
		return StorageJob{}, fmt.Errorf("positive archive wait timeout required")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		j, err := c.GetStorageJob(ctx, taskID, id)
		if err != nil {
			return j, err
		}
		switch j.State {
		case "stored":
			return j, nil
		case "dead", "deleted":
			return j, fmt.Errorf("archive job %d ended in %s: %s", id, j.State, j.ErrorCode)
		}
		select {
		case <-ctx.Done():
			return j, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

type StorageJobsPage struct {
	Jobs      []StorageJob `json:"jobs"`
	NextAfter int64        `json:"next_after"`
	HasMore   bool         `json:"has_more"`
}

// Persist/reuse versionID on retries. 201 means accepted, NOT cloud completion.
func (c *Client) UploadContent(ctx context.Context, taskID, artifactID, versionID, filename, contentType string, content []byte, parentVersionID string) (protocol.Artifact, error) {
	var result protocol.Artifact
	if len(content) > 8<<20 {
		return result, fmt.Errorf("use resumable upload for content larger than 8 MiB")
	}
	err := c.Do(ctx, "POST", "/v1/tasks/"+url.PathEscape(taskID)+"/contents", map[string]string{"artifact_id": artifactID, "version_id": versionID, "parent_version_id": parentVersionID, "filename": filename, "content_type": contentType, "content_base64": base64.StdEncoding.EncodeToString(content)}, &result)
	return result, err
}
func (c *Client) ListStorageJobs(ctx context.Context, taskID string, after int64) (StorageJobsPage, error) {
	var result StorageJobsPage
	err := c.Do(ctx, "GET", fmt.Sprintf("/v1/tasks/%s/storage-jobs?after=%d", url.PathEscape(taskID), after), nil, &result)
	return result, err
}
func (c *Client) GetStorageJob(ctx context.Context, taskID string, id int64) (StorageJob, error) {
	var result StorageJob
	err := c.Do(ctx, "GET", fmt.Sprintf("/v1/tasks/%s/storage-jobs/%d", url.PathEscape(taskID), id), nil, &result)
	return result, err
}
func (c *Client) RetryStorageJob(ctx context.Context, taskID string, id int64) error {
	return c.Do(ctx, "POST", fmt.Sprintf("/v1/tasks/%s/storage-jobs/%d/retry", url.PathEscape(taskID), id), nil, nil)
}
