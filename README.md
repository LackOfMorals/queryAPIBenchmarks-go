# queryAPIBenchmarks-go

A Go port of [queryAPIBenchmarks](https://github.com/LackOfMorals/queryAPIBenchmarks), benchmarking
three ways to talk to Neo4j: the Query API v2 and Legacy HTTP Transaction API via
[query-go-sdk](https://github.com/neo4j-contrib/query-go-sdk), and Bolt via the official
[neo4j-go-driver/v6](https://github.com/neo4j/neo4j-go-driver).

## Requirements

- Go 1.23+
- A running Neo4j instance or Aura database — with its Bolt port (default `7687`) reachable if using `-api bolt`

## Installation

```bash
git clone https://github.com/LackOfMorals/queryAPIBenchmarks-go.git
cd queryAPIBenchmarks-go
go mod tidy
go build -o bench ./cmd/bench
```

## Configuration

Copy `.env.example` to `.env` and edit the values, then:

```bash
set -a && source .env && set +a
```

Or pass flags directly on the command line (flags take precedence over env vars).

| Environment variable | Flag        | Default              | Description                                      |
|----------------------|-------------|----------------------|--------------------------------------------------|
| `NEO4J_HOST`         | `-host`     | `localhost`          | Host only — an IP or FQDN, no scheme or port     |
| `NEO4J_HTTP_SCHEME`  | `-http-scheme` | `http`            | Scheme for `queryv2`/`legacy`: `http` or `https` |
| `NEO4J_HTTP_PORT`    | `-http-port`   | `7474`            | Port for `queryv2`/`legacy`                      |
| `NEO4J_BOLT_SCHEME`  | `-bolt-scheme` | `neo4j`           | Scheme for `-api bolt`: `bolt`, `bolt+s`, `bolt+ssc`, `neo4j`, `neo4j+s`, or `neo4j+ssc` |
| `NEO4J_BOLT_PORT`    | `-bolt-port`   | `7687`            | Port for `-api bolt`                             |
| `NEO4J_USERNAME`     | `-usr`      | `neo4j`              | Username                                         |
| `NEO4J_PASSWORD`     | `-pwd`      | _(empty)_            | Password                                         |
| `NEO4J_DATABASE`     | `-db`       | `neo4j`              | Database name                                    |
| `NEO4J_CYPHER`       | `-cypher`   | `RETURN 1`           | Cypher statement to benchmark                    |
| `NUM_REQUESTS`       | `-n`        | `50`                 | Timed iterations                                 |
| `WARMUP_REQUESTS`    | `-warmup`   | `5`                  | Warmup iterations before timing (0 to skip)      |
| `MAX_WORKERS`        | `-workers`  | `4`                  | Goroutines for concurrent tests                  |
| `NETWORK_TIMEOUT`    | `-timeout`  | `30`                 | Per-request timeout (seconds)                    |
| `NETWORK_HTTP2`      | `-http2`    | `0`                  | Use HTTP/2 for `-connection pooled` (requires `-http-scheme https`; ignored with `-api bolt`) |
| `OUTPUT_FORMAT`      | `-format`   | `table`              | Output format: `table` or `json`                 |
| `NEO4J_API`          | `-api`      | `queryv2`            | Backend to target: `queryv2`, `legacy`, or `bolt` |
| —                    | `-transaction` | _(required, unless `-api bolt`)_ | Transaction style (repeatable): `implicit`, `managed`, or `all` |
| —                    | `-concurrency` | `sequential`      | Concurrency (repeatable): `sequential`, `concurrent`, or `all` |
| —                    | `-connection`  | `fresh`           | HTTP connection reuse (repeatable): `fresh`, `pooled`, or `all` — not applicable to `-api bolt` |

## Usage

```bash
# Single test: implicit transaction, sequential, fresh connection per request
./bench -transaction implicit

# Every combination of concurrency and connection reuse for implicit
# transactions in one run (results table covers all four)
./bench -transaction implicit -concurrency all -connection all

# Same sweep, 100 requests, 10 goroutines for the concurrent cases
./bench -transaction implicit -concurrency all -connection all -n 100 -workers 10

# All managed-transaction tests (explicit begin -> execute -> commit)
./bench -transaction managed -concurrency all -connection all -n 100

# Skip warmup
./bench -transaction implicit -warmup 0

# JSON output (stdout is clean; progress goes to stderr)
./bench -transaction implicit -concurrency all -n 100 -format json
./bench -transaction implicit -n 100 -format json 2>/dev/null | python3 -m json.tool

# Target the legacy Cypher HTTP Transaction API (/db/{db}/tx/commit)
./bench -transaction implicit -api legacy -n 100

# Target the official Bolt driver (v6) — no -transaction/-connection needed,
# Bolt only runs the auto-commit implicit style over one pooled Driver.
# -bolt-scheme/-bolt-port default to neo4j/7687, so -host is often all you need.
./bench -api bolt -host db01.example.internal -concurrency all -n 100

# Same, but explicit about scheme and port (e.g. a single instance behind TLS)
./bench -api bolt -host db01.example.internal -bolt-scheme bolt+s -bolt-port 7687 -n 100

# -host has no scheme or port, so the same value works unchanged across
# backends — only -http-scheme/-http-port or -bolt-scheme/-bolt-port differ
./bench -api queryv2 -host db01.example.internal -http-scheme https -http-port 7473 -n 100
./bench -api bolt    -host db01.example.internal -n 100

# Side-by-side backend comparison
./bench -transaction implicit -connection all -n 100 -api queryv2 -format json > v2.json
./bench -transaction implicit -connection all -n 100 -api legacy  -format json > legacy.json
./bench -concurrency all -n 100 -api bolt -host db01.example.internal -format json > bolt.json
```

## Output

### Table format (default)

```
--------------------------------------------------------------------
Test                              Time (s)   Req/s     Failures
  min       p50       p95       p99       max     stddev
--------------------------------------------------------------------
implicit/sequential/fresh          1.234      81        0 / 50
  10.20ms   12.10ms   15.30ms   18.20ms   22.40ms 1.80ms
--------------------------------------------------------------------
```

Each test's name is the composite of the axes it ran with —
`<transaction>/<concurrency>/<connection>` (HTTP) or
`<transaction>/<concurrency>` (Bolt, which has no connection axis) — so the
table is self-describing without a lookup table.

### JSON format (`-format json`)

```json
[
  {
    "name": "implicit/sequential/fresh",
    "total_seconds": 1.234,
    "request_count": 50,
    "failure_count": 0,
    "requests_per_second": 81.0,
    "unreliable": false,
    "latency_ms": {
      "min": 10.2,
      "mean": 12.3,
      "p50": 11.8,
      "p95": 15.3,
      "p99": 18.2,
      "max": 22.4,
      "stddev": 1.8
    }
  }
]
```

Progress output always goes to stderr, so stdout can be cleanly piped when using `-format json`.

The JSON envelope includes an `api_flavor` key so output files are self-describing:

```json
{
  "api_flavor": "Neo4j Query API v2 (/db/{db}/query/v2)",
  "results": [ ... ]
}
```

## API flavors

Use `-api` to select which Neo4j access backend the benchmarks target:

| Value | Transport | Scheme + port come from |
|---|---|---|
| `queryv2` _(default)_ | HTTP `/db/{db}/query/v2` | `-http-scheme`/`-http-port` |
| `legacy` | HTTP `/db/{db}/tx/commit` (implicit) / `/db/{db}/tx` (managed) | `-http-scheme`/`-http-port` |
| `bolt` | Bolt, via the official [neo4j-go-driver/v6](https://github.com/neo4j/neo4j-go-driver) | `-bolt-scheme`/`-bolt-port` |

`-host` is scheme/port-free (an IP or FQDN only) and used unchanged for every value above; `buildURL` in `cmd/bench/main.go` combines it with whichever scheme/port apply to `-api`. The selected backend is printed to stderr before any test runs.

> **Note:** `legacy` requires Neo4j 4.x or later with the HTTP transaction API enabled. Aura instances use `queryv2`.

## Available tests

Every test is described by up to three orthogonal flags, and a run sweeps
the cartesian product of whatever values you pass:

| Flag | Values | Meaning |
|---|---|---|
| `-transaction` | `implicit`, `managed` | `implicit`: one HTTP call per iteration, the API manages the transaction. `managed`: three HTTP calls per iteration — begin → execute → commit. |
| `-concurrency` | `sequential`, `concurrent` | Sequential loop vs. a goroutine pool (`-workers`). |
| `-connection` | `fresh`, `pooled` | `fresh`: new TCP connection per request. `pooled`: keep-alive connection reuse (+ optional `-http2`). |

For example, `-transaction implicit -concurrency concurrent -connection pooled`
is what used to be called `GoroutinesSessionsImplicit`; the same run now
prints as `implicit/concurrent/pooled`. Passing `all` for any flag (or the
flag more than once) runs every value on that axis in the same invocation.

### Bolt (`-api bolt`) is narrower by design

Per the [Go Driver Manual's performance recommendations](https://neo4j.com/docs/go-manual/current/performance/),
a `Session`/explicit transaction only pays off when batching several
queries into one transaction or lazily streaming a huge result — neither
applies to a benchmark that runs exactly one eagerly-loaded statement per
iteration. So Bolt:

- **only runs `-transaction implicit`**, via `neo4j.ExecuteQuery` against one
  shared, pooled `Driver` — passing `-transaction managed` errors out.
- **has no `-connection` axis** — Bolt always pools via its `Driver`, so
  passing `-connection fresh` errors out (there is no "fresh" mode to
  benchmark).
- **ignores `-http2`** (the driver handles its own multiplexing) — passing it
  prints a warning rather than an error, since it's harmless.

`-concurrency` still applies normally: `./bench -api bolt -concurrency all`
runs both `implicit/sequential` and `implicit/concurrent`.

### Comparing legacy/queryv2/bolt fairly

Two things affect a close three-way comparison that aren't bugs, just
properties of how each backend actually works — worth knowing before reading
too much into a small delta:

- **`-bolt-scheme neo4j` (the default) bypasses the load balancer** for
  everything after the initial connection: the driver fetches a routing
  table and talks directly to cluster members from then on, while
  `queryv2`/`legacy` go through the LB on every single call. Some of any
  Bolt-vs-HTTP gap is that extra hop and proxy processing, not the wire
  protocol. To measure Bolt through the LB the same way HTTP is measured,
  use `-bolt-scheme bolt` (no client-side routing, one fixed address) instead
  — comparing both scheme's results decomposes "LB overhead" from "protocol
  difference."
- **`-transaction implicit` on `-api queryv2` cannot currently read-route.**
  That path delegates to [query-go-sdk](https://github.com/neo4j-contrib/query-go-sdk),
  which sends Query API v2's access-mode hint as an `accessMode` HTTP
  header; the server only reads it from the JSON body
  (https://neo4j.com/docs/query-api/current/routing/), so the header is
  silently ignored and reads may all land on the leader regardless of
  `-mode`. `-transaction managed` on `queryv2` doesn't have this problem —
  `internal/managed` sends it correctly in the body — so prefer `managed`
  over `implicit` for any comparison where read-routing across the 3 nodes
  matters. (Bolt's `ExecuteQueryWithReadersRouting`/`WritersRouting` is a
  protocol-level driver feature and is unaffected either way.)

## Project layout

```
queryAPIBenchmarks-go/
├── cmd/bench/main.go           # CLI entry point: flags, axis expansion, dispatch
├── benchmarks/
│   ├── benchmarks.go           # Client interface + HTTP (Query API v2 / Legacy) implementations
│   └── bolt.go                 # Bolt (neo4j-go-driver/v6) Client implementation
├── internal/
│   ├── managed/
│   │   └── managed.go          # Raw HTTP managed-transaction client (begin/execute/commit)
│   ├── runner/
│   │   ├── result.go           # Result type with latency stats (min/p50/p95/p99/max/stddev)
│   │   └── runner.go           # Warmup + timed loop, sequential and concurrent
│   ├── transport/
│   │   └── transport.go        # http.Client factories (fresh / session / HTTP2)
│   └── results/
│       └── table.go            # Table and JSON output
├── .env.example
├── Makefile
└── go.mod
```

## Notes on Python parity

| Python concept | Go equivalent |
|---|---|
| `TXrequest` (new conn per call) | `transport.NewFresh` |
| `TXsession` (reuse conn) | `transport.NewSession` / `transport.NewSessionHTTP2` |
| `ThreadPoolExecutor` | goroutine worker pool in `runner.RunConcurrent` |
| `tqdm` progress bar | inline `\r` progress line (stderr) |
| `generate_table` | `results.PrintTable` / `results.PrintJSON` |
| `is_unreliable` (>10% failures) | `Result.IsUnreliable()` |

HTTP/2 requires `-http-scheme https` — point `-host` at an Aura endpoint or a
locally TLS-terminated Neo4j instance when using `-http2`.
