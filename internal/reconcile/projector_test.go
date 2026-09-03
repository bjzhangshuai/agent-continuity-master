package reconcile

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

func TestBuildSnapshotUsesCausalOrderAcrossClockSkew(t *testing.T) {
	now := time.Now().UTC()
	task := protocol.Task{ID: "task_a", Title: "old", Objective: "old", Status: "active", CreatedAt: now, UpdatedAt: now}
	parent := protocol.Event{EventID: "parent", TaskID: task.ID, BranchID: "main", ParentEventIDs: []string{}, LogicalTime: protocol.HLC{WallTimeMS: 200, Node: "a"}, Type: "task.metadata.updated", Payload: json.RawMessage(`{"title":"new"}`), ServerSequence: 1, ReceivedAtServer: now.Add(time.Second)}
	child := protocol.Event{EventID: "child", TaskID: task.ID, BranchID: "main", ParentEventIDs: []string{"parent"}, LogicalTime: protocol.HLC{WallTimeMS: 100, Node: "b"}, Type: "task.completed", ServerSequence: 2, ReceivedAtServer: now.Add(2 * time.Second)}
	branch := protocol.TaskBranch{TaskID: task.ID, ID: "main", HeadEventIDs: []string{"child"}}
	snapshot, err := BuildSnapshot(task, branch, []protocol.Event{child, parent}, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.State.Task.Title != "new" || snapshot.State.Task.Status != "completed" {
		t.Fatalf("unexpected state: %+v", snapshot.State.Task)
	}
	if snapshot.State.EventIDs[0] != "parent" || snapshot.State.EventIDs[1] != "child" {
		t.Fatalf("causal order lost: %v", snapshot.State.EventIDs)
	}
	if err := VerifySnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
}

func TestBuildSnapshotRejectsMissingParentAndCycle(t *testing.T) {
	task := protocol.Task{ID: "task_a"}
	branch := protocol.TaskBranch{TaskID: task.ID, ID: "main", HeadEventIDs: []string{"a"}}
	_, err := BuildSnapshot(task, branch, []protocol.Event{{EventID: "a", TaskID: task.ID, BranchID: "main", ParentEventIDs: []string{"missing"}}}, nil, time.Now())
	if err == nil {
		t.Fatal("missing parent was accepted")
	}
	events := []protocol.Event{{EventID: "a", TaskID: task.ID, BranchID: "main", ParentEventIDs: []string{"b"}}, {EventID: "b", TaskID: task.ID, BranchID: "main", ParentEventIDs: []string{"a"}}}
	_, err = BuildSnapshot(task, branch, events, nil, time.Now())
	if err == nil {
		t.Fatal("causal cycle was accepted")
	}
}
