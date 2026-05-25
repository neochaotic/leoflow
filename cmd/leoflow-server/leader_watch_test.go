package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fakeChecker is a leadershipChecker with a fixed result, counting calls.
type fakeChecker struct {
	mu    sync.Mutex
	held  bool
	err   error
	calls int
}

func (f *fakeChecker) HoldsLock(context.Context) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.held, f.err
}

func (f *fakeChecker) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// TestWatchLeadershipStepsDownOnLostLock: a definitive "not held" cancels the
// run immediately (the split-brain guard).
func TestWatchLeadershipStepsDownOnLostLock(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	chk := &fakeChecker{held: false}
	done := make(chan struct{})
	go func() { watchLeadership(ctx, chk, time.Millisecond, cancel, discardLog()); close(done) }()

	select {
	case <-ctx.Done(): // stepped down
	case <-time.After(2 * time.Second):
		t.Fatal("did not step down when the lock was lost")
	}
	<-done
}

// TestWatchLeadershipToleratesTransientErrors: a single check error does not
// churn leadership; it steps down only after maxLeaderCheckFailures.
func TestWatchLeadershipToleratesTransientErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	chk := &fakeChecker{err: errors.New("connection blip")}
	start := time.Now()
	done := make(chan struct{})
	go func() { watchLeadership(ctx, chk, 10*time.Millisecond, cancel, discardLog()); close(done) }()

	select {
	case <-ctx.Done():
		if elapsed := time.Since(start); elapsed < 25*time.Millisecond {
			t.Errorf("stepped down after %v; should tolerate %d errors first", elapsed, maxLeaderCheckFailures)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("did not step down after repeated check errors")
	}
	<-done
	if got := chk.callCount(); got < maxLeaderCheckFailures {
		t.Errorf("expected >= %d checks before stepping down, got %d", maxLeaderCheckFailures, got)
	}
}

// TestWatchLeadershipStaysWhileHolding: while the lock is held, the watchdog
// never cancels the run.
func TestWatchLeadershipStaysWhileHolding(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	chk := &fakeChecker{held: true}
	done := make(chan struct{})
	go func() { watchLeadership(ctx, chk, time.Millisecond, cancel, discardLog()); close(done) }()

	time.Sleep(40 * time.Millisecond)
	select {
	case <-ctx.Done():
		t.Fatal("must not step down while still holding the lock")
	default:
	}
	cancel() // simulate shutdown; the watchdog should return
	<-done
}
