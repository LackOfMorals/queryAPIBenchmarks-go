package main

import (
	"os"
	"slices"
	"testing"

	"github.com/LackOfMorals/queryAPIBenchmarks-go/benchmarks"
)

func TestEnv(t *testing.T) {
	t.Setenv("BENCH_TEST_ENV", "set-value")

	tests := []struct {
		name     string
		key      string
		fallback string
		want     string
	}{
		{name: "set variable wins", key: "BENCH_TEST_ENV", fallback: "fallback", want: "set-value"},
		{name: "unset variable uses fallback", key: "BENCH_TEST_ENV_UNSET", fallback: "fallback", want: "fallback"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := env(tt.key, tt.fallback); got != tt.want {
				t.Errorf("env(%q, %q) = %q, want %q", tt.key, tt.fallback, got, tt.want)
			}
		})
	}
}

func TestTestCases(t *testing.T) {
	txs := []benchmarks.Transaction{benchmarks.TransactionImplicit, benchmarks.TransactionManaged}
	concs := []benchmarks.Concurrency{benchmarks.ConcurrencySequential, benchmarks.ConcurrencyConcurrent}
	conns := []benchmarks.Connection{benchmarks.ConnectionFresh, benchmarks.ConnectionPooled}
	streams := []benchmarks.Streaming{benchmarks.StreamingOff, benchmarks.StreamingOn}

	got := testCases(txs, concs, conns, streams)

	if want := len(txs) * len(concs) * len(conns) * len(streams); len(got) != want {
		t.Fatalf("testCases returned %d cases, want %d (cartesian product)", len(got), want)
	}

	seen := make(map[testCase]bool)
	for _, c := range got {
		if seen[c] {
			t.Errorf("duplicate case %+v", c)
		}
		seen[c] = true
	}
	if !seen[testCase{tx: benchmarks.TransactionManaged, conc: benchmarks.ConcurrencyConcurrent, conn: benchmarks.ConnectionPooled, stream: benchmarks.StreamingOff}] {
		t.Errorf("expected the managed/concurrent/pooled/buffered combination to be present")
	}
	if !seen[testCase{tx: benchmarks.TransactionImplicit, conc: benchmarks.ConcurrencySequential, conn: benchmarks.ConnectionFresh, stream: benchmarks.StreamingOn}] {
		t.Errorf("expected the implicit/sequential/fresh/streaming combination to be present")
	}
}

func TestResolveTransactions(t *testing.T) {
	tests := []struct {
		name    string
		raw     []string
		kind    benchmarks.Kind
		want    []benchmarks.Transaction
		wantErr bool
	}{
		{
			name:    "empty is required for HTTP kinds",
			raw:     nil,
			kind:    benchmarks.KindQueryV2,
			wantErr: true,
		},
		{
			name: "empty defaults to implicit for bolt",
			raw:  nil,
			kind: benchmarks.KindBolt,
			want: []benchmarks.Transaction{benchmarks.TransactionImplicit},
		},
		{
			name: "explicit value",
			raw:  []string{"managed"},
			kind: benchmarks.KindQueryV2,
			want: []benchmarks.Transaction{benchmarks.TransactionManaged},
		},
		{
			name: "all expands to every value",
			raw:  []string{"all"},
			kind: benchmarks.KindLegacy,
			want: []benchmarks.Transaction{benchmarks.TransactionImplicit, benchmarks.TransactionManaged},
		},
		{
			name:    "managed rejected for bolt",
			raw:     []string{"managed"},
			kind:    benchmarks.KindBolt,
			wantErr: true,
		},
		{
			name:    "all rejected for bolt (includes managed)",
			raw:     []string{"all"},
			kind:    benchmarks.KindBolt,
			wantErr: true,
		},
		{
			name:    "unknown value",
			raw:     []string{"bogus"},
			kind:    benchmarks.KindQueryV2,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveTransactions(tt.raw, tt.kind)
			if (err != nil) != tt.wantErr {
				t.Fatalf("resolveTransactions(%v, %v) error = %v, wantErr %v", tt.raw, tt.kind, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("resolveTransactions(%v, %v) = %v, want %v", tt.raw, tt.kind, got, tt.want)
			}
		})
	}
}

