package logs

import (
	"context"
	"testing"
	"time"
)

func memTailRef() Ref {
	return Ref{TenantID: "t", DagID: "d", RunID: "r", TaskID: "task", TryNumber: 1}
}

// TestMemoryTailerDeliversToSubscriber: a published line reaches a live subscriber.
func TestMemoryTailerDeliversToSubscriber(t *testing.T) {
	tl := NewMemoryTailer()
	ctx := context.Background()
	lines, cancel := tl.Subscribe(ctx, memTailRef())
	defer cancel()
	if err := tl.Publish(ctx, memTailRef(), "hello"); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	select {
	case got := <-lines:
		if got != "hello" {
			t.Errorf("got %q, want %q", got, "hello")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the published line")
	}
}

// TestMemoryTailerCancelClosesChannel: canceling a subscription closes its channel
// so a `for range` consumer terminates.
func TestMemoryTailerCancelClosesChannel(t *testing.T) {
	tl := NewMemoryTailer()
	lines, cancel := tl.Subscribe(context.Background(), memTailRef())
	cancel()
	select {
	case _, open := <-lines:
		if open {
			t.Error("channel should be closed after cancel")
		}
	case <-time.After(time.Second):
		t.Fatal("channel not closed after cancel")
	}
}

// TestMemoryTailerPublishWithoutSubscribersIsNoError: publishing to a task nobody
// is tailing must not error or block (the agent log path must never stall).
func TestMemoryTailerPublishWithoutSubscribersIsNoError(t *testing.T) {
	tl := NewMemoryTailer()
	if err := tl.Publish(context.Background(), memTailRef(), "x"); err != nil {
		t.Fatalf("Publish with no subscribers: %v", err)
	}
}

// TestMemoryTailerContextCancelEndsSubscription: a canceled context closes the
// subscription channel (mirrors the RedisTailer contract).
func TestMemoryTailerContextCancelEndsSubscription(t *testing.T) {
	tl := NewMemoryTailer()
	ctx, cancelCtx := context.WithCancel(context.Background())
	lines, cancel := tl.Subscribe(ctx, memTailRef())
	defer cancel()
	cancelCtx()
	select {
	case _, open := <-lines:
		if open {
			t.Error("channel should close when the context is canceled")
		}
	case <-time.After(time.Second):
		t.Fatal("context cancel did not close the subscription")
	}
}
