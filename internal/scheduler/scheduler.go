// Package scheduler owns every background goroutine in the daemon. Nothing
// outside this package should use the go statement for long-lived work: that
// rule is what makes "no unbounded goroutines" auditable.
package scheduler

import (
	"context"
	"errors"
	"log/slog"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"
)

// Group runs named goroutines bound to one context and waits for all of them.
// Panics inside a task are recovered, logged and reported as errors so a
// malformed input in one worker cannot take the daemon down.
type Group struct {
	ctx    context.Context
	cancel context.CancelFunc
	log    *slog.Logger
	wg     sync.WaitGroup
	mu     sync.Mutex
	errs   []error
	active atomic.Int64
}

// NewGroup creates a group whose tasks are cancelled when ctx ends or Stop is called.
func NewGroup(ctx context.Context, log *slog.Logger) *Group {
	ctx, cancel := context.WithCancel(ctx)
	return &Group{ctx: ctx, cancel: cancel, log: log}
}

// Context returns the group's context.
func (g *Group) Context() context.Context { return g.ctx }

// Go starts fn as a supervised goroutine.
func (g *Group) Go(name string, fn func(ctx context.Context) error) {
	g.wg.Add(1)
	g.active.Add(1)
	go func() {
		defer g.wg.Done()
		defer g.active.Add(-1)
		defer func() {
			if r := recover(); r != nil {
				g.log.Error("task panicked", "task", name, "panic", r, "stack", string(debug.Stack()))
				g.record(errors.New("task " + name + " panicked"))
			}
		}()
		if err := fn(g.ctx); err != nil && !errors.Is(err, context.Canceled) {
			g.log.Error("task failed", "task", name, "err", err)
			g.record(err)
		}
	}()
}

// Every runs fn on a fixed interval until the context ends. Each run is
// recovered individually so a single failure does not stop the ticker.
func (g *Group) Every(name string, interval time.Duration, fn func(ctx context.Context) error) {
	g.Go(name, func(ctx context.Context) error {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-t.C:
				g.safeRun(name, ctx, fn)
			}
		}
	})
}

func (g *Group) safeRun(name string, ctx context.Context, fn func(context.Context) error) {
	defer func() {
		if r := recover(); r != nil {
			g.log.Error("periodic task panicked", "task", name, "panic", r, "stack", string(debug.Stack()))
		}
	}()
	if err := fn(ctx); err != nil && !errors.Is(err, context.Canceled) {
		g.log.Warn("periodic task failed", "task", name, "err", err)
	}
}

func (g *Group) record(err error) {
	g.mu.Lock()
	g.errs = append(g.errs, err)
	g.mu.Unlock()
}

// Active returns the number of running tasks.
func (g *Group) Active() int64 { return g.active.Load() }

// Stop cancels the group and waits for all tasks, returning any errors.
func (g *Group) Stop() error {
	g.cancel()
	g.wg.Wait()
	g.mu.Lock()
	defer g.mu.Unlock()
	return errors.Join(g.errs...)
}

// Wait blocks until all tasks have returned without cancelling them.
func (g *Group) Wait() error {
	g.wg.Wait()
	g.mu.Lock()
	defer g.mu.Unlock()
	return errors.Join(g.errs...)
}

// Pool bounds concurrent execution of short tasks. Submit blocks when the pool
// is full (backpressure) rather than queueing without limit.
type Pool struct {
	sem  chan struct{}
	busy atomic.Int64
}

// NewPool creates a pool allowing size concurrent tasks.
func NewPool(size int) *Pool {
	if size < 1 {
		size = 1
	}
	return &Pool{sem: make(chan struct{}, size)}
}

// Run executes fn once a slot is free, or returns ctx.Err() while waiting.
// It runs fn on the caller's goroutine; combine with Group.Go for async work.
func (p *Pool) Run(ctx context.Context, fn func(ctx context.Context) error) error {
	select {
	case p.sem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-p.sem }()
	p.busy.Add(1)
	defer p.busy.Add(-1)
	return fn(ctx)
}

// Busy returns the number of occupied slots.
func (p *Pool) Busy() int64 { return p.busy.Load() }

// Size returns the pool capacity.
func (p *Pool) Size() int { return cap(p.sem) }

// Parallel runs each task through the pool concurrently and waits for all of
// them, collecting errors. It never starts more than the pool size at once.
func Parallel(ctx context.Context, p *Pool, tasks []func(ctx context.Context) error) error {
	var wg sync.WaitGroup
	errs := make([]error, len(tasks))
	for i, task := range tasks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					errs[i] = errors.New("parallel task panicked")
				}
			}()
			errs[i] = p.Run(ctx, task)
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}
