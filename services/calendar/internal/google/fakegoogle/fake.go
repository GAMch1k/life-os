// Package fakegoogle is an in-process stand-in for Google's OAuth and
// Calendar endpoints, for tests. It never reaches Google.
package fakegoogle

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/GAMch1k/life-os/services/calendar/internal/google"
)

// Client credentials the fake accepts.
const (
	ClientID     = "fake-client-id.apps.googleusercontent.com"
	ClientSecret = "fake-client-secret"
)

// Calendar is one entry of the fake account's calendar list.
type Calendar struct {
	ID, Summary, Color, AccessRole string
	Primary                        bool
}

// Server is the fake Google. Tests change its fields under Lock/Unlock (or
// with the helpers) between requests.
type Server struct {
	*httptest.Server
	mu sync.Mutex

	// Calendars is the account's calendar list.
	Calendars []Calendar
	// GrantedScopes are returned with tokens; nil grants every scope asked.
	GrantedScopes []string
	// RefreshError, when set, is the OAuth error code a refresh returns
	// (invalid_grant: the refresh token expired or was revoked).
	RefreshError string
	// TokenStatus, when set, fails the token endpoint with this HTTP status.
	TokenStatus int
	// ListStatus, when set, fails calendar list requests with this status.
	ListStatus int
	// OmitRefreshToken makes the code exchange issue no refresh token.
	OmitRefreshToken bool
	// RotateRefreshToken makes refreshes issue a new refresh token.
	RotateRefreshToken bool
	// ExpiresIn is the access token lifetime in seconds (default 3600).
	ExpiresIn int

	codes       map[string]string // code -> PKCE challenge
	refresh     map[string]bool   // live refresh tokens
	access      map[string]bool   // live access tokens
	serial      int
	Exchanges   int
	Refreshes   int
	Lists       int
	Revoked     []string
	PageSize    int
	LastRefresh string
}

// New starts a fake Google serving calendars; it stops with the test.
func New(t *testing.T, calendars ...Calendar) *Server {
	t.Helper()
	server := &Server{Calendars: calendars, codes: map[string]string{}, refresh: map[string]bool{}, access: map[string]bool{}, PageSize: 2}
	server.Server = httptest.NewServer(http.HandlerFunc(server.serve))
	t.Cleanup(server.Close)
	return server
}

// Endpoints point a google.Client at the fake.
func (s *Server) Endpoints() google.Endpoints {
	return google.Endpoints{
		AuthURL: s.URL + "/o/oauth2/v2/auth", TokenURL: s.URL + "/token", RevokeURL: s.URL + "/revoke", CalendarAPI: s.URL + "/calendar/v3",
	}
}

// Lock and Unlock guard changes to the fields.
func (s *Server) Lock()   { s.mu.Lock() }
func (s *Server) Unlock() { s.mu.Unlock() }

// IssueCode is what Google does when the owner consents: it returns a code
// bound to the authorization's PKCE challenge.
func (s *Server) IssueCode(challenge string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.serial++
	code := fmt.Sprintf("code-%d", s.serial)
	s.codes[code] = challenge
	return code
}

// RevokeAll makes every issued token invalid, as when the owner removes
// Life OS from their Google Account.
func (s *Server) RevokeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refresh, s.access = map[string]bool{}, map[string]bool{}
}

// Tokens lists every token the fake issued, live or not, so tests can check
// that none leaks.
func (s *Server) Tokens() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var tokens []string
	for token := range s.refresh {
		tokens = append(tokens, token)
	}
	for token := range s.access {
		tokens = append(tokens, token)
	}
	tokens = append(tokens, s.Revoked...)
	return tokens
}

// Counts returns the exchange, refresh, and list request counts.
func (s *Server) Counts() (exchanges, refreshes, lists int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Exchanges, s.Refreshes, s.Lists
}

