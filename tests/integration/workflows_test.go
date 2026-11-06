package integration

import (
	"context"
	"errors"
	"fmt"
	"github.com/agent-continuity/agent-continuity-master/internal/operations"
	"github.com/agent-continuity/agent-continuity-master/internal/orchestration"
	pgstore "github.com/agent-continuity/agent-continuity-master/internal/storage/postgres"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPostgresWorkflowPersistenceAndFencing(t *testing.T) {
	dsn := os.Getenv("CONTINUITY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("dedicated workflow test database required")
	}
	u, e := url.Parse(dsn)
	if e != nil || !strings.HasPrefix(u.Path, "/continuity_test_") && !strings.HasPrefix(u.Path, "/continuity_archive_test_") {
		t.Fatal("refusing non-test database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	repo, e := pgstore.Open(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer repo.Close()
	if e = repo.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	owner := fmt.Sprintf("workflow-owner-%d", time.Now().UnixNano())
	if e = repo.CreateAccount(ctx, protocol.Account{ID: owner, Name: "workflow integration", CreatedAt: time.Now().UTC()}); e != nil {
		t.Fatal(e)
	}
	s := orchestration.New(repo, map[string]orchestration.Executor{"agent": orchestration.DemoAgent{Name: "persisted"}})
	r, e := s.Create(ctx, owner, "persistent-run", orchestration.Request{Steps: []orchestration.Step{{ID: "a", Agent: "agent"}}, RequireApproval: true})
	if e != nil {
		t.Fatal(e)
	}
	second, e := pgstore.Open(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer second.Close()
	recovered := orchestration.New(second, s.Agents)
	if _, e = recovered.Control(ctx, owner, r.ID, "approve"); e != nil {
		t.Fatal(e)
	}
	if e = recovered.Tick(ctx); e != nil {
		t.Fatal(e)
	}
	r, e = s.Get(ctx, owner, r.ID)
	if e != nil || r.Status != "completed" {
		t.Fatal("restart recovery", r, e)
	}
	usage, e := repo.ListUsage(ctx, owner, time.Time{}, time.Now().Add(time.Hour), 100)
	if e != nil || len(usage) != 1 {
		t.Fatal("transactional workflow meter", len(usage), e)
	}
	o := operations.Record{Kind: "contract", ID: owner, Owner: owner, Status: "queued", Due: time.Now(), Data: operations.Encode(map[string]string{"state": "before"})}
	if e = repo.SaveOperation(ctx, o, 0, nil); e != nil {
		t.Fatal(e)
	}
	old, _ := repo.GetOperation(ctx, o.Kind, o.ID)
	var won atomic.Int32
	var wg sync.WaitGroup
	for _, store := range []*pgstore.Store{repo, second} {
		wg.Add(1)
		go func(store *pgstore.Store) {
			defer wg.Done()
			candidate := old
			candidate.Status = "running"
			e := store.SaveOperation(ctx, candidate, old.Version, nil)
			if e == nil {
				won.Add(1)
			} else if !errors.Is(e, operations.ErrConflict) {
				t.Errorf("CAS: %v", e)
			}
		}(store)
	}
	wg.Wait()
	if won.Load() != 1 {
		t.Fatal("lease has multiple owners")
	}
	current, _ := repo.GetOperation(ctx, o.Kind, o.ID)
	candidate := current
	candidate.Data = operations.Encode(map[string]string{"state": "must rollback"})
	duplicate := usage[0]
	duplicate.IdempotencyKey = "different-effect"
	candidate.Usage = []protocol.UsageRecord{duplicate}
	if e = repo.SaveOperation(ctx, candidate, current.Version, nil); e == nil {
		t.Fatal("expected metering uniqueness failure")
	}
	after, _ := repo.GetOperation(ctx, o.Kind, o.ID)
	if after.Version != current.Version || string(after.Data) != string(current.Data) {
		t.Fatal("workflow committed despite failed meter")
	}
	t.Log("PASS: independent stores recover runs, one CAS claimant, atomic usage rollback")
}
