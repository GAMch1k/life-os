package email

import (
	"context"
	"errors"
	"html"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	store "github.com/GAMch1k/life-os/services/notifications/internal/repository/generated"
)

// Sending is what a Mailer sends with; *Sender satisfies it.
type Sending interface {
	Send(ctx context.Context, message Message) error
}

// Store keeps email's status (email_status); *store.Queries satisfies it.
type Store interface {
	GetEmailStatus(ctx context.Context) (store.GetEmailStatusRow, error)
	NoteEmailSent(ctx context.Context, at pgtype.Timestamptz) error
	NoteEmailFailed(ctx context.Context, arg store.NoteEmailFailedParams) error
}

// ErrNotConfigured: email is off (no settings) or its settings cannot work.
var ErrNotConfigured = errors.New("email is not set up")

// ErrRateLimited: more emails than the server sends in a minute or an hour.
var ErrRateLimited = errors.New("too many emails")

// Limits on what the server sends at all, whatever asks: a safety net for
// the owner's SMTP account, under the Dashboard's own per-person and
// per-address limits.
const (
	MaxPerMinute = 30
	MaxPerHour   = 300
)

// sendTimeout bounds one send: the Dashboard waits for it.
const sendTimeout = 30 * time.Second

// Mailer sends Life OS's emails and keeps their status. With email off (or
// its settings unusable) it sends nothing and says why.
type Mailer struct {
	sender  Sending
	config  Config
	problem string
	store   Store
	logger  *slog.Logger
	now     func() time.Time

	mu   sync.Mutex
	sent []time.Time
}

// NewMailer is a Mailer for settings: on with a usable configuration (sent
// through sender, or a new Sender when nil), else off, with the settings'
// problem if they have one.
func NewMailer(settings Settings, sender Sending, st Store, logger *slog.Logger) *Mailer {
	config, ok, problem := Parse(settings)
	m := &Mailer{config: config, problem: problem, store: st, logger: logger, now: time.Now}
	if ok {
		if sender == nil {
			sender = NewSender(config)
		}
		m.sender = sender
	}
	return m
}

// Configured reports whether email can be sent.
func (m *Mailer) Configured() bool { return m != nil && m.sender != nil }

// Problem is what is wrong with the settings, or "".
func (m *Mailer) Problem() string { return m.problem }

// Config is the usable configuration (zero when off).
func (m *Mailer) Config() Config { return m.config }

// Send sends one email now: nil once the server accepted it,
// ErrNotConfigured, ErrRateLimited, or a *Failure. The outcome counts
// towards the status, except a refused recipient, which says nothing about
// the server; the log names the kind and the outcome code only.
func (m *Mailer) Send(ctx context.Context, message Message) error {
	if !m.Configured() {
		return ErrNotConfigured
	}
	if !m.take(m.now()) {
		m.logger.Warn("email not sent: rate limit", "kind", message.Kind, "code", "rate_limited")
		return ErrRateLimited
	}
	sendCtx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	started := m.now()
	err := m.sender.Send(sendCtx, message)
	at := pgtype.Timestamptz{Time: m.now().UTC(), Valid: true}
	// The status is written even when the request that asked went away.
	noteCtx, cancelNote := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancelNote()
	if err == nil {
		if noteErr := m.store.NoteEmailSent(noteCtx, at); noteErr != nil {
			m.logger.Error("email status not written", "error", noteErr)
		}
		m.logger.Info("email sent", "kind", message.Kind, "duration_ms", m.now().Sub(started).Milliseconds())
		return nil
	}
	var failure *Failure
	if !errors.As(err, &failure) {
		failure = &Failure{Code: CodeFailed, Message: "The email could not be sent."}
	}
	if !failure.Recipient {
		if noteErr := m.store.NoteEmailFailed(noteCtx, store.NoteEmailFailedParams{At: at, Code: failure.Code}); noteErr != nil {
			m.logger.Error("email status not written", "error", noteErr)
		}
	}
	m.logger.Warn("email not sent", "kind", message.Kind, "code", failure.Code)
	return failure
}

