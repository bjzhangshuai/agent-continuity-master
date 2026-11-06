// Package orchestration executes bounded DAGs using durable leases and fenced CAS.
package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/agent-continuity/agent-continuity-master/internal/operations"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

type Step struct {
	ID          string   `json:"id"`
	Agent       string   `json:"agent"`
	Needs       []string `json:"needs,omitempty"`
	Input       string   `json:"input"`
	MaxAttempts int      `json:"max_attempts"`
	CostLimit   int64    `json:"cost_limit"`
}
type Request struct {
	TaskID          string          `json:"task_id,omitempty"`
	OrganizationID  string          `json:"organization_id,omitempty"`
	ContextID       string          `json:"context_id,omitempty"`
	MessageID       string          `json:"message_id,omitempty"`
	Message         json.RawMessage `json:"message,omitempty"`
	Steps           []Step          `json:"steps"`
	Budget          int64           `json:"budget"`
	RequireApproval bool            `json:"require_approval"`
}
type StepState struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Attempts int    `json:"attempts"`
	Output   string `json:"output,omitempty"`
	Error    string `json:"error,omitempty"`
}
type Event struct {
	At     time.Time `json:"at"`
	Action string    `json:"action"`
	Step   string    `json:"step,omitempty"`
	Actor  string    `json:"actor,omitempty"`
}
type Run struct {
	ID        string      `json:"run_id"`
	AccountID string      `json:"account_id"`
	Request   Request     `json:"request"`
	Status    string      `json:"status"`
	Spent     int64       `json:"spent"`
	Steps     []StepState `json:"steps"`
	Events    []Event     `json:"events"`
	Updated   time.Time   `json:"updated_at"`
}
type Input struct {
	ID           string            `json:"idempotency_key"`
	Prompt       string            `json:"prompt"`
	Dependencies map[string]string `json:"dependencies"`
}
type Executor interface {
	Execute(context.Context, Input) (string, error)
}
type Service struct {
	Repo   operations.Repository
	Agents map[string]Executor
	Now    func() time.Time
	Lease  time.Duration
}

