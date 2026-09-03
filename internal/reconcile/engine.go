package reconcile

import (
	"sort"

	"github.com/agent-continuity/agent-continuity-master/internal/event"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

type Result struct {
	Ordered      []protocol.Event
	Heads        []string
	Missing      []string
	Concurrent   [][2]string
	DuplicateIDs []string
}

func Analyze(events []protocol.Event) Result {
	byID := make(map[string]protocol.Event, len(events))
	duplicates := make([]string, 0)
	for _, item := range events {
		if _, ok := byID[item.EventID]; ok {
			duplicates = append(duplicates, item.EventID)
			continue
		}
		byID[item.EventID] = item
	}
	children := map[string][]string{}
	missingSet := map[string]struct{}{}
	for _, item := range byID {
		for _, parent := range item.ParentEventIDs {
			if _, ok := byID[parent]; !ok {
				missingSet[parent] = struct{}{}
			} else {
				children[parent] = append(children[parent], item.EventID)
			}
		}
	}
	ordered := make([]protocol.Event, 0, len(byID))
	for _, item := range byID {
		ordered = append(ordered, item)
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		if cmp := event.Compare(ordered[i].LogicalTime, ordered[j].LogicalTime); cmp != 0 {
			return cmp < 0
		}
		if ordered[i].DeviceSequence != ordered[j].DeviceSequence {
			return ordered[i].DeviceSequence < ordered[j].DeviceSequence
		}
		return ordered[i].EventID < ordered[j].EventID
	})
	heads := make([]string, 0)
	for id := range byID {
		if len(children[id]) == 0 {
			heads = append(heads, id)
		}
	}
	sort.Strings(heads)
	missing := make([]string, 0, len(missingSet))
	for id := range missingSet {
		missing = append(missing, id)
	}
	sort.Strings(missing)
	sort.Strings(duplicates)
	concurrent := make([][2]string, 0)
	for i := 0; i < len(heads); i++ {
		for j := i + 1; j < len(heads); j++ {
			concurrent = append(concurrent, [2]string{heads[i], heads[j]})
		}
	}
	return Result{Ordered: ordered, Heads: heads, Missing: missing, Concurrent: concurrent, DuplicateIDs: duplicates}
}
