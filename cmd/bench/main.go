// Command bench is the Go equivalent of the Python queryAPIBenchmarks CLI.
//
// Usage:
//
//	bench -transaction implicit -concurrency all -n 100 \
//	      -host localhost -usr neo4j -pwd secret \
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
	"log/slog"
	"os"
	"strings"
	"time"

	query "github.com/neo4j-contrib/query-go-sdk"

	"github.com/LackOfMorals/queryAPIBenchmarks-go/benchmarks"
	"github.com/LackOfMorals/queryAPIBenchmarks-go/internal/queryfile"
	"github.com/LackOfMorals/queryAPIBenchmarks-go/internal/results"
	"github.com/LackOfMorals/queryAPIBenchmarks-go/internal/runner"
	"github.com/joho/godotenv"
)

// multiFlag allows a flag to be specified more than once.
type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	_ = godotenv.Load()

	var transactionFlag, concurrencyFlag, connectionFlag, streamingFlag multiFlag

	flag.Var(&transactionFlag, "transaction", "Transaction style (repeatable): implicit, managed, or all. Required unless -api bolt (fixed at implicit).")
	flag.Var(&concurrencyFlag, "concurrency", "Concurrency (repeatable): sequential, concurrent, or all. Default: sequential.")
	flag.Var(&connectionFlag, "connection", "HTTP connection reuse (repeatable): fresh, pooled, or all. Default: fresh. Not applicable to -api bolt (always pooled).")
	flag.Var(&streamingFlag, "streaming", "Response decoding (repeatable): buffered, streaming, or all. Default: buffered. Only supported with -api queryv2 -transaction implicit (requires query-go-sdk v0.5.0+).")

	numRequests := flag.Int("n", intEnv("NUM_REQUESTS", 50), "Number of timed requests")
	warmupRequests := flag.Int("warmup", intEnv("WARMUP_REQUESTS", 5), "Warmup iterations before timing (0 to skip)")
	maxWorkers := flag.Int("workers", intEnv("MAX_WORKERS", 4), "Goroutine count for concurrent tests")
	timeoutSecs := flag.Int("timeout", intEnv("NETWORK_TIMEOUT", 30), "Per-request timeout in seconds")
	http2Flag := flag.Bool("http2", boolEnv("NETWORK_HTTP2"), "Use HTTP/2 for session transports (ignored with -api bolt)")
	format := flag.String("format", env("OUTPUT_FORMAT", "table"), "Output format: table, json, or benchstat")
	runsFlag := flag.Int("runs", intEnv("BENCH_RUNS", 1), "Number of independent runs (use with -format benchstat for confidence intervals)")
	apiFlag := flag.String("api", env("NEO4J_API", "queryv2"), "API to benchmark: queryv2 (Neo4j Query API v2), legacy (Cypher HTTP Transaction API), or bolt (Neo4j Go Driver v6)")
	modeFlag := flag.String("mode", env("NEO4J_ACCESS_MODE", "read"), "Access mode: read or write")
	debugFlag := flag.Bool("debug", boolEnv("DEBUG"), "Enable debug log output")

	hostFlag := flag.String("host", env("NEO4J_HOST", "localhost"), "Neo4j host: an IP or FQDN, no scheme or port")
	httpSchemeFlag := flag.String("http-scheme", env("NEO4J_HTTP_SCHEME", "http"), "Scheme for queryv2/legacy: http or https")
	httpPortFlag := flag.Int("http-port", intEnv("NEO4J_HTTP_PORT", 7474), "Port for queryv2/legacy")
	boltSchemeFlag := flag.String("bolt-scheme", env("NEO4J_BOLT_SCHEME", "neo4j"), "Scheme for -api bolt: bolt, bolt+s, bolt+ssc, neo4j, neo4j+s, or neo4j+ssc")
	boltPortFlag := flag.Int("bolt-port", intEnv("NEO4J_BOLT_PORT", 7687), "Port for -api bolt")
	neo4jUsr := flag.String("usr", env("NEO4J_USERNAME", "neo4j"), "Neo4j username")
	neo4jPwd := flag.String("pwd", env("NEO4J_PASSWORD", "password"), "Neo4j password")
	neo4jDB := flag.String("db", env("NEO4J_DATABASE", "neo4j"), "Neo4j database name")
	neo4jCypher := flag.String("cypher", env("NEO4J_CYPHER", "RETURN 1"), "Cypher statement to benchmark")
	queriesFile := flag.String("queries-file", "", "TOML file of named queries (mutually exclusive with -cypher)")

	flag.Parse()

	if *apiFlag != "queryv2" && *apiFlag != "legacy" && *apiFlag != "bolt" {
		fmt.Fprintf(os.Stderr, "Error: -api must be \"queryv2\", \"legacy\", or \"bolt\"\n")
		os.Exit(1)
	}
	kind := apiKind(*apiFlag)

	if *modeFlag != "read" && *modeFlag != "write" {
		fmt.Fprintf(os.Stderr, "Error: -mode must be \"read\" or \"write\"\n")
		os.Exit(1)
	}

	if *runsFlag < 1 {
		fmt.Fprintf(os.Stderr, "Error: -runs must be >= 1\n")
		os.Exit(1)
	}
	if *runsFlag > 1 && *format != "benchstat" {
		fmt.Fprintf(os.Stderr, "Warning: -runs %d has no effect with -format %s; use -format benchstat\n", *runsFlag, *format)
	}

	if kind == benchmarks.KindBolt {
		if !validBoltSchemes[*boltSchemeFlag] {
			fmt.Fprintf(os.Stderr, "Error: -bolt-scheme must be one of: bolt, bolt+s, bolt+ssc, neo4j, neo4j+s, neo4j+ssc (got %q)\n", *boltSchemeFlag)
			os.Exit(1)
		}
		if *http2Flag {
			fmt.Fprintf(os.Stderr, "Warning: -http2 has no effect with -api bolt; ignoring\n")
		}
	} else if !validHTTPSchemes[*httpSchemeFlag] {
		fmt.Fprintf(os.Stderr, "Error: -http-scheme must be \"http\" or \"https\" (got %q)\n", *httpSchemeFlag)
		os.Exit(1)
	}

	targetURL := buildURL(kind, *hostFlag, *httpSchemeFlag, *httpPortFlag, *boltSchemeFlag, *boltPortFlag)

	transactions, err := resolveTransactions(transactionFlag, kind)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	concurrencies, err := resolveConcurrencies(concurrencyFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	connections, err := resolveConnections(connectionFlag, kind)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	streamings, err := resolveStreaming(streamingFlag, kind)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if err := validateStreamingTransaction(transactions, streamings); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	cases := testCases(transactions, concurrencies, connections, streamings)

	if *queriesFile != "" {
		var cypherExplicit bool
		flag.Visit(func(f *flag.Flag) {
			if f.Name == "cypher" {
				cypherExplicit = true
			}
		})
		if cypherExplicit {
			fmt.Fprintf(os.Stderr, "Error: -queries-file and -cypher are mutually exclusive\n")
			os.Exit(1)
		}
	}

	accessMode := query.AccessModeRead
	if *modeFlag == "write" {
		accessMode = query.AccessModeWrite
	}

	level := slog.LevelInfo
	if *debugFlag {
		level = slog.LevelDebug
	}
	opts := &slog.HandlerOptions{Level: level}
	handler := slog.NewTextHandler(os.Stderr, opts)
	customLogger := slog.New(handler)

	timeout := time.Duration(*timeoutSecs) * time.Second

	benchCfg := benchmarks.Config{
		URL:        targetURL,
		Username:   *neo4jUsr,
		Password:   *neo4jPwd,
		Database:   *neo4jDB,
		Timeout:    timeout,
		HTTP2:      *http2Flag,
		Kind:       kind,
		AccessMode: accessMode,
		Logger:     customLogger,
		Config: runner.Config{
			Requests:       *numRequests,
			WarmupRequests: *warmupRequests,
			Workers:        *maxWorkers,
			Timeout:        timeout,
		},
	}

	label := apiLabel(kind)
	fmt.Fprintf(os.Stderr, "API: %s\n", label)

	// Resolve the query list — either from a file or the single -cypher flag.
	queries, err := resolveQueries(*queriesFile, *neo4jCypher)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	ctx := context.Background()
	var entries []results.Entry

	// A benchmark that could not run at all (warmup failed, target unreachable)
	// must not exit 0 — otherwise a scheduled or scripted run looks successful.
	var aborted int

	for run := range *runsFlag {
		if *runsFlag > 1 {
			fmt.Fprintf(os.Stderr, "\n=== Run %d/%d ===\n", run+1, *runsFlag)
		}

		var runEntries []results.Entry
		for _, q := range queries {
			for _, c := range cases {
				name := benchmarks.DisplayName(kind, c.tx, c.conc, c.conn, c.stream)
				if q.Label != "" {
					fmt.Fprintf(os.Stderr, "\nRunning %s [%s]...\n", name, q.Label)
				} else {
					fmt.Fprintf(os.Stderr, "\nRunning %s...\n", name)
				}

				result, err := benchmarks.Run(ctx, benchCfg, q.Cypher, c.tx, c.conc, c.conn, c.stream)
				if err != nil {
					log.Printf("ERROR %s: %v\n", name, err)
					aborted++
					continue
				}

				// A run where every request failed is not a result. Without this
				// the table shows "0 / 50", an asterisk, and exit code 0 — which a
				// scheduled job reads as success. The warmup guard alone doesn't
				// cover it, since -warmup 0 skips warmup entirely.
				if result.RequestCount > 0 && result.SuccessCount == 0 {
					log.Printf("ERROR %s: all %d requests failed", name, result.RequestCount)
					aborted++
				}

				runEntries = append(runEntries, results.Entry{Name: name, QueryLabel: q.Label, Result: result})
			}
		}

		if *format == "benchstat" {
			// Flush each run immediately so output streams and benchstat can
			// accumulate repeated benchmark names as independent samples.
			results.PrintBenchstat(runEntries)
		} else {
			entries = append(entries, runEntries...)
		}
	}

	if *format != "benchstat" && len(entries) > 0 {
		switch *format {
		case "json":
			results.PrintJSON(entries, label)
		default:
			results.PrintTable(entries, label)
		}
	}

	if aborted > 0 {
		fmt.Fprintf(os.Stderr, "\n%d benchmark(s) could not run.\n", aborted)
		os.Exit(1)
	}
}

