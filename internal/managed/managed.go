// Package managed executes explicit Neo4j transactions via raw HTTP calls:
// POST /tx (begin) → POST /tx/{id} (execute) → POST /tx/{id}/commit.
//
// Two API flavors are supported, and they use different cluster-routing
// mechanisms for keeping all three calls of a cycle on the node that owns
// the transaction:
//
//   - Legacy HTTP (/db/{db}/tx, statements-array body): the begin response's
//     Location header carries the transaction's full URL, built from that
//     node's server.http.advertised_address. This package follows that URL
//     directly for execute/commit, bypassing any load balancer for those two
//     calls. For that to work, each node's server.http.advertised_address
//     must be its own resolvable, directly-reachable address (see
//     docs/awsSetup/neo4/neo4j.conf), not the load balancer's — if the
//     Location host is unreachable you will see "dns" or "conn-refused" in
//     the runner's error breakdown rather than a confusing 404.
//
//   - Query API v2 (/db/{db}/query/v2/tx, single-statement body): there is no
//     dependable Location header — the documented, always-present source for
//     the transaction id is the "transaction.id" field of the begin
//     response's JSON body (https://neo4j.com/docs/query-api/current/transactions/).
//     Routing back to the owning member happens via the "neo4j-cluster-affinity"
//     response header on begin, which this package captures and replays on
//     execute/commit/rollback; execute/commit/rollback keep going through
//     whatever endpoint begin used (typically the load balancer), and the
//     cluster itself routes using that header rather than the client
//     targeting a node's address directly.
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

// clusterAffinityHeader is the Query API v2 header that pins the
// execute/commit/rollback calls of an explicit transaction to the cluster
// member that began it. The server sets it on the begin response; the client
// must replay the same value on every later call in that transaction.
const clusterAffinityHeader = "neo4j-cluster-affinity"

// transaction identifies an in-flight explicit transaction: the URL to send
// execute/commit/rollback to, and (Query API v2 only) the cluster-affinity
// token to replay on each of those calls.
type transaction struct {
	url      string
	affinity string
}

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
	tx, err := c.begin(ctx)
	if err != nil {
		return err
	}
	if err := c.execute(ctx, tx, cypher); err != nil {
		_ = c.rollback(ctx, tx)
		return err
	}
	return c.commit(ctx, tx)
}

// begin opens a transaction. See the package comment for how the returned
// transaction is built and routed for each flavor.
func (c *Client) begin(ctx context.Context) (transaction, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.txURL, http.NoBody)
	if err != nil {
		return transaction{}, err
	}
	c.setHeaders(req, "")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return transaction{}, err
	}
	defer drain(resp)

	// Accept any 2xx: Query API v2 and the legacy API have both used 200 and
	// 201 for this across versions.
	if !isSuccess(resp.StatusCode) {
		return transaction{}, &httperr.StatusError{Op: "begin", Code: resp.StatusCode, Body: snippet(resp.Body)}
	}

	if c.legacy {
		return c.beginLegacy(resp)
	}
	return c.beginQueryV2(resp)
}

// beginLegacy resolves the transaction URL from the begin response's
// Location header — the mechanism the Legacy Cypher HTTP Transaction API has
// always used.
func (c *Client) beginLegacy(resp *http.Response) (transaction, error) {
	loc := resp.Header.Get("Location")
	if loc == "" {
		return transaction{}, fmt.Errorf("begin: no Location header in response")
	}

	// Resolve against the request URL so a relative Location still works.
	// An absolute Location keeps its own host, which is the point: it names the
	// node that owns this transaction.
	base, err := url.Parse(c.txURL)
	if err != nil {
		return transaction{}, fmt.Errorf("begin: bad base URL %q: %w", c.txURL, err)
	}
	ref, err := url.Parse(loc)
	if err != nil {
		return transaction{}, fmt.Errorf("begin: bad Location %q: %w", loc, err)
	}
	return transaction{url: base.ResolveReference(ref).String()}, nil
}

// beginQueryV2Response is the minimal shape needed to read the transaction
// id out of a Query API v2 begin response.
type beginQueryV2Response struct {
	Transaction struct {
		ID string `json:"id"`
	} `json:"transaction"`
}

// beginQueryV2 builds the transaction URL from the begin response body's
// "transaction.id" field — Query API v2 does not reliably set Location
// behind a load balancer or cluster, but the body field is documented as
// always present. It also captures the cluster-affinity header so later
// calls route to the same member without needing that member's address.
func (c *Client) beginQueryV2(resp *http.Response) (transaction, error) {
	affinity := resp.Header.Get(clusterAffinityHeader)

	var env beginQueryV2Response
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return transaction{}, fmt.Errorf("begin: decode response body: %w", err)
	}
	if env.Transaction.ID == "" {
		return transaction{}, fmt.Errorf(`begin: response body has no "transaction.id"`)
	}

	return transaction{
		url:      strings.TrimRight(c.txURL, "/") + "/" + env.Transaction.ID,
		affinity: affinity,
	}, nil
}

func (c *Client) execute(ctx context.Context, tx transaction, cypher string) error {
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

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tx.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	c.setHeaders(req, tx.affinity)

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

func (c *Client) commit(ctx context.Context, tx transaction) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(tx.url, "/")+"/commit", http.NoBody)
	if err != nil {
		return err
	}
	c.setHeaders(req, tx.affinity)

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

func (c *Client) rollback(ctx context.Context, tx transaction) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, tx.url, http.NoBody)
	if err != nil {
		return err
	}
	c.setHeaders(req, tx.affinity)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	drain(resp)
	return nil
}

// setHeaders sets the headers common to every call in a transaction cycle.
// affinity is the cluster-affinity token to replay (Query API v2 only); pass
// "" for begin, where there is nothing to replay yet.
func (c *Client) setHeaders(req *http.Request, affinity string) {
	req.Header.Set("Authorization", c.authHeader)
	req.Header.Set("Content-Type", "application/json")
	if affinity != "" {
		req.Header.Set(clusterAffinityHeader, affinity)
	}
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
