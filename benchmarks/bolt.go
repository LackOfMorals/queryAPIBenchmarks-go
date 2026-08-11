package benchmarks

import (
	"context"

	query "github.com/neo4j-contrib/query-go-sdk"
	"github.com/neo4j/neo4j-go-driver/v6/neo4j"
	"github.com/neo4j/neo4j-go-driver/v6/neo4j/config"

	"github.com/LackOfMorals/queryAPIBenchmarks-go/internal/runner"
	"github.com/LackOfMorals/queryAPIBenchmarks-go/internal/transport"
)

// boltClient runs every iteration as an auto-commit call via
// neo4j.ExecuteQuery against a single shared, pooled Driver.
//
// This is the only execution style Bolt benchmarks: per the Go Driver
// Manual's performance recommendations
// (https://neo4j.com/docs/go-manual/current/performance/), Sessions and
// explicit transactions exist to batch multiple queries into one
// transaction or to lazily stream huge result sets. Neither applies here —
// every benchmark iteration is exactly one statement and already wants the
// result eagerly (matching EagerResultTransformer's use on the HTTP side).
// ExecuteQuery already runs each call as its own explicit, retryable,
// driver-managed transaction, so standing up a Session-based "managed"
// variant just to mirror the HTTP transaction axis would add a second path
// with no behavioural difference worth measuring.
//
// Calling ExecuteQuery directly against the shared Driver (never through a
// Session) also means concurrency needs no extra care: Driver is documented
// safe for concurrent use, so every goroutine can share the one boltClient.
type boltClient struct {
	driver  neo4j.Driver
	dbName  string
	routing neo4j.ExecuteQueryConfigurationOption
}

var _ Client = (*boltClient)(nil)

func newBoltClient(cfg Config, concurrency int) (Client, error) {
	driver, err := neo4j.NewDriver(cfg.URL, neo4j.BasicAuth(cfg.Username, cfg.Password, ""), boltDriverConfig(concurrency))
	if err != nil {
		return nil, err
	}

	routing := neo4j.ExecuteQueryWithWritersRouting()
	if cfg.AccessMode == query.AccessModeRead {
		routing = neo4j.ExecuteQueryWithReadersRouting()
	}

	return &boltClient{driver: driver, dbName: cfg.Database, routing: routing}, nil
}

func (c *boltClient) Tx(cypher string) runner.TxFunc {
	return func(ctx context.Context) error {
		_, err := neo4j.ExecuteQuery[*neo4j.EagerResult](ctx, c.driver, cypher, nil,
			neo4j.EagerResultTransformer,
			neo4j.ExecuteQueryWithDatabase(c.dbName),
			c.routing,
		)
		return err
	}
}

func (c *boltClient) Close(ctx context.Context) error {
	return c.driver.Close(ctx)
}

// boltDriverConfig aligns two driver defaults with how the HTTP paths behave,
// so a close legacy/queryv2/bolt comparison isn't skewed by client-side
// configuration differences that have nothing to do with the wire protocol.
// Factored out (rather than an inline closure in newBoltClient) so both
// settings are independently unit-testable without constructing a Driver.
func boltDriverConfig(concurrency int) func(*config.Config) {
	return func(c *config.Config) {
		// Match the HTTP paths' pool sizing (transport.PoolSize) instead of
		// the driver default of a flat 100: without this, a saturation
		// sweep above ~50 concurrent workers hits Bolt's pool limit and
		// queues (up to ConnectionAcquisitionTimeout, 1 minute by default)
		// at a different concurrency than HTTP's pool does, making a
		// client-side pool-sizing mismatch look like a protocol-level
		// throughput difference.
		c.MaxConnectionPoolSize = transport.PoolSize(concurrency)

		// ExecuteQuery retries retryable failures (leader switch, deadlock,
		// transient unavailability) by default for up to 30s (config
		// default). query-go-sdk retries only bare network errors up to 3x,
		// and internal/managed never retries a Cypher failure — so leaving
		// Bolt's default in place would silently fold retry time into a
		// "successful" call's latency and lower its measured failure count,
		// biasing exactly the kind of close 3-way comparison this tool
		// exists to make. Disabling it (any non-negative value works; the
		// retry loop only checks elapsed time after a failure, and a real
		// round trip always elapses > 0) keeps Bolt's failure/latency
		// accounting on the same footing as the other two backends.
		c.MaxTransactionRetryTime = 0
	}
}
