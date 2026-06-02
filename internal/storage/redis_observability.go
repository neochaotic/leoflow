package storage

import (
	"context"
	"errors"
	"net"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisMetrics is the subset of observability.Metrics the redis hook + pool
// scraper need. Declared as a local interface so internal/storage doesn't
// import internal/observability (cycle-avoidance, also lets tests inject a
// fake without standing up a Prometheus registry).
type RedisMetrics interface {
	RecordRedisCommandFailure(reason string)
	RecordRedisDialFailure(reason string)
	ObserveRedisDialDuration(d time.Duration)
	UpdateRedisPoolStats(active, idle, total uint32)
	RecordRedisPoolTimeout()
}

// observabilityHook implements redis.Hook. It runs *around* every command and
// every dial, classifying any error into one of a small bounded label set so
// alerts can be keyed by reason without unbounded cardinality.
type observabilityHook struct {
	metrics RedisMetrics
}

func newRedisObservabilityHook(m RedisMetrics) redis.Hook { return &observabilityHook{metrics: m} }

// DialHook wraps go-redis's net.Dial. A failure here means we never reached
// Redis — the root cause is upstream (DNS, TLS, firewall). The latency
// observation covers both the TCP handshake and (for rediss://) the TLS
// handshake.
func (h *observabilityHook) DialHook(next redis.DialHook) redis.DialHook {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		start := time.Now()
		conn, err := next(ctx, network, addr)
		h.metrics.ObserveRedisDialDuration(time.Since(start))
		if err != nil {
			h.metrics.RecordRedisDialFailure(classifyDialError(err))
		}
		return conn, err
	}
}

// ProcessHook wraps every individual command. We see the post-retry final
// outcome go-redis returns to the caller; transient retries inside the client
// are invisible by design.
func (h *observabilityHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		err := next(ctx, cmd)
		if err != nil {
			h.metrics.RecordRedisCommandFailure(classifyCommandError(err))
		}
		return err
	}
}

// ProcessPipelineHook is the pipeline counterpart of ProcessHook. We treat
// the pipeline-level error the same way; per-command errors inside the
// pipeline are surfaced by go-redis through individual Cmder.Err() values
// the caller inspects, but the pipeline-level error captures "the network
// died" cases that affect the whole batch.
func (h *observabilityHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		err := next(ctx, cmds)
		if err != nil {
			h.metrics.RecordRedisCommandFailure(classifyCommandError(err))
		}
		return err
	}
}

// classifyCommandError maps a go-redis command error to one of the bounded
// reason labels used in alerts. Order matters — we check the most specific
// known cases first, then fall back to substring matches for the long tail.
// Keep the label set small (the metric is per-label, so cardinality matters).
func classifyCommandError(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, redis.ErrClosed):
		return "client_closed"
	case errors.Is(err, redis.Nil):
		// redis.Nil is "key not found" — that is a valid command outcome,
		// not a failure. We should never hit this branch (callers handle it
		// before the hook gets the err), but stay defensive.
		return "nil_reply"
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "pool exhausted") || strings.Contains(msg, "pool timeout"):
		return "pool_timeout"
	case strings.Contains(msg, "NOAUTH") || strings.Contains(msg, "WRONGPASS"):
		return "auth"
	case strings.Contains(msg, "i/o timeout"):
		return "timeout"
	case strings.Contains(msg, "connection reset") || strings.Contains(msg, "broken pipe"):
		return "connection_reset"
	case strings.Contains(msg, "EOF"):
		return "eof"
	}
	return "other"
}

// classifyDialError covers the dial-time failure modes (TCP connect + TLS
// handshake). These are upstream-network shapes; a sudden uptick almost
// always means a config or routing problem outside Redis itself.
func classifyDialError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "x509:") || strings.Contains(msg, "tls:"):
		return "tls_handshake"
	case strings.Contains(msg, "no such host"):
		return "dns"
	case strings.Contains(msg, "connection refused"):
		return "connection_refused"
	case strings.Contains(msg, "i/o timeout") || strings.Contains(msg, "deadline exceeded"):
		return "timeout"
	}
	return "other"
}

// AttachRedisObservability registers the metrics hook and starts a goroutine
// that scrapes the pool stats every interval. The returned function cancels
// the scraper goroutine; the caller defers it (typically the datastore
// cleanup chain). Lite (Redis nil) is a no-op.
//
// The scraper is a closure so the "last seen cumulative timeouts" counter
// (used to compute per-scrape DELTAS for the Prometheus counter, since
// go-redis exposes Timeouts as a cumulative value) is goroutine-local — no
// shared mutable state.
func AttachRedisObservability(ctx context.Context, r *Redis, m RedisMetrics, interval time.Duration) func() {
	if r == nil || m == nil {
		return func() {}
	}
	r.Client.AddHook(newRedisObservabilityHook(m))
	var lastTimeouts uint32
	scrape := func() { lastTimeouts = updateRedisPool(r, m, lastTimeouts) }
	// One initial snapshot so the gauges are populated from t=0 instead of
	// waiting `interval` for the first tick.
	scrape()
	scrapeCtx, cancel := context.WithCancel(ctx)
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-scrapeCtx.Done():
				return
			case <-t.C:
				scrape()
			}
		}
	}()
	return cancel
}

// updateRedisPool snapshots go-redis's PoolStats into the gauge set and
// returns the cumulative timeouts seen so the caller can compute the next
// delta. It is cheap (struct copy under a sync.Mutex inside go-redis) so the
// scraper can run every few seconds without affecting hot paths.
func updateRedisPool(r *Redis, m RedisMetrics, lastTimeouts uint32) uint32 {
	if r == nil || r.Client == nil {
		return lastTimeouts
	}
	ps := r.Client.PoolStats()
	// PoolStats.Timeouts is cumulative since process start; the exported
	// metric is a Counter, so we increment by the DELTA each scrape.
	delta := ps.Timeouts - lastTimeouts
	for i := uint32(0); i < delta; i++ {
		m.RecordRedisPoolTimeout()
	}
	m.UpdateRedisPoolStats(ps.IdleConns+ps.StaleConns, ps.IdleConns, ps.TotalConns)
	return ps.Timeouts
}
