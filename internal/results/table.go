// Package results renders benchmark output to stdout.
// It mirrors the Python generate_table function in showResults.py.
package results

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"sort"
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

// truncate shortens s to fit within n characters, appending "..." when trimmed.
//
// Delegates to a rune-aware helper: error messages passed through here can
// contain multi-byte characters, and a byte-boundary cut would emit invalid
// UTF-8.
func truncate(s string, n int) string {
	return runner.TruncateRunes(s, n)
}

// PrintTable writes an ASCII table of benchmark results to stdout, marking any
// run that had failures with an asterisk.
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

		if s := e.Result.Stats(); s.Count > 0 {
			fmt.Printf("  %-*s %-*s %-*s %-*s %-*s %-*s\n",
				colLat, fmtDur(s.Min),
				colLat, fmtDur(s.P50),
				colLat, fmtDur(s.P95),
				colLat, fmtDur(s.P99),
				colLat, fmtDur(s.Max),
				colLat, fmtDur(s.StdDev),
			)
		}

		// A failure breakdown is the difference between "412 requests failed"
		// and "the load balancer had no healthy backend".
		if e.Result.FailureCount > 0 {
			fmt.Printf("  failures: %s\n", formatErrors(e.Result.Errors))
			if fs := e.Result.FailureStats(); fs.Count > 0 {
				fmt.Printf("  time-to-fail: p50 %s  max %s\n", fmtDur(fs.P50), fmtDur(fs.Max))
			}
			for _, sample := range e.Result.ErrorSamples {
				fmt.Printf("  e.g. %s\n", truncate(sample, 120))
			}
		}
	}

	fmt.Println(divider)

	if hasUnreliable {
		fmt.Println("\n* Run had failed requests — latency and Req/s cover successes only.")
	}

	fmt.Println()
}

// formatErrors renders the error class counts most-frequent first, e.g.
// "http-503=412, timeout=17, conn-reset=2".
func formatErrors(errs map[string]int) string {
	if len(errs) == 0 {
		return "none"
	}

	type kv struct {
		class string
		n     int
	}
	pairs := make([]kv, 0, len(errs))
	for class, n := range errs {
		pairs = append(pairs, kv{class, n})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].n != pairs[j].n {
			return pairs[i].n > pairs[j].n
		}
		return pairs[i].class < pairs[j].class
	})

	parts := make([]string, 0, len(pairs))
	for _, p := range pairs {
		parts = append(parts, fmt.Sprintf("%s=%d", p.class, p.n))
	}
	return strings.Join(parts, ", ")
}

// jsonEntry is the shape written by PrintJSON.
type jsonEntry struct {
	Name              string         `json:"name"`
	TotalSeconds      float64        `json:"total_seconds"`
	RequestCount      int            `json:"request_count"`
	SuccessCount      int            `json:"success_count"`
	FailureCount      int            `json:"failure_count"`
	RequestsPerSecond float64        `json:"requests_per_second"`
	AttemptedPerSec   float64        `json:"attempted_per_second"`
	Unreliable        bool           `json:"unreliable"`
	LatencyMS         *latencyMS     `json:"latency_ms,omitempty"`
	Errors            map[string]int `json:"errors,omitempty"`
	ErrorSamples      []string       `json:"error_samples,omitempty"`
}

type latencyMS struct {
	Min    float64 `json:"min"`
	Mean   float64 `json:"mean"`
	P50    float64 `json:"p50"`
	P90    float64 `json:"p90"`
	P95    float64 `json:"p95"`
	P99    float64 `json:"p99"`
	P999   float64 `json:"p99_9"`
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
			SuccessCount:      e.Result.SuccessCount,
			FailureCount:      e.Result.FailureCount,
			RequestsPerSecond: e.Result.RequestsPerSecond(),
			AttemptedPerSec:   e.Result.AttemptedPerSecond(),
			Unreliable:        e.Result.IsUnreliable(),
			ErrorSamples:      e.Result.ErrorSamples,
		}
		if len(e.Result.Errors) > 0 {
			je.Errors = e.Result.Errors
		}
		if s := e.Result.Stats(); s.Count > 0 {
			je.LatencyMS = &latencyMS{
				Min:    msf(s.Min),
				Mean:   msf(s.Mean),
				P50:    msf(s.P50),
				P90:    msf(s.P90),
				P95:    msf(s.P95),
				P99:    msf(s.P99),
				P999:   msf(s.P999),
				Max:    msf(s.Max),
				StdDev: msf(s.StdDev),
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
// N is GOMAXPROCS. Iterations and ns/op count successful requests only; any
// failures are appended as a trailing comment so they can't pass unnoticed.
func PrintBenchstat(entries []Entry) {
	procs := runtime.GOMAXPROCS(0)
	for _, e := range entries {
		// Iterations and ns/op are based on successes only, so a run that mostly
		// failed can't masquerade as a fast one.
		if e.Result.SuccessCount == 0 {
			continue
		}
		nsPerOp := int64(e.Result.TotalSeconds * 1e9 / float64(e.Result.SuccessCount))
		benchName := e.Name
		if e.QueryLabel != "" {
			benchName = e.Name + "/" + e.QueryLabel
		}
		line := fmt.Sprintf("Benchmark%s-%d\t%d\t%d ns/op",
			benchName, procs,
			e.Result.SuccessCount,
			nsPerOp,
		)
		if s := e.Result.Stats(); s.Count > 0 {
			line += fmt.Sprintf("\t%d p50-ns/op\t%d p99-ns/op",
				s.P50.Nanoseconds(),
				s.P99.Nanoseconds(),
			)
		}
		if e.Result.FailureCount > 0 {
			line += fmt.Sprintf("\t# %d failed: %s",
				e.Result.FailureCount, formatErrors(e.Result.Errors))
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
