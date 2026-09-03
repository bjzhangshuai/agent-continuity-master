package protocol

import (
	"encoding/json"
	"time"
)

type TaskBranch struct {
	TaskID       string     `json:"task_id"`
	ID           string     `json:"branch_id"`
	Name         string     `json:"name"`
	BaseEventID  string     `json:"base_event_id,omitempty"`
	HeadEventIDs []string   `json:"head_event_ids"`
	Status       string     `json:"status"`
	CreatedBy    string     `json:"created_by"`
	CreatedAt    time.Time  `json:"created_at"`
	MergedInto   string     `json:"merged_into,omitempty"`
	MergedAt     *time.Time `json:"merged_at,omitempty"`
}

type BranchMerge struct {
	SourceBranchID string          `json:"source_branch_id"`
	TargetBranchID string          `json:"target_branch_id"`
	Decision       json.RawMessage `json:"decision"`
	MergeEvent     Event           `json:"merge_event"`
}

type ConflictResolution struct {
	ID                string    `json:"resolution_id"`
	ConflictID        string    `json:"conflict_id"`
	TaskID            string    `json:"task_id"`
	AccountID         string    `json:"account_id"`
	Strategy          string    `json:"strategy"`
	ResolutionEventID string    `json:"resolution_event_id,omitempty"`
	Notes             string    `json:"notes,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
}

type SnapshotDecision struct {
	EventID string          `json:"event_id"`
	Value   json.RawMessage `json:"value"`
}

type TaskSnapshotState struct {
	Task             Task               `json:"task"`
	BranchID         string             `json:"branch_id"`
	EventIDs         []string           `json:"event_ids"`
	HeadEventIDs     []string           `json:"head_event_ids"`
	LastEventID      string             `json:"last_event_id,omitempty"`
	Progress         json.RawMessage    `json:"progress,omitempty"`
	Decisions        []SnapshotDecision `json:"decisions,omitempty"`
	ArtifactRefs     []string           `json:"artifact_refs,omitempty"`
	UnresolvedIssues []string           `json:"unresolved_issues,omitempty"`
}

type TaskSnapshot struct {
	ID                    string            `json:"snapshot_id"`
	TaskID                string            `json:"task_id"`
	BranchID              string            `json:"branch_id"`
	ThroughServerSequence uint64            `json:"through_server_sequence"`
	EventCount            int               `json:"event_count"`
	State                 TaskSnapshotState `json:"state"`
	StateDigest           string            `json:"state_digest"`
	CreatedAt             time.Time         `json:"created_at"`
}
