// Package benchmarks runs one Cypher statement per iteration against a
// chosen Neo4j access backend (Query API v2, the Legacy Cypher HTTP
// Transaction API, or the official Bolt driver) and reports timing/failure
// data via the runner package.
//
// A benchmark run is fully described by three orthogonal axes:
//
//   - Kind        which backend: KindQueryV2, KindLegacy, or KindBolt
//   - Transaction  how a call executes: implicit (single auto-commit call)
//     or managed (explicit begin -> execute -> commit)
//   - Concurrency  sequential or concurrent (goroutine pool)
//   - Connection   fresh TCP connection per request, or a persistent/pooled
//     connection — HTTP only; Bolt always pools via its Driver and has no
//     fresh mode, and always runs as Transaction == implicit (see bolt.go)
//
// Run dispatches on these axes to a small Client interface so the rest of
// the package (and the runner it drives) doesn't need to know which backend
// is in play.
package benchmarks

import (
	"context"
	"net/http"
	"time"

	query "github.com/neo4j-contrib/query-go-sdk"

	"log/slog"

	"github.com/LackOfMorals/queryAPIBenchmarks-go/internal/managed"
	"github.com/LackOfMorals/queryAPIBenchmarks-go/internal/runner"
	"github.com/LackOfMorals/queryAPIBenchmarks-go/internal/transport"
)

// Kind selects which Neo4j access backend a benchmark run targets.
type Kind int

const (
	KindQueryV2 Kind = iota
	KindLegacy
	KindBolt
)

// Transaction selects how a single Cypher statement is executed.
type Transaction string

const (
	TransactionImplicit Transaction = "implicit"
	TransactionManaged  Transaction = "managed"
)

// Concurrency selects whether iterations run sequentially or across a
// goroutine pool.
type Concurrency string

const (
	ConcurrencySequential Concurrency = "sequential"
	ConcurrencyConcurrent Concurrency = "concurrent"
)

// Connection selects HTTP connection reuse. Meaningless for Kind == KindBolt.
type Connection string

const (
	ConnectionFresh  Connection = "fresh"
	ConnectionPooled Connection = "pooled"
)

// Streaming selects whether an implicit Query API v2 call buffers the whole
// response before returning (query-go-sdk's Execute) or decodes it
// incrementally as it arrives (ExecuteStream, added in query-go-sdk v0.5.0).
// Meaningless for KindLegacy (the legacy Cypher HTTP Transaction API has no
// streaming format) and KindBolt (Bolt already streams at the protocol
// level regardless of client choice), and for TransactionManaged
// (query-go-sdk's ExecuteStream only covers the auto-commit implicit path).
type Streaming string

const (
	StreamingOff Streaming = "buffered"
	StreamingOn  Streaming = "streaming"
)

// Config holds everything needed to construct a client and run a benchmark.
type Config struct {
	URL      string
	Username string
	Password string
	Database string
	Timeout  time.Duration
	Logger   *slog.Logger

	// HTTP2 enables HTTP/2 for session-based transports. Ignored for KindBolt.
	HTTP2 bool

	// Kind selects the Neo4j access backend.
	Kind Kind

	// AccessMode controls the access-mode header (HTTP) or routing (Bolt)
	// sent with every request.
	AccessMode query.AccessMode

	runner.Config
}

// flavor maps Kind to the query-go-sdk API flavor. Only meaningful for the
// HTTP kinds — callers must not invoke it for KindBolt.
func (cfg Config) flavor() query.APIFlavor {
	if cfg.Kind == KindLegacy {
		return query.FlavorLegacyHTTP
	}
	return query.FlavorQueryV2
}

// Client executes one Cypher call per invocation of Tx's returned TxFunc.
// Transaction style and connection reuse are fixed once at construction by
// newClient — a single Client instance only ever runs one axis combination,
// so this stays a two-method interface rather than exposing a method per
// transaction style.
type Client interface {
	Tx(cypher string) runner.TxFunc
	Close(ctx context.Context) error
}

// Run executes cypher against the backend and execution shape selected by
// cfg.Kind, tx, conc and conn, and returns the measured result.
func Run(ctx context.Context, cfg Config, cypher string, tx Transaction, conc Concurrency, conn Connection, streaming Streaming) (runner.Result, error) {
	client, err := newClient(cfg, tx, conn, streaming, effectiveConcurrency(cfg, conc))
	if err != nil {
		return runner.Result{}, err
	}
	// A close failure after a completed run is nothing the caller can act
	// on — the measurement already happened — so it's discarded rather than
	// shadowing a successful Result with an unrelated teardown error.
	defer func() { _ = client.Close(ctx) }()

	runnerCfg := cfg.Config
	runnerCfg.Name = DisplayName(cfg.Kind, tx, conc, conn, streaming)

	fn := client.Tx(cypher)
	if conc == ConcurrencyConcurrent {
		return runner.RunConcurrent(ctx, runnerCfg, fn)
	}
	return runner.Run(ctx, runnerCfg, fn)
}

