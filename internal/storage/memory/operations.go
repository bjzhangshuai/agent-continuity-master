package memory

import (
	"context"
	"encoding/json"
	"github.com/agent-continuity/agent-continuity-master/internal/operations"
	"sort"
	"time"
)

func (s *Store) GetOperation(_ context.Context, kind, id string) (operations.Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.operations[kind+":"+id]
	if !ok {
		return r, operations.ErrNotFound
	}
	return operations.Clone(r), nil
}
func (s *Store) ListOperations(_ context.Context, kind, owner, after string, limit int) ([]operations.Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []operations.Record{}
	for _, r := range s.operations {
		if r.Kind == kind && (r.Owner == owner || r.Peer == owner) && r.ID > after {
			out = append(out, operations.Clone(r))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out[:min(len(out), min(max(limit, 1), 1000))], nil
}
func (s *Store) DueOperations(_ context.Context, kind string, now time.Time, limit int) ([]operations.Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []operations.Record{}
	for _, r := range s.operations {
		if r.Kind == kind && (r.Status == "queued" || r.Status == "running" || r.Status == "pending") && !r.Due.After(now) {
			out = append(out, operations.Clone(r))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Due.Equal(out[j].Due) {
			return out[i].ID < out[j].ID
		}
		return out[i].Due.Before(out[j].Due)
	})
	return out[:min(len(out), min(max(limit, 1), 100))], nil
}
func (s *Store) SaveOperation(_ context.Context, r operations.Record, v int64, guards []operations.BillGuard) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Updated.IsZero() {
		r.Updated = time.Now().UTC()
	}
	for _, g := range guards {
		d, ok := s.billingDocuments[g.ID]
		if !ok || d.Version != g.Version {
			return operations.ErrConflict
		}
	}
	if s.operations == nil {
		s.operations = map[string]operations.Record{}
	}
	key := r.Kind + ":" + r.ID
	old, ok := s.operations[key]
	if (v == 0 && ok) || (v > 0 && (!ok || old.Version != v || r.Owner != old.Owner || r.Peer != old.Peer)) {
		return operations.ErrConflict
	}
	r.Version = v + 1
	for _, u := range r.Usage {
		k := u.AccountID + "\x00" + u.Metric + "\x00" + u.IdempotencyKey
		if _, ok := s.usageIdempotency[k]; !ok {
			s.usageRecords[u.ID] = u
			s.usageIdempotency[k] = u.ID
		}
	}
	r.Usage = nil
	s.operations[key] = operations.Clone(r)
	return nil
}

func (s *Store) QueryRuns(_ context.Context, owner string, q operations.RunQuery) ([]operations.Record, int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []operations.Record{}
	total := 0
	for _, o := range s.operations {
		if o.Kind != "run" || o.Owner != owner {
			continue
		}
		var v struct {
			Request struct {
				Context string `json:"context_id"`
			} `json:"request"`
		}
		if e := json.Unmarshal(o.Data, &v); e != nil {
			return nil, 0, e
		}
		contextID := v.Request.Context
		if contextID == "" {
			contextID = "ctx_" + o.ID
		}
		if q.ContextID != "" && q.ContextID != contextID || q.Status != "" && q.Status != o.Status || q.TaskID != "" && q.TaskID != o.Resource || !q.After.IsZero() && o.Updated.Before(q.After) {
			continue
		}
		total++
		if !q.Before.IsZero() && (o.Updated.After(q.Before) || o.Updated.Equal(q.Before) && o.ID >= q.BeforeID) {
			continue
		}
		out = append(out, operations.Clone(o))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Updated.Equal(out[j].Updated) {
			return out[i].ID > out[j].ID
		}
		return out[i].Updated.After(out[j].Updated)
	})
	return out[:min(len(out), min(max(q.Limit, 1), 501))], total, nil
}
