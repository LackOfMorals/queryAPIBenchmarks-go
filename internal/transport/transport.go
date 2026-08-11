// Package transport provides http.Client factories for each benchmark mode.
//
// The Python benchmark distinguishes two transport modes:
//   - TXrequest: new TCP connection per request (DisableKeepAlives)
//   - TXsession: persistent connection reuse (keep-alives on, optional HTTP/2)
//
// The query-go-sdk accepts a custom *http.Client via query.WithHTTPClient,
// so these factories are the Go equivalent of choosing TXrequest vs TXsession.
package transport

import (
	"crypto/tls"
	"net/http"
	"time"

	"golang.org/x/net/http2"
)

// idleConnTimeout is how long an unused pooled connection is kept.
//
// This must stay BELOW the load balancer's keep-alive idle timeout
// (HAProxy: timeout http-keep-alive, 30s in the reviewed config). If the client
// holds connections longer than the proxy does, HAProxy closes them first and
// the client hands out a dead connection — which surfaces as a sporadic EOF
// that Go will not retry for a POST with a body.
const idleConnTimeout = 20 * time.Second

// minPoolSize floors the connection pool so the sequential modes, which have a
// concurrency of 1, still keep a reasonable pool.
const minPoolSize = 100

// PoolSize derives a connection pool size from the benchmark's concurrency.
// Exported so every transport that pools connections — including the Bolt
// driver's own connection pool in benchmarks/bolt.go — sizes itself the same
// way; a pool sized only for one transport would plateau at a different
// concurrency than the others, turning a client-side artifact into what
// looks like a protocol difference.
//
// Go's default MaxIdleConnsPerHost is 2, and a hardcoded 100 was fine at 4
// workers but became a silent ceiling above that: surplus connections were
// closed rather than pooled, so every request past the limit paid a fresh
// TCP (and TLS) handshake. The resulting throughput plateau looks exactly
// like server saturation.
func PoolSize(concurrency int) int {
	n := concurrency * 2
	if n < minPoolSize {
		return minPoolSize
	}
	return n
}

// noHTTP2 makes HTTP/2 negotiation explicit rather than ambient. A plain
// &http.Transport{} will opportunistically negotiate h2 over TLS, which would
// mean -http2=0 silently ran HTTP/2 against an https:// URL.
func noHTTP2() map[string]func(string, *tls.Conn) http.RoundTripper {
	return map[string]func(string, *tls.Conn) http.RoundTripper{}
}

// NewFresh returns an http.Client that opens a new TCP connection for every
// request, matching the Python TXrequest / Sync / Threads behaviour.
//
// Note this mode measures connection setup as much as query execution: over a
// high-latency link it costs an extra round trip per request, and a large
// response additionally pays TCP slow start on every single call.
func NewFresh(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DisableKeepAlives: true,
			TLSNextProto:      noHTTP2(),
		},
	}
}

// NewSession returns an http.Client that reuses connections across requests
// (keep-alives on), matching the Python TXsession / SyncSessions /
// ThreadsSessions behaviour.
//
// concurrency is the number of goroutines that will share this client; the
// connection pool is sized from it.
func NewSession(timeout time.Duration, concurrency int) *http.Client {
	n := PoolSize(concurrency)
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			MaxIdleConns:        n,
			MaxIdleConnsPerHost: n,
			MaxConnsPerHost:     0, // unlimited: let the server be the limit, not the client
			IdleConnTimeout:     idleConnTimeout,
			TLSNextProto:        noHTTP2(),
		},
	}
}

// NewSessionHTTP2 returns an http.Client that reuses connections and negotiates
// HTTP/2, matching the Python TXsession with NETWORK_HTTP2=1.
//
// Note: HTTP/2 requires TLS in practice; plain-text h2c is not attempted here.
// Point the benchmark at an https:// URL when using this transport.
//
// The idle-connection settings matter far less here: HTTP/2 multiplexes all
// requests to a host over one connection, so concurrency is bounded by the
// server's SETTINGS_MAX_CONCURRENT_STREAMS rather than by the pool. Neo4j
// behind HAProxy in HTTP/2 mode typically advertises 100.
func NewSessionHTTP2(timeout time.Duration, concurrency int) (*http.Client, error) {
	n := PoolSize(concurrency)
	t := &http.Transport{
		MaxIdleConns:        n,
		MaxIdleConnsPerHost: n,
		IdleConnTimeout:     idleConnTimeout,
		ForceAttemptHTTP2:   true,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
	}

	if err := http2.ConfigureTransport(t); err != nil {
		return nil, err
	}

	return &http.Client{
		Timeout:   timeout,
		Transport: t,
	}, nil
}
