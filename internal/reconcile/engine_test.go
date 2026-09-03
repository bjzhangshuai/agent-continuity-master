package reconcile

import (
	"testing"

	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

func TestAnalyzePreservesConcurrentHeadsAndMissingParents(t *testing.T) {
	events := []protocol.Event{
		{EventID: "b", ParentEventIDs: []string{"a"}, LogicalTime: protocol.HLC{WallTimeMS: 2, Node: "b"}},
		{EventID: "c", ParentEventIDs: []string{"missing"}, LogicalTime: protocol.HLC{WallTimeMS: 3, Node: "c"}},
		{EventID: "a", LogicalTime: protocol.HLC{WallTimeMS: 1, Node: "a"}},
		{EventID: "c", LogicalTime: protocol.HLC{WallTimeMS: 3, Node: "c"}},
	}
	result := Analyze(events)
	if len(result.Heads) != 2 || result.Heads[0] != "b" || result.Heads[1] != "c" {
		t.Fatalf("unexpected heads: %v", result.Heads)
	}
	if len(result.Missing) != 1 || result.Missing[0] != "missing" {
		t.Fatalf("unexpected missing: %v", result.Missing)
	}
	if len(result.DuplicateIDs) != 1 || result.DuplicateIDs[0] != "c" {
		t.Fatalf("unexpected duplicates: %v", result.DuplicateIDs)
	}
}