func TestResolveConcurrencies(t *testing.T) {
	tests := []struct {
		name    string
		raw     []string
		want    []benchmarks.Concurrency
		wantErr bool
	}{
		{name: "empty defaults to sequential", raw: nil, want: []benchmarks.Concurrency{benchmarks.ConcurrencySequential}},
		{name: "all expands to every value", raw: []string{"all"}, want: []benchmarks.Concurrency{benchmarks.ConcurrencySequential, benchmarks.ConcurrencyConcurrent}},
		{name: "repeated flag deduplicates", raw: []string{"concurrent", "concurrent"}, want: []benchmarks.Concurrency{benchmarks.ConcurrencyConcurrent}},
		{name: "unknown value", raw: []string{"bogus"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveConcurrencies(tt.raw)
			if (err != nil) != tt.wantErr {
				t.Fatalf("resolveConcurrencies(%v) error = %v, wantErr %v", tt.raw, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("resolveConcurrencies(%v) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}

func TestResolveConnections(t *testing.T) {
	tests := []struct {
		name    string
		raw     []string
		kind    benchmarks.Kind
		want    []benchmarks.Connection
		wantErr bool
	}{
		{name: "empty defaults to fresh for HTTP", raw: nil, kind: benchmarks.KindQueryV2, want: []benchmarks.Connection{benchmarks.ConnectionFresh}},
		{name: "empty defaults to pooled for bolt", raw: nil, kind: benchmarks.KindBolt, want: []benchmarks.Connection{benchmarks.ConnectionPooled}},
		{name: "explicit pooled is fine for bolt", raw: []string{"pooled"}, kind: benchmarks.KindBolt, want: []benchmarks.Connection{benchmarks.ConnectionPooled}},
		{name: "fresh rejected for bolt", raw: []string{"fresh"}, kind: benchmarks.KindBolt, wantErr: true},
		{name: "all rejected for bolt (includes fresh)", raw: []string{"all"}, kind: benchmarks.KindBolt, wantErr: true},
		{name: "all expands to every value for HTTP", raw: []string{"all"}, kind: benchmarks.KindLegacy, want: []benchmarks.Connection{benchmarks.ConnectionFresh, benchmarks.ConnectionPooled}},
		{name: "unknown value", raw: []string{"bogus"}, kind: benchmarks.KindQueryV2, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveConnections(tt.raw, tt.kind)
			if (err != nil) != tt.wantErr {
				t.Fatalf("resolveConnections(%v, %v) error = %v, wantErr %v", tt.raw, tt.kind, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("resolveConnections(%v, %v) = %v, want %v", tt.raw, tt.kind, got, tt.want)
			}
		})
	}
}

func TestResolveStreaming(t *testing.T) {
	tests := []struct {
		name    string
		raw     []string
		kind    benchmarks.Kind
		want    []benchmarks.Streaming
		wantErr bool
	}{
		{name: "empty defaults to buffered", raw: nil, kind: benchmarks.KindQueryV2, want: []benchmarks.Streaming{benchmarks.StreamingOff}},
		{name: "streaming is fine for queryv2", raw: []string{"streaming"}, kind: benchmarks.KindQueryV2, want: []benchmarks.Streaming{benchmarks.StreamingOn}},
		{name: "all expands to both for queryv2", raw: []string{"all"}, kind: benchmarks.KindQueryV2, want: []benchmarks.Streaming{benchmarks.StreamingOff, benchmarks.StreamingOn}},
		{name: "streaming rejected for legacy", raw: []string{"streaming"}, kind: benchmarks.KindLegacy, wantErr: true},
		{name: "streaming rejected for bolt", raw: []string{"streaming"}, kind: benchmarks.KindBolt, wantErr: true},
		{name: "all rejected for legacy (includes streaming)", raw: []string{"all"}, kind: benchmarks.KindLegacy, wantErr: true},
		{name: "buffered is fine for legacy", raw: []string{"buffered"}, kind: benchmarks.KindLegacy, want: []benchmarks.Streaming{benchmarks.StreamingOff}},
		{name: "unknown value", raw: []string{"bogus"}, kind: benchmarks.KindQueryV2, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveStreaming(tt.raw, tt.kind)
			if (err != nil) != tt.wantErr {
				t.Fatalf("resolveStreaming(%v, %v) error = %v, wantErr %v", tt.raw, tt.kind, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("resolveStreaming(%v, %v) = %v, want %v", tt.raw, tt.kind, got, tt.want)
			}
		})
	}
}

func TestValidateStreamingTransaction(t *testing.T) {
	tests := []struct {
		name    string
		txs     []benchmarks.Transaction
		streams []benchmarks.Streaming
		wantErr bool
	}{
		{
			name:    "implicit + streaming is fine",
			txs:     []benchmarks.Transaction{benchmarks.TransactionImplicit},
			streams: []benchmarks.Streaming{benchmarks.StreamingOn},
		},
		{
			name:    "managed + buffered is fine",
			txs:     []benchmarks.Transaction{benchmarks.TransactionManaged},
			streams: []benchmarks.Streaming{benchmarks.StreamingOff},
		},
		{
			name:    "managed + streaming is rejected",
			txs:     []benchmarks.Transaction{benchmarks.TransactionManaged},
			streams: []benchmarks.Streaming{benchmarks.StreamingOn},
			wantErr: true,
		},
		{
			name:    "transaction all + streaming all is rejected (covers managed+streaming)",
			txs:     []benchmarks.Transaction{benchmarks.TransactionImplicit, benchmarks.TransactionManaged},
			streams: []benchmarks.Streaming{benchmarks.StreamingOff, benchmarks.StreamingOn},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateStreamingTransaction(tt.txs, tt.streams)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateStreamingTransaction(%v, %v) error = %v, wantErr %v", tt.txs, tt.streams, err, tt.wantErr)
			}
		})
	}
}

func TestExpandValues(t *testing.T) {
	values := map[string]string{"a": "A", "b": "B"}
	all := []string{"A", "B"}

	tests := []struct {
		name    string
		raw     []string
		want    []string
		wantErr bool
	}{
		{name: "literal values in order", raw: []string{"b", "a"}, want: []string{"B", "A"}},
		{name: "all expands and dedupes against explicit values", raw: []string{"a", "all"}, want: []string{"A", "B"}},
		{name: "unknown value errors", raw: []string{"z"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := expandValues(tt.raw, "axis", values, all)
			if (err != nil) != tt.wantErr {
				t.Fatalf("expandValues(%v) error = %v, wantErr %v", tt.raw, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("expandValues(%v) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}

func TestContains(t *testing.T) {
	if !contains([]int{1, 2, 3}, 2) {
		t.Error("contains([1,2,3], 2) = false, want true")
	}
	if contains([]int{1, 2, 3}, 4) {
		t.Error("contains([1,2,3], 4) = true, want false")
	}
	if contains([]int{}, 1) {
		t.Error("contains([], 1) = true, want false")
	}
}

func TestApiKind(t *testing.T) {
	tests := []struct {
		api  string
		want benchmarks.Kind
	}{
		{api: "queryv2", want: benchmarks.KindQueryV2},
		{api: "legacy", want: benchmarks.KindLegacy},
		{api: "bolt", want: benchmarks.KindBolt},
	}
	for _, tt := range tests {
		t.Run(tt.api, func(t *testing.T) {
			if got := apiKind(tt.api); got != tt.want {
				t.Errorf("apiKind(%q) = %v, want %v", tt.api, got, tt.want)
			}
		})
	}
}

func TestApiLabel(t *testing.T) {
	tests := []struct {
		kind benchmarks.Kind
		want string
	}{
		{kind: benchmarks.KindQueryV2, want: "Neo4j Query API v2 (/db/{db}/query/v2)"},
		{kind: benchmarks.KindLegacy, want: "Legacy Cypher HTTP Transaction API (/db/{db}/tx/commit)"},
		{kind: benchmarks.KindBolt, want: "Neo4j Go Driver v6 (Bolt)"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := apiLabel(tt.kind); got != tt.want {
				t.Errorf("apiLabel(%v) = %q, want %q", tt.kind, got, tt.want)
			}
		})
	}
}

func TestValidSchemes(t *testing.T) {
	httpTests := []struct {
		scheme string
		want   bool
	}{
		{scheme: "http", want: true},
		{scheme: "https", want: true},
		{scheme: "bolt", want: false},
		{scheme: "", want: false},
	}
	for _, tt := range httpTests {
		t.Run("http/"+tt.scheme, func(t *testing.T) {
			if got := validHTTPSchemes[tt.scheme]; got != tt.want {
				t.Errorf("validHTTPSchemes[%q] = %v, want %v", tt.scheme, got, tt.want)
			}
		})
	}

	boltTests := []struct {
		scheme string
		want   bool
	}{
		{scheme: "bolt", want: true},
		{scheme: "bolt+s", want: true},
		{scheme: "bolt+ssc", want: true},
		{scheme: "neo4j", want: true},
		{scheme: "neo4j+s", want: true},
		{scheme: "neo4j+ssc", want: true},
		{scheme: "http", want: false},
		{scheme: "https", want: false},
		{scheme: "", want: false},
	}
	for _, tt := range boltTests {
		t.Run("bolt/"+tt.scheme, func(t *testing.T) {
			if got := validBoltSchemes[tt.scheme]; got != tt.want {
				t.Errorf("validBoltSchemes[%q] = %v, want %v", tt.scheme, got, tt.want)
			}
		})
	}
}

func TestBuildURL(t *testing.T) {
	tests := []struct {
		name       string
		kind       benchmarks.Kind
		host       string
		httpScheme string
		httpPort   int
		boltScheme string
		boltPort   int
		want       string
	}{
		{
			name: "query v2 uses the http scheme and port",
			kind: benchmarks.KindQueryV2, host: "ip-10-0-29-201.ec2.internal",
			httpScheme: "http", httpPort: 7474, boltScheme: "neo4j", boltPort: 7687,
			want: "http://ip-10-0-29-201.ec2.internal:7474",
		},
		{
			name: "legacy also uses the http scheme and port",
			kind: benchmarks.KindLegacy, host: "localhost",
			httpScheme: "https", httpPort: 7473, boltScheme: "neo4j", boltPort: 7687,
			want: "https://localhost:7473",
		},
		{
			name: "bolt uses the bolt scheme and port, ignoring the http ones",
			kind: benchmarks.KindBolt, host: "ip-10-0-29-201.ec2.internal",
			httpScheme: "http", httpPort: 7474, boltScheme: "bolt+s", boltPort: 7687,
			want: "bolt+s://ip-10-0-29-201.ec2.internal:7687",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildURL(tt.kind, tt.host, tt.httpScheme, tt.httpPort, tt.boltScheme, tt.boltPort)
			if got != tt.want {
				t.Errorf("buildURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIntEnv(t *testing.T) {
	t.Setenv("BENCH_TEST_INT", "42")
	if got := intEnv("BENCH_TEST_INT", 7); got != 42 {
		t.Errorf("intEnv with set valid int = %d, want 42", got)
	}

	t.Setenv("BENCH_TEST_INT_BAD", "not-a-number")
	if got := intEnv("BENCH_TEST_INT_BAD", 7); got != 7 {
		t.Errorf("intEnv with invalid value = %d, want fallback 7", got)
	}

	if err := os.Unsetenv("BENCH_TEST_INT_UNSET"); err != nil {
		t.Fatal(err)
	}
	if got := intEnv("BENCH_TEST_INT_UNSET", 7); got != 7 {
		t.Errorf("intEnv with unset variable = %d, want fallback 7", got)
	}
}

func TestBoolEnv(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{value: "1", want: true},
		{value: "true", want: true},
		{value: "TRUE", want: true},
		{value: "yes", want: true},
		{value: "0", want: false},
		{value: "false", want: false},
		{value: "", want: false},
		{value: "nope", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			t.Setenv("BENCH_TEST_BOOL", tt.value)
			if got := boolEnv("BENCH_TEST_BOOL"); got != tt.want {
				t.Errorf("boolEnv() with value %q = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}

func TestResolveQueries(t *testing.T) {
	queries, err := resolveQueries("", "RETURN 1")
	if err != nil {
		t.Fatalf("resolveQueries with no file: unexpected error: %v", err)
	}
	if len(queries) != 1 || queries[0].Cypher != "RETURN 1" || queries[0].Label != "" {
		t.Errorf("resolveQueries with no file = %+v, want a single unlabelled query", queries)
	}

	if _, err := resolveQueries("does-not-exist.toml", "RETURN 1"); err == nil {
		t.Error("resolveQueries with a missing file: expected an error, got nil")
	}
}
