package storage

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// fakeRedisMetrics records every metric call so tests can assert the contract
// without standing up a Prometheus registry. Concurrency-safe because the
// pool-stats scraper runs in its own goroutine.
type fakeRedisMetrics struct {
	mu            sync.Mutex
	cmdFailures   map[string]int
	dialFailures  map[string]int
	dialDurations []time.Duration
	poolActive    int64
	poolIdle      int64
	poolTotal     int64
	poolTimeouts  int
	updateCalls   int
}

func newFakeRedisMetrics() *fakeRedisMetrics {
	return &fakeRedisMetrics{
		cmdFailures:  map[string]int{},
		dialFailures: map[string]int{},
	}
}

func (f *fakeRedisMetrics) RecordRedisCommandFailure(reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cmdFailures[reason]++
}
func (f *fakeRedisMetrics) RecordRedisDialFailure(reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dialFailures[reason]++
}
func (f *fakeRedisMetrics) ObserveRedisDialDuration(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dialDurations = append(f.dialDurations, d)
}
func (f *fakeRedisMetrics) UpdateRedisPoolStats(active, idle, total uint32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.poolActive = int64(active)
	f.poolIdle = int64(idle)
	f.poolTotal = int64(total)
	f.updateCalls++
}
func (f *fakeRedisMetrics) RecordRedisPoolTimeout() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.poolTimeouts++
}

func (f *fakeRedisMetrics) cmdCount(reason string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cmdFailures[reason]
}
func (f *fakeRedisMetrics) dialCount(reason string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dialFailures[reason]
}

