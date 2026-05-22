//go:build integration

package scheduler_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/neochaotic/leoflow/internal/scheduler"
)

func singleConnPool(t *testing.T, url string) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return pool
}

func TestLeaderElectionMutualExclusion(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL must be set for the leader election integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	poolA := singleConnPool(t, url)
	defer poolA.Close()
	poolB := singleConnPool(t, url)
	defer poolB.Close()

	a := scheduler.NewLeader(poolA)
	b := scheduler.NewLeader(poolB)

	gotA, err := a.TryAcquire(ctx)
	if err != nil || !gotA {
		t.Fatalf("A should acquire: ok=%v err=%v", gotA, err)
	}
	gotB, err := b.TryAcquire(ctx)
	if err != nil || gotB {
		t.Fatalf("B should not acquire while A holds: ok=%v err=%v", gotB, err)
	}
	if err := a.Release(ctx); err != nil {
		t.Fatalf("A release: %v", err)
	}
	gotB, err = b.TryAcquire(ctx)
	if err != nil || !gotB {
		t.Fatalf("B should acquire after A releases: ok=%v err=%v", gotB, err)
	}
	if err := b.Release(ctx); err != nil {
		t.Fatalf("B release: %v", err)
	}
}
