package enablebanking

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// APIError is an error answer from the API: the HTTP status, the API's
// error code (such as EXPIRED_SESSION), and its message with identifiers
// removed.
type APIError struct {
	Op      string
	Status  int
	Code    string
	Message string
}

func newAPIError(op string, status int, raw []byte) *APIError {
	apiError := &APIError{Op: op, Status: status}
	var body struct {
		Message string `json:"message"`
		Error   string `json:"error"`
		Detail  any    `json:"detail"`
	}
	if json.Unmarshal(raw, &body) == nil {
		apiError.Code = strings.ToUpper(strings.TrimSpace(body.Error))
		apiError.Message = Redact(body.Message)
	}
	if len(apiError.Message) > 300 {
		apiError.Message = apiError.Message[:300]
	}
	return apiError
}

func (e *APIError) Error() string {
	text := fmt.Sprintf("Enable Banking %s: %d", e.Op, e.Status)
	if e.Code != "" {
		text += " " + e.Code
	}
	if e.Message != "" {
		text += ": " + e.Message
	}
	return text
}

// HTTPStatus and ErrorCode are what a log line keeps of the refusal
// (pkg/diagnostics.ErrorCode), never its message.
func (e *APIError) HTTPStatus() int   { return e.Status }
func (e *APIError) ErrorCode() string { return e.Code }

// SessionEnded means the consent is over: it expired, the owner revoked it
// at the bank, or the provider closed or no longer knows the session.
func (e *APIError) SessionEnded() bool {
	switch e.Code {
	case "EXPIRED_SESSION", "REVOKED_SESSION", "CLOSED_SESSION", "SESSION_DOES_NOT_EXIST":
		return true
	}
	return false
}

// RateLimited means the bank or the provider refuses more reads for now.
func (e *APIError) RateLimited() bool {
	return e.Status == http.StatusTooManyRequests || e.Code == "ASPSP_RATE_LIMIT_EXCEEDED"
}

// Temporary means the same request may succeed later.
func (e *APIError) Temporary() bool {
	switch e.Code {
	case "ASPSP_ERROR", "ASPSP_TIMEOUT", "ASPSP_RATE_LIMIT_EXCEEDED":
		return true
	}
	return e.Status >= 500 || e.Status == http.StatusRequestTimeout || e.Status == http.StatusTooManyRequests
}

// PSUHeaders means the bank refused the PSU headers sent for the owner.
func (e *APIError) PSUHeaders() bool {
	return e.Code == "PSU_HEADER_NOT_PROVIDED" || e.Code == "PSU_HEADER_INVALID"
}

// WrongPeriod means the bank does not serve the requested dates.
func (e *APIError) WrongPeriod() bool {
	return e.Code == "WRONG_TRANSACTIONS_PERIOD"
}

// CodeRejected means the authorization code expired, was used, or is wrong.
func (e *APIError) CodeRejected() bool {
	switch e.Code {
	case "EXPIRED_AUTHORIZATION_CODE", "WRONG_AUTHORIZATION_CODE", "ALREADY_AUTHORIZED":
		return true
	}
	return false
}

// ActionRequired means the bank needs the owner to act (consent again).
func (e *APIError) ActionRequired() bool {
	return e.Code == "ASPSP_PSU_ACTION_REQUIRED"
}

// Unauthorized means the provider refuses the application itself: a wrong
// application id or key, or a service the application may not use.
func (e *APIError) Unauthorized() bool {
	switch e.Code {
	case "UNAUTHORIZED_ACCESS", "ACCESS_DENIED", "AUTHORIZATION_NOT_PROVIDED", "UNAUTHORIZED_IP", "REDIRECT_URI_NOT_ALLOWED", "NO_ACCOUNTS_ADDED":
		return true
	}
	return e.Code == "" && (e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden)
}

// TransportError is a request that got no answer from the API (the network
// or a timeout) or an unreadable one. It never names the URL.
type TransportError struct {
	Op  string
	Err error
}

func (e *TransportError) Error() string {
	return fmt.Sprintf("Enable Banking %s failed: %v", e.Op, e.Err)
}

func (e *TransportError) Unwrap() error { return e.Err }

// AsAPIError returns the APIError in err's chain, or nil.
func AsAPIError(err error) *APIError {
	var apiError *APIError
	if errors.As(err, &apiError) {
		return apiError
	}
	return nil
}

var (
	uuidPattern = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	// An IBAN, possibly grouped in fours: two letters, two digits, then
	// 11-30 letters or digits.
	ibanPattern = regexp.MustCompile(`\b[A-Z]{2}\d{2}(?:[ ]?[A-Z0-9]){11,30}\b`)
)

// Redact removes session and account identifiers and IBANs from provider
// text before it is stored or shown.
func Redact(text string) string {
	text = uuidPattern.ReplaceAllString(text, "[id]")
	text = ibanPattern.ReplaceAllString(text, "[account]")
	return strings.Join(strings.Fields(text), " ")
}
