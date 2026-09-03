package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// TestMaintenanceCycleReconcilesBeforeReaping: one maintenance cycle runs the
// reconciler's sweep and THEN the reapers, in that order, every time. The
// ordering is the structural fix for the drill race — a reaper that judged a
// task pod "gone" before the reconciler had recovered the pod's durable outcome
// — and it holds by construction rather than by two independent timers lining
// up.
func TestMaintenanceCycleReconcilesBeforeReaping(t *testing.T) {
	var order []string
	reconcile := func(context.Context) error { order = append(order, "reconcile"); return nil }
	reap := func(context.Context) error { order = append(order, "reap"); return nil }

	for i := 0; i < 3; i++ {
		maintenanceCycle(context.Background(), reconcile, reap, discardLog())
	}
	want := []string{"reconcile", "reap", "reconcile", "reap", "reconcile", "reap"}
	if len(order) != len(want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}
}

// TestMaintenanceCycleReapsWhenReconcileFails: a reconcile error (the apiserver
// LIST failed) is logged, not fatal to the cycle — the reapers still run. They
// are independent backstops with their own fail-closed liveness reads, and the
// reaper's settling gate, not this loop, decides whether a leader that has never
// completed a sweep may act.
func TestMaintenanceCycleReapsWhenReconcileFails(t *testing.T) {
	reaped := false
	reconcile := func(context.Context) error { return errors.New("apiserver down") }
	reap := func(context.Context) error { reaped = true; return nil }

	maintenanceCycle(context.Background(), reconcile, reap, discardLog())
	if !reaped {
		t.Fatal("the reapers must run even when the reconcile sweep failed")
	}
}

// TestMaintenanceLoopHonorsLeaderGate: the maintenance cycle runs on the
// leader-gated ticker, so a follower tick neither reconciles nor reaps —
// reaping writes state and must stay single-writer across the fleet — and a
// leader tick does both. Same stable-gate construction as the gated-ticker
// tests: two sends on an unbuffered channel prove the first tick was fully
// processed.
func TestMaintenanceLoopHonorsLeaderGate(t *testing.T) {
	newLoop := func(leading *atomic.Bool) (ticks chan time.Time, ran chan string, stop func()) {
		ticks = make(chan time.Time)
		ran = make(chan string, 8)
		ctx, cancel := context.WithCancel(context.Background())
		go runGatedTicker(ctx, "maintenance", ticks, leading.Load, discardLog(), func() {
			maintenanceCycle(ctx,
				func(context.Context) error { ran <- "reconcile"; return nil },
				func(context.Context) error { ran <- "reap"; return nil },
				discardLog())
		})
		return ticks, ran, cancel
	}

	t.Run("follower", func(t *testing.T) {
		var leading atomic.Bool // stays false
		ticks, ran, stop := newLoop(&leading)
		defer stop()
		ticks <- time.Now()
		ticks <- time.Now()
		select {
		case got := <-ran:
			t.Errorf("a follower tick ran %q; the maintenance cycle must be leader-only", got)
		default:
		}
	})
	t.Run("leader", func(t *testing.T) {
		var leading atomic.Bool
		leading.Store(true)
		ticks, ran, stop := newLoop(&leading)
		defer stop()
		ticks <- time.Now()
		var got []string
		for len(got) < 2 {
			select {
			case s := <-ran:
				got = append(got, s)
			case <-time.After(2 * time.Second):
				t.Fatalf("leader tick did not run the whole cycle, got %v", got)
			}
		}
		if got[0] != "reconcile" || got[1] != "reap" {
			t.Errorf("cycle order = %v, want [reconcile reap]", got)
		}
	})
}
