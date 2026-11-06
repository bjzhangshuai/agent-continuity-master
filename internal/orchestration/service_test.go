package orchestration_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/agent-continuity/agent-continuity-master/internal/operations"
	"github.com/agent-continuity/agent-continuity-master/internal/orchestration"
	"github.com/agent-continuity/agent-continuity-master/internal/storage/memory"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type executorFunc func(context.Context, orchestration.Input) (string, error)

func (f executorFunc) Execute(ctx context.Context, in orchestration.Input) (string, error) {
	return f(ctx, in)
}
func TestDAGApprovalRetryBudgetAndExactlyOnceMeter(t *testing.T) {
	ctx := context.Background()
	repo := memory.New()
	now := time.Now()
	calls := 0
	ids := []string{}
	s := orchestration.New(repo, map[string]orchestration.Executor{"first": executorFunc(func(_ context.Context, i orchestration.Input) (string, error) {
		calls++
		ids = append(ids, i.ID)
		if calls == 1 {
			return "", errors.New("lost reply")
		}
		return "research result", nil
	}), "review": executorFunc(func(_ context.Context, i orchestration.Input) (string, error) {
		if i.Dependencies["a"] != "research result" {
			t.Fatal("upstream output missing")
		}
		return "reviewed", nil
	})})
	s.Now = func() time.Time { return now }
	q := orchestration.Request{Steps: []orchestration.Step{{ID: "a", Agent: "first", CostLimit: 2}, {ID: "b", Agent: "review", Needs: []string{"a"}, CostLimit: 1}}, Budget: 6, RequireApproval: true}
	r, e := s.Create(ctx, "owner", "request-key", q)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Tick(ctx); e != nil || calls != 0 {
		t.Fatal("approval bypass", e)
	}
	if _, e = s.Get(ctx, "other", r.ID); !errors.Is(e, operations.ErrNotFound) {
		t.Fatal("tenant leak")
	}
	_, e = s.Control(ctx, "owner", r.ID, "approve")
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 4; i++ {
		now = now.Add(10 * time.Second)
		if e = s.Tick(ctx); e != nil {
			t.Fatal(e)
		}
	}
	r, e = s.Get(ctx, "owner", r.ID)
	if e != nil || r.Status != "completed" || r.Spent != 5 || r.Steps[1].Output != "reviewed" || ids[0] != ids[1] {
		t.Fatalf("bad run %+v %v", r, e)
	}
	usage, e := repo.ListUsage(ctx, "owner", time.Time{}, now.Add(time.Hour), 100)
	if e != nil || len(usage) != 2 {
		t.Fatalf("duplicate/missing meters %d %v", len(usage), e)
	}
	q.Steps[0].Input = "different"
	if _, e = s.Create(ctx, "owner", "request-key", q); !errors.Is(e, operations.ErrConflict) {
		t.Fatal("idempotency conflict missing")
	}
	q.Steps[0].Needs = []string{"b"}
	if _, e = s.Create(ctx, "owner", "cyclic-key", q); !errors.Is(e, operations.ErrInvalid) {
		t.Fatal("cycle accepted")
	}
}
func TestCancellationFencesWorkerAndConcurrentClaim(t *testing.T) {
	ctx := context.Background()
	repo := memory.New()
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	s := orchestration.New(repo, map[string]orchestration.Executor{"agent": executorFunc(func(context.Context, orchestration.Input) (string, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return "late result", nil
	})})
	r, e := s.Create(ctx, "owner", "cancel-key", orchestration.Request{Steps: []orchestration.Step{{ID: "a", Agent: "agent"}}})
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = s.Tick(ctx) }()
	<-started
	if e = s.Tick(ctx); e != nil {
		t.Fatal(e)
	}
	if calls.Load() != 1 {
		t.Fatal("double claim")
	}
	if _, e = s.Control(ctx, "owner", r.ID, "cancel"); e != nil {
		t.Fatal(e)
	}
	close(release)
	wg.Wait()
	r, e = s.Get(ctx, "owner", r.ID)
	if e != nil || r.Status != "canceled" || r.Steps[0].Output != "" {
		t.Fatalf("late worker overwrote cancellation: %+v", r)
	}
	u, _ := repo.ListUsage(ctx, "owner", time.Time{}, time.Now().Add(time.Hour), 100)
	if len(u) != 0 {
		t.Fatal("canceled attempt charged")
	}
}
func TestLeaseTakeoverAndBudgetExhaustion(t *testing.T) {
	ctx := context.Background()
	repo := memory.New()
	s := orchestration.New(repo, map[string]orchestration.Executor{"agent": orchestration.DemoAgent{Name: "recovery"}})
	r, e := s.Create(ctx, "owner", "lease-key", orchestration.Request{Steps: []orchestration.Step{{ID: "a", Agent: "agent", CostLimit: 1}}, Budget: 1})
	if e != nil {
		t.Fatal(e)
	}
	o, _ := repo.GetOperation(ctx, "run", r.ID)
	r.Status = "running"
	r.Spent = 1
	r.Steps[0].Status = "running"
	r.Steps[0].Attempts = 1
	o.Status = "running"
	o.Due = time.Now().Add(-time.Minute)
	o.Data = operations.Encode(r)
	if e = repo.SaveOperation(ctx, o, o.Version, nil); e != nil {
		t.Fatal(e)
	}
	if e = s.Tick(ctx); e != nil {
		t.Fatal(e)
	}
	o, _ = repo.GetOperation(ctx, "run", r.ID)
	_ = json.Unmarshal(o.Data, &r)
	if r.Status != "failed" || r.Steps[0].Error != "budget exhausted" {
		t.Fatal("lease exceeded budget", r)
	}
}
