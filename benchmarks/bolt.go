package benchmarks

import (
	"context"

	query "github.com/neo4j-contrib/query-go-sdk"
	"github.com/neo4j/neo4j-go-driver/v6/neo4j"

	"github.com/LackOfMorals/queryAPIBenchmarks-go/internal/runner"
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

func newBoltClient(cfg Config) (Client, error) {
	driver, err := neo4j.NewDriver(cfg.URL, neo4j.BasicAuth(cfg.Username, cfg.Password, ""))
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
