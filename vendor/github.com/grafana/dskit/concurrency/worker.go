package concurrency

import (
	"sync"
)

// NewReusableGoroutinesPool creates a new worker pool with the given size.
// These workers will run the workloads passed through Go() calls.
// If all workers are busy, Go() will spawn a new goroutine to run the workload.
func NewReusableGoroutinesPool(size int) *ReusableGoroutinesPool {
	p := &ReusableGoroutinesPool{
		jobs: make(chan func()),
	}
	for i := 0; i < size; i++ {
		go func() {
			for f := range p.jobs {
				f()
			}
		}()
	}
	return p
}

type ReusableGoroutinesPool struct {
	jobsMu sync.RWMutex
	closed bool
	jobs   chan func()
}

// Go will run the given function in a worker of the pool.
// If all workers are busy, Go() will spawn a new goroutine to run the workload.
func (p *ReusableGoroutinesPool) Go(f func()) {
	p.jobsMu.RLock()
	defer p.jobsMu.RUnlock()

	// If the pool is closed, run the function in a new goroutine.
	if p.closed {
		go f()
		return
	}

	select {
	case p.jobs <- f:
	default:
		go f()
	}
}

// Close stops the workers of the pool.
// No new Go() calls should be performed after calling Close().
// Close does NOT wait for all jobs to finish, it is the caller's responsibility to ensure that in the provided workloads.
// Close is intended to be used in tests to ensure that no goroutines are leaked.
func (p *ReusableGoroutinesPool) Close() {
	p.jobsMu.Lock()
	defer p.jobsMu.Unlock()
	p.closed = true
	close(p.jobs)
}
