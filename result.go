package runner

// Result holds the outcome of a single benchmark run.
// It mirrors the Python BenchmarkResult dataclass.
type Result struct {
	TotalSeconds float64
	RequestCount int
	FailureCount int
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
