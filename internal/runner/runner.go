package runner

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// TxFunc is the unit of work executed once per benchmark iteration.
// It receives a context so callers can respect deadlines and cancellations.
type TxFunc func(ctx context.Context) error

// Config controls how a benchmark run behaves.
type Config struct {
	// Name is printed in progress output.
	Name string

	// Requests is the number of timed iterations.
	Requests int

	// WarmupRequests is the number of iterations run before timing begins.
	// Set to 0 to skip warmup entirely.
	WarmupRequests int

	// Workers controls concurrent goroutines in Goroutine mode.
	// Ignored in Sequential mode.
	Workers int

	// Timeout is applied per-request via context.WithTimeout.
	Timeout time.Duration
}

// Run executes fn sequentially for cfg.Requests iterations, preceded by an
// optional warmup phase.  It returns a Result with timing and failure data.
func Run(ctx context.Context, cfg Config, fn TxFunc) (Result, error) {
	if err := warmup(ctx, cfg, fn); err != nil {
		return Result{}, err
	}
	return measure(ctx, cfg, fn, false)
}

// RunConcurrent executes fn with cfg.Workers goroutines in parallel, preceded
// by an optional warmup phase.
func RunConcurrent(ctx context.Context, cfg Config, fn TxFunc) (Result, error) {
	if err := warmup(ctx, cfg, fn); err != nil {
		return Result{}, err
	}
	return measure(ctx, cfg, fn, true)
}

// warmup runs fn for cfg.WarmupRequests iterations without timing.
func warmup(ctx context.Context, cfg Config, fn TxFunc) error {
	if cfg.WarmupRequests <= 0 {
		return nil
	}

	fmt.Printf("Warmup: %s [0/%d]\r", cfg.Name, cfg.WarmupRequests)

	for i := range cfg.WarmupRequests {
		reqCtx, cancel := requestContext(ctx, cfg.Timeout)
		err := fn(reqCtx)
		cancel()

		if err != nil {
			fmt.Printf("Warning: warmup request %d failed: %v\n", i+1, err)
		}

		fmt.Printf("Warmup: %s [%d/%d]\r", cfg.Name, i+1, cfg.WarmupRequests)
	}

	fmt.Printf("Warmup: %s done             \n", cfg.Name)
	return nil
}

// measure runs the timed benchmark phase, sequential or concurrent.
func measure(ctx context.Context, cfg Config, fn TxFunc, concurrent bool) (Result, error) {
	var failures atomic.Int64

	if !concurrent {
		fmt.Printf("%s [0/%d]\r", cfg.Name, cfg.Requests)

		start := time.Now()

		for i := range cfg.Requests {
			reqCtx, cancel := requestContext(ctx, cfg.Timeout)
			err := fn(reqCtx)
			cancel()

			if err != nil {
				failures.Add(1)
				fmt.Printf("Warning: request %d failed: %v\n", i+1, err)
			}

			fmt.Printf("%s [%d/%d]\r", cfg.Name, i+1, cfg.Requests)
		}

		elapsed := time.Since(start)
		fmt.Printf("%s done             \n", cfg.Name)

		return Result{
			TotalSeconds: elapsed.Seconds(),
			RequestCount: cfg.Requests,
			FailureCount: int(failures.Load()),
		}, nil
	}

	// Concurrent path: fan out cfg.Requests jobs across cfg.Workers goroutines.
	workers := cfg.Workers
	if workers <= 0 {
		workers = cfg.Requests // unbounded — one goroutine per request
	}

	jobs := make(chan struct{}, cfg.Requests)
	for range cfg.Requests {
		jobs <- struct{}{}
	}
	close(jobs)

	var wg sync.WaitGroup
	var completed atomic.Int64

	fmt.Printf("%s [0/%d]\r", cfg.Name, cfg.Requests)

	start := time.Now()

	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range jobs {
				reqCtx, cancel := requestContext(ctx, cfg.Timeout)
				err := fn(reqCtx)
				cancel()
				if err != nil {
					failures.Add(1)
				}
				done := completed.Add(1)
				fmt.Printf("%s [%d/%d]\r", cfg.Name, done, cfg.Requests)
			}
		}()
	}

	wg.Wait()
	elapsed := time.Since(start)

	fmt.Printf("%s done             \n", cfg.Name)

	return Result{
		TotalSeconds: elapsed.Seconds(),
		RequestCount: cfg.Requests,
		FailureCount: int(failures.Load()),
	}, nil
}

// requestContext returns a child context and its cancel func when a per-request
// timeout is configured.  The caller must call cancel() after fn returns to
// release timer resources — even when the deadline fires first.
// When no timeout is set the parent context and a no-op cancel are returned.
func requestContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return parent, func() {}
	}
	return context.WithTimeout(parent, timeout)
}
