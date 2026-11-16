package diagnostics

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"strings"
	"syscall"
)

// HTTPStatusError is an error that knows the HTTP status of the answer it
// stands for, such as a provider's refusal. ErrorCode logs the status, never
// the answer's text; a status of 0 means none is known.
type HTTPStatusError interface {
	error
	HTTPStatus() int
}

// sqlStateError is PostgreSQL's error (pgconn.PgError), by its method, so
// that every service can use this package without pgx.
type sqlStateError interface {
	error
	SQLState() string
}

// CodedError is an error with a provider's own code (such as Enable
// Banking's ASPSP_ERROR) besides its status. ErrorCode keeps the code when
// it has a code's form.
type CodedError interface {
	error
	ErrorCode() string
}

// ErrorCode is what a log line says an error was: a classified code, never
// its message, which can quote a provider's answer, a title, or an amount
// (docs/architecture/multi-user.md#logs). In order: the HTTP status (and a
// provider's code), a timeout, a cancellation, DNS, a refused or broken
// connection, PostgreSQL's SQLSTATE, no rows, and as a last resort the Go
// type of the innermost error.
func ErrorCode(err error) string {
	if err == nil {
		return ""
	}
	providerCode := ""
	var coded CodedError
	if errors.As(err, &coded) {
		providerCode = codeForm(coded.ErrorCode())
	}
	var status HTTPStatusError
	if errors.As(err, &status) && status.HTTPStatus() > 0 {
		code := fmt.Sprintf("http_%d", status.HTTPStatus())
		if providerCode != "" {
			code += "_" + providerCode
		}
		return code
	}
	if providerCode != "" {
		return providerCode
	}
	var pgErr sqlStateError
	var dnsErr *net.DNSError
	var netErr net.Error
	var opErr *net.OpError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.As(err, &dnsErr):
		return "dns"
	case errors.As(err, &netErr) && netErr.Timeout():
		return "timeout"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection_refused"
	case errors.Is(err, syscall.ECONNRESET):
		return "connection_reset"
	case errors.As(err, &opErr):
		return "network"
	case errors.As(err, &pgErr):
		return "sqlstate_" + codeForm(pgErr.SQLState())
	case errors.Is(err, sql.ErrNoRows):
		return "no_rows"
	}
	return "go_" + typeName(innermost(err))
}

// innermost follows a chain of wrapped errors to its end (a joined error is
// an end of its own).
func innermost(err error) error {
	for {
		next := errors.Unwrap(err)
		if next == nil {
			return err
		}
		err = next
	}
}

// typeName is an error's Go type as a code: *net.OpError is net.OpError.
func typeName(err error) string {
	name := strings.TrimLeft(fmt.Sprintf("%T", err), "*")
	var b strings.Builder
	for _, r := range name {
		if r == '.' || r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 || b.Len() > 64 {
		return "error"
	}
	return b.String()
}

// codeForm keeps a provider's code when it has a code's form, lowercased;
// anything else is dropped.
func codeForm(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	if code == "" || len(code) > 64 || strings.Trim(code, "abcdefghijklmnopqrstuvwxyz0123456789_.-") != "" {
		return ""
	}
	return code
}
