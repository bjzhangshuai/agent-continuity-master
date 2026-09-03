package reconcile

import (
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/agent-continuity/agent-continuity-master/internal/event"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

func BuildSnapshot(task protocol.Task, branch protocol.TaskBranch, allEvents []protocol.Event, conflicts []protocol.Conflict, now time.Time) (protocol.TaskSnapshot, error) {
	history, err := branchHistory(branch, allEvents)
	if err != nil {
		return protocol.TaskSnapshot{}, err
	}
	ordered, err := topologicalOrder(history)
	if err != nil {
		return protocol.TaskSnapshot{}, err
	}
	state := protocol.TaskSnapshotState{Task: task, BranchID: branch.ID, EventIDs: make([]string, 0, len(ordered)), HeadEventIDs: append([]string(nil), branch.HeadEventIDs...)}
	artifactSet := map[string]struct{}{}
	var sequence uint64
	for _, value := range ordered {
		state.EventIDs = append(state.EventIDs, value.EventID)
		state.LastEventID = value.EventID
		if value.ServerSequence > sequence {
			sequence = value.ServerSequence
		}
		for _, artifactID := range value.ArtifactRefs {
			artifactSet[artifactID] = struct{}{}
		}
		applyEvent(&state, value)
	}
	for artifactID := range artifactSet {
		state.ArtifactRefs = append(state.ArtifactRefs, artifactID)
	}
	sort.Strings(state.ArtifactRefs)
	for _, conflict := range conflicts {
		if !conflict.Resolved {
			state.UnresolvedIssues = append(state.UnresolvedIssues, conflict.ID)
		}
	}
	sort.Strings(state.UnresolvedIssues)
	raw, err := json.Marshal(state)
	if err != nil {
		return protocol.TaskSnapshot{}, err
	}
	id, err := protocol.NewID("snapshot")
	if err != nil {
		return protocol.TaskSnapshot{}, err
	}
	return protocol.TaskSnapshot{ID: id, TaskID: task.ID, BranchID: branch.ID, ThroughServerSequence: sequence, EventCount: len(ordered), State: state, StateDigest: protocol.Digest(raw), CreatedAt: now.UTC()}, nil
}

func VerifySnapshot(value protocol.TaskSnapshot) error {
	raw, err := json.Marshal(value.State)
	if err != nil {
		return err
	}
	if protocol.Digest(raw) != value.StateDigest {
		return errors.New("snapshot state digest mismatch")
	}
	if value.EventCount != len(value.State.EventIDs) {
		return errors.New("snapshot event count mismatch")
	}
	return nil
}

func branchHistory(branch protocol.TaskBranch, allEvents []protocol.Event) ([]protocol.Event, error) {
	byID := make(map[string]protocol.Event, len(allEvents))
	selected := map[string]struct{}{}
	for _, value := range allEvents {
		byID[value.EventID] = value
		if value.BranchID == branch.ID {
			selected[value.EventID] = struct{}{}
		}
	}
	stack := append([]string(nil), branch.HeadEventIDs...)
	for id := range selected {
		stack = append(stack, id)
	}
	visited := map[string]struct{}{}
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if _, ok := visited[id]; ok {
			continue
		}
		visited[id] = struct{}{}
		selected[id] = struct{}{}
		value, ok := byID[id]
		if !ok {
			return nil, errors.New("branch references a missing event: " + id)
		}
		for _, parent := range value.ParentEventIDs {
			if _, exists := visited[parent]; !exists {
				stack = append(stack, parent)
			}
		}
	}
	result := make([]protocol.Event, 0, len(selected))
	for id := range selected {
		value, ok := byID[id]
		if !ok {
			return nil, errors.New("event history is incomplete: " + id)
		}
		result = append(result, value)
	}
	return result, nil
}

func topologicalOrder(values []protocol.Event) ([]protocol.Event, error) {
	byID := make(map[string]protocol.Event, len(values))
	indegree := make(map[string]int, len(values))
	children := make(map[string][]string, len(values))
	for _, value := range values {
		if _, duplicate := byID[value.EventID]; duplicate {
			return nil, errors.New("duplicate event in snapshot history: " + value.EventID)
		}
		byID[value.EventID] = value
		indegree[value.EventID] = 0
	}
	for _, value := range values {
		for _, parent := range value.ParentEventIDs {
			if _, ok := byID[parent]; !ok {
				return nil, errors.New("snapshot history has missing parent: " + parent)
			}
			indegree[value.EventID]++
			children[parent] = append(children[parent], value.EventID)
		}
	}
	ready := make([]protocol.Event, 0)
	for id, degree := range indegree {
		if degree == 0 {
			ready = append(ready, byID[id])
		}
	}
	less := func(i, j int) bool {
		if compared := event.Compare(ready[i].LogicalTime, ready[j].LogicalTime); compared != 0 {
			return compared < 0
		}
		return ready[i].EventID < ready[j].EventID
	}
	result := make([]protocol.Event, 0, len(values))
	for len(ready) > 0 {
		sort.Slice(ready, less)
		current := ready[0]
		ready = ready[1:]
		result = append(result, current)
		for _, child := range children[current.EventID] {
			indegree[child]--
			if indegree[child] == 0 {
				ready = append(ready, byID[child])
			}
		}
	}
	if len(result) != len(values) {
		return nil, errors.New("event history contains a causal cycle")
	}
	return result, nil
}

func applyEvent(state *protocol.TaskSnapshotState, value protocol.Event) {
	if value.ContentType == "application/vnd.agent-continuity.encrypted+json" {
		return
	}
	switch value.Type {
	case "task.metadata.updated":
		var update struct {
			Title     *string         `json:"title"`
			Objective *string         `json:"objective"`
			Metadata  json.RawMessage `json:"metadata"`
		}
		if json.Unmarshal(value.Payload, &update) == nil {
			if update.Title != nil {
				state.Task.Title = *update.Title
			}
			if update.Objective != nil {
				state.Task.Objective = *update.Objective
			}
			if update.Metadata != nil {
				state.Task.Metadata = append([]byte(nil), update.Metadata...)
			}
		}
	case "task.paused":
		state.Task.Status = "paused"
	case "task.resumed":
		state.Task.Status = "active"
	case "task.completed":
		state.Task.Status = "completed"
	case "progress.updated":
		state.Progress = append([]byte(nil), value.Payload...)
	case "decision.recorded":
		state.Decisions = append(state.Decisions, protocol.SnapshotDecision{EventID: value.EventID, Value: append([]byte(nil), value.Payload...)})
	}
	state.Task.UpdatedAt = value.ReceivedAtServer
}
