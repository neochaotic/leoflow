package main

import (
	"context"
	"log/slog"
	"testing"

	"github.com/neochaotic/leoflow/internal/config"
	"github.com/neochaotic/leoflow/internal/logs"
	"github.com/neochaotic/leoflow/internal/storage"
	"github.com/neochaotic/leoflow/internal/xcom"
)

// TestSelectDatastoreEmbeddedWhenNoRedis pins the switch that broke the embedded
// edition: with no Redis URL configured, selectDatastore must pick the embedded
// backends (Postgres XCom + in-process tailer) and dial no Redis — not fall into
// the Redis branch (which a non-empty default redis.url once forced, crashing the
// server on a refused localhost:6379 dial). Guards ADR 0026.
func TestSelectDatastoreEmbeddedWhenNoRedis(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // stops the XCom sweep goroutine

	cfg := &config.ServerConfig{} // zero value: Redis.URL == ""
	pg := &storage.Postgres{}     // nil pool; the embedded path does not dial it here

	backend, tailer, redisHealth, cleanup, err := selectDatastore(ctx, cfg, pg, slog.Default())
	if err != nil {
		t.Fatalf("selectDatastore: %v", err)
	}
	defer cleanup()

	if redisHealth != nil {
		t.Error("embedded mode must register no Redis health check")
	}
	if _, ok := backend.(*xcom.PostgresBackend); !ok {
		t.Errorf("embedded backend = %T, want *xcom.PostgresBackend", backend)
	}
	if _, ok := tailer.(*logs.MemoryTailer); !ok {
		t.Errorf("embedded tailer = %T, want *logs.MemoryTailer", tailer)
	}
}
