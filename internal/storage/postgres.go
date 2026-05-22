// Package storage wraps the Postgres and Redis connections used by the control
// plane, exposing the sqlc-generated query set and health checks.
package storage

import (
	"context"
	"fmt"
	"math"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/neochaotic/leoflow/internal/config"
	"github.com/neochaotic/leoflow/internal/storage/queries"
)

// Postgres holds a pgx connection pool and the generated query set.
type Postgres struct {
	Pool    *pgxpool.Pool
	Queries *queries.Queries
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

// NewPostgres opens a connection pool and verifies connectivity.
func NewPostgres(ctx context.Context, cfg config.DatabaseSection) (*Postgres, error) {
	pc, err := poolConfig(cfg)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, fmt.Errorf("creating connection pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pinging database: %w", err)
	}
	return &Postgres{Pool: pool, Queries: queries.New(pool)}, nil
}

// Ping checks database connectivity (used by /readyz).
func (p *Postgres) Ping(ctx context.Context) error {
	return p.Pool.Ping(ctx)
}

// Close releases the connection pool.
func (p *Postgres) Close() {
	p.Pool.Close()
}
