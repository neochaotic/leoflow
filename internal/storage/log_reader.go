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
// and reads it from the log sink, and tails its live lines.
type LogReader struct {
	q      *queries.Queries
	sink   logs.Sink
	tailer logs.Tailer
}

// NewLogReader builds a LogReader over the given Postgres connection, sink, and
// live-tail tailer (tailer may be nil to disable following).
func NewLogReader(pg *Postgres, sink logs.Sink, tailer logs.Tailer) *LogReader {
	return &LogReader{q: pg.Queries, sink: sink, tailer: tailer}
}

// Tail subscribes to the task attempt's live log lines, returning a line channel
// and a cancel function. It resolves the run reference the same way ReadLogs
// does so the channel matches what the agent publishes.
func (r *LogReader) Tail(ctx context.Context, tenant, dagID, runID, taskID string, tryNumber int) (lines <-chan string, cancel func(), err error) {
	if r.tailer == nil {
		return nil, nil, fmt.Errorf("%w: live tailing not configured", domain.ErrNotFound)
	}
	ref, rerr := r.q.ResolveRunRef(ctx, queries.ResolveRunRefParams{Name: tenant, DagID: dagID, RunID: runID})
	if rerr != nil {
		return nil, nil, mapNotFound(rerr)
	}
	lines, cancel = r.tailer.Subscribe(ctx, logs.Ref{
		TenantID:  uuidToString(ref.TenantID),
		DagID:     dagID,
		RunID:     uuidToString(ref.DagRunID),
		TaskID:    taskID,
		TryNumber: tryNumber,
	})
	return lines, cancel, nil
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