func New(repo operations.Repository, agents map[string]Executor) *Service {
	return &Service{Repo: repo, Agents: agents, Now: time.Now, Lease: 45 * time.Second}
}
func (s *Service) Create(ctx context.Context, owner, key string, q Request) (Run, error) {
	if len(key) < 8 || len(key) > 200 || len(q.Steps) == 0 || len(q.Steps) > 32 || q.Budget < 0 || q.Budget > 100000000 || len(q.ContextID) > 200 || len(q.Message) > 65536 {
		return Run{}, operations.ErrInvalid
	}
	seen := map[string]bool{}
	// Topological order is explicit: dependencies must precede their consumers.
	for i := range q.Steps {
		p := &q.Steps[i]
		if p.MaxAttempts == 0 {
			p.MaxAttempts = 3
		}
		if p.ID == "" || len(p.ID) > 100 || seen[p.ID] || s.Agents[p.Agent] == nil || len(p.Input) > 32768 || p.MaxAttempts < 1 || p.MaxAttempts > 5 || p.CostLimit < 0 || p.CostLimit > q.Budget {
			return Run{}, operations.ErrInvalid
		}
		depsSeen := map[string]bool{}
		for _, n := range p.Needs {
			if depsSeen[n] {
				return Run{}, operations.ErrInvalid
			}
			depsSeen[n] = true
			if !seen[n] {
				return Run{}, fmt.Errorf("%w: cyclic or unordered dependency", operations.ErrInvalid)
			}
		}
		seen[p.ID] = true
	}
	id := operations.ID("run", owner, key)
	if old, err := s.Repo.GetOperation(ctx, "run", id); err == nil {
		var r Run
		if err = json.Unmarshal(old.Data, &r); err != nil {
			return r, err
		}
		if string(operations.Encode(r.Request)) != string(operations.Encode(q)) {
			return r, operations.ErrConflict
		}
		return r, nil
	} else if !errors.Is(err, operations.ErrNotFound) {
		return Run{}, err
	}
	r := Run{ID: id, AccountID: owner, Request: q, Status: "queued", Steps: []StepState{}, Events: []Event{}, Updated: s.Now().UTC()}
	if q.RequireApproval {
		r.Status = "approval_required"
	}
	for _, p := range q.Steps {
		r.Steps = append(r.Steps, StepState{ID: p.ID, Status: "queued"})
	}
	s.event(&r, "created", "", owner)
	err := s.Repo.SaveOperation(ctx, record(r), 0, nil)
	if errors.Is(err, operations.ErrConflict) {
		return s.Create(ctx, owner, key, q)
	}
	return r, err
}
func (s *Service) Get(ctx context.Context, owner, id string) (Run, error) {
	o, err := s.Repo.GetOperation(ctx, "run", id)
	if err != nil {
		return Run{}, err
	}
	if o.Owner != owner {
		return Run{}, operations.ErrNotFound
	}
	var r Run
	err = json.Unmarshal(o.Data, &r)
	return r, err
}
func (s *Service) Control(ctx context.Context, owner, id, action string) (Run, error) {
	for attempt := 0; attempt < 8; attempt++ {
		o, err := s.Repo.GetOperation(ctx, "run", id)
		if err != nil || o.Owner != owner {
			return Run{}, operations.ErrNotFound
		}
		var r Run
		if err = json.Unmarshal(o.Data, &r); err != nil {
			return r, err
		}
		switch action {
		case "approve":
			if r.Status != "approval_required" {
				return r, operations.ErrConflict
			}
			r.Status = "queued"
		case "cancel":
			if r.Status == "canceled" {
				return r, nil
			}
			if terminal(r.Status) {
				return r, operations.ErrConflict
			}
			r.Status = "canceled"
			for i := range r.Steps {
				if r.Steps[i].Status != "completed" {
					r.Steps[i].Status = "canceled"
				}
			}
		default:
			return r, operations.ErrInvalid
		}
		s.event(&r, action, "", owner)
		err = s.Repo.SaveOperation(ctx, record(r), o.Version, nil)
		if !errors.Is(err, operations.ErrConflict) {
			return r, err
		}
	}
	return Run{}, operations.ErrConflict
}
func terminal(state string) bool {
	return slices.Contains([]string{"completed", "failed", "canceled"}, state)
}
func record(r Run) operations.Record {
	return operations.Record{Kind: "run", ID: r.ID, Updated: r.Updated, Resource: r.Request.TaskID, Owner: r.AccountID, Status: r.Status, Due: r.Updated, Data: operations.Encode(r)}
}
func (s *Service) event(r *Run, action, step, actor string) {
	r.Updated = s.Now().UTC().Truncate(time.Microsecond)
	r.Events = append(r.Events, Event{At: r.Updated, Action: action, Step: step, Actor: actor})
}

