// Package managed executes explicit Neo4j transactions via raw HTTP calls:
// POST /tx (begin) → POST /tx/{id} (execute) → POST /tx/{id}/commit.
//
// Two API flavors are supported:
//   - Query API v2: /db/{db}/query/v2/tx  (single-statement body)
//   - Legacy HTTP:  /db/{db}/tx           (statements-array body)
//
// # Cluster routing
//
// A transaction exists only on the cluster member that created it. All three
// calls in a begin/execute/commit cycle must therefore reach the same node.
//
// Neo4j supports this by returning the transaction's full URL in the Location
// header of the begin response, built from that node's
// server.http.advertised_address. This package follows that URL for the
// remaining calls, so the cycle stays pinned to the owning node even when
// begin was sent through a round-robin load balancer.
//
// Two things must hold for that to work:
//
//   - Each node's server.http.advertised_address must be its own resolvable
//     address, not the load balancer's.
//   - The client must be able to reach nodes directly (i.e. run the benchmark
//     inside the VPC).
//
// If the Location host is unreachable you will see "dns" or "conn-refused" in
// the runner's error breakdown rather than a confusing 404.
package managed

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/LackOfMorals/queryAPIBenchmarks-go/internal/httperr"
)

// bodySnippetLimit caps how much of an error response body is retained.
const bodySnippetLimit = 256

// Client executes managed transactions against a single Neo4j database.
type Client struct {
	httpClient     *http.Client
	txURL          string // base transaction URL used for begin, flavor-specific
	authHeader     string // "Basic <base64(user:pass)>"
	legacy         bool   // true → Legacy Cypher HTTP Transaction API
	readAccessMode bool   // true → send read access mode header
}

// NewClient constructs a Client. Set legacy=true to target /db/{db}/tx
// (Legacy Cypher HTTP Transaction API) instead of /db/{db}/query/v2/tx.
// Set readAccessMode=true to hint to the server that the transaction is read-only.
// httpClient controls connection behaviour (fresh vs. session, HTTP/2).
func NewClient(httpClient *http.Client, baseURL, database, username, password string, legacy, readAccessMode bool) *Client {
	auth := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
	base := strings.TrimRight(baseURL, "/")
	var txURL string
	if legacy {
		txURL = base + "/db/" + database + "/tx"
	} else {
		txURL = base + "/db/" + database + "/query/v2/tx"
	}
	return &Client{
		httpClient:     httpClient,
		txURL:          txURL,
		authHeader:     "Basic " + auth,
		legacy:         legacy,
		readAccessMode: readAccessMode,
	}
}

// v2 execute body: {"statement": "..."}
type statementBody struct {
	Statement string `json:"statement"`
}

// legacy execute body: {"statements": [{"statement": "..."}]}
type legacyStatements struct {
	Statements []statementBody `json:"statements"`
}

// legacyEnvelope is the minimal shape needed to spot Cypher errors in a Legacy
// HTTP Transaction API response, which reports them in the body at HTTP 200.
type legacyEnvelope struct {
	Errors []struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
}


// RunTransaction executes begin → execute(cypher) → commit in sequence.
// On execute failure a best-effort rollback (DELETE) is attempted before
// returning the error.
func (c *Client) RunTransaction(ctx context.Context, cypher string) error {
	txURL, err := c.begin(ctx)
	if err != nil {
		return err
	}
	if err := c.execute(ctx, txURL, cypher); err != nil {
		_ = c.rollback(ctx, txURL)
		return err
	}
	return c.commit(ctx, txURL)
}

// begin opens a transaction and returns its absolute URL.
//
// The returned URL — not just the transaction id — is what subsequent calls
// must use. See the package comment on cluster routing.
func (c *Client) begin(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.txURL, http.NoBody)
	if err != nil {
		return "", err
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer drain(resp)

	// Accept any 2xx: Query API v2 and the legacy API have both used 200 and
	// 201 for this across versions.
	if !isSuccess(resp.StatusCode) {
		return "", &httperr.StatusError{Op: "begin", Code: resp.StatusCode, Body: snippet(resp.Body)}
	}

	loc := resp.Header.Get("Location")
	if loc == "" {
		return "", fmt.Errorf("begin: no Location header in response")
	}

	// Resolve against the request URL so a relative Location still works.
	// An absolute Location keeps its own host, which is the point: it names the
	// node that owns this transaction.
	base, err := url.Parse(c.txURL)
	if err != nil {
		return "", fmt.Errorf("begin: bad base URL %q: %w", c.txURL, err)
	}
	ref, err := url.Parse(loc)
	if err != nil {
		return "", fmt.Errorf("begin: bad Location %q: %w", loc, err)
	}
	return base.ResolveReference(ref).String(), nil
}

