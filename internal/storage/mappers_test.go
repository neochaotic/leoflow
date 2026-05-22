package storage

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/neochaotic/leoflow/internal/domain"
	"github.com/neochaotic/leoflow/internal/storage/queries"
)

func TestUUIDRoundTrip(t *testing.T) {
	const s = "11111111-2222-3333-4444-555555555555"
	u, err := parseUUID(s)
	if err != nil {
		t.Fatal(err)
	}
	if got := uuidToString(u); got != s {
		t.Errorf("uuid round trip = %q, want %q", got, s)
	}
	if uuidToString(pgtype.UUID{}) != "" {
		t.Error("invalid uuid should map to empty string")
	}
	if _, err := parseUUID("not-a-uuid"); err == nil {
		t.Error("invalid uuid string should error")
	}
}

func TestTimestamptzHelpers(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	if timePtr(pgtype.Timestamptz{}) != nil {
		t.Error("invalid timestamptz should map to nil pointer")
	}
	if got := timePtr(pgtype.Timestamptz{Time: now, Valid: true}); got == nil || !got.Equal(now) {
		t.Errorf("timePtr = %v, want %v", got, now)
	}
	if !timeVal(pgtype.Timestamptz{}).IsZero() {
		t.Error("invalid timestamptz should map to zero time")
	}
	if !ptrTimestamptz(nil).Valid == false {
		t.Error("nil time should map to invalid timestamptz")
	}
	if ts := ptrTimestamptz(&now); !ts.Valid || !ts.Time.Equal(now) {
		t.Errorf("ptrTimestamptz = %+v", ts)
	}
}

func TestStrHelpers(t *testing.T) {
	if strOrEmpty(nil) != "" {
		t.Error("nil -> empty")
	}
	v := "x"
	if strOrEmpty(&v) != "x" {
		t.Error("ptr -> value")
	}
	if strPtr("") != nil {
		t.Error("empty -> nil")
	}
	if got := strPtr("y"); got == nil || *got != "y" {
		t.Error("value -> ptr")
	}
}

func TestMapDag(t *testing.T) {
	sched := "0 5 * * *"
	owner := "data"
	d := mapDag(queries.Dag{
		DagID: "etl", Owner: &owner, Tags: []string{"a"},
		Schedule: &sched, IsPaused: true, IsActive: true, MaxActiveRuns: 16, Catchup: true,
	})
	if d.DagID != "etl" || d.Owner != "data" || !d.IsPaused || !d.Catchup || d.MaxActiveRuns != 16 {
		t.Errorf("unexpected dag: %+v", d)
	}
	if d.Schedule == nil || *d.Schedule != sched {
		t.Errorf("schedule = %v", d.Schedule)
	}
}

func TestMapDagRun(t *testing.T) {
	now := time.Now().UTC()
	r := mapDagRun(queries.DagRun{
		RunID: "r1", State: queries.DagRunStateRunning, Trigger: queries.DagRunTriggerManual,
		LogicalDate: pgtype.Timestamptz{Time: now, Valid: true},
		QueuedAt:    pgtype.Timestamptz{Time: now, Valid: true},
	}, "etl")
	if r.DagID != "etl" || r.RunID != "r1" || r.State != domain.DagRunStateRunning || r.RunType != "manual" {
		t.Errorf("unexpected run: %+v", r)
	}
	if r.StartedAt != nil {
		t.Error("unset started_at should be nil")
	}
}

func TestMapTaskInstance(t *testing.T) {
	ti := mapTaskInstance(queries.TaskInstance{
		TaskID: "extract", State: queries.TaskStateQueued, Operator: "python",
		TryNumber: 1, MaxTries: 3,
	}, "etl", "r1")
	if ti.TaskID != "extract" || ti.State != domain.TaskStateQueued || ti.Operator != "python" || ti.MaxTries != 3 {
		t.Errorf("unexpected ti: %+v", ti)
	}
}
