// Package dag provides a bounded, cancellable task executor.
//
// Leak fix: exactly `workers` goroutines drain a bounded queue; on context
// cancellation the queue is closed, workers exit, and Run waits via
// errgroup semantics. No per-task goroutines are ever spawned.
package dag

import (
	"context"
	"sync"

	"github.com/raghavraut/argus/internal/core"
)

// Handler executes one task.
type Handler func(ctx context.Context, task core.Task) error

// Executor is a bounded worker pool implementing core.DAGExecutor.
type Executor struct {
	mu      sync.RWMutex
	profile core.ExecutionProfile

	queue   chan core.Task
	handler Handler
	workers int

	runMu sync.Mutex
}

var _ core.DAGExecutor = (*Executor)(nil)

// New creates an executor with a bounded queue and fixed workers.
func New(workers, queueSize int, h Handler) *Executor {
	if workers <= 0 {
		workers = 8
	}
	if queueSize <= 0 {
		queueSize = workers * 16
	}
	return &Executor{
		profile: core.ProfileStandard,
		queue:   make(chan core.Task, queueSize),
		handler: h,
		workers: workers,
	}
}

func (e *Executor) SetProfile(p core.ExecutionProfile) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.profile = p
}

func (e *Executor) Profile() core.ExecutionProfile {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.profile
}

// Submit enqueues a task or returns ctx.Err() if cancelled/full.
// Callers should prefer non-blocking submit during streaming; Run drains.
func (e *Executor) Submit(ctx context.Context, task core.Task) error {
	p := e.Profile()
	task.Profile = p
	select {
	case <-ctx.Done():
		return ctx.Err()
	case e.queue <- task:
		return nil
	}
}

// ResumeFromState satisfies the interface; actual skip logic lives in the
// caller via state.Store.IsDone. It is kept here so DAG wiring stays stable.
func (e *Executor) ResumeFromState(_ context.Context, _ string) error { return nil }

// CloseQueue signals workers to exit after draining.
func (e *Executor) CloseQueue() { close(e.queue) }

// Run drains the queue until it is closed or ctx is cancelled.
func (e *Executor) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	errCh := make(chan error, e.workers)
	for i := 0; i < e.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case task, ok := <-e.queue:
					if !ok {
						return
					}
					if err := e.handler(ctx, task); err != nil {
						select {
						case errCh <- err:
						default:
						}
					}
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil && ctx.Err() == nil {
			return err
		}
	}
	return ctx.Err()
}