// DisplayName renders the composite, self-describing test name shown in
// output tables/JSON/benchstat, e.g. "implicit/sequential/fresh". Bolt has
// no connection axis, so its name omits that segment, e.g.
// "implicit/concurrent". The streaming segment is shown only for the one
// combination it's meaningful for — KindQueryV2 + TransactionImplicit —
// since it's forced to StreamingOff everywhere else (see resolveStreaming);
// showing a segment that never varies would just be noise.
func DisplayName(kind Kind, tx Transaction, conc Concurrency, conn Connection, streaming Streaming) string {
	name := string(tx) + "/" + string(conc)
	if kind == KindBolt {
		return name
	}
	name += "/" + string(conn)
	if kind == KindQueryV2 && tx == TransactionImplicit {
		name += "/" + string(streaming)
	}
	return name
}

// newClient picks the concrete Client implementation for cfg.Kind, baking in
// the transaction, connection, and streaming style once instead of selecting
// per call — building every combination eagerly would waste most of the
// work, since a given run only ever needs one.
func newClient(cfg Config, tx Transaction, conn Connection, streaming Streaming, concurrency int) (Client, error) {
	if cfg.Kind == KindBolt {
		return newBoltClient(cfg, concurrency)
	}
	if tx == TransactionManaged {
		return newManagedHTTPClient(cfg, conn, concurrency)
	}
	return newImplicitHTTPClient(cfg, conn, streaming, concurrency)
}

// effectiveConcurrency reports how many goroutines a concurrent benchmark
// will actually run, mirroring runner.RunConcurrent's substitution when
// Workers is unset.
//
// Sizing the pool from the raw cfg.Workers would mean -workers 0 -n 5000 runs
// 5000 concurrent requests through a 100-connection pool, reintroducing the
// exact connection-churn plateau the pool sizing exists to remove.
func effectiveConcurrency(cfg Config, conc Concurrency) int {
	if conc != ConcurrencyConcurrent {
		return 1
	}
	if cfg.Workers <= 0 {
		return cfg.Requests
	}
	return cfg.Workers
}

// --------------------------------------------------------------------------
// Implicit transactions over HTTP (Query API v2 / Legacy)
// --------------------------------------------------------------------------
// The query-go-sdk covers implicit transactions natively via
// client.Query.Execute / query.WithTransformer.

// httpImplicitClient runs auto-commit transactions via the query-go-sdk
// client — the collapsed form of the four former Sync*Implicit /
// Goroutines*Implicit functions.
type httpImplicitClient struct {
	client *query.QueryAPIClient

	// httpClient is non-nil only for the pooled/session variant. It's kept
	// alongside the SDK client because the caller owns it: an injected
	// transport is not closed by the SDK's Close, so without this the
	// pool's idle sockets stay open for idleConnTimeout after the benchmark
	// ends and fd usage climbs across a multi-test invocation.
	httpClient *http.Client

	// streaming selects ExecuteStream (incremental JSON Lines decode) over
	// Execute (buffer the whole response) in Tx. Baked in at construction —
	// see newClient — since a given run only ever needs one.
	streaming bool
}

var _ Client = (*httpImplicitClient)(nil)

func newImplicitHTTPClient(cfg Config, conn Connection, streaming Streaming, concurrency int) (Client, error) {
	if conn == ConnectionFresh {
		client, err := newFreshClient(cfg, streaming)
		if err != nil {
			return nil, err
		}
		return &httpImplicitClient{client: client, streaming: streaming == StreamingOn}, nil
	}

	client, httpClient, err := newSessionClient(cfg, streaming, concurrency)
	if err != nil {
		return nil, err
	}
	return &httpImplicitClient{client: client, httpClient: httpClient, streaming: streaming == StreamingOn}, nil
}

// CAVEAT (KindLegacy): the Legacy Cypher HTTP Transaction API reports Cypher
// failures as HTTP 200 with a populated "errors" array. internal/managed
// checks for that, but this path delegates to query-go-sdk, so it only
// surfaces such failures if the SDK inspects that array itself. Verify
// before trusting a legacy implicit run: point one at deliberately invalid
// Cypher and confirm the failure count is non-zero rather than a clean 0 / N.
func (c *httpImplicitClient) Tx(cypher string) runner.TxFunc {
	if c.streaming {
		return c.txStream(cypher)
	}
	return func(ctx context.Context) error {
		_, err := query.WithTransformer(c.client.Query, ctx, cypher, nil, query.EagerResultTransformer)
		return err
	}
}

