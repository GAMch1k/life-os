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
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

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

	// Events are each calendar's events (see PutEvent); EventsStatus, when
	// set, fails events.list with this HTTP status; EventsPageSize is the
	// page size of events.list (default 250).
	events         map[string]*eventLog
	EventsStatus   int
	EventsPageSize int
	// EventRequests are the events.list queries received, by calendar.
	EventRequests map[string][]url.Values
}

// eventLog is one calendar's events with a change sequence: a sync token
// "sync-N" asks for the events changed after change N.
type eventLog struct {
	timeZone string
	defaults []google.Reminder
	seq      int
	// minSeq: tokens before it have expired (410).
	minSeq int
	order  []string
	events map[string]*loggedEvent
}

type loggedEvent struct {
	event   google.Event
	changed int
}

// New starts a fake Google serving calendars; it stops with the test.
func New(t *testing.T, calendars ...Calendar) *Server {
	t.Helper()
	server := &Server{
		Calendars: calendars, codes: map[string]string{}, refresh: map[string]bool{}, access: map[string]bool{}, PageSize: 2,
		events: map[string]*eventLog{}, EventRequests: map[string][]url.Values{},
	}
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
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/calendar/v3/calendars/") && strings.HasSuffix(r.URL.Path, "/events"):
		calendarID, err := url.PathUnescape(strings.TrimSuffix(strings.TrimPrefix(r.URL.EscapedPath(), "/calendar/v3/calendars/"), "/events"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		s.listEvents(w, r, calendarID)
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

func (s *Server) log(calendarID string) *eventLog {
	log := s.events[calendarID]
	if log == nil {
		log = &eventLog{timeZone: "Europe/Berlin", events: map[string]*loggedEvent{}}
		s.events[calendarID] = log
	}
	return log
}

// SetCalendarDetails sets the time zone and default reminders events.list
// reports for a calendar.
func (s *Server) SetCalendarDetails(calendarID, timeZone string, defaults ...google.Reminder) {
	s.mu.Lock()
	defer s.mu.Unlock()
	log := s.log(calendarID)
	log.timeZone, log.defaults = timeZone, defaults
}

// PutEvent adds or replaces (by id) an event of a calendar, as a change.
func (s *Server) PutEvent(calendarID string, events ...google.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	log := s.log(calendarID)
	for _, event := range events {
		if event.Status == "" {
			event.Status = "confirmed"
		}
		log.seq++
		if _, known := log.events[event.ID]; !known {
			log.order = append(log.order, event.ID)
		}
		log.events[event.ID] = &loggedEvent{event: event, changed: log.seq}
	}
}

// DeleteEvent deletes an event as Google does: it stays listed (with
// showDeleted) as cancelled, with only its identity.
func (s *Server) DeleteEvent(calendarID string, ids ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	log := s.log(calendarID)
	for _, id := range ids {
		stored, known := log.events[id]
		if !known {
			continue
		}
		log.seq++
		gone := google.Event{ID: id, Status: "cancelled", RecurringEventID: stored.event.RecurringEventID, OriginalStartTime: stored.event.OriginalStartTime}
		log.events[id] = &loggedEvent{event: gone, changed: log.seq}
	}
}

// ForgetEvent drops an event without a trace, as Google eventually does
// with deleted events: a full listing no longer has it.
func (s *Server) ForgetEvent(calendarID string, ids ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	log := s.log(calendarID)
	for _, id := range ids {
		delete(log.events, id)
	}
}

// ExpireSyncTokens makes Google refuse every sync token issued so far (410).
func (s *Server) ExpireSyncTokens(calendarID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	log := s.log(calendarID)
	log.minSeq = log.seq + 1
}

// Requests returns the events.list queries received for a calendar.
func (s *Server) Requests(calendarID string) []url.Values {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]url.Values(nil), s.EventRequests[calendarID]...)
}

func (s *Server) listEvents(w http.ResponseWriter, r *http.Request, calendarID string) {
	query := r.URL.Query()
	s.EventRequests[calendarID] = append(s.EventRequests[calendarID], query)
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !s.access[token] {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]any{"code": 401, "message": "Request had invalid authentication credentials."}})
		return
	}
	if s.EventsStatus != 0 {
		writeJSON(w, s.EventsStatus, map[string]any{"error": map[string]any{"code": s.EventsStatus, "message": "Backend Error"}})
		return
	}
	known := false
	for _, calendar := range s.Calendars {
		known = known || calendar.ID == calendarID
	}
	if !known {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]any{"code": 404, "message": "Not Found"}})
		return
	}
	if query.Get("singleEvents") != "false" || query.Get("showDeleted") != "true" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"code": 400, "message": "expected singleEvents=false and showDeleted=true"}})
		return
	}
	log := s.log(calendarID)
	after := -1
	if syncToken := query.Get("syncToken"); syncToken != "" {
		if query.Has("timeMin") {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"code": 400, "message": "syncToken and timeMin together"}})
			return
		}
		if _, err := fmt.Sscanf(syncToken, "sync-%d", &after); err != nil || after < log.minSeq {
			writeJSON(w, http.StatusGone, map[string]any{"error": map[string]any{"code": 410, "message": "Sync token is no longer valid, a full sync is required."}})
			return
		}
	}
	var timeMin time.Time
	if raw := query.Get("timeMin"); raw != "" {
		timeMin, _ = time.Parse(time.RFC3339, raw)
	}
	var matching []google.Event
	for _, id := range log.order {
		stored, ok := log.events[id]
		if !ok || stored.changed <= after {
			continue
		}
		event := stored.event
		if after < 0 && event.End != nil && event.End.DateTime != "" && len(event.Recurrence) == 0 && !timeMin.IsZero() {
			if end, err := time.Parse(time.RFC3339, event.End.DateTime); err == nil && !end.After(timeMin) {
				continue
			}
		}
		matching = append(matching, event)
	}
	size := s.EventsPageSize
	if size <= 0 {
		size = 250
	}
	start := 0
	if pageToken := query.Get("pageToken"); pageToken != "" {
		_, _ = fmt.Sscanf(pageToken, "events-%d", &start)
	}
	end := min(start+size, len(matching))
	if start > end {
		start = end
	}
	page := map[string]any{"kind": "calendar#events", "summary": calendarID, "timeZone": log.timeZone, "items": matching[start:end]}
	if log.defaults != nil {
		page["defaultReminders"] = log.defaults
	} else {
		page["defaultReminders"] = []google.Reminder{}
	}
	if end < len(matching) {
		page["nextPageToken"] = fmt.Sprintf("events-%d", end)
	} else {
		page["nextSyncToken"] = fmt.Sprintf("sync-%d", log.seq)
	}
	writeJSON(w, http.StatusOK, page)
}
