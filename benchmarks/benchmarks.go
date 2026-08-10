// Package benchmarks contains the eight benchmark modes that mirror the
// Python queryAPIBenchmarks test suite.
//
// Naming convention follows the Python classes exactly so results tables
// from both tools can be compared directly:
//
//	Implicit transactions  (single HTTP call, SDK manages the transaction)
//	  - SyncImplicit            sequential, fresh connection per request
//	  - SyncSessionsImplicit    sequential, persistent connection
//	  - GoroutinesImplicit      concurrent, fresh connection per request
//	  - GoroutinesSessionsImplicit  concurrent, persistent connection
//
//	Managed transactions  (begin → execute → commit, 3 HTTP calls per cycle)
//	  - Sync                    sequential, fresh connection per request
//	  - SyncSessions            sequential, persistent connection
//	  - Goroutines              concurrent, fresh connection per request
//	  - GoroutinesSessions      concurrent, persistent connection
//
// The Go SDK (query-go-sdk) covers implicit transactions natively via
// client.Query.Execute / query.WithTransformer.  Managed transactions require
// direct HTTP calls to /db/{db}/query/v2/tx because the SDK does not yet
// expose a begin/commit API — that layer lives in internal/managed.
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

// Config holds everything needed to construct a client and run a benchmark.
type Config struct {
	URL      string
	Username string
	Password string
	Database string
	Timeout  time.Duration
	Logger   *slog.Logger

	// HTTP2 enables HTTP/2 for session-based transports.
	HTTP2 bool

	// Flavor selects which Neo4j HTTP API to target.
	Flavor query.APIFlavor

	// AccessMode controls the access mode header sent with every request.
	AccessMode query.AccessMode

	runner.Config
}

// --------------------------------------------------------------------------
// Helpers
// --------------------------------------------------------------------------

// CAVEAT (-api legacy + implicit benchmarks): the Legacy Cypher HTTP
// Transaction API reports Cypher failures as HTTP 200 with a populated
// "errors" array. internal/managed checks for that, but the implicit
// benchmarks below delegate to query-go-sdk, so they only surface such
// failures if the SDK inspects that array itself. Verify before trusting a
// legacy implicit run: point one at deliberately invalid Cypher and confirm
// the failure count is non-zero rather than a clean 0 / N.
func implicitFn(client *query.QueryAPIClient, cypher string) runner.TxFunc {
	return func(ctx context.Context) error {
		_, err := query.WithTransformer(
			client.Query,
			ctx,
			cypher,
			nil,
			query.EagerResultTransformer,
		)
		return err
	}

}

func newFreshClient(cfg Config) (*query.QueryAPIClient, error) {
	httpClient := transport.NewFresh(cfg.Timeout)
	return query.NewClient(
		query.WithBasicAuth(cfg.Username, cfg.Password),
		query.WithBaseURL(cfg.URL),
		query.WithDatabase(cfg.Database),
		query.WithTimeout(cfg.Timeout),
		query.WithHTTPClient(httpClient),
		query.WithLogger(cfg.Logger),
		query.WithAPIFlavor(cfg.Flavor),
		query.WithAccessMode(cfg.AccessMode),
	)
}

// newSessionClient builds a keep-alive client. concurrency is the number of
// goroutines that will share it, and sizes the connection pool — pass 1 for the
// sequential modes and the worker count for the concurrent ones.
//
// The *http.Client is returned alongside the SDK client because the caller owns
// it: an injected transport is not closed by the SDK's Close, so without this
// the pool's idle sockets stay open for idleConnTimeout after each benchmark
// and fd usage climbs across a multi-test or -runs N invocation.
func newSessionClient(cfg Config, concurrency int) (*query.QueryAPIClient, *http.Client, error) {
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
		query.WithAPIFlavor(cfg.Flavor),
		query.WithAccessMode(cfg.AccessMode),
	)
	if err != nil {
		httpClient.CloseIdleConnections()
		return nil, nil, err
	}
	return client, httpClient, nil
}

// --------------------------------------------------------------------------
// Implicit transaction benchmarks
// --------------------------------------------------------------------------

// SyncImplicit mirrors Python BenchmarkSyncImplicit.
// Sequential, fresh TCP connection per request.
func SyncImplicit(ctx context.Context, cfg Config, cypher string) (runner.Result, error) {
	client, err := newFreshClient(cfg)
	if err != nil {
		return runner.Result{}, err
	}
	defer client.Close()

	cfg.Config.Name = "SyncImplicit"
	return runner.Run(ctx, cfg.Config, implicitFn(client, cypher))
}

// SyncSessionsImplicit mirrors Python BenchmarkSyncSessionsImplicit.
// Sequential, persistent connection (+ optional HTTP/2).
func SyncSessionsImplicit(ctx context.Context, cfg Config, cypher string) (runner.Result, error) {
	client, httpClient, err := newSessionClient(cfg, 1)
	if err != nil {
		return runner.Result{}, err
	}
	defer client.Close()
	defer httpClient.CloseIdleConnections()

	cfg.Config.Name = "SyncSessionsImplicit"
	return runner.Run(ctx, cfg.Config, implicitFn(client, cypher))
}

