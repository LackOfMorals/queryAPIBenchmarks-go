// Command bench is the Go equivalent of the Python queryAPIBenchmarks CLI.
//
// Usage:
//
//	bench -t SyncImplicit -t GoroutinesSessionsImplicit -n 100 \
//	      -url http://localhost:7474 -usr neo4j -pwd secret \
//	      -db neo4j -cypher 'RETURN 1'
//
// All flags can also be set via environment variables (matching the Python
// .env convention).  Command-line flags take precedence.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/LackOfMorals/queryAPIBenchmarks-go/benchmarks"
	"github.com/LackOfMorals/queryAPIBenchmarks-go/internal/results"
	"github.com/LackOfMorals/queryAPIBenchmarks-go/internal/runner"
)

// multiFlag allows -t to be specified more than once.
type multiFlag []string

func (m *multiFlag) String() string  { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

// available lists all benchmark names in the same order as the Python tool.
var available = []string{
	"Sync",
	"SyncSessions",
	"Goroutines",
	"GoroutinesSessions",
	"SyncImplicit",
	"SyncSessionsImplicit",
	"GoroutinesImplicit",
	"GoroutinesSessionsImplicit",
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	var tests multiFlag

	flag.Var(&tests, "t", "Benchmark to run (repeatable). Choices: "+strings.Join(available, ", "))

	numRequests    := flag.Int("n", intEnv("NUM_REQUESTS", 5), "Number of timed requests")
	warmupRequests := flag.Int("warmup", intEnv("WARMUP_REQUESTS", 3), "Warmup iterations before timing (0 to skip)")
	maxWorkers     := flag.Int("workers", intEnv("MAX_WORKERS", 4), "Goroutine count for concurrent tests")
	timeoutSecs    := flag.Int("timeout", intEnv("NETWORK_TIMEOUT", 30), "Per-request timeout in seconds")
	http2Flag      := flag.Bool("http2", boolEnv("NETWORK_HTTP2"), "Use HTTP/2 for session transports")

	neo4jURL  := flag.String("url", env("NEO4J_URL", "http://localhost:7474"), "Neo4j Query API base URL")
	neo4jUsr  := flag.String("usr", env("NEO4J_USERNAME", "neo4j"), "Neo4j username")
	neo4jPwd  := flag.String("pwd", env("NEO4J_PASSWORD", ""), "Neo4j password")
	neo4jDB   := flag.String("db", env("NEO4J_DATABASE", "neo4j"), "Neo4j database name")
	neo4jCypher := flag.String("cypher", env("NEO4J_CYPHER", "RETURN 1"), "Cypher statement to benchmark")

	flag.Parse()

	if len(tests) == 0 {
		fmt.Fprintf(os.Stderr, "Error: at least one -t <test> is required.\nAvailable: %s\n", strings.Join(available, ", "))
		os.Exit(1)
	}

	for _, t := range tests {
		if !isKnown(t) {
			fmt.Fprintf(os.Stderr, "Error: unknown test %q. Available: %s\n", t, strings.Join(available, ", "))
			os.Exit(1)
		}
	}

	timeout := time.Duration(*timeoutSecs) * time.Second

	benchCfg := benchmarks.Config{
		URL:      *neo4jURL,
		Username: *neo4jUsr,
		Password: *neo4jPwd,
		Database: *neo4jDB,
		Timeout:  timeout,
		HTTP2:    *http2Flag,
		Config: runner.Config{
			Requests:       *numRequests,
			WarmupRequests: *warmupRequests,
			Workers:        *maxWorkers,
			Timeout:        timeout,
		},
	}

	ctx := context.Background()
	var entries []results.Entry

	for _, name := range tests {
		fmt.Printf("\nRunning %s...\n", name)

		result, err := dispatch(ctx, name, benchCfg, *neo4jCypher)
		if err != nil {
			log.Printf("ERROR %s: %v\n", name, err)
			continue
		}

		entries = append(entries, results.Entry{Name: name, Result: result})
	}

	if len(entries) > 0 {
		results.PrintTable(entries)
	}
}

// dispatch routes a test name to the corresponding benchmark function.
func dispatch(ctx context.Context, name string, cfg benchmarks.Config, cypher string) (runner.Result, error) {
	switch name {
	// Implicit
	case "SyncImplicit":
		return benchmarks.SyncImplicit(ctx, cfg, cypher)
	case "SyncSessionsImplicit":
		return benchmarks.SyncSessionsImplicit(ctx, cfg, cypher)
	case "GoroutinesImplicit":
		return benchmarks.GoroutinesImplicit(ctx, cfg, cypher)
	case "GoroutinesSessionsImplicit":
		return benchmarks.GoroutinesSessionsImplicit(ctx, cfg, cypher)
	// Managed
	case "Sync":
		return benchmarks.Sync(ctx, cfg, cypher)
	case "SyncSessions":
		return benchmarks.SyncSessions(ctx, cfg, cypher)
	case "Goroutines":
		return benchmarks.Goroutines(ctx, cfg, cypher)
	case "GoroutinesSessions":
		return benchmarks.GoroutinesSessions(ctx, cfg, cypher)
	default:
		return runner.Result{}, fmt.Errorf("unknown test: %s", name)
	}
}

func isKnown(name string) bool {
	for _, a := range available {
		if a == name {
			return true
		}
	}
	return false
}

func intEnv(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
		return fallback
	}
	return n
}

func boolEnv(key string) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	return v == "1" || v == "true" || v == "yes"
}