// testCase is one resolved (transaction, concurrency, connection, streaming)
// combination to run — the cartesian product of the
// -transaction/-concurrency/-connection/-streaming flag values.
type testCase struct {
	tx     benchmarks.Transaction
	conc   benchmarks.Concurrency
	conn   benchmarks.Connection
	stream benchmarks.Streaming
}

func testCases(txs []benchmarks.Transaction, concs []benchmarks.Concurrency, conns []benchmarks.Connection, streams []benchmarks.Streaming) []testCase {
	cases := make([]testCase, 0, len(txs)*len(concs)*len(conns)*len(streams))
	for _, tx := range txs {
		for _, conc := range concs {
			for _, conn := range conns {
				for _, stream := range streams {
					cases = append(cases, testCase{tx: tx, conc: conc, conn: conn, stream: stream})
				}
			}
		}
	}
	return cases
}

var transactionValues = map[string]benchmarks.Transaction{
	"implicit": benchmarks.TransactionImplicit,
	"managed":  benchmarks.TransactionManaged,
}
var transactionAll = []benchmarks.Transaction{benchmarks.TransactionImplicit, benchmarks.TransactionManaged}

var concurrencyValues = map[string]benchmarks.Concurrency{
	"sequential": benchmarks.ConcurrencySequential,
	"concurrent": benchmarks.ConcurrencyConcurrent,
}
var concurrencyAll = []benchmarks.Concurrency{benchmarks.ConcurrencySequential, benchmarks.ConcurrencyConcurrent}

