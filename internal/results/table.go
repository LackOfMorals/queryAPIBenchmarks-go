// Package results renders benchmark output to stdout.
// It mirrors the Python generate_table function in showResults.py.
package results

import (
	"fmt"
	"strings"

	"github.com/LackOfMorals/queryAPIBenchmarks-go/internal/runner"
)

// Entry pairs a test name with its result.
type Entry struct {
	Name   string
	Result runner.Result
}

// PrintTable writes an ASCII table of benchmark results to stdout, marking
// unreliable runs (>10% failure rate) with an asterisk.
func PrintTable(entries []Entry) {
	const (
		colTest    = 32
		colTime    = 16
		colRPS     = 16
		colFail    = 14
	)

	header := fmt.Sprintf("%-*s  %-*s  %-*s  %-*s",
		colTest, "Test",
		colTime, "Time (s)",
		colRPS, "Req/s",
		colFail, "Failures",
	)

	divider := strings.Repeat("-", len(header))

	fmt.Println()
	fmt.Println(divider)
	fmt.Println(header)
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
	}

	fmt.Println(divider)

	if hasUnreliable {
		fmt.Println("\n* Result unreliable: >10% of requests failed")
	}

	fmt.Println()
}
