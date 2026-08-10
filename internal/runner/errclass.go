package runner

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"regexp"
	"strings"
	"syscall"

	"github.com/LackOfMorals/queryAPIBenchmarks-go/internal/httperr"
)

// maxErrorSamples caps how many verbatim error strings each worker retains.
// The class counts tell you what is failing; a couple of samples tell you why,
// without letting a fully-failing run write millions of lines.
const maxErrorSamples = 3

// summaryLimit truncates the normalised message used as a map key for errors
// that don't match a known class.
const summaryLimit = 70

var digitRun = regexp.MustCompile(`\d+`)

// classify buckets an error into a short stable label used as a map key in
// Result.Errors.
//
// The point is to make a failing benchmark self-diagnosing. "412 failures" is
// useless; "http-503: 412" says the load balancer had no healthy backend, and
// "timeout: 412" says requests were being dropped on the floor.
func classify(err error) string {
	if err == nil {
		return ""
	}

	// Non-2xx responses from Neo4j, tagged with the status code.
	var se *httperr.StatusError
	if errors.As(err, &se) {
		return fmt.Sprintf("http-%d", se.Code)
	}

	// Cypher errors the legacy API hides inside a 200 response.
	var ce *httperr.CypherError
	if errors.As(err, &ce) {
		if ce.Code == "" {
			return "cypher"
		}
		return "cypher: " + ce.Code
	}

	// Deadlines first: a timeout often wraps a lower-level net error, and the
	// timeout is the more useful label.
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "dns"
	}

	var cve *tls.CertificateVerificationError
	if errors.As(err, &cve) {
		return "tls-verify"
	}
	var rhe tls.RecordHeaderError
	if errors.As(err, &rhe) {
		// Classic symptom of speaking TLS to a plaintext port, or vice versa.
		return "tls-handshake"
	}

	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return "conn-refused"
	case errors.Is(err, syscall.ECONNRESET):
		return "conn-reset"
	case errors.Is(err, syscall.ECONNABORTED):
		return "conn-aborted"
	case errors.Is(err, syscall.EPIPE):
		return "broken-pipe"
	case errors.Is(err, syscall.ETIMEDOUT):
		return "conn-timeout"
	case errors.Is(err, syscall.EMFILE), errors.Is(err, syscall.ENFILE):
		// Client-side file descriptor exhaustion — raise ulimit -n. Worth its own
		// label because it looks like a server limit in the results.
		return "fd-exhausted"
	case errors.Is(err, syscall.EADDRNOTAVAIL):
		// Ephemeral port exhaustion on the load generator.
		return "ports-exhausted"
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		return "eof"
	}

	// Anything else that reports itself as a timeout.
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "timeout"
	}

	return "other: " + summarize(err)
}

// summarize normalises an unrecognised error message into a stable map key:
// it drops the URL that url.Error prefixes, collapses digit runs so per-request
// ids and ports don't each create their own bucket, and truncates.
func summarize(err error) string {
	msg := err.Error()

	// url.Error formats as: Post "http://host/path": <cause>
	if i := strings.LastIndex(msg, `": `); i >= 0 {
		msg = msg[i+len(`": `):]
	}

	msg = strings.Join(strings.Fields(msg), " ")
	msg = digitRun.ReplaceAllString(msg, "N")

	return TruncateRunes(msg, summaryLimit)
}

// TruncateRunes shortens s to at most limit runes, appending an ellipsis when
// it trims.
//
// Cutting on a byte boundary would split a multi-byte rune. That matters here
// because the result becomes a map key in Result.Errors and is then JSON
// encoded: encoding/json substitutes U+FFFD for invalid UTF-8, so two distinct
// error classes could silently collapse into one bucket.
func TruncateRunes(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(s) <= limit { // byte length is an upper bound on rune count
		return s
	}

	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	if limit <= 3 {
		return string(runes[:limit])
	}
	return string(runes[:limit-3]) + "..."
}