var connectionValues = map[string]benchmarks.Connection{
	"fresh":  benchmarks.ConnectionFresh,
	"pooled": benchmarks.ConnectionPooled,
}
var connectionAll = []benchmarks.Connection{benchmarks.ConnectionFresh, benchmarks.ConnectionPooled}

var streamingValues = map[string]benchmarks.Streaming{
	"buffered":  benchmarks.StreamingOff,
	"streaming": benchmarks.StreamingOn,
}
var streamingAll = []benchmarks.Streaming{benchmarks.StreamingOff, benchmarks.StreamingOn}

// resolveTransactions expands -transaction into the set of styles to run.
// It's required for the HTTP APIs (matching the old "-t is required"
// strictness) but optional for bolt, which only ever runs implicit.
func resolveTransactions(raw []string, kind benchmarks.Kind) ([]benchmarks.Transaction, error) {
	if len(raw) == 0 {
		if kind == benchmarks.KindBolt {
			return []benchmarks.Transaction{benchmarks.TransactionImplicit}, nil
		}
		return nil, fmt.Errorf("at least one -transaction is required: implicit, managed, or all")
	}
	txs, err := expandValues(raw, "transaction", transactionValues, transactionAll)
	if err != nil {
		return nil, err
	}
	if kind == benchmarks.KindBolt && contains(txs, benchmarks.TransactionManaged) {
		return nil, fmt.Errorf("-transaction managed is not supported with -api bolt: Bolt only runs the auto-commit implicit style via ExecuteQuery")
	}
	return txs, nil
}

