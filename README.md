# queryAPIBenchmarks-go

A Go port of [queryAPIBenchmarks](https://github.com/LackOfMorals/queryAPIBenchmarks), using the
[query-go-sdk](https://github.com/neo4j-contrib/query-go-sdk) to benchmark the Neo4j Query API.

## Requirements

- Go 1.23+
- A running Neo4j instance or Aura database

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
| `NEO4J_URL`          | `-url`      | `http://localhost:7474` | Query API base URL                            |
| `NEO4J_USERNAME`     | `-usr`      | `neo4j`              | Username                                         |
| `NEO4J_PASSWORD`     | `-pwd`      | _(empty)_            | Password                                         |
| `NEO4J_DATABASE`     | `-db`       | `neo4j`              | Database name                                    |
| `NEO4J_CYPHER`       | `-cypher`   | `RETURN 1`           | Cypher statement to benchmark                    |
| `NUM_REQUESTS`       | `-n`        | `50`                 | Timed iterations                                 |
| `WARMUP_REQUESTS`    | `-warmup`   | `5`                  | Warmup iterations before timing (0 to skip)      |
| `MAX_WORKERS`        | `-workers`  | `4`                  | Goroutines for concurrent tests                  |
| `NETWORK_TIMEOUT`    | `-timeout`  | `30`                 | Per-request timeout (seconds)                    |
| `NETWORK_HTTP2`      | `-http2`    | `0`                  | Use HTTP/2 for session transports (requires https) |
| `OUTPUT_FORMAT`      | `-format`   | `table`              | Output format: `table` or `json`                 |

## Usage

```bash
# Single test
./bench -t SyncImplicit

# Multiple tests in one run (results table covers all)
./bench -t SyncImplicit -t SyncSessionsImplicit -t GoroutinesImplicit -t GoroutinesSessionsImplicit

# All implicit tests with 100 requests, 10 goroutines
./bench -t SyncImplicit -t SyncSessionsImplicit \
        -t GoroutinesImplicit -t GoroutinesSessionsImplicit \
        -n 100 -workers 10

# All managed transaction tests
./bench -t Sync -t SyncSessions -t Goroutines -t GoroutinesSessions -n 100

# Skip warmup
./bench -t SyncImplicit -warmup 0

# JSON output (stdout is clean; progress goes to stderr)
./bench -t SyncImplicit -t GoroutinesSessionsImplicit -n 100 -format json
./bench -t SyncImplicit -n 100 -format json 2>/dev/null | python3 -m json.tool
```

## Output

### Table format (default)

```
--------------------------------------------------------------------
Test                              Time (s)   Req/s     Failures
  min       p50       p95       p99       max     stddev
--------------------------------------------------------------------
SyncImplicit                      1.234      81        0 / 50
  10.20ms   12.10ms   15.30ms   18.20ms   22.40ms 1.80ms
--------------------------------------------------------------------
```

### JSON format (`-format json`)

```json
[
  {
    "name": "SyncImplicit",
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

## Available tests

### Implicit transactions
A single HTTP call per iteration; the Query API manages the transaction.

| Test name                    | Transport       | Concurrency  |
|------------------------------|-----------------|--------------|
| `SyncImplicit`               | Fresh conn      | Sequential   |
| `SyncSessionsImplicit`       | Persistent conn | Sequential   |
| `GoroutinesImplicit`         | Fresh conn      | Concurrent   |
| `GoroutinesSessionsImplicit` | Persistent conn | Concurrent   |

### Managed transactions
Three HTTP calls per iteration: begin → execute → commit.

| Test name           | Transport       | Concurrency  |
|---------------------|-----------------|--------------|
| `Sync`              | Fresh conn      | Sequential   |
| `SyncSessions`      | Persistent conn | Sequential   |
| `Goroutines`        | Fresh conn      | Concurrent   |
| `GoroutinesSessions`| Persistent conn | Concurrent   |

## Project layout

```
queryAPIBenchmarks-go/
├── cmd/bench/main.go           # CLI entry point
├── benchmarks/benchmarks.go    # All eight benchmark implementations
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

HTTP/2 requires `https://` — point `-url` at an Aura endpoint or a
locally TLS-terminated Neo4j instance when using `-http2`.