// take counts one send against the limits; false when one is reached.
func (m *Mailer) take(now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	keep := m.sent[:0]
	for _, at := range m.sent {
		if now.Sub(at) < time.Hour {
			keep = append(keep, at)
		}
	}
	m.sent = keep
	if len(m.sent) >= MaxPerHour {
		return false
	}
	lastMinute := 0
	for _, at := range m.sent {
		if now.Sub(at) < time.Minute {
			lastMinute++
		}
	}
	if lastMinute >= MaxPerMinute {
		return false
	}
	m.sent = append(m.sent, now)
	return true
}

// State is email's status for Status and ./setup.sh status.
type State struct {
	Configured    bool
	State         string // off, invalid, idle, ok, failing
	Problem       string
	LastSuccessAt *time.Time
	LastFailureAt *time.Time
	FailingSince  *time.Time
	LastErrorCode string
	LastError     string
}

// Status reads the status; never an address, a message, or the password.
func (m *Mailer) Status(ctx context.Context) (State, error) {
	if !m.Configured() {
		if m.problem != "" {
			return State{State: "invalid", Problem: m.problem}, nil
		}
		return State{State: "off"}, nil
	}
	row, err := m.store.GetEmailStatus(ctx)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return State{}, err
	}
	state := State{
		Configured:    true,
		State:         "idle",
		LastSuccessAt: timeOf(row.LastSuccessAt),
		LastFailureAt: timeOf(row.LastFailureAt),
		FailingSince:  timeOf(row.FailingSince),
	}
	if row.LastErrorCode != nil {
		state.LastErrorCode = *row.LastErrorCode
		state.LastError = Explain(*row.LastErrorCode)
	}
	switch {
	case state.FailingSince != nil:
		state.State = "failing"
	case state.LastSuccessAt != nil:
		state.State = "ok"
	}
	return state, nil
}

// Explain says what a failure code means, for the status (which keeps only
// the code).
func Explain(code string) string {
	switch code {
	case CodeConnect:
		return "Life OS could not connect to the mail server: check LIFE_OS_SMTP_HOST and LIFE_OS_SMTP_PORT."
	case CodeTLS:
		return "The secure connection to the mail server failed: check LIFE_OS_SMTP_TLS and the port."
	case CodeAuth:
		return "The mail server refused the user name or password."
	case CodeSender:
		return "The mail server refused the From address (LIFE_OS_SMTP_FROM)."
	case CodeMessage:
		return "The mail server refused the message."
	case CodeTimeout:
		return "The mail server did not answer in time."
	case CodeRecipient:
		return "The mail server refused the recipient address."
	}
	return "The email could not be sent."
}

func timeOf(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	at := value.Time.UTC()
	return &at
}

// TestMessage is the test email ./setup.sh email sends.
func (m *Mailer) TestMessage(to string) Message {
	server := m.config.ServerURL
	if server == "" {
		server = "this server"
	}
	when := m.now().UTC().Format("2006-01-02 15:04 UTC")
	lines := []string{
		"This is a test email from Life OS at " + server + ".",
		"",
		"If you are reading it, Life OS can send email: verification links, password reset links, sign-in codes, and security alerts.",
		"",
		"Sent " + when + ".",
	}
	var body strings.Builder
	body.WriteString(`<!doctype html><html><body style="font-family:sans-serif;line-height:1.5;color:#111">`)
	for _, line := range lines {
		if line == "" {
			continue
		}
		body.WriteString("<p>" + html.EscapeString(line) + "</p>")
	}
	body.WriteString("</body></html>")
	return Message{
		To:      to,
		Subject: "Life OS can send email",
		Text:    strings.Join(lines, "\n") + "\n",
		HTML:    body.String(),
		Kind:    "test",
	}
}
