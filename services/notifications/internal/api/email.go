package api

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gofiber/fiber/v3"

	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/pkg/identity"
	"github.com/GAMch1k/life-os/services/notifications/internal/api/generated"
	"github.com/GAMch1k/life-os/services/notifications/internal/email"
)

// Mailing is email as the API uses it; *email.Mailer satisfies it.
type Mailing interface {
	Configured() bool
	Problem() string
	Send(ctx context.Context, message email.Message) error
	Status(ctx context.Context) (email.State, error)
	TestMessage(to string) email.Message
}

// WithEmail sends email through mailer (docs/install/email.md). Without
// it, email is off: the status says so and sends are refused.
func (h *Handler) WithEmail(mailer Mailing) *Handler {
	h.email = mailer
	return h
}

var (
	emailKind = regexp.MustCompile(`^[a-z][a-z0-9_.:-]{0,47}$`)
	// What an email's HTML may not hold: anything that runs, loads, or
	// tracks (scripts, frames, images, styles from elsewhere).
	unsafeHTML = regexp.MustCompile(`(?i)<\s*(script|iframe|frame|object|embed|img|image|link|meta|base|form|svg|video|audio|source)\b|\bsrc\s*=|\bon[a-z]+\s*=|url\s*\(`)
)

// GetEmailStatus says whether email works. Status's detail, so the server's
// admins' through the gateway; the Dashboard and ./setup.sh read it without
// a token.
func (h *Handler) GetEmailStatus(c fiber.Ctx) error {
	if err := identity.RequireAdmin(c, identity.AdminPolicy{}); err != nil {
		return err
	}
	result := generated.EmailStatus{State: generated.EmailStateOff}
	if h.email == nil {
		return c.JSON(result)
	}
	state, err := h.email.Status(c.Context())
	if err != nil {
		return internal(err)
	}
	result.Configured = state.Configured
	result.State = generated.EmailState(state.State)
	result.Problem = optional(state.Problem)
	result.LastSuccessAt = state.LastSuccessAt
	result.LastFailureAt = state.LastFailureAt
	result.FailingSince = state.FailingSince
	result.LastErrorCode = optional(state.LastErrorCode)
	result.LastError = optional(state.LastError)
	return c.JSON(result)
}

// SendEmail sends one of the Dashboard's account emails.
func (h *Handler) SendEmail(c fiber.Ctx) error {
	if err := internalOnly(c, "only Life OS itself sends email"); err != nil {
		return err
	}
	var body generated.EmailMessage
	if err := json.Unmarshal(c.Body(), &body); err != nil {
		return badRequest(`the body must be an email: {"to", "subject", "text", "html", "kind"}`)
	}
	message, err := newEmail(body)
	if err != nil {
		return badRequest(err.Error())
	}
	return h.send(c, message)
}

// SendEmailTest sends the test email to one address.
func (h *Handler) SendEmailTest(c fiber.Ctx) error {
	if err := internalOnly(c, "only Life OS itself sends email"); err != nil {
		return err
	}
	var body generated.EmailTestRequest
	if err := json.Unmarshal(c.Body(), &body); err != nil {
		return badRequest(`the body must be {"to": "you@example.com"}`)
	}
	to, err := email.ParseAddress(body.To)
	if err != nil {
		return badRequest("to must be one email address, such as you@example.com")
	}
	if h.email == nil || !h.email.Configured() {
		return h.notConfigured()
	}
	return h.send(c, h.email.TestMessage(to))
}

func (h *Handler) send(c fiber.Ctx, message email.Message) error {
	if h.email == nil || !h.email.Configured() {
		return h.notConfigured()
	}
	err := h.email.Send(c.Context(), message)
	var failure *email.Failure
	switch {
	case err == nil:
		return c.SendStatus(fiber.StatusNoContent)
	case errors.Is(err, email.ErrNotConfigured):
		return h.notConfigured()
	case errors.Is(err, email.ErrRateLimited):
		return httpserver.NewError(fiber.StatusTooManyRequests, "rate_limited",
			"Too many emails: Life OS sends at most 30 a minute and 300 an hour. Try again later.")
	case errors.As(err, &failure):
		return httpserver.NewError(fiber.StatusBadGateway, failure.Code, failure.Message)
	}
	return internal(err)
}

func (h *Handler) notConfigured() error {
	message := "Email is not set up on this server: ./setup.sh email sets it up (docs/install/email.md)."
	if h.email != nil && h.email.Problem() != "" {
		message = "Email's settings cannot work: " + h.email.Problem() + "."
	}
	return httpserver.NewError(fiber.StatusConflict, "email_not_configured", message)
}

// internalOnly refuses a request with an identity token: every request
// through the gateway carries one, so only Life OS's own services on the
// private network get past (as for deleting a device key's subscriptions).
func internalOnly(c fiber.Ctx, message string) error {
	if _, ok := identity.FromFiber(c); ok {
		return httpserver.NewError(fiber.StatusForbidden, "internal_only", message)
	}
	return nil
}

// newEmail validates a message as the contract says.
func newEmail(body generated.EmailMessage) (email.Message, error) {
	to, err := email.ParseAddress(body.To)
	if err != nil {
		return email.Message{}, errors.New("to must be one email address, such as you@example.com")
	}
	subject := strings.TrimSpace(body.Subject)
	if subject == "" || utf8.RuneCountInString(subject) > 200 || hasControlChars(subject) {
		return email.Message{}, errors.New("subject must be one line of 1 to 200 characters")
	}
	if strings.TrimSpace(body.Text) == "" || utf8.RuneCountInString(body.Text) > 20000 || !utf8.ValidString(body.Text) {
		return email.Message{}, errors.New("text must be 1 to 20000 characters")
	}
	html := ""
	if body.Html != nil {
		html = *body.Html
		if utf8.RuneCountInString(html) > 100000 || !utf8.ValidString(html) {
			return email.Message{}, errors.New("html must be at most 100000 characters")
		}
		if unsafeHTML.MatchString(html) {
			return email.Message{}, errors.New("html may not load or run anything: no scripts, frames, images, forms, src attributes, event handlers, or url()")
		}
	}
	if !emailKind.MatchString(body.Kind) {
		return email.Message{}, errors.New("kind must be a code such as verify_email or alert.new_device")
	}
	return email.Message{To: to, Subject: subject, Text: body.Text, HTML: html, Kind: body.Kind}, nil
}

func hasControlChars(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func optional(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