// Tick claims a single ready step. A stale worker cannot publish after cancellation
// or lease takeover. Executors receive the same effect ID on every attempt.
func (s *Service) Tick(ctx context.Context) error {
	rows, err := s.Repo.DueOperations(ctx, "run", s.Now(), 16)
	if err != nil {
		return err
	}
	for _, o := range rows {
		var r Run
		if err = json.Unmarshal(o.Data, &r); err != nil {
			return err
		}
		index := -1
		for i, st := range r.Steps {
			if st.Status == "completed" {
				continue
			}
			if st.Attempts >= r.Request.Steps[i].MaxAttempts {
				r.Status = "failed"
				r.Steps[i].Status = "failed"
				s.event(&r, "attempts_exhausted", st.ID, "")
				break
			}
			index = i
			break
		}
		if terminal(r.Status) || index < 0 {
			if !terminal(r.Status) {
				r.Status = "completed"
				s.event(&r, "completed", "", "")
			}
			if e := s.Repo.SaveOperation(ctx, record(r), o.Version, nil); e != nil && !errors.Is(e, operations.ErrConflict) {
				return e
			}
			continue
		}
		p := r.Request.Steps[index]
		st := &r.Steps[index]
		if p.CostLimit > r.Request.Budget-r.Spent {
			r.Status = "failed"
			st.Status = "failed"
			st.Error = "budget exhausted"
			s.event(&r, "budget_exhausted", p.ID, "")
			_ = s.Repo.SaveOperation(ctx, record(r), o.Version, nil)
			continue
		}
		deps := map[string]string{}
		for _, n := range p.Needs {
			for _, v := range r.Steps {
				if v.ID == n && v.Status == "completed" {
					deps[n] = v.Output
				}
			}
		}
		if len(deps) != len(p.Needs) {
			return errors.New("invalid persisted dependency state")
		}
		st.Attempts++
		st.Status = "running"
		r.Status = "running"
		r.Spent += p.CostLimit
		s.event(&r, "step_started", p.ID, "")
		claimed := record(r)
		claimed.Due = s.Now().Add(s.Lease)
		if err = s.Repo.SaveOperation(ctx, claimed, o.Version, nil); errors.Is(err, operations.ErrConflict) {
			continue
		} else if err != nil {
			return err
		}
		callCtx, cancel := context.WithTimeout(ctx, s.Lease/2)
		executor := s.Agents[p.Agent]
		var output string
		var callErr error
		if executor == nil {
			callErr = errors.New("agent unavailable")
		} else {
			output, callErr = executor.Execute(callCtx, Input{ID: r.ID + ":" + p.ID, Prompt: p.Input, Dependencies: deps})
		}
		cancel()
		// The output bound protects the aggregate and downstream agents.
		if len(output) > 128<<10 {
			callErr = errors.New("agent output exceeds 128 KiB")
		}
		if callErr != nil {
			st.Error = "agent execution failed; retry uses original effect ID"
			st.Status = "queued"
			r.Status = "queued"
			if st.Attempts >= p.MaxAttempts {
				st.Status = "failed"
				r.Status = "failed"
			}
			s.event(&r, "step_failed", p.ID, "")
		} else {
			st.Error = ""
			st.Output = output
			st.Status = "completed"
			r.Status = "queued"
			if index == len(r.Steps)-1 {
				r.Status = "completed"
			}
			s.event(&r, "step_completed", p.ID, "")
		}
		finished := record(r)
		if callErr != nil {
			finished.Due = s.Now().Add(time.Duration(st.Attempts) * time.Second)
		} else {
			uid := operations.ID("usage_run", r.AccountID, r.ID+":"+p.ID)
			finished.Usage = []protocol.UsageRecord{{ID: uid, AccountID: r.AccountID, OrganizationID: r.Request.OrganizationID, Metric: "api.mutation", Quantity: 1, Unit: "operation", ResourceID: r.Request.TaskID, IdempotencyKey: uid, Metadata: operations.Encode(map[string]string{"action": "orchestration.step.completed", "run_id": r.ID, "step_id": p.ID}), RecordedAt: r.Updated}}
		}
		if err = s.Repo.SaveOperation(ctx, finished, o.Version+1, nil); err != nil && !errors.Is(err, operations.ErrConflict) {
			return err
		}
	}
	return nil
}
func (s *Service) Run(ctx context.Context, logger *slog.Logger) {
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if err := s.Tick(ctx); err != nil {
				logger.Error("orchestration worker", "error", err)
			}
		}
	}
}

// DemoAgent is deliberately deterministic and labelled; it never calls a model.
type DemoAgent struct{ Name string }

func (d DemoAgent) Execute(_ context.Context, in Input) (string, error) {
	keys := make([]string, 0, len(in.Dependencies))
	for k := range in.Dependencies {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	parts := []string{"[SIMULATED " + d.Name + "] " + in.Prompt}
	for _, k := range keys {
		parts = append(parts, k+": "+in.Dependencies[k])
	}
	return strings.Join(parts, "\n"), nil
}
