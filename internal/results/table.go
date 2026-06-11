// Package results renders benchmark output to stdout.
// It mirrors the Python generate_table function in showResults.py.
package results

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/LackOfMorals/queryAPIBenchmarks-go/internal/runner"
)

// Entry pairs a test name with its result.
type Entry struct {
	Name   string
	Result runner.Result
}

// PrintTable writes an ASCII table of benchmark results to stdout, marking
// unreliable runs (>10% failure rate) with an asterisk.
// Each entry occupies two lines: summary stats on the first, latency
// percentiles on the second (when duration data is available).
func PrintTable(entries []Entry) {
	const (
		colTest = 32
		colTime = 12
		colRPS  = 10
		colFail = 14
	)

	header := fmt.Sprintf("%-*s  %-*s  %-*s  %-*s",
		colTest, "Test",
		colTime, "Time (s)",
		colRPS, "Req/s",
		colFail, "Failures",
	)
	latHeader := fmt.Sprintf("  %-10s %-10s %-10s %-10s %-10s %-10s",
		"min", "p50", "p95", "p99", "max", "stddev",
	)

	divider := strings.Repeat("-", max(len(header), len(latHeader)))

	fmt.Println()
	fmt.Println(divider)
	fmt.Println(header)
	fmt.Println(latHeader)
	fmt.Println(divider)

	hasUnreliable := false

	for _, e := range entries {
		name := e.Name
		if e.Result.IsUnreliable() {
			name += " *"
			hasUnreliable = true
		}

		fmt.Printf("%-*s  %-*s  %-*s  %-*s\n",
			colTest, name,
			colTime, fmt.Sprintf("%.3f", e.Result.TotalSeconds),
			colRPS, fmt.Sprintf("%.0f", e.Result.RequestsPerSecond()),
			colFail, fmt.Sprintf("%d / %d", e.Result.FailureCount, e.Result.RequestCount),
		)

		if len(e.Result.Durations) > 0 {
			fmt.Printf("  %-10s %-10s %-10s %-10s %-10s %-10s\n",
				fmtDur(e.Result.Min()),
				fmtDur(e.Result.Percentile(0.50)),
				fmtDur(e.Result.Percentile(0.95)),
				fmtDur(e.Result.Percentile(0.99)),
				fmtDur(e.Result.Max()),
				fmtDur(e.Result.StdDev()),
			)
		}
	}

	fmt.Println(divider)

	if hasUnreliable {
		fmt.Println("\n* Result unreliable: >10% of requests failed")
	}

	fmt.Println()
}

// jsonEntry is the shape written by PrintJSON.
type jsonEntry struct {
	Name               string      `json:"name"`
	TotalSeconds       float64     `json:"total_seconds"`
	RequestCount       int         `json:"request_count"`
	FailureCount       int         `json:"failure_count"`
	RequestsPerSecond  float64     `json:"requests_per_second"`
	Unreliable         bool        `json:"unreliable"`
	LatencyMS          *latencyMS  `json:"latency_ms,omitempty"`
}

type latencyMS struct {
	Min    float64 `json:"min"`
	Mean   float64 `json:"mean"`
	P50    float64 `json:"p50"`
	P95    float64 `json:"p95"`
	P99    float64 `json:"p99"`
	Max    float64 `json:"max"`
	StdDev float64 `json:"stddev"`
}

// PrintJSON writes benchmark results as a JSON array to stdout.
func PrintJSON(entries []Entry) {
	out := make([]jsonEntry, 0, len(entries))
	for _, e := range entries {
		je := jsonEntry{
			Name:              e.Name,
			TotalSeconds:      e.Result.TotalSeconds,
			RequestCount:      e.Result.RequestCount,
			FailureCount:      e.Result.FailureCount,
			RequestsPerSecond: e.Result.RequestsPerSecond(),
			Unreliable:        e.Result.IsUnreliable(),
		}
		if len(e.Result.Durations) > 0 {
			je.LatencyMS = &latencyMS{
				Min:    msf(e.Result.Min()),
				Mean:   msf(e.Result.Mean()),
				P50:    msf(e.Result.Percentile(0.50)),
				P95:    msf(e.Result.Percentile(0.95)),
				P99:    msf(e.Result.Percentile(0.99)),
				Max:    msf(e.Result.Max()),
				StdDev: msf(e.Result.StdDev()),
			}
		}
		out = append(out, je)
	}

	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		fmt.Printf(`{"error": %q}`, err.Error())
		return
	}
	fmt.Println(string(b))
}

// fmtDur formats a duration as a human-readable string (ms or µs).
func fmtDur(d time.Duration) string {
	if d >= time.Millisecond {
		return fmt.Sprintf("%.2fms", float64(d)/float64(time.Millisecond))
	}
	return fmt.Sprintf("%.1fµs", float64(d)/float64(time.Microsecond))
}

// msf converts a duration to milliseconds as a float64.
func msf(d time.Duration) float64 {
	return float64(d) / float64(time.Millisecond)
}

