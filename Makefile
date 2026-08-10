.PHONY: build run test lint tidy

build:
	go build -o bench ./cmd/bench

run:
	go run ./cmd/bench -transaction implicit -connection all \
		-host $(NEO4J_HOST) -usr $(NEO4J_USERNAME) -pwd $(NEO4J_PASSWORD) \
		-db $(NEO4J_DATABASE) -n 10

test:
	go test -race ./...

lint:
	golangci-lint run ./...

tidy:
	go mod tidy

# =============================================================================
# Benchmark suite — Neo4j Query API vs UK Companies House graph
# Add these targets to the existing Makefile in queryAPIBenchmarks-go
#
# Usage:
#   make bench-all          # run every suite in sequence
#   make bench-<name>       # run one suite
#
# Prerequisites:
#   NEO4J_HOST, NEO4J_USERNAME, NEO4J_PASSWORD, NEO4J_DATABASE must be set
#   (via .env + `set -a && source .env && set +a`, or exported in shell)
#
# Recommended run order for a clean comparison:
#   1. bench-warmup          (discard — just primes JVM plan cache + page cache)
#   2. bench-point-lookup
#   3. bench-filtered-scan
#   4. bench-one-hop
#   5. bench-two-hop
#   6. bench-aggregation
#   7. bench-bulk-rows
# =============================================================================

# ---------------------------------------------------------------------------
# Shared defaults — override on the command line if needed
#   make bench-point-lookup N=200 WORKERS=10
# ---------------------------------------------------------------------------
N       ?= 100
WARMUP  ?= 10
WORKERS ?= 8

# All four implicit-transaction test types.
# Covers: fresh-conn-sequential, persistent-conn-sequential,
#         fresh-conn-concurrent, persistent-conn-concurrent.
ALL_IMPLICIT := -transaction implicit -concurrency all -connection all

# ---------------------------------------------------------------------------
# 1. POINT LOOKUP
#    Index-backed single-node retrieval on companyNumber.
#    Baseline: measures pure request/response overhead with minimal server work.
#    Expected result set: 1 row, 3 scalar properties.
# ---------------------------------------------------------------------------
bench-point-lookup: build
	./bench $(ALL_IMPLICIT) \
	  -host $(NEO4J_HOST) -usr $(NEO4J_USERNAME) -pwd $(NEO4J_PASSWORD) \
	  -db $(NEO4J_DATABASE) \
	  -cypher "MATCH (c:Company {companyNumber: '02026964'}) RETURN c.name, c.status, c.incorporationDate" \
	  -n $(N) -warmup $(WARMUP) -workers $(WORKERS)

# ---------------------------------------------------------------------------
# 2. FILTERED SCAN WITH LIMIT
#    Property filter on status + postCodePrefix, bounded result set.
#    Tests: filter pushdown, moderate serialisation (~100 rows, 2 properties).
#    SE22 prefix has ~2000 active companies; LIMIT 100 keeps it controlled.
# ---------------------------------------------------------------------------
bench-filtered-scan: build
	./bench $(ALL_IMPLICIT) \
	  -host $(NEO4J_HOST) -usr $(NEO4J_USERNAME) -pwd $(NEO4J_PASSWORD) \
	  -db $(NEO4J_DATABASE) \
	  -cypher "MATCH (c:Company) WHERE c.status = 'Active' AND c.postCodePrefix = 'SE22' RETURN c.name, c.companyNumber LIMIT 100" \
	  -n $(N) -warmup $(WARMUP) -workers $(WORKERS)

# ---------------------------------------------------------------------------
# 3. ONE-HOP TRAVERSAL
#    OFFICER_OF inbound, low fan-out (~7 officers for SC521741).
#    Tests: relationship traversal + node hydration for a small result set.
#    Expected result set: ~7 rows, 3 properties each.
# ---------------------------------------------------------------------------
bench-one-hop: build
	./bench $(ALL_IMPLICIT) \
	  -host $(NEO4J_HOST) -usr $(NEO4J_USERNAME) -pwd $(NEO4J_PASSWORD) \
	  -db $(NEO4J_DATABASE) \
	  -cypher "MATCH (:Company {companyNumber: 'SC521741'})<-[:OFFICER_OF]-(p:Person) RETURN p.chName, p.nationality, p.occupation" \
	  -n $(N) -warmup $(WARMUP) -workers $(WORKERS)

