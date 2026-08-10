package runner

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// TxFunc is the unit of work executed once per benchmark iteration.
// It receives a context so callers can respect deadlines and cancellations.
type TxFunc func(ctx context.Context) error

// warmupFailureRatio is the fraction of failed warmup requests above which the
// run is abandoned rather than measured. A benchmark against an unreachable or
// unhealthy cluster should fail loudly, not report timings for nothing.
const warmupFailureRatio = 0.10

// warmupFailureFloor is the minimum number of warmup failures needed to abort,
// regardless of ratio.
//
// Without it the default -warmup 5 makes a single transient blip fatal
// (1 > 0.10*5), discarding the whole run and exiting non-zero. The ratio should
// only take over once the sample is big enough for it to mean something.
const warmupFailureFloor = 2

// jobBufferMax bounds the dispatch channel. The original pre-filled a channel
// with one token per request before starting, which is fine for n=100 and
// wasteful for n=10,000,000.
const jobBufferMax = 1024

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
	return measure(ctx, cfg, fn, 1)
}

// RunConcurrent executes fn with cfg.Workers goroutines in parallel, preceded
// by an optional warmup phase.
func RunConcurrent(ctx context.Context, cfg Config, fn TxFunc) (Result, error) {
	if err := warmup(ctx, cfg, fn); err != nil {
		return Result{}, err
	}

	workers := cfg.Workers
	if workers <= 0 {
		workers = cfg.Requests // unbounded — one goroutine per request
	}
	if workers > cfg.Requests {
		workers = cfg.Requests
	}
	return measure(ctx, cfg, fn, workers)
}

// warmup runs fn for cfg.WarmupRequests iterations without timing.
//
// It aborts the benchmark when more than warmupFailureRatio of warmup requests
// fail. Previously warmup only printed a warning, so a completely unreachable
// target still produced a results table.
func warmup(ctx context.Context, cfg Config, fn TxFunc) error {
	if cfg.WarmupRequests <= 0 {
		return nil
	}

	var done atomic.Int64
	p := startProgress(os.Stderr, "Warmup "+cfg.Name, cfg.WarmupRequests, &done)

	var failures int
	var lastErr error

	for range cfg.WarmupRequests {
		reqCtx, cancel := requestContext(ctx, cfg.Timeout)
		err := fn(reqCtx)
		cancel()

		if err != nil {
			failures++
			lastErr = err
		}
		done.Add(1)
	}
	p.Stop()

	if failures >= warmupFailureFloor && float64(failures) > warmupFailureRatio*float64(cfg.WarmupRequests) {
		return fmt.Errorf("warmup failed: %d/%d requests errored, last error: %w",
			failures, cfg.WarmupRequests, lastErr)
	}
	if failures > 0 {
		fmt.Fprintf(os.Stderr, "Warning: %d/%d warmup requests failed (last: %v)\n",
			failures, cfg.WarmupRequests, lastErr)
	}
	return nil
}

// shard is one worker's private accumulator. Per-worker shards merged at the
// end avoid any shared mutable state on the request path, so adding workers
// doesn't introduce lock contention that would show up as server latency.
type shard struct {
	success []time.Duration
	failure []time.Duration
	errors  map[string]int
	samples []string
}

func (s *shard) record(d time.Duration, err error) {
	if err == nil {
		s.success = append(s.success, d)
		return
	}

	// Failed requests are kept apart from successful ones. A request that hit a
	// 30s timeout is not a latency observation, and counting it as throughput
	// makes a broken run look healthy.
	s.failure = append(s.failure, d)
	s.errors[classify(err)]++
	if len(s.samples) < maxErrorSamples {
		s.samples = append(s.samples, err.Error())
	}
}

// measure runs the timed phase with the given number of workers.
// workers == 1 takes a channel-free path.
func measure(ctx context.Context, cfg Config, fn TxFunc, workers int) (Result, error) {
	if workers < 1 {
		workers = 1
	}

	shards := make([]shard, workers)
	perShard := cfg.Requests/workers + 1
	for i := range shards {
		shards[i].success = make([]time.Duration, 0, perShard)
		shards[i].errors = make(map[string]int)
	}

	var done atomic.Int64
	p := startProgress(os.Stderr, cfg.Name, cfg.Requests, &done)

	// One iteration: time fn, record, bump the progress counter.
	run := func(s *shard) {
		reqCtx, cancel := requestContext(ctx, cfg.Timeout)
		reqStart := time.Now()
		err := fn(reqCtx)
		elapsed := time.Since(reqStart)
		cancel()

		s.record(elapsed, err)
		done.Add(1)
	}

	var elapsed time.Duration

	if workers == 1 {
		s := &shards[0]
		start := time.Now()
		for range cfg.Requests {
			run(s)
		}
		elapsed = time.Since(start)
	} else {
		jobs := make(chan struct{}, min(cfg.Requests, jobBufferMax))

		var wg sync.WaitGroup
		for w := range workers {
			wg.Add(1)
			go func(s *shard) {
				defer wg.Done()
				for range jobs {
					run(s)
				}
			}(&shards[w])
		}

		start := time.Now()
		for range cfg.Requests {
			jobs <- struct{}{}
		}
		close(jobs)

		wg.Wait()
		elapsed = time.Since(start)
	}

	p.Stop()

	return mergeShards(shards, cfg.Requests, elapsed), nil
}

// mergeShards flattens per-worker accumulators into a single Result.
func mergeShards(shards []shard, attempted int, elapsed time.Duration) Result {
	var nSuccess, nFailure int
	for i := range shards {
		nSuccess += len(shards[i].success)
		nFailure += len(shards[i].failure)
	}

	res := Result{
		TotalSeconds: elapsed.Seconds(),
		RequestCount: attempted,
		SuccessCount: nSuccess,
		FailureCount: nFailure,
		Durations:    make([]time.Duration, 0, nSuccess),
		Errors:       make(map[string]int),
	}
	if nFailure > 0 {
		res.FailureDurations = make([]time.Duration, 0, nFailure)
	}

	for i := range shards {
		res.Durations = append(res.Durations, shards[i].success...)
		res.FailureDurations = append(res.FailureDurations, shards[i].failure...)
		for class, n := range shards[i].errors {
			res.Errors[class] += n
		}
	}

	res.ErrorSamples = collectSamples(shards)
	return res
}

// mergedSampleCap bounds how many verbatim error strings reach the report.
const mergedSampleCap = 8

// collectSamples gathers error samples round-robin across shards, skipping
// duplicates.
//
// Taking the first N from a flat concatenation would only ever surface the
// lowest-indexed worker's errors — so with eight workers hitting three distinct
// failure modes, you would see just one of them.
func collectSamples(shards []shard) []string {
	var out []string
	seen := make(map[string]bool)

	for round := range maxErrorSamples {
		for i := range shards {
			if len(out) >= mergedSampleCap {
				return out
			}
			if round >= len(shards[i].samples) {
				continue
			}
			s := shards[i].samples[round]
			if seen[s] {
				continue
			}
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
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
