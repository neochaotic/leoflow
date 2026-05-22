//go:build integration

package storage_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/neochaotic/leoflow/internal/config"
	"github.com/neochaotic/leoflow/internal/domain"
	"github.com/neochaotic/leoflow/internal/scheduler"
	"github.com/neochaotic/leoflow/internal/storage"
)

// TestEndToEndPushTriggerSchedule exercises the Phase 2 vertical slice against a
// real Postgres: register a version, create a run, and tick the scheduler until
// the root task reaches queued (no executor, so it stops there).
func TestEndToEndPushTriggerSchedule(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL must point at a migrated database for the e2e test")
	}
	ctx := context.Background()
	pg, err := storage.NewPostgres(ctx, config.DatabaseSection{URL: url})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pg.Close()

	repo := storage.NewRepository(pg)
	sched := scheduler.NewScheduler(storage.NewSchedulerStore(pg),
		slog.New(slog.NewTextHandler(io.Discard, nil)), time.Millisecond)

	dagID := fmt.Sprintf("e2e_%d", time.Now().UnixNano())
	spec := domain.DAGSpec{
		SchemaVersion: "1.0", DagID: dagID, DagVersion: "v1", Image: "img:v1",
		Tasks: []domain.TaskSpec{
			{TaskID: "a", Type: domain.TaskTypePython, Entrypoint: "dag:a"},
			{TaskID: "b", Type: domain.TaskTypePython, Entrypoint: "dag:b", DependsOn: []string{"a"}},
		},
	}
	hash, err := spec.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	if created, rerr := repo.RegisterDagVersion(ctx, "default", spec, hash); rerr != nil || !created {
		t.Fatalf("register version: created=%v err=%v", created, rerr)
	}
	if _, rerr := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: "r1", State: domain.DagRunStateQueued, RunType: "manual", LogicalDate: time.Now().UTC(),
	}); rerr != nil {
		t.Fatalf("create run: %v", rerr)
	}

	// Tick: materialize + start running, then a none->scheduled, then a->queued.
	for i := 0; i < 3; i++ {
		if serr := sched.Step(ctx); serr != nil {
			t.Fatalf("step %d: %v", i, serr)
		}
	}

	tis, _, err := repo.ListTaskInstances(ctx, "default", dagID, "r1", 100, 0)
	if err != nil {
		t.Fatalf("list task instances: %v", err)
	}
	states := map[string]domain.TaskState{}
	for _, ti := range tis {
		states[ti.TaskID] = ti.State
	}
	if states["a"] != domain.TaskStateQueued {
		t.Errorf("task a = %q, want queued", states["a"])
	}
	if states["b"] != domain.TaskStateNone {
		t.Errorf("task b = %q, want none (waiting on a)", states["b"])
	}
}
