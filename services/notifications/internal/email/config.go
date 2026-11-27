// Package email sends Life OS's emails through the owner's SMTP provider
// (docs/install/email.md): any provider, over implicit TLS, STARTTLS, or (for
// a relay on the owner's own network) none. Email is optional: without a
// host it is off, and every email option in the Dashboard is hidden.
//
// The password stays in the process's settings: it is never stored, logged,
// returned, or put into an error. Logs carry an email's kind and outcome
// code, never an address or its content.
package email

import (
	"fmt"
	"net"
	"net/mail"
	"strconv"
	"strings"
	"unicode"
)

// Mode is how the connection to the SMTP server is encrypted.
type Mode string

const (
	// ModeTLS is implicit TLS from the first byte (SMTPS, usually port 465).
	ModeTLS Mode = "tls"
	// ModeSTARTTLS upgrades a plain connection, which must offer STARTTLS
	// (usually port 587); a server that does not is refused.
	ModeSTARTTLS Mode = "starttls"
	// ModeNone is no encryption, for a relay on the owner's own network
	// only (usually port 25), and then without a user or password.
	ModeNone Mode = "none"
)

// DefaultFromName is the sender's name when none is set.
const DefaultFromName = "Life OS"

// Settings are the raw values from the environment (LIFE_OS_SMTP_* in
// .env, NOTIFICATIONS_SMTP_* in the container).
type Settings struct {
	Host     string
	Port     string
	TLS      string
	Username string
	Password string
	From     string
	FromName string
	// LinkBaseURL is Life OS's public URL: the test email names it, and
	// its host name greets the SMTP server (EHLO).
	LinkBaseURL string
}

// Config is a usable configuration. Its String never shows the password.
type Config struct {
	Host     string
	Port     int
	Mode     Mode
	Username string
	// password is a function, so that no printing of a Config (or of a
	// struct holding one, %#v included) ever shows it.
	password func() string
	From     string
	FromName string
	// HELO is the name Life OS greets the server with.
	HELO string
	// ServerURL is Life OS's public URL, for the test email.
	ServerURL string
}

// Password is the SMTP password (or API key), for the sender only.
func (c Config) Password() string {
	if c.password == nil {
		return ""
	}
	return c.password()
}

// GoString hides the password from %#v as String does.
func (c Config) GoString() string { return c.String() }

func (c Config) String() string {
	user := "none"
	if c.Username != "" {
		user = "set"
	}
	return fmt.Sprintf("smtp %s:%d (%s, user %s)", c.Host, c.Port, c.Mode, user)
}

// Set reports whether email is meant to be on: a host is set.
func (s Settings) Set() bool { return strings.TrimSpace(s.Host) != "" }

// Parse checks the settings. Without a host email is off: ok is false and
// problem is empty. With settings that cannot work, ok is false and problem
// says why in plain words (never with the password); the service keeps
// running without email, and Status says so.
func Parse(s Settings) (config Config, ok bool, problem string) {
	host := strings.TrimSpace(s.Host)
	if host == "" {
		return Config{}, false, ""
	}
	fail := func(format string, args ...any) (Config, bool, string) {
		return Config{}, false, fmt.Sprintf(format, args...)
	}
	if !validHost(host) {
		return fail("LIFE_OS_SMTP_HOST must be the mail server's host name or address, such as smtp.example.com")
	}
	mode := Mode(strings.ToLower(strings.TrimSpace(s.TLS)))
	if mode == "" {
		mode = ModeSTARTTLS
	}
	switch mode {
	case ModeTLS, ModeSTARTTLS, ModeNone:
	default:
		return fail("LIFE_OS_SMTP_TLS must be tls, starttls, or none")
	}
	port := defaultPort(mode)
	if value := strings.TrimSpace(s.Port); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 65535 {
			return fail("LIFE_OS_SMTP_PORT must be a port number from 1 to 65535")
		}
		port = parsed
	}
	username := strings.TrimSpace(s.Username)
	password := s.Password
	if hasControl(username) || strings.ContainsAny(password, "\r\n\x00") {
		return fail("LIFE_OS_SMTP_USERNAME and LIFE_OS_SMTP_SECRET must be one line each")
	}
	if username == "" && password != "" {
		return fail("LIFE_OS_SMTP_SECRET is set without LIFE_OS_SMTP_USERNAME: set the user too, or leave both empty")
	}
	if mode == ModeNone && username != "" {
		return fail("with LIFE_OS_SMTP_TLS=none Life OS sends no user or password: use starttls or tls, or leave LIFE_OS_SMTP_USERNAME empty for a relay that needs none")
	}
	from, err := ParseAddress(s.From)
	if err != nil {
		return fail("LIFE_OS_SMTP_FROM must be the address emails come from, such as lifeos@example.com")
	}
	name := strings.TrimSpace(s.FromName)
	if name == "" {
		name = DefaultFromName
	}
	if hasControl(name) || len(name) > 100 {
		return fail("LIFE_OS_SMTP_FROM_NAME must be one line of at most 100 characters")
	}
	serverURL := strings.TrimRight(strings.TrimSpace(s.LinkBaseURL), "/")
	return Config{
		Host:      host,
		Port:      port,
		Mode:      mode,
		Username:  username,
		password:  func() string { return password },
		From:      from,
		FromName:  name,
		HELO:      heloName(serverURL),
		ServerURL: serverURL,
	}, true, ""
}

func defaultPort(mode Mode) int {
	switch mode {
	case ModeTLS:
		return 465
	case ModeNone:
		return 25
	default:
		return 587
	}
}

// validHost accepts a DNS name (letters, digits, '-', '.', '_') or an IP
// address, as a single token.
func validHost(host string) bool {
	if len(host) > 253 {
		return false
	}
	if net.ParseIP(strings.Trim(host, "[]")) != nil {
		return true
	}
	for _, r := range host {
		if !(r == '.' || r == '-' || r == '_' || r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r))) {
			return false
		}
	}
	return !strings.HasPrefix(host, "-") && !strings.HasPrefix(host, ".")
}

// heloName is the public URL's host name, when it is a name with a dot,
// else "localhost" (go-mail's default): some servers dislike an address.
func heloName(serverURL string) string {
	host := serverURL
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	if i := strings.IndexAny(host, "/?#"); i >= 0 {
		host = host[:i]
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if host == "" || net.ParseIP(strings.Trim(host, "[]")) != nil || !strings.Contains(host, ".") || !validHost(host) {
		return "localhost"
	}
	return strings.ToLower(host)
}

// ParseAddress accepts one bare email address (no name, no list) and
// returns it trimmed.
func ParseAddress(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 254 || hasControl(value) || strings.ContainsAny(value, "<>,; \t\"") {
		return "", fmt.Errorf("not an email address")
	}
	parsed, err := mail.ParseAddress(value)
	if err != nil || parsed.Address != value || parsed.Name != "" {
		return "", fmt.Errorf("not an email address")
	}
	at := strings.LastIndex(value, "@")
	if at < 1 || at == len(value)-1 {
		return "", fmt.Errorf("not an email address")
	}
	return value, nil
}

func hasControl(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}
