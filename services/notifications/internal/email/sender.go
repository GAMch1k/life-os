package email

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/textproto"
	"strings"
	"time"

	mail "github.com/wneessen/go-mail"
)

// Message is one email to one address: plain text, with an optional HTML
// alternative of the same text. Kind says what it is, for the log.
type Message struct {
	To      string
	Subject string
	Text    string
	HTML    string
	Kind    string
}

// Failure is an email that did not go out, in words a person can act on.
// Neither the code nor the message carries the password, an address, or
// the server's own reply text (which may quote the address).
type Failure struct {
	// Code is one of the email_* codes (docs/install/email.md).
	Code string
	// Message says what happened and what to check.
	Message string
	// Recipient is true when only this recipient was refused: the server
	// and the settings work.
	Recipient bool
}

func (f *Failure) Error() string { return f.Code + ": " + f.Message }

// The failure codes.
const (
	CodeConnect   = "email_connect_failed"
	CodeTLS       = "email_tls_failed"
	CodeAuth      = "email_auth_failed"
	CodeSender    = "email_sender_refused"
	CodeRecipient = "email_recipient_refused"
	CodeMessage   = "email_message_refused"
	CodeTimeout   = "email_timeout"
	CodeFailed    = "email_failed"
)

// Sender sends through one SMTP server. Each send opens its own connection:
// Life OS sends a few emails a day, and a fresh connection never finds a
// server that dropped an idle one.
type Sender struct {
	config  Config
	timeout time.Duration
	roots   *x509.CertPool
}

// Option changes a Sender, for tests.
type Option func(*Sender)

// WithRootCAs trusts these roots instead of the system's.
func WithRootCAs(roots *x509.CertPool) Option {
	return func(s *Sender) { s.roots = roots }
}

// WithTimeout bounds connecting and each command (default 15 s).
func WithTimeout(timeout time.Duration) Option {
	return func(s *Sender) { s.timeout = timeout }
}

// NewSender sends with config.
func NewSender(config Config, options ...Option) *Sender {
	s := &Sender{config: config, timeout: 15 * time.Second}
	for _, option := range options {
		option(s)
	}
	return s
}

// Config is the sender's configuration (its String hides the password).
func (s *Sender) Config() Config { return s.config }

// Send delivers message to the SMTP server. It returns nil once the server
// accepted it, or a *Failure.
func (s *Sender) Send(ctx context.Context, message Message) error {
	msg, err := s.compose(message)
	if err != nil {
		return err
	}
	client, err := mail.NewClient(s.config.Host, s.options()...)
	if err != nil {
		return &Failure{Code: CodeFailed, Message: "The email settings cannot be used: " + safeText(err)}
	}
	if err := client.DialAndSendWithContext(ctx, msg); err != nil {
		return classify(ctx, err, s.config)
	}
	return nil
}

func (s *Sender) options() []mail.Option {
	tlsConfig := &tls.Config{
		ServerName: s.config.Host,
		MinVersion: tls.VersionTLS12,
		RootCAs:    s.roots,
	}
	options := []mail.Option{
		mail.WithPort(s.config.Port),
		mail.WithTimeout(s.timeout),
		mail.WithTLSConfig(tlsConfig),
		mail.WithHELO(s.config.HELO),
	}
	switch s.config.Mode {
	case ModeTLS:
		options = append(options, mail.WithSSL())
	case ModeSTARTTLS:
		options = append(options, mail.WithTLSPolicy(mail.TLSMandatory))
	default:
		options = append(options, mail.WithTLSPolicy(mail.NoTLS))
	}
	if s.config.Username != "" {
		// The strongest mechanism the server offers; PLAIN and LOGIN only
		// over TLS (Parse refuses a user without it anyway).
		options = append(options,
			mail.WithSMTPAuth(mail.SMTPAuthAutoDiscover),
			mail.WithUsername(s.config.Username),
			mail.WithPassword(s.config.Password()),
		)
	}
	return options
}

// compose builds the MIME message: UTF-8, quoted-printable, plain text
// first, HTML as the alternative; a Message-ID, a Date, and
// Auto-Submitted (RFC 3834), so that vacation replies stay away.
func (s *Sender) compose(message Message) (*mail.Msg, error) {
	to, err := ParseAddress(message.To)
	if err != nil {
		return nil, &Failure{Code: CodeRecipient, Message: "The recipient is not an email address.", Recipient: true}
	}
	msg := mail.NewMsg(mail.WithNoDefaultUserAgent())
	if err := msg.FromFormat(s.config.FromName, s.config.From); err != nil {
		return nil, &Failure{Code: CodeSender, Message: "LIFE_OS_SMTP_FROM is not an address the email can come from."}
	}
	if err := msg.To(to); err != nil {
		return nil, &Failure{Code: CodeRecipient, Message: "The recipient is not an email address.", Recipient: true}
	}
	msg.Subject(message.Subject)
	msg.SetMessageID()
	msg.SetDate()
	msg.SetGenHeader(mail.Header("Auto-Submitted"), "auto-generated")
	msg.SetGenHeader(mail.HeaderXAutoResponseSuppress, "All")
	msg.SetBodyString(mail.TypeTextPlain, message.Text)
	if message.HTML != "" {
		msg.AddAlternativeString(mail.TypeTextHTML, message.HTML)
	}
	return msg, nil
}

