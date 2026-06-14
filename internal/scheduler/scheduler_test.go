package scheduler

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

func TestPoolBoundsConcurrency(t *testing.T) {
	p := NewPool(3)
	var cur, peak atomic.Int64
	tasks := make([]func(context.Context) error, 20)
	for i := range tasks {
		tasks[i] = func(ctx context.Context) error {
			n := cur.Add(1)
			for {
				old := peak.Load()
				if n <= old || peak.CompareAndSwap(old, n) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			cur.Add(-1)
			return nil
		}
	}
	if err := Parallel(context.Background(), p, tasks); err != nil {
		t.Fatal(err)
	}
	if peak.Load() > 3 {
		t.Fatalf("peak concurrency %d exceeded pool size", peak.Load())
	}
}

func TestGroupRecoversPanicAndCancels(t *testing.T) {
	g := NewGroup(context.Background(), slog.New(slog.DiscardHandler))
	g.Go("boom", func(ctx context.Context) error { panic("x") })
	blocked := make(chan struct{})
	g.Go("waiter", func(ctx context.Context) error { close(blocked); <-ctx.Done(); return ctx.Err() })
	<-blocked
	err := g.Stop()
	if err == nil {
		t.Fatal("expected panic error")
	}
	if g.Active() != 0 {
		t.Fatal("tasks still active")
	}
}

func TestPoolRunHonoursCancellation(t *testing.T) {
	p := NewPool(1)
	ctx, cancel := context.WithCancel(context.Background())
	release := make(chan struct{})
	go p.Run(context.Background(), func(context.Context) error { <-release; return nil })
	time.Sleep(time.Millisecond)
	cancel()
	if err := p.Run(ctx, func(context.Context) error { return nil }); err != context.Canceled {
		t.Fatalf("expected cancellation, got %v", err)
	}
	close(release)
}
