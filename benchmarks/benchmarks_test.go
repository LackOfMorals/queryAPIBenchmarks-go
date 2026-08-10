package benchmarks

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	query "github.com/neo4j-contrib/query-go-sdk"

	"github.com/LackOfMorals/queryAPIBenchmarks-go/internal/runner"
)

func TestConfigFlavor(t *testing.T) {
	tests := []struct {
		name string
		kind Kind
		want query.APIFlavor
	}{
		{name: "query v2", kind: KindQueryV2, want: query.FlavorQueryV2},
		{name: "legacy", kind: KindLegacy, want: query.FlavorLegacyHTTP},
		{name: "bolt falls back to query v2 (unused by the bolt client)", kind: KindBolt, want: query.FlavorQueryV2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{Kind: tt.kind}
			if got := cfg.flavor(); got != tt.want {
				t.Errorf("flavor() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDisplayName(t *testing.T) {
	tests := []struct {
		name string
		kind Kind
		tx   Transaction
		conc Concurrency
		conn Connection
		want string
	}{
		{
			name: "http includes the connection segment",
			kind: KindQueryV2,
			tx:   TransactionImplicit,
			conc: ConcurrencySequential,
			conn: ConnectionFresh,
			want: "implicit/sequential/fresh",
		},
		{
			name: "legacy managed concurrent pooled",
			kind: KindLegacy,
			tx:   TransactionManaged,
			conc: ConcurrencyConcurrent,
			conn: ConnectionPooled,
			want: "managed/concurrent/pooled",
		},
		{
			name: "bolt omits the connection segment",
			kind: KindBolt,
			tx:   TransactionImplicit,
			conc: ConcurrencyConcurrent,
			conn: ConnectionPooled,
			want: "implicit/concurrent",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DisplayName(tt.kind, tt.tx, tt.conc, tt.conn); got != tt.want {
				t.Errorf("DisplayName(%v, %v, %v, %v) = %q, want %q", tt.kind, tt.tx, tt.conc, tt.conn, got, tt.want)
			}
		})
	}
}

// TestNewClient_SelectsImplementation constructs every (Kind, Transaction,
// Connection) combination and checks the concrete type newClient picked.
// None of query.NewClient, managed.NewClient, or neo4j.NewDriver make a
// network call — connectivity only happens when a TxFunc actually runs — so
// this needs no live Neo4j.
func TestNewClient_SelectsImplementation(t *testing.T) {
	baseCfg := Config{
		URL:      "http://localhost:7474",
		Username: "neo4j",
		Password: "password",
		Database: "neo4j",
		Timeout:  time.Second,
		Logger:   slog.New(slog.DiscardHandler),
	}
	boltCfg := baseCfg
	boltCfg.URL = "neo4j://localhost:7687"

	tests := []struct {
		name string
		cfg  Config
		tx   Transaction
		conn Connection
		want any // zero value of the expected concrete type
	}{
		{name: "query v2 implicit fresh", cfg: withKind(baseCfg, KindQueryV2), tx: TransactionImplicit, conn: ConnectionFresh, want: &httpImplicitClient{}},
		{name: "query v2 implicit pooled", cfg: withKind(baseCfg, KindQueryV2), tx: TransactionImplicit, conn: ConnectionPooled, want: &httpImplicitClient{}},
		{name: "query v2 managed fresh", cfg: withKind(baseCfg, KindQueryV2), tx: TransactionManaged, conn: ConnectionFresh, want: &httpManagedClient{}},
		{name: "legacy managed pooled", cfg: withKind(baseCfg, KindLegacy), tx: TransactionManaged, conn: ConnectionPooled, want: &httpManagedClient{}},
		{name: "bolt implicit", cfg: withKind(boltCfg, KindBolt), tx: TransactionImplicit, conn: ConnectionPooled, want: &boltClient{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := newClient(tt.cfg, tt.tx, tt.conn, 1)
			if err != nil {
				t.Fatalf("newClient() error = %v", err)
			}
			t.Cleanup(func() { _ = client.Close(context.Background()) })

			if gotType, wantType := typeName(client), typeName(tt.want); gotType != wantType {
				t.Errorf("newClient() returned %s, want %s", gotType, wantType)
			}
		})
	}
}

func TestEffectiveConcurrency(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		conc Concurrency
		want int
	}{
		{name: "sequential is always 1", cfg: Config{Config: runner.Config{Workers: 8, Requests: 100}}, conc: ConcurrencySequential, want: 1},
		{name: "concurrent uses Workers when set", cfg: Config{Config: runner.Config{Workers: 8, Requests: 100}}, conc: ConcurrencyConcurrent, want: 8},
		{name: "concurrent falls back to Requests when Workers is unset", cfg: Config{Config: runner.Config{Workers: 0, Requests: 100}}, conc: ConcurrencyConcurrent, want: 100},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := effectiveConcurrency(tt.cfg, tt.conc); got != tt.want {
				t.Errorf("effectiveConcurrency() = %d, want %d", got, tt.want)
			}
		})
	}
}

// fakeClient is a hand-written Client double: no network, canned
// latency/error per call. It exists to show that the Client interface
// (Tx/Close) is enough to drive the runner without a live Neo4j — the
// "how" (HTTP vs Bolt) genuinely doesn't matter to this test.
type fakeClient struct {
	err      error
	closeErr error
	closed   bool
}

var _ Client = (*fakeClient)(nil)

func (f *fakeClient) Tx(string) runner.TxFunc {
	return func(context.Context) error { return f.err }
}

func (f *fakeClient) Close(context.Context) error {
	f.closed = true
	return f.closeErr
}

func TestClient_DrivesRunnerWithoutABackend(t *testing.T) {
	fake := &fakeClient{}
	cfg := runner.Config{Name: "fake", Requests: 5, WarmupRequests: 1}

	result, err := runner.Run(context.Background(), cfg, fake.Tx("RETURN 1"))
	if err != nil {
		t.Fatalf("runner.Run() error = %v", err)
	}
	if result.RequestCount != 5 || result.SuccessCount != 5 {
		t.Errorf("result = %+v, want 5 requests all succeeding", result)
	}

	failing := &fakeClient{err: errors.New("boom")}
	result, err = runner.Run(context.Background(), cfg, failing.Tx("RETURN 1"))
	if err != nil {
		t.Fatalf("runner.Run() with a failing Client error = %v", err)
	}
	if result.FailureCount != 5 {
		t.Errorf("result.FailureCount = %d, want 5", result.FailureCount)
	}

	if err := fake.Close(context.Background()); err != nil {
		t.Errorf("Close() error = %v", err)
	}
	if !fake.closed {
		t.Error("Close() did not mark the fake client closed")
	}
}

func withKind(cfg Config, kind Kind) Config {
	cfg.Kind = kind
	return cfg
}

func typeName(v any) string {
	return fmt.Sprintf("%T", v)
}
