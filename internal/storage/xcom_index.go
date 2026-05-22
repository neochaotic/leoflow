package storage

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

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
