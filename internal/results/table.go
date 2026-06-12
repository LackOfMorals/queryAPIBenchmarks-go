// Package results renders benchmark output to stdout.
// It mirrors the Python generate_table function in showResults.py.
package results

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/LackOfMorals/queryAPIBenchmarks-go/internal/runner"
)

// Entry pairs a test name with its result.
type Entry struct {
	Name       string
	QueryLabel string // non-empty when running a named query file (sub-benchmark)
	Result     runner.Result
}

// fixed column widths (chars) that don't resize with the terminal.
const (
	colTime = 12
	colRPS  = 10
	colFail = 14
	colSeps = 6 // 3 × "  " between the four summary columns
	colLat  = 10 // width of each latency stat column
	colTestMin = 20
)

// terminalWidth returns the current terminal width, checking $COLUMNS first,
// then the stdout fd, and falling back to 80 when neither is available
// (e.g. when stdout is redirected).
func terminalWidth() int {
	if s := os.Getenv("COLUMNS"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			return n
		}
	}
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		return w
	}
	return 80
}

// testColWidth derives the test-name column width from the terminal width,
// leaving fixed columns and separators their standard sizes.
func testColWidth() int {
	w := terminalWidth() - colTime - colRPS - colFail - colSeps
	if w < colTestMin {
		return colTestMin
	}
	return w
}

// truncate shortens s to fit within n chars, appending "..." when trimmed.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 3 {
		return s[:n]
	}
	return s[:n-3] + "..."
}

// PrintTable writes an ASCII table of benchmark results to stdout, marking
// unreliable runs (>10% failure rate) with an asterisk.
// The test-name column expands or contracts to fill the available terminal
// width. Each entry occupies two lines: summary stats on the first, latency
// percentiles on the second (when duration data is available).
func PrintTable(entries []Entry, apiLabel string) {
	colTest := testColWidth()

	header := fmt.Sprintf("%-*s  %-*s  %-*s  %-*s",
		colTest, "Test",
		colTime, "Time (s)",
		colRPS, "Req/s",
		colFail, "Failures",
	)
	latHeader := fmt.Sprintf("  %-*s %-*s %-*s %-*s %-*s %-*s",
		colLat, "min",
		colLat, "p50",
		colLat, "p95",
		colLat, "p99",
		colLat, "max",
		colLat, "stddev",
	)

	width := max(len(header), len(latHeader))
	divider := strings.Repeat("-", width)

	fmt.Println()
	fmt.Printf("API: %s\n", apiLabel)
	fmt.Println(divider)
	fmt.Println(header)
	fmt.Println(latHeader)
	fmt.Println(divider)

	hasUnreliable := false

	for _, e := range entries {
		name := e.Name
		if e.QueryLabel != "" {
			name = e.QueryLabel + "/" + name
		}
		if e.Result.IsUnreliable() {
			name += " *"
			hasUnreliable = true
		}
		name = truncate(name, colTest)

		fmt.Printf("%-*s  %-*s  %-*s  %-*s\n",
			colTest, name,
			colTime, fmt.Sprintf("%.3f", e.Result.TotalSeconds),
			colRPS, fmt.Sprintf("%.0f", e.Result.RequestsPerSecond()),
			colFail, fmt.Sprintf("%d / %d", e.Result.FailureCount, e.Result.RequestCount),
		)

		if len(e.Result.Durations) > 0 {
			fmt.Printf("  %-*s %-*s %-*s %-*s %-*s %-*s\n",
				colLat, fmtDur(e.Result.Min()),
				colLat, fmtDur(e.Result.Percentile(0.50)),
				colLat, fmtDur(e.Result.Percentile(0.95)),
				colLat, fmtDur(e.Result.Percentile(0.99)),
				colLat, fmtDur(e.Result.Max()),
				colLat, fmtDur(e.Result.StdDev()),
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
	Name              string     `json:"name"`
	TotalSeconds      float64    `json:"total_seconds"`
	RequestCount      int        `json:"request_count"`
	FailureCount      int        `json:"failure_count"`
	RequestsPerSecond float64    `json:"requests_per_second"`
	Unreliable        bool       `json:"unreliable"`
	LatencyMS         *latencyMS `json:"latency_ms,omitempty"`
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

// PrintJSON writes benchmark results as a JSON object to stdout.
// apiLabel is included as the "api_flavor" key so runs are self-describing.
func PrintJSON(entries []Entry, apiLabel string) {
	out := make([]jsonEntry, 0, len(entries))
	for _, e := range entries {
		name := e.Name
		if e.QueryLabel != "" {
			name = e.QueryLabel + "/" + name
		}
		je := jsonEntry{
			Name:              name,
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

	envelope := struct {
		APIFlavor string      `json:"api_flavor"`
		Results   []jsonEntry `json:"results"`
	}{APIFlavor: apiLabel, Results: out}

	b, err := json.MarshalIndent(envelope, "", "  ")
	if err != nil {
		fmt.Printf(`{"error": %q}`, err.Error())
		return
	}
	fmt.Println(string(b))
}

// PrintBenchstat emits one line per entry in the standard Go benchmark format
// consumed by golang.org/x/perf/cmd/benchstat:
//
//	BenchmarkName/api-N   iterations   ns/op   [p50-ns/op   p99-ns/op]
//
// apiSlug is the raw -api flag value ("queryv2" or "legacy"). N is GOMAXPROCS.
func PrintBenchstat(entries []Entry) {
	procs := runtime.GOMAXPROCS(0)
	for _, e := range entries {
		if e.Result.RequestCount == 0 {
			continue
		}
		nsPerOp := int64(e.Result.TotalSeconds * 1e9 / float64(e.Result.RequestCount))
		benchName := e.Name
		if e.QueryLabel != "" {
			benchName = e.Name + "/" + e.QueryLabel
		}
		line := fmt.Sprintf("Benchmark%s-%d\t%d\t%d ns/op",
			benchName, procs,
			e.Result.RequestCount,
			nsPerOp,
		)
		if len(e.Result.Durations) > 0 {
			line += fmt.Sprintf("\t%d p50-ns/op\t%d p99-ns/op",
				e.Result.Percentile(0.50).Nanoseconds(),
				e.Result.Percentile(0.99).Nanoseconds(),
			)
		}
		fmt.Println(line)
	}
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
