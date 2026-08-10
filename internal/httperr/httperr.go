// Package httperr defines error types shared between the HTTP call sites and
// the benchmark runner's error classifier.
//
// It exists as its own package so that internal/managed can produce typed
// errors and internal/runner can classify them without either importing the
// other.
package httperr

import "fmt"

// StatusError reports a non-success HTTP status from a Neo4j API call.
//
// The runner classifies these by Code, so the results table can break failures
// down per status ("http-503: 412") instead of lumping every kind of failure
// into one counter. Body carries a truncated snippet of the response, which is
// where Neo4j puts the actual Neo.ClientError code.
type StatusError struct {
	Op   string // which call failed: "begin", "execute", "commit", "rollback"
	Code int    // HTTP status code
	Body string // truncated response body, may be empty
}

func (e *StatusError) Error() string {
	if e.Body != "" {
		return fmt.Sprintf("%s: http %d: %s", e.Op, e.Code, e.Body)
	}
	return fmt.Sprintf("%s: http %d", e.Op, e.Code)
}

// CypherError reports a query failure that the server returned inside an
// otherwise successful HTTP response.
//
// The Legacy Cypher HTTP Transaction API does not use HTTP status codes for
// Cypher errors: a syntax error or constraint violation comes back as 200 or
// 201 with a populated "errors" array. Without inspecting the body, a query
// that fails on every single iteration reports 100% success and produces a
// latency distribution for work that never happened.
type CypherError struct {
	Op      string // which call failed: "execute", "commit"
	Code    string // Neo4j status code, e.g. Neo.ClientError.Statement.SyntaxError
	Message string
}

func (e *CypherError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("%s: %s: %s", e.Op, e.Code, e.Message)
	}
	return fmt.Sprintf("%s: %s", e.Op, e.Code)
}
