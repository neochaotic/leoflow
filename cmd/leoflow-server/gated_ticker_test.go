package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// runGatedTicker must run its cycle only while leading() is true, so at
// replicaCount>1 a follower's reconciler/GC does not sweep and delete pods the
// leader owns. Deterministic: an unbuffered ticks channel means the send of the
// next tick blocks until the previous one is fully processed, so no sleeps.
func TestRunGatedTickerRunsOnlyWhileLeading(t *testing.T) {
	ticks := make(chan time.Time)
	ran := make(chan struct{}, 8)
	var leading atomic.Bool
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go runGatedTicker(ctx, "test", ticks, leading.Load, discardLog(), func() {
		ran <- struct{}{}
	})

	// Follower: the tick is processed but the cycle must not run.
	leading.Store(false)
	ticks <- time.Now()
	// Leader: this send blocks until the follower tick above was processed
	// (sequential select loop, unbuffered channel), so ordering is guaranteed.
	leading.Store(true)
	ticks <- time.Now()

	select {
	case <-ran:
	case <-time.After(2 * time.Second):
		t.Fatal("leader tick did not run the cycle")
	}
	// Exactly one run: the follower tick contributed nothing.
	select {
	case <-ran:
		t.Error("a follower tick ran the cycle; it must be skipped when not leading")
	default:
	}
}

// A nil gate means "always run" — single-replica / no election.
func TestRunGatedTickerNilGateAlwaysRuns(t *testing.T) {
	ticks := make(chan time.Time)
	ran := make(chan struct{}, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go runGatedTicker(ctx, "test", ticks, nil, discardLog(), func() { ran <- struct{}{} })

	ticks <- time.Now()
	select {
	case <-ran:
	case <-time.After(2 * time.Second):
		t.Fatal("a nil gate should always run the cycle")
	}
}