# ---------------------------------------------------------------------------
# 4. TWO-HOP TRAVERSAL
#    Shared-officer pattern: companies connected via a common officer.
#    Tests: 2-hop expansion, deduplication, moderate fan-out.
#    LIMIT 50 prevents runaway expansion on well-connected officers.
# ---------------------------------------------------------------------------
bench-two-hop: build
	./bench $(ALL_IMPLICIT) \
	  -host $(NEO4J_HOST) -usr $(NEO4J_USERNAME) -pwd $(NEO4J_PASSWORD) \
	  -db $(NEO4J_DATABASE) \
	  -cypher "MATCH (:Company {companyNumber: 'SC521741'})<-[:OFFICER_OF]-(p:Person)-[:OFFICER_OF]->(other:Company) RETURN DISTINCT other.name, other.companyNumber, p.chName LIMIT 50" \
	  -n $(N) -warmup $(WARMUP) -workers $(WORKERS)

# ---------------------------------------------------------------------------
# 5. AGGREGATION
#    Group companies by SIC code, count, sort, return top 20.
#    Tests: aggregation pipeline throughput + numeric serialisation.
#    HAS_SIC_CODE has 7.5M relationships — this is a real aggregation workload.
#    Expected result set: 20 rows (code STRING, count INTEGER).
# ---------------------------------------------------------------------------
bench-aggregation: build
	./bench $(ALL_IMPLICIT) \
	  -host $(NEO4J_HOST) -usr $(NEO4J_USERNAME) -pwd $(NEO4J_PASSWORD) \
	  -db $(NEO4J_DATABASE) \
	  -cypher "MATCH (c:Company)-[:HAS_SIC_CODE]->(s:SICCode) RETURN s.code, count(c) AS companyCount ORDER BY companyCount DESC LIMIT 20" \
	  -n $(N) -warmup $(WARMUP) -workers $(WORKERS)

# ---------------------------------------------------------------------------
# 6. BULK ROW RETURN
#    Paginated scan returning 1000 rows per request.
#    Tests: serialisation cost at volume — the axis most sensitive to the
#    Bolt-vs-HTTP transport change.
#    SKIP 0 for a stable, cache-warm page on every iteration.
# ---------------------------------------------------------------------------
bench-bulk-rows: build
	./bench $(ALL_IMPLICIT) \
	  -host $(NEO4J_HOST) -usr $(NEO4J_USERNAME) -pwd $(NEO4J_PASSWORD) \
	  -db $(NEO4J_DATABASE) \
	  -cypher "MATCH (c:Company) WHERE c.status = 'Active' RETURN c.companyNumber, c.name, c.postCode SKIP 0 LIMIT 1000" \
	  -n $(N) -warmup $(WARMUP) -workers $(WORKERS)

# ---------------------------------------------------------------------------
# WARMUP-ONLY (run first, results discarded)
# Sends a representative mix of queries to prime the JVM plan cache and
# Neo4j page cache before any timed run. Use -warmup 0 here so nothing
# is double-counted.
# ---------------------------------------------------------------------------
bench-warmup: build
	@echo ">>> Warming up plan cache and page cache — results discarded <<<"
	./bench -transaction implicit -connection fresh \
	  -host $(NEO4J_HOST) -usr $(NEO4J_USERNAME) -pwd $(NEO4J_PASSWORD) \
	  -db $(NEO4J_DATABASE) \
	  -cypher "MATCH (c:Company {companyNumber: '02026964'}) RETURN c.name" \
	  -n 50 -warmup 0 -workers 4
	./bench -transaction implicit -connection fresh \
	  -host $(NEO4J_HOST) -usr $(NEO4J_USERNAME) -pwd $(NEO4J_PASSWORD) \
	  -db $(NEO4J_DATABASE) \
	  -cypher "MATCH (c:Company)-[:HAS_SIC_CODE]->(s:SICCode) RETURN s.code, count(c) AS n ORDER BY n DESC LIMIT 20" \
	  -n 20 -warmup 0 -workers 4
	@echo ">>> Warmup complete <<<"

# ---------------------------------------------------------------------------
# RUN ALL SUITES IN SEQUENCE
# ---------------------------------------------------------------------------
bench-all: bench-warmup \
           bench-point-lookup \
           bench-filtered-scan \
           bench-one-hop \
           bench-two-hop \
           bench-aggregation \
           bench-bulk-rows


