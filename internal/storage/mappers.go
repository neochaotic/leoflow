package storage

import (
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/neochaotic/leoflow/internal/domain"
	"github.com/neochaotic/leoflow/internal/storage/queries"
)

func uuidToString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	b := u.Bytes
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func parseUUID(s string) (pgtype.UUID, error) {
	var u pgtype.UUID
	if err := u.Scan(s); err != nil {
		return pgtype.UUID{}, fmt.Errorf("parsing uuid %q: %w", s, err)
	}
	return u, nil
}

func timePtr(ts pgtype.Timestamptz) *time.Time {
	if !ts.Valid {
		return nil
	}
	t := ts.Time
	return &t
}

func timeVal(ts pgtype.Timestamptz) time.Time {
	if !ts.Valid {
		return time.Time{}
	}
	return ts.Time
}

func ptrTimestamptz(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *t, Valid: true}
}

func strOrEmpty(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// timeFromAny extracts a *time.Time from a scanned interface{} column (used for
// aggregate columns whose type sqlc cannot infer), returning nil when absent.
func timeFromAny(v any) *time.Time {
	if t, ok := v.(time.Time); ok {
		return &t
	}
	return nil
}

func mapDag(d queries.Dag) domain.DAG {
	return domain.DAG{
		DagID:         d.DagID,
		Description:   strOrEmpty(d.Description),
		Owner:         strOrEmpty(d.Owner),
		Tags:          d.Tags,
		Schedule:      d.Schedule,
		IsPaused:      d.IsPaused,
		IsActive:      d.IsActive,
		MaxActiveRuns: int(d.MaxActiveRuns),
		Catchup:       d.Catchup,
	}
}

func mapDagRun(r queries.DagRun, dagID string) domain.DagRun {
	return domain.DagRun{
		DagID:       dagID,
		RunID:       r.RunID,
		LogicalDate: timeVal(r.LogicalDate),
		State:       domain.DagRunState(r.State),
		RunType:     string(r.Trigger),
		QueuedAt:    timeVal(r.QueuedAt),
		StartedAt:   timePtr(r.StartedAt),
		EndedAt:     timePtr(r.EndedAt),
		Note:        strOrEmpty(r.Note),
	}
}

func mapTaskInstance(ti queries.TaskInstance, dagID, runID string) domain.TaskInstance {
	return domain.TaskInstance{
		DagID:     dagID,
		RunID:     runID,
		TaskID:    ti.TaskID,
		MapIndex:  int(ti.MapIndex),
		TryNumber: int(ti.TryNumber),
		MaxTries:  int(ti.MaxTries),
		State:     domain.TaskState(ti.State),
		Operator:  ti.Operator,
		StartedAt: timePtr(ti.StartedAt),
		EndedAt:   timePtr(ti.EndedAt),
		Duration:  ti.DurationSeconds,
		Hostname:  strOrEmpty(ti.Hostname),
		Note:      strOrEmpty(ti.Note),
	}
}
