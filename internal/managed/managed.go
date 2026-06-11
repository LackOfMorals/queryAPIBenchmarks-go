// Package managed executes explicit Neo4j Query API v2 transactions via raw
// HTTP calls: POST /tx (begin) → POST /tx/{id} (execute) → POST /tx/{id}/commit.
// The query-go-sdk does not yet expose a managed-transaction API, so this
// package talks directly to the HTTP endpoints.
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
	txURL      string // "{baseURL}/db/{database}/query/v2/tx"
	authHeader string // "Basic <base64(user:pass)>"
}

// NewClient constructs a Client.  httpClient controls connection behaviour
// (fresh vs. session, HTTP/2) and must not be nil.
func NewClient(httpClient *http.Client, baseURL, database, username, password string) *Client {
	auth := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
	return &Client{
		httpClient: httpClient,
		txURL:      strings.TrimRight(baseURL, "/") + "/db/" + database + "/query/v2/tx",
		authHeader: "Basic " + auth,
	}
}

type statementBody struct {
	Statement string `json:"statement"`
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
	body, _ := json.Marshal(statementBody{Statement: cypher})
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
