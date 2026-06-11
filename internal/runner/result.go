package runner

import (
	"math"
	"sort"
	"time"
)

// Result holds the outcome of a single benchmark run.
// It mirrors the Python BenchmarkResult dataclass.
type Result struct {
	TotalSeconds float64
	RequestCount int
	FailureCount int
	Durations    []time.Duration // one entry per timed request
}

// IsUnreliable returns true when more than 10% of requests failed,
// matching the Python is_unreliable property.
func (r Result) IsUnreliable() bool {
	if r.RequestCount == 0 {
		return false
	}
	return float64(r.FailureCount)/float64(r.RequestCount) > 0.10
}

// RequestsPerSecond returns throughput, guarding against a zero duration.
func (r Result) RequestsPerSecond() float64 {
	if r.TotalSeconds <= 0 {
		return 0
	}
	return float64(r.RequestCount) / r.TotalSeconds
}

// Min returns the minimum request duration. Zero if no durations recorded.
func (r Result) Min() time.Duration {
	if len(r.Durations) == 0 {
		return 0
	}
	min := r.Durations[0]
	for _, d := range r.Durations[1:] {
		if d < min {
			min = d
		}
	}
	return min
}

// Max returns the maximum request duration. Zero if no durations recorded.
func (r Result) Max() time.Duration {
	if len(r.Durations) == 0 {
		return 0
	}
	max := r.Durations[0]
	for _, d := range r.Durations[1:] {
		if d > max {
			max = d
		}
	}
	return max
}

// Mean returns the arithmetic mean of recorded durations. Zero if none.
func (r Result) Mean() time.Duration {
	if len(r.Durations) == 0 {
		return 0
	}
	var sum time.Duration
	for _, d := range r.Durations {
		sum += d
	}
	return sum / time.Duration(len(r.Durations))
}

// Percentile returns the p-th percentile (0.0–1.0) of recorded durations.
// Uses nearest-rank method. Zero if no durations recorded.
func (r Result) Percentile(p float64) time.Duration {
	if len(r.Durations) == 0 {
		return 0
	}
	sorted := make([]time.Duration, len(r.Durations))
	copy(sorted, r.Durations)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := int(p * float64(len(sorted)-1))
	return sorted[idx]
}

// StdDev returns the population standard deviation of recorded durations.
// Zero if fewer than 2 durations recorded.
func (r Result) StdDev() time.Duration {
	if len(r.Durations) < 2 {
		return 0
	}
	mean := float64(r.Mean())
	var sumSq float64
	for _, d := range r.Durations {
		diff := float64(d) - mean
		sumSq += diff * diff
	}
	return time.Duration(math.Sqrt(sumSq / float64(len(r.Durations))))
}
