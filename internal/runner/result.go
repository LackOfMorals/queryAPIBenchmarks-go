package runner

import (
	"math"
	"sort"
	"time"
)

// Result holds the outcome of a single benchmark run.
//
// Successful and failed requests are kept separate throughout. Mixing them —
// as the original did — means a request that sat until a 30s timeout lands in
// the latency distribution, and failures are credited as throughput.
type Result struct {
	// TotalSeconds is wall time for the timed phase.
	TotalSeconds float64

	// RequestCount is requests attempted (successes + failures).
	RequestCount int

	// SuccessCount is requests that returned without error.
	SuccessCount int

	// FailureCount is requests that returned an error.
	FailureCount int

	// Durations holds one entry per *successful* request.
	Durations []time.Duration

	// FailureDurations holds one entry per failed request, kept for diagnosis
	// (a cluster of ~30s entries means requests are being timed out, not served).
	FailureDurations []time.Duration

	// Errors counts failures by class — "http-503", "timeout", "conn-refused".
	Errors map[string]int

	// ErrorSamples holds a few verbatim error strings for context.
	ErrorSamples []string
}

// Stats is a full summary of a duration set, computed from a single sort.
//
// The original recomputed Min, Max, Mean, StdDev and each percentile
// independently, sorting the whole slice once per percentile call.
type Stats struct {
	Count  int
	Min    time.Duration
	Max    time.Duration
	Mean   time.Duration
	StdDev time.Duration
	P50    time.Duration
	P90    time.Duration
	P95    time.Duration
	P99    time.Duration
	P999   time.Duration
}

// IsUnreliable reports whether the run had any failures at all.
//
// The previous 10% threshold was far too permissive for a benchmark: a run
// where one request in twelve failed was reported without qualification.
func (r Result) IsUnreliable() bool {
	return r.FailureCount > 0
}

// FailureRatio returns the fraction of attempted requests that failed.
func (r Result) FailureRatio() float64 {
	if r.RequestCount == 0 {
		return 0
	}
	return float64(r.FailureCount) / float64(r.RequestCount)
}

// RequestsPerSecond returns successful throughput.
//
// Failures are excluded deliberately. Counting them meant a run where HAProxy
// 504'd a third of the load still reported healthy RPS.
func (r Result) RequestsPerSecond() float64 {
	if r.TotalSeconds <= 0 {
		return 0
	}
	return float64(r.SuccessCount) / r.TotalSeconds
}

// AttemptedPerSecond returns offered load including failures, which is the
// number to compare against a target rate.
func (r Result) AttemptedPerSecond() float64 {
	if r.TotalSeconds <= 0 {
		return 0
	}
	return float64(r.RequestCount) / r.TotalSeconds
}

// Stats summarises latency across successful requests only.
func (r Result) Stats() Stats {
	return computeStats(r.Durations)
}

// FailureStats summarises how long failed requests took before failing.
func (r Result) FailureStats() Stats {
	return computeStats(r.FailureDurations)
}

// computeStats sorts a copy of d once and derives every statistic from it.
func computeStats(d []time.Duration) Stats {
	if len(d) == 0 {
		return Stats{}
	}

	sorted := make([]time.Duration, len(d))
	copy(sorted, d)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	var sum time.Duration
	for _, v := range sorted {
		sum += v
	}
	mean := sum / time.Duration(len(sorted))

	var stddev time.Duration
	if len(sorted) > 1 {
		meanF := float64(mean)
		var sumSq float64
		for _, v := range sorted {
			diff := float64(v) - meanF
			sumSq += diff * diff
		}
		stddev = time.Duration(math.Sqrt(sumSq / float64(len(sorted))))
	}

	return Stats{
		Count:  len(sorted),
		Min:    sorted[0],
		Max:    sorted[len(sorted)-1],
		Mean:   mean,
		StdDev: stddev,
		P50:    percentile(sorted, 0.50),
		P90:    percentile(sorted, 0.90),
		P95:    percentile(sorted, 0.95),
		P99:    percentile(sorted, 0.99),
		P999:   percentile(sorted, 0.999),
	}
}

// percentile returns the linearly interpolated p-th percentile (0.0–1.0) of an
// already-sorted slice.
//
// The original truncated the index with int(p * float64(len-1)), which biases
// percentiles low — badly so on small samples. With n=5, p99 resolved to the
// 4th of 5 observations, i.e. roughly a p80 labelled p99.
func percentile(sorted []time.Duration, p float64) time.Duration {
	n := len(sorted)
	switch {
	case n == 0:
		return 0
	case n == 1 || p <= 0:
		return sorted[0]
	case p >= 1:
		return sorted[n-1]
	}

	pos := p * float64(n-1)
	lo := int(math.Floor(pos))
	if lo >= n-1 {
		return sorted[n-1]
	}

	frac := pos - float64(lo)
	gap := float64(sorted[lo+1] - sorted[lo])
	return sorted[lo] + time.Duration(frac*gap)
}
