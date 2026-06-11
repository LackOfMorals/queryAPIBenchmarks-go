// Package managed executes explicit Neo4j transactions via raw HTTP calls:
// POST /tx (begin) → POST /tx/{id} (execute) → POST /tx/{id}/commit.
//
// Two API flavors are supported:
//   - Query API v2: /db/{db}/query/v2/tx  (single-statement body)
//   - Legacy HTTP:  /db/{db}/tx           (statements-array body)
package managed

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Client executes managed transactions against a single Neo4j database.
type Client struct {
	httpClient *http.Client
	txURL      string // base transaction URL, flavor-specific
	authHeader string // "Basic <base64(user:pass)>"
	legacy     bool   // true → Legacy Cypher HTTP Transaction API
}

// NewClient constructs a Client. Set legacy=true to target /db/{db}/tx
// (Legacy Cypher HTTP Transaction API) instead of /db/{db}/query/v2/tx.
// httpClient controls connection behaviour (fresh vs. session, HTTP/2).
func NewClient(httpClient *http.Client, baseURL, database, username, password string, legacy bool) *Client {
	auth := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
	base := strings.TrimRight(baseURL, "/")
	var txURL string
	if legacy {
		txURL = base + "/db/" + database + "/tx"
	} else {
		txURL = base + "/db/" + database + "/query/v2/tx"
	}
	return &Client{
		httpClient: httpClient,
		txURL:      txURL,
		authHeader: "Basic " + auth,
		legacy:     legacy,
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

// RunTransaction executes begin → execute(cypher) → commit in sequence.
// On execute failure a best-effort rollback (DELETE) is attempted before
// returning the error.
func (c *Client) RunTransaction(ctx context.Context, cypher string) error {
	txID, err := c.begin(ctx)
	if err != nil {
		return err
	}
	if err := c.execute(ctx, txID, cypher); err != nil {
		_ = c.rollback(ctx, txID)
		return err
	}
	return c.commit(ctx, txID)
}

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
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("begin: unexpected status %d", resp.StatusCode)
	}

	loc := resp.Header.Get("Location")
	if loc == "" {
		return "", fmt.Errorf("begin: no Location header in response")
	}
	parts := strings.Split(strings.TrimRight(loc, "/"), "/")
	return parts[len(parts)-1], nil
}

func (c *Client) execute(ctx context.Context, txID, cypher string) error {
	var payload any
	if c.legacy {
		payload = legacyStatements{Statements: []statementBody{{Statement: cypher}}}
	} else {
		payload = statementBody{Statement: cypher}
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.txURL+"/"+txID, bytes.NewReader(body))
	if err != nil {
		return err
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("execute: unexpected status %d", resp.StatusCode)
	}
	return nil
}

func (c *Client) commit(ctx context.Context, txID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.txURL+"/"+txID+"/commit", http.NoBody)
	if err != nil {
		return err
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("commit: unexpected status %d", resp.StatusCode)
	}
	return nil
}

func (c *Client) rollback(ctx context.Context, txID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.txURL+"/"+txID, http.NoBody)
	if err != nil {
		return err
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); resp.Body.Close() }()
	return nil
}

func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("Authorization", c.authHeader)
	req.Header.Set("Content-Type", "application/json")
}
