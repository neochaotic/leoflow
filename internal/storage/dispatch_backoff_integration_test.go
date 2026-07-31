//go:build integration

package storage_test

import (
	"context"
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

type failingDispatcher struct{}

func (failingDispatcher) Dispatch(context.Context, string, string, domain.TaskSpec) error {
	return context.DeadlineExceeded // any non-nil error
}

// TestDispatchBackoffPersists exercises the two dispatch-backoff queries and the
// ActiveRuns read of the new columns against real Postgres (ADR 0031 Amendment
// A). It verifies the migration applied and the queries round-trip; it does not
// depend on backoff timing.
func TestDispatchBackoffPersists(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL must point at a migrated database")
	}
	ctx := context.Background()
	pg, err := storage.NewPostgres(ctx, config.DatabaseSection{URL: url})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pg.Close()

	repo := storage.NewRepository(pg)
	store := storage.NewSchedulerStore(pg)
	sched := scheduler.NewScheduler(store, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Millisecond)
	sched.SetDispatcher(failingDispatcher{})
	sched.SetLeading(true)

	dagID := "dispatch_backoff_test"
	spec := domain.DAGSpec{
		SchemaVersion: "1.0", DagID: dagID, DagVersion: "v1", Image: "img:v1",
		Tasks: []domain.TaskSpec{{TaskID: "a", Type: domain.TaskTypePython, Entrypoint: "dag:a"}},
	}
	hash, err := spec.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	if _, rerr := repo.RegisterDagVersion(ctx, "default", spec, hash); rerr != nil {
		t.Fatalf("register version: %v", rerr)
	}
	if _, rerr := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: "backoff-run", State: domain.DagRunStateQueued, RunType: "manual", LogicalDate: time.Now().UTC(),
	}); rerr != nil {
		t.Fatalf("create run: %v", rerr)
	}

	// Tick: materialize -> running -> none->scheduled -> scheduled->dispatch(fail).
	for i := 0; i < 4; i++ {
		if serr := sched.Step(ctx); serr != nil {
			t.Fatalf("step %d: %v", i, serr)
		}
	}

	runs, err := store.ActiveRuns(ctx)
	if err != nil {
		t.Fatalf("active runs: %v", err)
	}
	var run *scheduler.RunState
	for i := range runs {
		if runs[i].DagID == dagID {
			run = &runs[i]
		}
	}
	if run == nil {
		t.Fatal("run not found in ActiveRuns")
	}
	// RecordDispatchFailure ran at least once: the counter and the backoff stamp
	// round-tripped through the new columns, and the task is still scheduled.
	if run.DispatchAttempts["a"] < 1 {
		t.Errorf("dispatch_attempts should be >=1 after a failed dispatch, got %d", run.DispatchAttempts["a"])
	}
	if run.NextDispatchAt["a"] == nil {
		t.Error("next_dispatch_at should be set after a failed dispatch")
	}
	if run.States["a"] != domain.TaskStateScheduled {
		t.Errorf("task should still be scheduled during backoff, got %s", run.States["a"])
	}

	// FailDispatchExhausted transitions the scheduled task to failed.
	if ferr := store.FailDispatchExhausted(ctx, run.RunID, "a", "dispatch_failed: test"); ferr != nil {
		t.Fatalf("FailDispatchExhausted: %v", ferr)
	}
	runs, _ = store.ActiveRuns(ctx)
	for i := range runs {
		if runs[i].DagID == dagID && runs[i].States["a"] != domain.TaskStateFailed {
			t.Errorf("task should be failed after FailDispatchExhausted, got %s", runs[i].States["a"])
		}
	}
}
