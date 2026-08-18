// Package storage wraps the Postgres and Redis connections used by the control
// plane, exposing the sqlc-generated query set and health checks.
package storage

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/neochaotic/leoflow/internal/config"
	"github.com/neochaotic/leoflow/internal/storage/queries"
)

// pgStartupBudget caps how long NewPostgres waits for the first successful
// ping. Lite hits this when docker compose marks Postgres Healthy (pg_isready)
// before the server accepts client TCP — the first ping gets `connection reset
// by peer`. Pro hits it during external-PG failover, network blips, or restart
// of the upstream cluster. 30s is comfortably above docker cold-start on a
// busy laptop but still surfaces a real misconfig (wrong DSN, blocked auth)
// before the operator gives up.
const pgStartupBudget = 30 * time.Second

// pgStartupBackoff is the first delay between failed pings; the loop doubles
// it up to a 3s cap. Small enough that a healthy Postgres still feels instant;
// large enough that real misconfigs don't get hammered with reconnect storms.
const pgStartupBackoff = 100 * time.Millisecond

// Postgres holds a pgx connection pool and the generated query set.
type Postgres struct {
	Pool    *pgxpool.Pool
	Queries *queries.Queries
	// specs memoizes parsed DAG specs by (immutable) dag_version_id, shared by
	// the scheduler read path and the dispatch/agent path so a spec is fetched
	// and decoded once per version, not once per active run per tick.
	specs *specCache
}

// poolConfig builds a pgxpool.Config from the database section.
func poolConfig(cfg config.DatabaseSection) (*pgxpool.Config, error) {
	pc, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("parsing database url: %w", err)
	}
	if cfg.MaxOpenConns > 0 && cfg.MaxOpenConns <= math.MaxInt32 {
		pc.MaxConns = int32(cfg.MaxOpenConns)
	}
	if cfg.MaxIdleConns > 0 && cfg.MaxIdleConns <= math.MaxInt32 {
		pc.MinConns = int32(cfg.MaxIdleConns)
	}
	return pc, nil
}

// NewPostgres opens a connection pool and verifies connectivity, retrying
// transient failures during boot for up to pgStartupBudget. Pre-2026-06,
// the first failed ping fatal-ed the server, so a docker compose race or
// any Pro failover blip became a hard crash. The retry loop keeps Lite
// boot ergonomic and Pro startup resilient under realistic upstream-PG
// dynamics. A truly broken setup (wrong DSN, bad auth) still surfaces
// quickly because the underlying error is wrapped into the final error.
func NewPostgres(ctx context.Context, cfg config.DatabaseSection) (*Postgres, error) {
	pc, err := poolConfig(cfg)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, fmt.Errorf("creating connection pool: %w", err)
	}
	if err := connectWithRetry(ctx, pool.Ping, pgStartupBudget, pgStartupBackoff); err != nil {
		pool.Close()
		return nil, err
	}
	return &Postgres{Pool: pool, Queries: queries.New(pool), specs: newSpecCache()}, nil
}

// connectWithRetry calls pingFn until it returns nil, the budget elapses, or
// the context is canceled. Backoff doubles each iteration up to 3s. The final
// error wraps the LAST underlying error so the operator can distinguish
// "connection reset" (transient race) from "auth failed" or "no such host"
// (real misconfig that will never recover). Extracted as a top-level helper
// so it is testable without a live Postgres — see postgres_retry_test.go.
func connectWithRetry(ctx context.Context, pingFn func(context.Context) error, budget, initialBackoff time.Duration) error {
	deadline := time.Now().Add(budget)
	backoff := initialBackoff
	const maxBackoff = 3 * time.Second
	var lastErr error
	for {
		lastErr = pingFn(ctx)
		if lastErr == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("postgres unreachable after %s: %w", budget, lastErr)
		}
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.Canceled) {
				return context.Canceled
			}
			return ctx.Err()
		}
		if backoff < maxBackoff {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

// NewLeaderPool opens a dedicated single-connection pool for the scheduler
// advisory lock, so the session holding the lock is stable (ADR 0009).
func NewLeaderPool(ctx context.Context, cfg config.DatabaseSection) (*pgxpool.Pool, error) {
	pc, err := poolConfig(cfg)
	if err != nil {
		return nil, err
	}
	pc.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, fmt.Errorf("creating leader pool: %w", err)
	}
	return pool, nil
}

// Ping checks database connectivity (used by /readyz).
func (p *Postgres) Ping(ctx context.Context) error {
	return p.Pool.Ping(ctx)
}

// Close releases the connection pool.
func (p *Postgres) Close() {
	p.Pool.Close()
}