// txStream drains ExecuteStream's incremental JSON Lines decode into a
// slice, matching Execute/EagerResultTransformer's fully-materialized shape.
// This is the fairness requirement, not an incidental choice: discarding
// records as they arrive instead of collecting them would measure "does
// less work," not "decodes the wire format differently" — the two
// implementations must do the same amount of work downstream of the wire
// for a streaming-vs-buffered comparison to mean what it claims to.
func (c *httpImplicitClient) txStream(cypher string) runner.TxFunc {
	return func(ctx context.Context) error {
		result, err := c.client.Query.ExecuteStream(ctx, cypher, nil)
		if err != nil {
			return err
		}
		records := make([]*query.Record, 0, 128)
		for rec, err := range result.Records() {
			if err != nil {
				return err
			}
			records = append(records, rec)
		}
		_ = records // kept alive deliberately -- see the fairness note above
		return nil
	}
}

func (c *httpImplicitClient) Close(context.Context) error {
	c.client.Close()
	if c.httpClient != nil {
		c.httpClient.CloseIdleConnections()
	}
	return nil
}

func newFreshClient(cfg Config, streaming Streaming) (*query.QueryAPIClient, error) {
	httpClient := transport.NewFresh(cfg.Timeout)
	return query.NewClient(
		query.WithBasicAuth(cfg.Username, cfg.Password),
		query.WithBaseURL(cfg.URL),
		query.WithDatabase(cfg.Database),
		query.WithTimeout(cfg.Timeout),
		query.WithHTTPClient(httpClient),
		query.WithLogger(cfg.Logger),
		query.WithAPIFlavor(cfg.flavor()),
		query.WithAccessMode(cfg.AccessMode),
		query.WithStreamingSupport(streaming == StreamingOn),
	)
}

// newSessionClient builds a keep-alive client. concurrency is the number of
// goroutines that will share it, and sizes the connection pool — pass 1 for
// the sequential mode and the worker count for the concurrent one.
func newSessionClient(cfg Config, streaming Streaming, concurrency int) (*query.QueryAPIClient, *http.Client, error) {
	httpClient := transport.NewSession(cfg.Timeout, concurrency)
	if cfg.HTTP2 {
		h2Client, err := transport.NewSessionHTTP2(cfg.Timeout, concurrency)
		if err != nil {
			return nil, nil, err
		}
		httpClient = h2Client
	}

	client, err := query.NewClient(
		query.WithBasicAuth(cfg.Username, cfg.Password),
		query.WithBaseURL(cfg.URL),
		query.WithDatabase(cfg.Database),
		query.WithTimeout(cfg.Timeout),
		query.WithHTTPClient(httpClient),
		query.WithLogger(cfg.Logger),
		query.WithAPIFlavor(cfg.flavor()),
		query.WithAccessMode(cfg.AccessMode),
		query.WithStreamingSupport(streaming == StreamingOn),
	)
	if err != nil {
		httpClient.CloseIdleConnections()
		return nil, nil, err
	}
	return client, httpClient, nil
}

// --------------------------------------------------------------------------
// Managed transactions over HTTP (Query API v2 / Legacy)
// --------------------------------------------------------------------------
// These call the raw Query API transaction endpoints (begin -> execute ->
// commit) because the query-go-sdk does not yet expose a managed-transaction
// API — that layer lives in internal/managed.

// httpManagedClient runs explicit begin/execute/commit transactions via
// internal/managed — the collapsed form of the four former Sync /
// Goroutines functions.
type httpManagedClient struct {
	client     *managed.Client
	httpClient *http.Client
}

var _ Client = (*httpManagedClient)(nil)

func newManagedHTTPClient(cfg Config, conn Connection, concurrency int) (Client, error) {
	var httpClient *http.Client
	if conn == ConnectionFresh {
		httpClient = transport.NewFresh(cfg.Timeout)
	} else {
		c, err := sessionHTTPClient(cfg, concurrency)
		if err != nil {
			return nil, err
		}
		httpClient = c
	}

	client := managed.NewClient(httpClient, cfg.URL, cfg.Database, cfg.Username, cfg.Password,
		cfg.Kind == KindLegacy, cfg.AccessMode == query.AccessModeRead)
	return &httpManagedClient{client: client, httpClient: httpClient}, nil
}

func (c *httpManagedClient) Tx(cypher string) runner.TxFunc {
	return func(ctx context.Context) error {
		return c.client.RunTransaction(ctx, cypher)
	}
}

// Release pooled sockets on the way out. Without this, each benchmark in a
// multi-test or -runs N invocation leaves a poolful of idle connections open
// for idleConnTimeout, so fd usage climbs across the process lifetime.
func (c *httpManagedClient) Close(context.Context) error {
	c.httpClient.CloseIdleConnections()
	return nil
}

// sessionHTTPClient returns the appropriate *http.Client for session-based
// managed benchmarks, respecting the HTTP2 flag. concurrency sizes the
// connection pool.
func sessionHTTPClient(cfg Config, concurrency int) (*http.Client, error) {
	if cfg.HTTP2 {
		return transport.NewSessionHTTP2(cfg.Timeout, concurrency)
	}
	return transport.NewSession(cfg.Timeout, concurrency), nil
}
