# Benchmark running order

How to run the full `queries.toml` suite against the UK Companies House graph
so results are comparable across backends and safe to draw conclusions from.
This assumes the 3-node cluster + HAProxy setup in `docs/awsSetup/`.

## 1. Smoke test every query, every backend (untimed)

Before spending time on a real run, confirm all 10 queries succeed against
the current dataset on all three backends — a stale ID (a dissolved company,
a re-numbered address) fails silently into "0 rows" rather than an error, and
is easy to miss in a timed run's summary table.

```bash
set -a && source .env && set +a

./bench -api queryv2 -queries-file queries.toml -transaction managed -n 1 -warmup 0 -format json | grep -c '"failure_count": 0'
./bench -api legacy  -queries-file queries.toml -transaction managed -n 1 -warmup 0 -format json | grep -c '"failure_count": 0'
./bench -api bolt    -queries-file queries.toml -n 1 -warmup 0 -format json | grep -c '"failure_count": 0'
```

Each should print `10`. If not, check the offending label's Cypher directly
against the graph via `read-cypher` before continuing — don't try to
diagnose it from benchmark output.

## 2. Warm the caches (discarded)

```bash
./bench -transaction implicit    -cypher "MATCH (c:Company {companyNumber: '02026964'}) RETURN c.name"           -n 50 -warmup 0 -workers 4
./bench -transaction implicit    -cypher "MATCH (c:Company)-[:HAS_SIC_CODE]->(s:SICCode) RETURN s.code, count(c) AS n ORDER BY n DESC LIMIT 20"          -n 20 -warmup 0 -workers 4
```

primes Neo4j's query plan cache and page cache. Do this
once per backend switch, not once per query — the plan cache is shared
across all Cypher text, and cold-cache latency on the *first* query of a
block will otherwise leak into that query's numbers.



## 3. Run each backend as its own block, not interleaved

Run the entire 10-query suite for one backend/axis combination to
completion before switching to the next. Interleaving backends
query-by-query reintroduces shared-cluster load variance (another backend's
in-flight requests, GC pauses, page cache churn) as a confound between the
numbers you're trying to compare — a block ordering isolates "backend
changed" as the only thing that changed between two runs.

Within a block, run the queries in ascending cost order — cheapest first.
This is already the order in `queries.toml` and mirrors `make bench-all`:

1. `point-lookup` — index seek, 1 row (pure overhead baseline)
2. `filtered-scan` — indexed filter, 100 rows
3. `one-hop` — inbound traversal, ~7 rows
4. `two-hop` — 2-hop expansion + dedup, ≤50 rows
5. `psc-lookup` — inbound traversal + relationship-property projection, 8 rows
6. `aggregation` — full 7.5M-relationship scan, 20 rows
7. `fulltext-search` — Lucene index path, ≤20 rows
8. `supernode-fanout` — bounded traversal off a 66.7k-degree hub, 100 rows
9. `count-scalar` — same predicate as `bulk-rows`, 1 scalar row
10. `bulk-rows` — 1000-row scan

Ascending order means a regression or timeout shows up on a cheap query
first rather than after a heavy `aggregation` run has already burned several
minutes. It also keeps `count-scalar` and `bulk-rows` — the pair meant to be
read together, since they share a predicate and differ only in payload size
— close together in the same cache-warm window rather than at opposite ends
of a long run.

`make bench-all` already runs one query through every
`-transaction`/`-concurrency`/`-connection` combination (`implicit` only,
per query, sequential+concurrent × fresh+pooled) in one invocation, so a
single block = one `make bench-all`-equivalent pass for that backend.

## 4. Backend order, and the two comparisons that need special handling

Run backends in this order:

1. **`queryv2`, `-transaction managed`** — the fair baseline for read-routing
   comparisons. `internal/managed` sends the access-mode hint correctly in
   the request body.
2. **`queryv2`, `-transaction implicit`** — HTTP overhead baseline, but
   **do not** use this to draw conclusions about read-routing: the
   query-go-sdk sends access mode as an HTTP header, which the server
   ignores in favor of the JSON body, so implicit reads may all land on the
   leader regardless of `-mode`.
3. **`legacy`** — both transaction styles; same caveat as `queryv2` for
   `implicit` does not apply here (legacy doesn't claim read-routing parity
   either way, so there's nothing extra to control for).
4. **`bolt`, `-bolt-scheme neo4j`** (default) — driver-side routing, bypasses
   HAProxy after the initial connection.
5. **`bolt`, `-bolt-scheme bolt`** — no client-side routing, one fixed
   address, goes through HAProxy like HTTP does. Run this as a *second*,
   separate bolt block specifically to compare against step 4: the delta
   between them is roughly "LB + proxy hop" overhead, decomposed from the
   Bolt-vs-HTTP protocol difference measured in steps 1–3.

Five blocks total. Don't skip step 5 if the headline comparison is
"Bolt vs HTTP" — without it, some of that gap is routing topology, not wire
protocol, and step 4 alone will overstate Bolt's advantage.

## 5. Repeat for statistical confidence

Once the single-pass numbers look sane, repeat the whole 5-block sequence
with `-runs N -format benchstat` (`N` ≥ 10 recommended) instead of a single
run, so `benchstat` has enough samples per backend/query pair to report
confidence intervals rather than point estimates.

## Quick reference

```bash
set -a && source .env && set +a
make bench-warmup

./bench -api queryv2 -transaction managed  -queries-file queries.toml -concurrency all -connection all -n 100 -runs 10 -format benchstat > ./results/queryv2-managed.txt
./bench -api queryv2 -transaction implicit -queries-file queries.toml -concurrency all -connection all -n 100 -runs 10 -format benchstat > ./results/queryv2-implicit.txt
./bench -api legacy  -queries-file queries.toml -concurrency all -connection all -n 100 -runs 10 -format benchstat > ./results/legacy.txt
./bench -api bolt -bolt-scheme neo4j -queries-file queries.toml -concurrency all -n 100 -runs 10 -format benchstat > ./results/bolt-neo4j.txt
./bench -api bolt -bolt-scheme bolt  -queries-file queries.toml -concurrency all -n 100 -runs 10 -format benchstat > ./results/bolt-bolt.txt
```