// TestClassifyCommandError pins the bounded label set for command errors —
// the metric is per-label, so any new shape MUST map to one of these reasons
// (not a unique cardinality-exploding string from the upstream error). #311's
// per-reason rate-of-events contract relies on this.
func TestClassifyCommandError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "nil → empty", err: nil, want: ""},
		{name: "context.Canceled → canceled", err: context.Canceled, want: "canceled"},
		{name: "context.DeadlineExceeded → timeout", err: context.DeadlineExceeded, want: "timeout"},
		{name: "fmt.Errorf %w canceled → canceled (unwrap)", err: fmt.Errorf("getting xcom: %w", context.Canceled), want: "canceled"},
		{name: "redis.ErrClosed → client_closed", err: redis.ErrClosed, want: "client_closed"},
		{name: "redis.Nil → nil_reply (defensive)", err: redis.Nil, want: "nil_reply"},
		// NOAUTH / WRONGPASS are verbatim Redis reply prefixes — keep the
		// case as Redis emits so the classifier matches what hits prod.
		{name: "NOAUTH → auth", err: errors.New("redis: NOAUTH Authentication required"), want: "auth"}, //nolint:revive // verbatim Redis reply prefix
		{name: "WRONGPASS → auth", err: errors.New("redis: WRONGPASS invalid password"), want: "auth"},  //nolint:revive // verbatim Redis reply prefix
		{name: "i/o timeout → timeout", err: errors.New("read tcp 10.0.0.1:6379: i/o timeout"), want: "timeout"},
		{name: "connection reset → connection_reset", err: errors.New("write tcp 10.0.0.1:6379: connection reset by peer"), want: "connection_reset"},
		{name: "broken pipe → connection_reset", err: errors.New("write tcp 10.0.0.1:6379: broken pipe"), want: "connection_reset"},
		{name: "EOF → eof", err: errors.New("EOF"), want: "eof"},
		{name: "pool exhausted → pool_timeout", err: errors.New("redis: connection pool exhausted"), want: "pool_timeout"},
		{name: "unknown shape → other (long-tail bucket)", err: errors.New("MOVED 1234 10.0.0.5:6379"), want: "other"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyCommandError(tc.err); got != tc.want {
				t.Errorf("classifyCommandError(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

// TestClassifyDialError pins the upstream-network failure shapes — a sudden
// rate spike on RedisDialFailures{reason="tls_handshake"} catches a managed
// Redis TLS misconfig (#312) before it surfaces as command errors.
func TestClassifyDialError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "nil → empty", err: nil, want: ""},
		{name: "context.Canceled → canceled", err: context.Canceled, want: "canceled"},
		{name: "context.DeadlineExceeded → timeout", err: context.DeadlineExceeded, want: "timeout"},
		{name: "x509 unknown CA → tls_handshake (Memorystore/ElastiCache shape)", err: errors.New("tls: failed to verify certificate: x509: certificate signed by unknown authority"), want: "tls_handshake"},
		{name: "TLS handshake timeout → tls_handshake", err: errors.New("tls: handshake failed: read tcp: i/o timeout"), want: "tls_handshake"},
		{name: "DNS lookup failure → dns", err: errors.New("dial tcp: lookup redis-broken.local: no such host"), want: "dns"},
		{name: "connection refused → connection_refused", err: errors.New("dial tcp 127.0.0.1:6379: connect: connection refused"), want: "connection_refused"},
		{name: "dial i/o timeout → timeout", err: errors.New("dial tcp 10.0.0.1:6379: i/o timeout"), want: "timeout"},
		{name: "unknown shape → other", err: errors.New("blackhole route"), want: "other"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyDialError(tc.err); got != tc.want {
				t.Errorf("classifyDialError(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

// TestObservabilityHookCountsCommandFailures: the ProcessHook must count the
// final error go-redis returns to the caller, classified by reason. A nil
// error must NOT increment (the metric is a failure counter, not a command
// counter).
func TestObservabilityHookCountsCommandFailures(t *testing.T) {
	fm := newFakeRedisMetrics()
	hook := newRedisObservabilityHook(fm)
	process := hook.ProcessHook(func(context.Context, redis.Cmder) error {
		return errors.New("redis: NOAUTH Authentication required") //nolint:revive // verbatim Redis reply prefix
	})
	if err := process(context.Background(), redis.NewCmd(context.Background(), "GET", "k")); err == nil {
		t.Fatal("expected error to propagate")
	}
	if got := fm.cmdCount("auth"); got != 1 {
		t.Errorf("auth count = %d, want 1", got)
	}

	processOK := hook.ProcessHook(func(context.Context, redis.Cmder) error { return nil })
	if err := processOK(context.Background(), redis.NewCmd(context.Background(), "GET", "k")); err != nil {
		t.Fatal(err)
	}
	if got := fm.cmdCount("other"); got != 0 {
		t.Errorf("success must not bump the failure counter; got other=%d", got)
	}
}

// TestAttachRedisObservabilityNilSafe pins the Lite contract: with no Redis
// configured selectDatastore returns rd=nil, and the cleanup chain MUST tolerate
// that without panic. Similarly a nil metrics target is a no-op (defensive — the
// Lite branch never gets here, but we don't want a future caller to NPE).
func TestAttachRedisObservabilityNilSafe(t *testing.T) {
	t.Run("nil Redis → no-op", func(t *testing.T) {
		stop := AttachRedisObservability(context.Background(), nil, newFakeRedisMetrics(), time.Hour)
		stop() // must not panic
	})
	t.Run("nil metrics → no-op", func(t *testing.T) {
		stop := AttachRedisObservability(context.Background(), &Redis{Client: redis.NewClient(&redis.Options{Addr: "127.0.0.1:0"})}, nil, time.Hour)
		stop()
	})
}

// TestAttachRedisObservabilityScrapesPool exercises the goroutine path: a real
// (offline) go-redis client + a fake metrics target, started with a tiny
// scrape interval. We don't need Redis to actually accept connections; we
// only care that the scrape goroutine populates the pool gauges from the
// client's PoolStats(). The closure pattern (no shared mutable state) is
// validated implicitly — go test -race would catch a data race.
func TestAttachRedisObservabilityScrapesPool(t *testing.T) {
	fm := newFakeRedisMetrics()
	// 127.0.0.1:0 is a safe "nowhere" address — the dial will fail on first
	// command but PoolStats() works on the unused client too.
	rd := &Redis{Client: redis.NewClient(&redis.Options{Addr: "127.0.0.1:0"})}
	defer rd.Close()

	stop := AttachRedisObservability(context.Background(), rd, fm, 5*time.Millisecond)
	defer stop()

	// Wait for the initial sync scrape PLUS at least one ticker scrape.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		fm.mu.Lock()
		calls := fm.updateCalls
		fm.mu.Unlock()
		if calls >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	fm.mu.Lock()
	calls := fm.updateCalls
	fm.mu.Unlock()
	if calls < 2 {
		t.Errorf("expected the scraper goroutine to run at least once after the initial sync scrape; updateCalls = %d", calls)
	}
}

// TestObservabilityHookObservesEveryDial: every dial — success or failure —
// records a duration sample (so a flapping TLS path is visible as a P99
// spike even when retries hide the error). On failure the reason counter
// fires too.
func TestObservabilityHookObservesEveryDial(t *testing.T) {
	fm := newFakeRedisMetrics()
	hook := newRedisObservabilityHook(fm)

	t.Run("successful dial: latency recorded, no failure counter", func(t *testing.T) {
		// Use a net.Pipe end as a throwaway non-nil net.Conn — nilnil bans
		// returning (nil, nil) and Pipe() is the cheapest stdlib way to get a
		// valid Conn we close immediately.
		_, end := net.Pipe()
		defer end.Close()
		ok := hook.DialHook(func(context.Context, string, string) (net.Conn, error) { return end, nil })
		_, err := ok(context.Background(), "tcp", "redis:6379")
		if err != nil {
			t.Fatal(err)
		}
		fm.mu.Lock()
		samples := len(fm.dialDurations)
		fm.mu.Unlock()
		if samples != 1 {
			t.Errorf("expected 1 dial sample, got %d", samples)
		}
	})

	t.Run("TLS handshake failure: latency + tls_handshake counter", func(t *testing.T) {
		fmBefore := fm.dialCount("tls_handshake")
		bad := hook.DialHook(func(context.Context, string, string) (net.Conn, error) {
			return nil, errors.New("tls: failed to verify certificate: x509: certificate signed by unknown authority")
		})
		_, err := bad(context.Background(), "tcp", "redis:6379")
		if err == nil {
			t.Fatal("expected error to propagate")
		}
		if got := fm.dialCount("tls_handshake"); got != fmBefore+1 {
			t.Errorf("tls_handshake count = %d, want %d", got, fmBefore+1)
		}
	})
}
