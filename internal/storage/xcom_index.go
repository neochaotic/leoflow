package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/neochaotic/leoflow/internal/domain"
	"github.com/neochaotic/leoflow/internal/storage/queries"
	"github.com/neochaotic/leoflow/internal/xcom"
)

// XComIndex is the Postgres-backed XCom metadata index. It implements
// xcom.Index, recording each pushed value so the API can find and list it.
type XComIndex struct {
	q *queries.Queries
}

// NewXComIndex builds an XComIndex over the given Postgres connection.
func NewXComIndex(pg *Postgres) *XComIndex {
	return &XComIndex{q: pg.Queries}
}

// RecordXCom upserts the metadata for a pushed XCom value.
func (x *XComIndex) RecordXCom(ctx context.Context, e xcom.IndexEntry) error {
	tid, err := parseUUID(e.TenantID)
	if err != nil {
		return fmt.Errorf("parsing tenant id: %w", err)
	}
	rid, err := parseUUID(e.RunID)
	if err != nil {
		return fmt.Errorf("parsing run id: %w", err)
	}
	return x.q.RecordXCom(ctx, queries.RecordXComParams{
		TenantID:    tid,
		DagRunID:    rid,
		TaskID:      e.TaskID,
		Key:         e.Name,
		RedisKey:    e.RedisKey,
		SizeBytes:   toInt32(e.SizeBytes),
		ContentType: e.ContentType,
		ExpiresAt:   pgtype.Timestamptz{Time: e.ExpiresAt, Valid: true},
	})
}

// XComReader reads XCom values for the API: it resolves the Redis key from the
// Postgres index by name and fetches the value from the backend.
type XComReader struct {
	q       *queries.Queries
	backend xcom.Backend
}

// NewXComReader builds an XComReader over the given Postgres connection and
// XCom backend.
func NewXComReader(pg *Postgres, backend xcom.Backend) *XComReader {
	return &XComReader{q: pg.Queries, backend: backend}
}

// GetXCom returns the XCom entry for the named value, or domain.ErrNotFound when
// it is absent or expired (in the index or in Redis).
func (r *XComReader) GetXCom(ctx context.Context, tenant, dagID, runID, taskID, key string) (xcom.Entry, error) {
	if key == "" {
		key = "return_value"
	}
	row, err := r.q.GetXComByNames(ctx, queries.GetXComByNamesParams{
		Name: tenant, DagID: dagID, RunID: runID, TaskID: taskID, Key: key,
	})
	if err != nil {
		return xcom.Entry{}, mapNotFound(err)
	}
	entry, err := r.backend.Fetch(ctx, row.RedisKey)
	if errors.Is(err, xcom.ErrNotFound) {
		return xcom.Entry{}, domain.ErrNotFound
	}
	if err != nil {
		return xcom.Entry{}, err
	}
	return entry, nil
}
