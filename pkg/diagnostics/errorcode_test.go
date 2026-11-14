package diagnostics

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"syscall"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type refusal struct {
	status int
	code   string
	text   string
}

func (r *refusal) Error() string     { return fmt.Sprintf("refused %d %s: %s", r.status, r.code, r.text) }
func (r *refusal) HTTPStatus() int   { return r.status }
func (r *refusal) ErrorCode() string { return r.code }

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

type privateError struct{ text string }

func (e privateError) Error() string { return e.text }

// A log line's error is a classified code, never the message, which here
// carries a title and an amount the way a provider's answer can.
func TestErrorCodeNeverCarriesTheMessage(t *testing.T) {
	marker := "Dentist with Anna, 4711.93 EUR"
	for _, tc := range []struct {
		err  error
		want string
	}{
		{nil, ""},
		{fmt.Errorf("push: %w", &refusal{status: 403, code: "forbidden", text: marker}), "http_403_forbidden"},
		{&refusal{status: 500, code: marker, text: marker}, "http_500"},
		{&refusal{status: 0, code: "tool_rejected", text: marker}, "tool_rejected"},
		{fmt.Errorf("%s: %w", marker, context.DeadlineExceeded), "timeout"},
		{fmt.Errorf("%s: %w", marker, context.Canceled), "canceled"},
		{&net.DNSError{Err: "no such host", Name: "ntfy." + marker}, "dns"},
		{&net.OpError{Op: "dial", Err: timeoutError{}}, "timeout"},
		{&net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}, "connection_refused"},
		{fmt.Errorf("query %s: %w", marker, &pgconn.PgError{Code: "23505", Message: marker}), "sqlstate_23505"},
		{fmt.Errorf("get %s: %w", marker, pgx.ErrNoRows), "no_rows"},
		{fmt.Errorf("wrapped: %w", privateError{text: marker}), "go_diagnostics.privateError"},
		{errors.New(marker), "go_errors.errorString"},
	} {
		got := ErrorCode(tc.err)
		if got != tc.want || strings.Contains(got, "Anna") || strings.Contains(got, "4711") {
			t.Errorf("ErrorCode(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}
