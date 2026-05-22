package scheduler

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// LockID is the fixed Postgres advisory-lock id gating scheduler leadership
// ("LeoFlow" in hex), per ADR 0009.
const LockID int64 = 0x4C656F466C6F77

// Leader acquires and releases the advisory lock that restricts the scheduler
// loop to a single replica. It must run on a dedicated single-connection pool
// so the session holding the lock is stable.
type Leader struct {
	pool *pgxpool.Pool
}

// NewLeader builds a Leader over a dedicated (single-connection) pool.
func NewLeader(pool *pgxpool.Pool) *Leader {
	return &Leader{pool: pool}
}

// TryAcquire attempts to take the scheduler advisory lock without blocking.
func (l *Leader) TryAcquire(ctx context.Context) (bool, error) {
	var acquired bool
	if err := l.pool.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", LockID).Scan(&acquired); err != nil {
		return false, fmt.Errorf("acquiring scheduler lock: %w", err)
	}
	return acquired, nil
}

// Release frees the scheduler advisory lock.
func (l *Leader) Release(ctx context.Context) error {
	if _, err := l.pool.Exec(ctx, "SELECT pg_advisory_unlock($1)", LockID); err != nil {
		return fmt.Errorf("releasing scheduler lock: %w", err)
	}
	return nil
}