// GoroutinesImplicit mirrors Python BenchmarkThreadsImplicit.
// Concurrent goroutines, fresh connection per request.
func GoroutinesImplicit(ctx context.Context, cfg Config, cypher string) (runner.Result, error) {
	client, err := newFreshClient(cfg)
	if err != nil {
		return runner.Result{}, err
	}
	defer client.Close()

	cfg.Config.Name = "GoroutinesImplicit"
	return runner.RunConcurrent(ctx, cfg.Config, implicitFn(client, cypher))
}

// GoroutinesSessionsImplicit mirrors Python BenchmarkThreadsSessionsImplicit.
// Concurrent goroutines, persistent connection (+ optional HTTP/2).
func GoroutinesSessionsImplicit(ctx context.Context, cfg Config, cypher string) (runner.Result, error) {
	client, httpClient, err := newSessionClient(cfg, effectiveConcurrency(cfg))
	if err != nil {
		return runner.Result{}, err
	}
	defer client.Close()
	defer httpClient.CloseIdleConnections()

	cfg.Config.Name = "GoroutinesSessionsImplicit"
	return runner.RunConcurrent(ctx, cfg.Config, implicitFn(client, cypher))
}

// --------------------------------------------------------------------------
// Managed transaction benchmarks
// --------------------------------------------------------------------------
// These call the raw Query API transaction endpoints (begin → execute → commit)
// because the query-go-sdk does not yet expose a managed-transaction API.

func managedFn(client *managed.Client, cypher string) runner.TxFunc {
	return func(ctx context.Context) error {
		return client.RunTransaction(ctx, cypher)
	}
}

// Sync mirrors Python BenchmarkSync.
// Sequential managed transactions, fresh connection per request.
func Sync(ctx context.Context, cfg Config, cypher string) (runner.Result, error) {
	client := managed.NewClient(
		transport.NewFresh(cfg.Timeout),
		cfg.URL, cfg.Database, cfg.Username, cfg.Password,
		cfg.Flavor == query.FlavorLegacyHTTP,
		cfg.AccessMode == query.AccessModeRead,
	)
	cfg.Config.Name = "Sync"
	return runner.Run(ctx, cfg.Config, managedFn(client, cypher))
}

// SyncSessions mirrors Python BenchmarkSyncSessions.
// Sequential managed transactions, persistent connection (+ optional HTTP/2).
func SyncSessions(ctx context.Context, cfg Config, cypher string) (runner.Result, error) {
	httpClient, err := sessionHTTPClient(cfg, 1)
	if err != nil {
		return runner.Result{}, err
	}
	// Release pooled sockets on the way out. Without this, each benchmark in a
	// multi-test or -runs N invocation leaves a poolful of idle connections open
	// for idleConnTimeout, so fd usage climbs across the process lifetime.
	defer httpClient.CloseIdleConnections()

	client := managed.NewClient(httpClient, cfg.URL, cfg.Database, cfg.Username, cfg.Password,
		cfg.Flavor == query.FlavorLegacyHTTP, cfg.AccessMode == query.AccessModeRead)
	cfg.Config.Name = "SyncSessions"
	return runner.Run(ctx, cfg.Config, managedFn(client, cypher))
}

// Goroutines mirrors Python BenchmarkThreads.
// Concurrent managed transactions, fresh connection per request.
func Goroutines(ctx context.Context, cfg Config, cypher string) (runner.Result, error) {
	client := managed.NewClient(
		transport.NewFresh(cfg.Timeout),
		cfg.URL, cfg.Database, cfg.Username, cfg.Password,
		cfg.Flavor == query.FlavorLegacyHTTP,
		cfg.AccessMode == query.AccessModeRead,
	)
	cfg.Config.Name = "Goroutines"
	return runner.RunConcurrent(ctx, cfg.Config, managedFn(client, cypher))
}

// GoroutinesSessions mirrors Python BenchmarkThreadsSessions.
// Concurrent managed transactions, persistent connection (+ optional HTTP/2).
func GoroutinesSessions(ctx context.Context, cfg Config, cypher string) (runner.Result, error) {
	httpClient, err := sessionHTTPClient(cfg, effectiveConcurrency(cfg))
	if err != nil {
		return runner.Result{}, err
	}
	defer httpClient.CloseIdleConnections()

	client := managed.NewClient(httpClient, cfg.URL, cfg.Database, cfg.Username, cfg.Password,
		cfg.Flavor == query.FlavorLegacyHTTP, cfg.AccessMode == query.AccessModeRead)
	cfg.Config.Name = "GoroutinesSessions"
	return runner.RunConcurrent(ctx, cfg.Config, managedFn(client, cypher))
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

// effectiveConcurrency reports how many goroutines a concurrent benchmark will
// actually run, mirroring runner.RunConcurrent's substitution when Workers is
// unset.
//
// Sizing the pool from the raw cfg.Workers would mean -workers 0 -n 5000 runs
// 5000 concurrent requests through a 100-connection pool, reintroducing the
// exact connection-churn plateau the pool sizing exists to remove.
func effectiveConcurrency(cfg Config) int {
	if cfg.Workers <= 0 {
		return cfg.Requests
	}
	return cfg.Workers
}