// classify turns a send's error into a Failure. go-mail's own errors may
// quote the recipient (SendError lists them) or the server's reply, so
// only their kind and the SMTP reply code are kept.
func classify(ctx context.Context, err error, config Config) *Failure {
	where := fmt.Sprintf("%s:%d", config.Host, config.Port)
	var sendErr *mail.SendError
	if errors.As(err, &sendErr) {
		code := sendErr.ErrorCode()
		switch sendErr.Reason {
		case mail.ErrSMTPMailFrom:
			return &Failure{Code: CodeSender, Message: "The mail server refused the From address" + reply(code) +
				": LIFE_OS_SMTP_FROM must be an address your provider lets you send from."}
		case mail.ErrSMTPRcptTo:
			return &Failure{Code: CodeRecipient, Recipient: code < 400 || code >= 500,
				Message: "The mail server refused the recipient address" + reply(code) + "."}
		case mail.ErrSMTPData, mail.ErrSMTPDataClose, mail.ErrWriteContent, mail.ErrNoUnencoded:
			return &Failure{Code: CodeMessage, Message: "The mail server refused the message" + reply(code) + "."}
		}
		if timedOut(ctx, err) {
			return timeoutFailure(where)
		}
		return &Failure{Code: CodeFailed, Message: "The mail server did not take the email" + reply(code) + "."}
	}
	if timedOut(ctx, err) {
		return timeoutFailure(where)
	}
	text := err.Error()
	switch {
	case strings.Contains(text, "does not support STARTTLS"):
		return &Failure{Code: CodeTLS, Message: fmt.Sprintf("The mail server at %s does not offer STARTTLS: use LIFE_OS_SMTP_TLS=tls (usually port 465), or the port your provider gives for STARTTLS (usually 587).", where)}
	case strings.Contains(text, "SMTP AUTH failed"):
		var protoErr *textproto.Error
		code := 0
		if errors.As(err, &protoErr) {
			code = protoErr.Code
		}
		return &Failure{Code: CodeAuth, Message: "The mail server refused the user name or password" + reply(code) +
			": check LIFE_OS_SMTP_USERNAME and LIFE_OS_SMTP_SECRET (many providers want an app password or an API key)."}
	case strings.Contains(text, "does not support SMTP AUTH"), errors.Is(err, mail.ErrNoSupportedAuthDiscovered):
		return &Failure{Code: CodeAuth, Message: fmt.Sprintf("The mail server at %s offers no way to sign in that Life OS can use: check the port and LIFE_OS_SMTP_TLS, or leave the user empty for a relay that needs none.", where)}
	}
	if reason := tlsReason(err); reason != "" {
		return &Failure{Code: CodeTLS, Message: fmt.Sprintf("The secure connection to the mail server at %s failed (%s): check LIFE_OS_SMTP_TLS and the port (tls is usually 465, starttls 587).", where, reason)}
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return &Failure{Code: CodeConnect, Message: fmt.Sprintf("The mail server's name %s does not resolve: check LIFE_OS_SMTP_HOST.", config.Host)}
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return &Failure{Code: CodeConnect, Message: fmt.Sprintf("Life OS could not connect to the mail server at %s (%s): check LIFE_OS_SMTP_HOST and LIFE_OS_SMTP_PORT, and that this server may connect out on that port.", where, dialReason(opErr))}
	}
	var protoErr *textproto.Error
	if errors.As(err, &protoErr) {
		return &Failure{Code: CodeFailed, Message: "The mail server refused the connection" + reply(protoErr.Code) + "."}
	}
	return &Failure{Code: CodeFailed, Message: fmt.Sprintf("The email could not be sent through %s.", where)}
}

func timeoutFailure(where string) *Failure {
	return &Failure{Code: CodeTimeout, Message: fmt.Sprintf("The mail server at %s did not answer in time: check LIFE_OS_SMTP_PORT and LIFE_OS_SMTP_TLS (tls on a starttls port waits forever).", where)}
}

func timedOut(ctx context.Context, err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// reply is " (SMTP 535)" for a server's reply code, else nothing.
func reply(code int) string {
	if code < 200 || code > 599 {
		return ""
	}
	return fmt.Sprintf(" (SMTP %d)", code)
}

// tlsReason names a TLS failure's kind, or "" when err is none.
func tlsReason(err error) string {
	var unknown x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	var record tls.RecordHeaderError
	var alert tls.AlertError
	var verify *tls.CertificateVerificationError
	switch {
	case errors.As(err, &unknown):
		return "its certificate is not signed by a known authority"
	case errors.As(err, &hostname):
		return "its certificate is for another name"
	case errors.As(err, &invalid):
		return "its certificate is expired or not valid"
	case errors.As(err, &verify):
		return "its certificate is not trusted"
	case errors.As(err, &record):
		return "it does not speak TLS on this port"
	case errors.As(err, &alert):
		return "it refused the TLS handshake"
	case strings.Contains(err.Error(), "tls:"):
		return "a TLS error"
	}
	return ""
}

func dialReason(err *net.OpError) string {
	text := err.Err.Error()
	switch {
	case strings.Contains(text, "connection refused"):
		return "connection refused"
	case strings.Contains(text, "no route to host"), strings.Contains(text, "network is unreachable"):
		return "no route to it"
	case err.Timeout():
		return "timed out"
	}
	return "the connection failed"
}

// safeText is an error's text without anything after a colon (where a
// library might quote a value) and at most 120 characters.
func safeText(err error) string {
	text := err.Error()
	if i := strings.Index(text, ":"); i > 0 {
		text = text[:i]
	}
	if len(text) > 120 {
		text = text[:120]
	}
	return text
}