func (c *Client) execute(ctx context.Context, txURL, cypher string) error {
	var payload any
	if c.legacy {
		payload = legacyStatements{Statements: []statementBody{{Statement: cypher}}}
	} else {
		payload = statementBody{Statement: cypher}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("execute: marshal body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, txURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer drain(resp)

	if !isSuccess(resp.StatusCode) {
		return &httperr.StatusError{Op: "execute", Code: resp.StatusCode, Body: snippet(resp.Body)}
	}
	return c.checkBody("execute", resp)
}

func (c *Client) commit(ctx context.Context, txURL string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(txURL, "/")+"/commit", http.NoBody)
	if err != nil {
		return err
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer drain(resp)

	if !isSuccess(resp.StatusCode) {
		return &httperr.StatusError{Op: "commit", Code: resp.StatusCode, Body: snippet(resp.Body)}
	}
	return c.checkBody("commit", resp)
}

func (c *Client) rollback(ctx context.Context, txURL string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, txURL, http.NoBody)
	if err != nil {
		return err
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	drain(resp)
	return nil
}

func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("Authorization", c.authHeader)
	req.Header.Set("Content-Type", "application/json")
	if c.legacy {
		if c.readAccessMode {
			req.Header.Set("Access-Mode", "READ")
		} else {
			req.Header.Set("Access-Mode", "WRITE")
		}
	} else {
		// NOTE: verify this is honoured. Query API v2 may expect accessMode as a
		// field in the request body rather than a header. If the hint is ignored,
		// every read routes to the leader and the other two nodes sit idle.
		if c.readAccessMode {
			req.Header.Set("accessMode", "read")
		} else {
			req.Header.Set("accessMode", "write")
		}
	}
}

// isSuccess reports whether code is a 2xx.
//
// All three calls use the same test. Requiring exactly 200 in execute/commit
// while begin accepted any 2xx would turn a 202 or 204 into a spurious
// "http-202" failure class.
func isSuccess(code int) bool {
	return code >= 200 && code <= 299
}

// checkBody inspects a successful response for errors reported in the payload.
//
// Only the legacy flavor needs this: the Legacy Cypher HTTP Transaction API
// returns Cypher failures as HTTP 200 with a populated "errors" array, so
// without this check a query that fails every iteration reports 100% success.
// Query API v2 signals errors with the HTTP status, so it skips the parse.
func (c *Client) checkBody(op string, resp *http.Response) error {
	if !c.legacy {
		return nil
	}

	// Stream-decode rather than io.ReadAll: encoding/json skips fields absent
	// from legacyEnvelope, so the (potentially large) "results" array is walked
	// as tokens instead of being allocated. Buffering the whole body here would
	// add a per-request allocation inside the timed region for legacy only,
	// biasing the very legacy-vs-v2 comparison this tool exists to measure.
	//
	// A substring test for `"errors":[]` would be cheaper still, but an
	// unanchored scan can be satisfied by returned data — any string property
	// containing that literal would mask a genuine failure.
	var env legacyEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		// Unrecognised envelope. Treat as success rather than failing every
		// request on a body-format change we don't understand.
		return nil
	}
	if len(env.Errors) == 0 {
		return nil
	}

	first := env.Errors[0]
	return &httperr.CypherError{
		Op:      op,
		Code:    first.Code,
		Message: strings.Join(strings.Fields(first.Message), " "),
	}
}

// snippet reads up to bodySnippetLimit bytes of an error response so the cause
// (typically a Neo.ClientError code) survives into the error message.
func snippet(r io.Reader) string {
	b, err := io.ReadAll(io.LimitReader(r, bodySnippetLimit))
	if err != nil || len(b) == 0 {
		return ""
	}
	return strings.Join(strings.Fields(string(b)), " ")
}

// drain consumes and closes a response body so the connection can be reused.
func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}
