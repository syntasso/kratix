package controller

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestDynamicController_RuntimeOptionsRaceFree exercises ApplyRuntimeOptions
// and SnapshotRuntimeOptions concurrently, where ApplyRuntimeOptions models
// the PromiseReconciler's reuse path writing new options and
// SnapshotRuntimeOptions models a Reconcile worker reading the same fields.
//
// Run with `go test -race ./internal/controller/...` to catch regressions if
// the mutex is removed or a new unguarded field is added.
func TestDynamicController_RuntimeOptionsRaceFree(t *testing.T) {
	c := &DynamicResourceRequestController{}

	const (
		writers     = 4
		readers     = 8
		iterations  = 500
		warnedFlips = 50
	)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	var warnedTotal int64

	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				select {
				case <-stop:
					return
				default:
				}
				opts := PromiseRuntimeOptions{
					RateLimitQPS:            float32(seed*1000 + i),
					RateLimitBurst:          seed*1000 + i,
					MaxConcurrentReconciles: (seed + i) % 8,
				}
				restartRequired := i < warnedFlips
				if c.ApplyRuntimeOptions(opts, restartRequired) {
					atomic.AddInt64(&warnedTotal, 1)
				}
			}
		}(w)
	}

	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations*2; i++ {
				select {
				case <-stop:
					return
				default:
				}
				_, _ = c.SnapshotRuntimeOptions()
			}
		}()
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		close(stop)
		t.Fatal("workers did not finish in 10s — likely a deadlock")
	}

	// At most one writer should ever flip RestartRequiredWarned for a single
	// controller instance, regardless of how many writers raced.
	if got := atomic.LoadInt64(&warnedTotal); got != 1 {
		t.Fatalf("expected exactly one warned-flip across all writers, got %d", got)
	}
}