// resolveConcurrencies expands -concurrency, defaulting to sequential when
// unset. Concurrency applies identically to every backend.
func resolveConcurrencies(raw []string) ([]benchmarks.Concurrency, error) {
	if len(raw) == 0 {
		return []benchmarks.Concurrency{benchmarks.ConcurrencySequential}, nil
	}
	return expandValues(raw, "concurrency", concurrencyValues, concurrencyAll)
}

// resolveConnections expands -connection. Bolt has no fresh-connection mode
// (it always shares one pooled Driver), so it defaults to — and is
// restricted to — pooled.
func resolveConnections(raw []string, kind benchmarks.Kind) ([]benchmarks.Connection, error) {
	if len(raw) == 0 {
		if kind == benchmarks.KindBolt {
			return []benchmarks.Connection{benchmarks.ConnectionPooled}, nil
		}
		return []benchmarks.Connection{benchmarks.ConnectionFresh}, nil
	}
	conns, err := expandValues(raw, "connection", connectionValues, connectionAll)
	if err != nil {
		return nil, err
	}
	if kind == benchmarks.KindBolt && contains(conns, benchmarks.ConnectionFresh) {
		return nil, fmt.Errorf("-connection fresh is not supported with -api bolt: Bolt always uses one shared, pooled Driver — recreating a Driver per request isn't real usage")
	}
	return conns, nil
}

// resolveStreaming expands -streaming, defaulting to buffered when unset.
// Streaming (query-go-sdk's ExecuteStream, v0.5.0+) only exists for the
// Query API v2 implicit path: it errors if the SDK client also has
// FlavorLegacyHTTP set, and query-go-sdk exposes no managed-transaction API
// at all (see internal/managed). The implicit-vs-managed conflict is caught
// separately in main, once both -transaction and -streaming are resolved.
func resolveStreaming(raw []string, kind benchmarks.Kind) ([]benchmarks.Streaming, error) {
	if len(raw) == 0 {
		return []benchmarks.Streaming{benchmarks.StreamingOff}, nil
	}
	streams, err := expandValues(raw, "streaming", streamingValues, streamingAll)
	if err != nil {
		return nil, err
	}
	if contains(streams, benchmarks.StreamingOn) && kind != benchmarks.KindQueryV2 {
		return nil, fmt.Errorf("-streaming streaming is only supported with -api queryv2: %s has no streaming response format", apiLabel(kind))
	}
	return streams, nil
}

