// Package google connects Calendar to the owner's Google account: the OAuth
// authorization (with a single-use state and PKCE), the encrypted refresh
// token and the access tokens refreshed from it, and the account's calendar
// list with the owner's choice of the calendars to sync. It talks to Google's
// OAuth and Calendar REST endpoints with plain net/http.
//
// Tokens and the client secret are never logged, returned, or put in errors.
package google

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// The scopes Life OS asks for: reading the calendar list (which calendars
// exist, and the account's address from the primary one), reading and
// writing events, and managing the calendars Life OS creates itself.
const (
	ScopeCalendarList = "https://www.googleapis.com/auth/calendar.calendarlist.readonly"
	ScopeEvents       = "https://www.googleapis.com/auth/calendar.events"
	ScopeAppCreated   = "https://www.googleapis.com/auth/calendar.app.created"
)

// Scopes are requested together, in this order.
var Scopes = []string{ScopeCalendarList, ScopeEvents, ScopeAppCreated}

// DefaultRedirectURI is the Dashboard route Google returns the owner to.
const DefaultRedirectURI = "https://lifeos.gamch1k.com/calendar/google/callback"

// TokenKeySize is the size of LIFE_OS_GOOGLE_TOKEN_KEY: an AES-256 key.
const TokenKeySize = 32

// Config is the OAuth client and the key that encrypts refresh tokens. The
// zero Config is "not configured".
type Config struct {
	ClientID     string
	ClientSecret string
	RedirectURI  string
	TokenKey     []byte
}

// Configured reports whether Google can be connected: a client and a valid
// token key are set.
func (c Config) Configured() bool {
	return c.ClientID != "" && c.ClientSecret != "" && len(c.TokenKey) == TokenKeySize && c.RedirectURI != ""
}

// NewConfig builds the configuration from the environment's values. Unset
// values leave Google unconfigured without an error; a partial or invalid
// configuration also leaves it unconfigured and is returned as an error to
// log (naming the variables, never their values), so Calendar still starts.
func NewConfig(clientID, clientSecret, tokenKey, redirectURI string) (Config, error) {
	clientID, clientSecret, tokenKey = strings.TrimSpace(clientID), strings.TrimSpace(clientSecret), strings.TrimSpace(tokenKey)
	redirectURI = strings.TrimSpace(redirectURI)
	if redirectURI == "" {
		redirectURI = DefaultRedirectURI
	}
	if clientID == "" && clientSecret == "" && tokenKey == "" {
		return Config{}, nil
	}
	var missing []string
	for _, setting := range []struct{ name, value string }{
		{"LIFE_OS_GOOGLE_CLIENT_ID", clientID},
		{"LIFE_OS_GOOGLE_CLIENT_SECRET", clientSecret},
		{"LIFE_OS_GOOGLE_TOKEN_KEY", tokenKey},
	} {
		if setting.value == "" {
			missing = append(missing, setting.name)
		}
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("Google Calendar is not configured: %s %s not set", strings.Join(missing, " and "), plural(len(missing), "is", "are"))
	}
	key, err := ParseTokenKey(tokenKey)
	if err != nil {
		return Config{}, fmt.Errorf("Google Calendar is not configured: %w", err)
	}
	if err := validRedirectURI(redirectURI); err != nil {
		return Config{}, fmt.Errorf("Google Calendar is not configured: %w", err)
	}
	return Config{ClientID: clientID, ClientSecret: clientSecret, RedirectURI: redirectURI, TokenKey: key}, nil
}

// ParseTokenKey decodes LIFE_OS_GOOGLE_TOKEN_KEY: 32 bytes in base64
// (standard or URL-safe, padded or not), as `openssl rand -base64 32` prints.
func ParseTokenKey(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if key, err := encoding.DecodeString(value); err == nil {
			if len(key) != TokenKeySize {
				return nil, fmt.Errorf("LIFE_OS_GOOGLE_TOKEN_KEY must be %d bytes in base64 (openssl rand -base64 32), not %d bytes", TokenKeySize, len(key))
			}
			return key, nil
		}
	}
	return nil, errors.New("LIFE_OS_GOOGLE_TOKEN_KEY must be base64 (openssl rand -base64 32)")
}

func validRedirectURI(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return errors.New("LIFE_OS_GOOGLE_REDIRECT_URI must be an absolute URL such as " + DefaultRedirectURI)
	}
	local := parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1"
	if parsed.Scheme != "https" && (parsed.Scheme != "http" || !local) {
		return errors.New("LIFE_OS_GOOGLE_REDIRECT_URI must use https (http only for localhost)")
	}
	return nil
}

// AuthURL is Google's consent page for this client: offline access with a
// forced consent (so Google issues a refresh token every time), the state
// that ties the answer to this authorization, and the PKCE challenge.
// loginHint, when not empty, preselects the account (a reconnect).
func (c Config) AuthURL(endpoint, state, challenge, loginHint string) string {
	query := url.Values{
		"client_id":             {c.ClientID},
		"redirect_uri":          {c.RedirectURI},
		"response_type":         {"code"},
		"scope":                 {strings.Join(Scopes, " ")},
		"access_type":           {"offline"},
		"prompt":                {"consent"},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	if loginHint != "" {
		query.Set("login_hint", loginHint)
	}
	separator := "?"
	if strings.Contains(endpoint, "?") {
		separator = "&"
	}
	return endpoint + separator + query.Encode()
}

// MissingScopes lists the requested scopes that granted lacks.
func MissingScopes(granted []string) []string {
	have := make(map[string]bool, len(granted))
	for _, scope := range granted {
		have[scope] = true
	}
	missing := []string{}
	for _, scope := range Scopes {
		if !have[scope] {
			missing = append(missing, scope)
		}
	}
	return missing
}

func plural(count int, one, many string) string {
	if count == 1 {
		return one
	}
	return many
}
