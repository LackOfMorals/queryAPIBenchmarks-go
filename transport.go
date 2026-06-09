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

// NewFresh returns an http.Client that opens a new TCP connection for every
// request, matching the Python TXrequest / Sync / Threads behaviour.
func NewFresh(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DisableKeepAlives: true,
		},
	}
}

// NewSession returns an http.Client that reuses connections across requests
// (keep-alives on), matching the Python TXsession / SyncSessions /
// ThreadsSessions behaviour.
func NewSession(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 100,
			IdleConnTimeout:     90 * time.Second,
		},
	}
}

// NewSessionHTTP2 returns an http.Client that reuses connections and negotiates
// HTTP/2, matching the Python TXsession with NETWORK_HTTP2=1.
//
// Note: HTTP/2 requires TLS in practice; plain-text h2c is not attempted here.
// Point the benchmark at an https:// URL when using this transport.
func NewSessionHTTP2(timeout time.Duration) (*http.Client, error) {
	t := &http.Transport{
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
