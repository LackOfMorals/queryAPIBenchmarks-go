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
| `NUM_REQUESTS`       | `-n`        | `5`                  | Timed iterations                                 |
| `WARMUP_REQUESTS`    | `-warmup`   | `3`                  | Warmup iterations before timing (0 to skip)      |
| `MAX_WORKERS`        | `-workers`  | `4`                  | Goroutines for concurrent tests                  |
| `NETWORK_TIMEOUT`    | `-timeout`  | `30`                 | Per-request timeout (seconds)                    |
| `NETWORK_HTTP2`      | `-http2`    | `0`                  | Use HTTP/2 for session transports (requires https) |

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

# Skip warmup
./bench -t SyncImplicit -warmup 0
```

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
*(Implementation in progress — currently returns zero results.)*

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
│   ├── runner/
│   │   ├── result.go           # Result type (mirrors Python BenchmarkResult)
│   │   └── runner.go           # Warmup + timed loop, sequential and concurrent
│   ├── transport/
│   │   └── transport.go        # http.Client factories (fresh / session / HTTP2)
│   └── results/
│       └── table.go            # ASCII results table (mirrors Python generate_table)
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
| `tqdm` progress bar | inline `\r` progress line |
| `generate_table` | `results.PrintTable` |
| `is_unreliable` (>10% failures) | `Result.IsUnreliable()` |

HTTP/2 requires `https://` — point `-url` at an Aura endpoint or a
locally TLS-terminated Neo4j instance when using `-http2`.