// validateStreamingTransaction rejects -streaming streaming (or all)
// combined with -transaction managed (or all): query-go-sdk's ExecuteStream
// has no managed-transaction equivalent (see internal/managed's package
// comment). Rejecting outright, rather than silently dropping the invalid
// pair from an -transaction all -streaming all sweep, matches how
// -api bolt -connection all is rejected instead of quietly filtered down to
// pooled only.
func validateStreamingTransaction(txs []benchmarks.Transaction, streams []benchmarks.Streaming) error {
	if contains(txs, benchmarks.TransactionManaged) && contains(streams, benchmarks.StreamingOn) {
		return fmt.Errorf("-streaming streaming is not supported with -transaction managed: query-go-sdk's ExecuteStream only covers the auto-commit implicit path; run managed separately with -streaming buffered")
	}
	return nil
}

// expandValues parses repeated flag values — each either a literal option or
// the shorthand "all" — into a deduplicated, order-preserving slice.
func expandValues[T comparable](raw []string, axis string, values map[string]T, all []T) ([]T, error) {
	var out []T
	seen := make(map[T]bool)
	add := func(v T) {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	for _, r := range raw {
		if r == "all" {
			for _, v := range all {
				add(v)
			}
			continue
		}
		v, ok := values[r]
		if !ok {
			return nil, fmt.Errorf("unknown -%s value %q", axis, r)
		}
		add(v)
	}
	return out, nil
}

func contains[T comparable](s []T, v T) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func apiKind(api string) benchmarks.Kind {
	switch api {
	case "legacy":
		return benchmarks.KindLegacy
	case "bolt":
		return benchmarks.KindBolt
	default:
		return benchmarks.KindQueryV2
	}
}

func apiLabel(kind benchmarks.Kind) string {
	switch kind {
	case benchmarks.KindLegacy:
		return "Legacy Cypher HTTP Transaction API (/db/{db}/tx/commit)"
	case benchmarks.KindBolt:
		return "Neo4j Go Driver v6 (Bolt)"
	default:
		return "Neo4j Query API v2 (/db/{db}/query/v2)"
	}
}

var validHTTPSchemes = map[string]bool{"http": true, "https": true}

var validBoltSchemes = map[string]bool{
	"bolt": true, "bolt+s": true, "bolt+ssc": true,
	"neo4j": true, "neo4j+s": true, "neo4j+ssc": true,
}

// buildURL combines the plain host with whichever scheme+port apply to
// kind, so -host never needs a scheme or port of its own: HTTP (queryv2,
// legacy) and Bolt use different schemes and, typically, different ports on
// the same node.
func buildURL(kind benchmarks.Kind, host, httpScheme string, httpPort int, boltScheme string, boltPort int) string {
	if kind == benchmarks.KindBolt {
		return fmt.Sprintf("%s://%s:%d", boltScheme, host, boltPort)
	}
	return fmt.Sprintf("%s://%s:%d", httpScheme, host, httpPort)
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

// resolveQueries returns the query list to benchmark.
// If queriesFilePath is set it loads queries from that TOML file;
// otherwise it wraps the single cypher string as an unlabelled query.
func resolveQueries(queriesFilePath, cypher string) ([]queryfile.Query, error) {
	if queriesFilePath != "" {
		return queryfile.Load(queriesFilePath)
	}
	return []queryfile.Query{{Label: "", Cypher: cypher}}, nil
}
