package storage

import (
	"context"
	"fmt"
	"io"

	"github.com/neochaotic/leoflow/internal/domain"
	"github.com/neochaotic/leoflow/internal/logs"
	"github.com/neochaotic/leoflow/internal/storage/queries"
)

// LogReader resolves a task attempt's log location from API-facing identifiers
// and reads it from the log sink.
type LogReader struct {
	q    *queries.Queries
	sink logs.Sink
}

// NewLogReader builds a LogReader over the given Postgres connection and sink.
func NewLogReader(pg *Postgres, sink logs.Sink) *LogReader {
	return &LogReader{q: pg.Queries, sink: sink}
}

// ReadLogs resolves the run reference (tenant name -> id, run_id -> dag_run id),
// then opens the stored log for the task attempt. It returns domain.ErrNotFound
// when the run or its log file is absent. See issue #21 for the resolution cost.
func (r *LogReader) ReadLogs(ctx context.Context, tenant, dagID, runID, taskID string, tryNumber int) (io.ReadCloser, error) {
	ref, err := r.q.ResolveRunRef(ctx, queries.ResolveRunRefParams{Name: tenant, DagID: dagID, RunID: runID})
	if err != nil {
		return nil, mapNotFound(err)
	}
	rc, err := r.sink.Read(logs.Ref{
		TenantID:  uuidToString(ref.TenantID),
		DagID:     dagID,
		RunID:     uuidToString(ref.DagRunID),
		TaskID:    taskID,
		TryNumber: tryNumber,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %s", domain.ErrNotFound, err.Error())
	}
	return rc, nil
}