// RevokedTokens returns the tokens revoked through the revoke endpoint.
func (s *Server) RevokedTokens() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.Revoked...)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/token":
		s.token(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/revoke":
		_ = r.ParseForm()
		token := r.PostForm.Get("token")
		if !s.refresh[token] && !s.access[token] {
			oauthError(w, http.StatusBadRequest, "invalid_token", "Token expired or revoked")
			return
		}
		s.Revoked = append(s.Revoked, token)
		delete(s.refresh, token)
		delete(s.access, token)
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodGet && r.URL.Path == "/calendar/v3/users/me/calendarList":
		s.calendarList(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
		oauthError(w, http.StatusBadRequest, "invalid_request", "form expected")
		return
	}
	form := r.PostForm
	if s.TokenStatus != 0 {
		w.WriteHeader(s.TokenStatus)
		return
	}
	if form.Get("client_id") != ClientID || form.Get("client_secret") != ClientSecret {
		oauthError(w, http.StatusUnauthorized, "invalid_client", "The OAuth client was not found.")
		return
	}
	scopes := strings.Join(google.Scopes, " ")
	if s.GrantedScopes != nil {
		scopes = strings.Join(s.GrantedScopes, " ")
	}
	response := map[string]any{"token_type": "Bearer", "scope": scopes, "expires_in": s.expiresIn()}
	switch form.Get("grant_type") {
	case "authorization_code":
		s.Exchanges++
		challenge, ok := s.codes[form.Get("code")]
		delete(s.codes, form.Get("code"))
		if !ok {
			oauthError(w, http.StatusBadRequest, "invalid_grant", "Malformed auth code.")
			return
		}
		if form.Get("redirect_uri") == "" {
			oauthError(w, http.StatusBadRequest, "redirect_uri_mismatch", "Bad Request")
			return
		}
		sum := sha256.Sum256([]byte(form.Get("code_verifier")))
		if base64.RawURLEncoding.EncodeToString(sum[:]) != challenge {
			oauthError(w, http.StatusBadRequest, "invalid_grant", "Invalid code verifier.")
			return
		}
		response["access_token"] = s.newToken("access", s.access)
		if !s.OmitRefreshToken {
			response["refresh_token"] = s.newToken("refresh", s.refresh)
		}
	case "refresh_token":
		s.Refreshes++
		s.LastRefresh = form.Get("refresh_token")
		if s.RefreshError != "" {
			oauthError(w, http.StatusBadRequest, s.RefreshError, "Token has been expired or revoked.")
			return
		}
		if !s.refresh[form.Get("refresh_token")] {
			oauthError(w, http.StatusBadRequest, "invalid_grant", "Token has been expired or revoked.")
			return
		}
		response["access_token"] = s.newToken("access", s.access)
		if s.RotateRefreshToken {
			delete(s.refresh, form.Get("refresh_token"))
			response["refresh_token"] = s.newToken("refresh", s.refresh)
		}
	default:
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type", "")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) calendarList(w http.ResponseWriter, r *http.Request) {
	s.Lists++
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if s.ListStatus != 0 {
		writeJSON(w, s.ListStatus, map[string]any{"error": map[string]any{"code": s.ListStatus, "message": "Failure"}})
		return
	}
	if !s.access[token] {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]any{"code": 401, "message": "Request had invalid authentication credentials."}})
		return
	}
	start := 0
	if pageToken := r.URL.Query().Get("pageToken"); pageToken != "" {
		_, _ = fmt.Sscanf(pageToken, "page-%d", &start)
	}
	end := min(start+s.PageSize, len(s.Calendars))
	items := []map[string]any{}
	for _, calendar := range s.Calendars[start:end] {
		item := map[string]any{"id": calendar.ID, "summary": calendar.Summary, "backgroundColor": calendar.Color, "accessRole": calendar.AccessRole}
		if calendar.Primary {
			item["primary"] = true
		}
		items = append(items, item)
	}
	page := map[string]any{"kind": "calendar#calendarList", "items": items}
	if end < len(s.Calendars) {
		page["nextPageToken"] = fmt.Sprintf("page-%d", end)
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) newToken(kind string, live map[string]bool) string {
	s.serial++
	token := fmt.Sprintf("ya29.fake-%s-token-%d", kind, s.serial)
	live[token] = true
	return token
}

func (s *Server) expiresIn() int {
	if s.ExpiresIn > 0 {
		return s.ExpiresIn
	}
	return 3600
}

func oauthError(w http.ResponseWriter, status int, code, description string) {
	writeJSON(w, status, map[string]string{"error": code, "error_description": description})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
