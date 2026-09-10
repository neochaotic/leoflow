// Package storage wraps the Postgres and Redis connections used by the control
// plane, exposing the sqlc-generated query set and health checks.
package storage

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync/atomic"
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
	// dirtyAheadLogged latches the readiness probe's "a migration is in flight"
	// log line so it is written once per episode rather than once per probe
	// period for the whole duration of a migration. SchemaReady clears it when
	// the condition clears, so a later upgrade is reported again.
	dirtyAheadLogged atomic.Bool
	// health is a dedicated one-connection pool used by Ping and the schema
	// read, so a probe never queues behind application traffic. See
	// newHealthPool for why that matters. Nil only in tests that construct a
	// Postgres literal; probePool falls back to Pool in that case.
	health *pgxpool.Pool
}

// probePool returns the pool health checks must use: the dedicated one when it
// exists, the main pool otherwise. Every probe read goes through here so no
// single call site can quietly put a probe back on the request path's pool.
func (p *Postgres) probePool() *pgxpool.Pool {
	if p.health != nil {
		return p.health
	}
	return p.Pool
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
	// Opened here rather than wired by the caller: a probe that shares the
	// request pool is the failure mode this exists to prevent (#1042), and a
	// caller that forgets to attach it reintroduces that silently — the checks
	// map still satisfies HealthChecker and every test still passes. pgxpool
	// connects lazily, so this costs no boot latency.
	health, herr := newHealthPool(ctx, cfg)
	if herr != nil {
		pool.Close()
		return nil, herr
	}
	return &Postgres{Pool: pool, Queries: queries.New(pool), specs: newSpecCache(), health: health}, nil
}

// newHealthPool opens the dedicated pool behind Ping and the readiness schema
// read.
//
// Readiness takes two pool acquires per probe — Ping borrows a connection, the
// schema SELECT borrows another — and both used to come from the pool serving
// requests. Under saturation, with that pool fully lent out, the probe blocks
// waiting for a connection and fails on its own deadline. Every replica shares
// the condition, because load is what caused it, so they fail readiness in the
// same window and the Service can empty: a load-induced outage reported as a
// database problem, and one that feeds itself as the pods left in rotation
// absorb the load of the ones that left.
//
// One connection is enough. Both probe reads are single-row SELECTs against
// schema_migrations issued back to back, so nothing here benefits from
// concurrency; what it needs is to never queue behind anything else. Same
// reasoning as NewLeaderPool, one layer over.
func newHealthPool(ctx context.Context, cfg config.DatabaseSection) (*pgxpool.Pool, error) {
	pc, err := singleConnPoolConfig(cfg)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, fmt.Errorf("creating health pool: %w", err)
	}
	return pool, nil
}

// singleConnPoolConfig builds a pool config capped at one connection.
//
// It clamps MinConns as well, which poolConfig sets from database.maxIdleConns
// (chart default 5). pgxpool does not reject MinConns > MaxConns — its
// background health check tries to open the difference and swallows the
// resulting puddle.ErrNotAvailable — so leaving it produces a pool that works
// while attempting four doomed connections every health-check period, forever.
func singleConnPoolConfig(cfg config.DatabaseSection) (*pgxpool.Config, error) {
	pc, err := poolConfig(cfg)
	if err != nil {
		return nil, err
	}
	pc.MaxConns = 1
	pc.MinConns = 0
	pc.MinIdleConns = 0
	return pc, nil
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
	pc, err := singleConnPoolConfig(cfg)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, fmt.Errorf("creating leader pool: %w", err)
	}
	return pool, nil
}

// Ping checks database connectivity (used by /readyz). It goes through
// probePool, not Pool: a connectivity check that has to wait for the request
// path to free a connection is reporting load, not connectivity.
func (p *Postgres) Ping(ctx context.Context) error {
	return p.probePool().Ping(ctx)
}

// Close releases the connection pools.
func (p *Postgres) Close() {
	if p.health != nil {
		p.health.Close()
	}
	p.Pool.Close()
}
