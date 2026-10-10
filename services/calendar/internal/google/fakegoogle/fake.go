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

	// WriteStatus, when set, fails every write (event insert, patch, and
	// delete, calendar insert) with this HTTP status.
	WriteStatus int
	// writes are the writes received (see Writes).
	writes []Write
	// gone keeps a deleted event's last version, so a patch with status
	// confirmed restores it as Google does.
	gone map[string]google.Event
	// timeZones are the time zones of calendars created through the API.
	timeZones map[string]string
}

// Write is one write request the fake received.
type Write struct {
	Method, Calendar, EventID string
	// IfMatch is the etag the request sent, if any.
	IfMatch string
	// Body is the JSON body.
	Body map[string]any
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
		events: map[string]*eventLog{}, EventRequests: map[string][]url.Values{}, gone: map[string]google.Event{}, timeZones: map[string]string{},
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
	case r.Method == http.MethodPost && r.URL.Path == "/calendar/v3/calendars":
		s.insertCalendar(w, r)
	case strings.HasPrefix(r.URL.EscapedPath(), "/calendar/v3/calendars/"):
		s.calendarRequest(w, r)
	default:
		http.NotFound(w, r)
	}
}

// calendarRequest routes /calendars/{calendar}/events[/{event}[/instances]].
func (s *Server) calendarRequest(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.EscapedPath(), "/calendar/v3/calendars/"), "/")
	for index, part := range parts {
		unescaped, err := url.PathUnescape(part)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		parts[index] = unescaped
	}
	switch {
	case len(parts) == 2 && parts[1] == "events" && r.Method == http.MethodGet:
		s.listEvents(w, r, parts[0])
	case len(parts) == 2 && parts[1] == "events" && r.Method == http.MethodPost:
		s.insertEvent(w, r, parts[0])
	case len(parts) == 3 && parts[1] == "events":
		s.eventRequest(w, r, parts[0], parts[2])
	case len(parts) == 4 && parts[1] == "events" && parts[3] == "instances" && r.Method == http.MethodGet:
		s.instances(w, r, parts[0], parts[2])
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
		s.store(log, event)
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

// apiError answers like the Calendar API refusing a request.
func apiError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": status, "message": message}})
}

// Writes returns the write requests received so far.
func (s *Server) Writes() []Write {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Write(nil), s.writes...)
}

// Event returns a calendar's stored event (a deleted one is cancelled).
func (s *Server) Event(calendarID, id string) (google.Event, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, ok := s.log(calendarID).events[id]
	if !ok {
		return google.Event{}, false
	}
	return stored.event, true
}

// Events returns a calendar's stored events in the order they were added.
func (s *Server) Events(calendarID string) []google.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	log := s.log(calendarID)
	out := []google.Event{}
	for _, id := range log.order {
		if stored, ok := log.events[id]; ok {
			out = append(out, stored.event)
		}
	}
	return out
}

// ChangeEvent changes a stored event as the owner would in Google: a new
// etag and updated time, reported by the next listing.
func (s *Server) ChangeEvent(calendarID, id string, change func(*google.Event)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	log := s.log(calendarID)
	stored, ok := log.events[id]
	if !ok {
		return
	}
	event := stored.event
	change(&event)
	s.store(log, event)
}

// CalendarTimeZone is the time zone a calendar was created with.
func (s *Server) CalendarTimeZone(calendarID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.timeZones[calendarID]
}

// stamp gives an event a new etag and updated time (always later).
func (s *Server) stamp(event *google.Event) {
	s.serial++
	event.ETag = fmt.Sprintf("\"etag-%d\"", s.serial)
	event.Updated = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(s.serial) * time.Second).Format("2006-01-02T15:04:05.000Z")
}

// store saves a changed event as a change of the calendar.
func (s *Server) store(log *eventLog, event google.Event) google.Event {
	if event.Status == "" {
		event.Status = "confirmed"
	}
	s.stamp(&event)
	log.seq++
	if _, known := log.events[event.ID]; !known {
		log.order = append(log.order, event.ID)
	}
	log.events[event.ID] = &loggedEvent{event: event, changed: log.seq}
	return event
}

func (s *Server) calendar(calendarID string) (Calendar, bool) {
	for _, calendar := range s.Calendars {
		if calendar.ID == calendarID {
			return calendar, true
		}
	}
	return Calendar{}, false
}

// write checks a write: a known, writable calendar, a live access token,
// and no configured failure; it logs it and reports whether to go on.
func (s *Server) write(w http.ResponseWriter, r *http.Request, calendarID, eventID string, body map[string]any) bool {
	s.writes = append(s.writes, Write{Method: r.Method, Calendar: calendarID, EventID: eventID, IfMatch: r.Header.Get("If-Match"), Body: body})
	if !s.access[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")] {
		apiError(w, http.StatusUnauthorized, "Request had invalid authentication credentials.")
		return false
	}
	if s.WriteStatus != 0 {
		apiError(w, s.WriteStatus, "Backend Error")
		return false
	}
	if calendarID == "" {
		return true
	}
	calendar, ok := s.calendar(calendarID)
	if !ok {
		apiError(w, http.StatusNotFound, "Not Found")
		return false
	}
	if calendar.AccessRole != "owner" && calendar.AccessRole != "writer" {
		apiError(w, http.StatusForbidden, "Forbidden")
		return false
	}
	return true
}

func readBody(r *http.Request) (map[string]any, error) {
	body := map[string]any{}
	if r.Body == nil {
		return body, nil
	}
	err := json.NewDecoder(r.Body).Decode(&body)
	if err != nil && err.Error() == "EOF" {
		return map[string]any{}, nil
	}
	return body, err
}

func (s *Server) insertCalendar(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(r)
	if err != nil {
		apiError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if !s.write(w, r, "", "", body) {
		return
	}
	s.serial++
	id := fmt.Sprintf("lifeos%d@group.calendar.google.com", s.serial)
	summary, _ := body["summary"].(string)
	zone, _ := body["timeZone"].(string)
	s.Calendars = append(s.Calendars, Calendar{ID: id, Summary: summary, AccessRole: "owner"})
	s.timeZones[id] = zone
	s.log(id).timeZone = zone
	writeJSON(w, http.StatusOK, map[string]any{"kind": "calendar#calendar", "id": id, "summary": summary, "timeZone": zone})
}

// toEvent decodes a request body as an event.
func toEvent(body map[string]any) (google.Event, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return google.Event{}, err
	}
	var event google.Event
	err = json.Unmarshal(raw, &event)
	return event, err
}

func (s *Server) insertEvent(w http.ResponseWriter, r *http.Request, calendarID string) {
	body, err := readBody(r)
	if err != nil {
		apiError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if !s.write(w, r, calendarID, "", body) {
		return
	}
	event, err := toEvent(body)
	if err != nil || event.Start == nil || event.End == nil {
		apiError(w, http.StatusBadRequest, "Missing start or end time.")
		return
	}
	log := s.log(calendarID)
	if event.ID == "" {
		s.serial++
		event.ID = fmt.Sprintf("evt%d", s.serial)
	} else if _, exists := log.events[event.ID]; exists {
		apiError(w, http.StatusConflict, "The requested identifier already exists.")
		return
	}
	event.Status = "confirmed"
	writeJSON(w, http.StatusOK, s.store(log, event))
}

// lookup finds an event by id: stored, or an instance of a stored recurring
// event named as Google names instances (not stored until changed).
func (s *Server) lookup(log *eventLog, id string) (google.Event, bool) {
	if stored, ok := log.events[id]; ok {
		return stored.event, true
	}
	master, suffix, ok := strings.Cut(id, "_")
	if !ok {
		return google.Event{}, false
	}
	stored, ok := log.events[master]
	if !ok || len(stored.event.Recurrence) == 0 || stored.event.Cancelled() {
		return google.Event{}, false
	}
	return instanceOf(stored.event, suffix)
}

// instanceOf is the unchanged instance of master originally starting at
// suffix (20261012T070000Z, or 20261024 all-day).
func instanceOf(master google.Event, suffix string) (google.Event, bool) {
	instance := master
	instance.ID, instance.RecurringEventID, instance.Recurrence = master.ID+"_"+suffix, master.ID, nil
	instance.ETag = fmt.Sprintf("\"%s-%s\"", strings.Trim(master.ETag, "\""), suffix)
	if master.Start != nil && master.Start.Date != "" {
		day, err := time.Parse("20060102", suffix)
		if err != nil {
			return google.Event{}, false
		}
		first, _ := time.Parse(time.DateOnly, master.Start.Date)
		last := first.AddDate(0, 0, 1)
		if master.End != nil && master.End.Date != "" {
			last, _ = time.Parse(time.DateOnly, master.End.Date)
		}
		instance.OriginalStartTime = &google.EventTime{Date: day.Format(time.DateOnly)}
		instance.Start = &google.EventTime{Date: day.Format(time.DateOnly)}
		instance.End = &google.EventTime{Date: day.Add(last.Sub(first)).Format(time.DateOnly)}
		return instance, true
	}
	at, err := time.Parse("20060102T150405Z", suffix)
	if err != nil || master.Start == nil || master.End == nil {
		return google.Event{}, false
	}
	start, err1 := time.Parse(time.RFC3339, master.Start.DateTime)
	end, err2 := time.Parse(time.RFC3339, master.End.DateTime)
	if err1 != nil || err2 != nil {
		return google.Event{}, false
	}
	zone := master.Start.TimeZone
	loc, err := time.LoadLocation(zone)
	if err != nil {
		loc = time.UTC
	}
	instance.OriginalStartTime = &google.EventTime{DateTime: at.In(loc).Format(time.RFC3339), TimeZone: zone}
	instance.Start = &google.EventTime{DateTime: at.In(loc).Format(time.RFC3339), TimeZone: zone}
	instance.End = &google.EventTime{DateTime: at.Add(end.Sub(start)).In(loc).Format(time.RFC3339), TimeZone: zone}
	return instance, true
}

func (s *Server) eventRequest(w http.ResponseWriter, r *http.Request, calendarID, eventID string) {
	log := s.log(calendarID)
	if r.Method == http.MethodGet {
		if !s.access[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")] {
			apiError(w, http.StatusUnauthorized, "Request had invalid authentication credentials.")
			return
		}
		if _, ok := s.calendar(calendarID); !ok {
			apiError(w, http.StatusNotFound, "Not Found")
			return
		}
		event, ok := s.lookup(log, eventID)
		if !ok {
			apiError(w, http.StatusNotFound, "Not Found")
			return
		}
		writeJSON(w, http.StatusOK, event)
		return
	}
	body, err := readBody(r)
	if err != nil {
		apiError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if r.Method != http.MethodPatch && r.Method != http.MethodDelete {
		http.NotFound(w, r)
		return
	}
	if !s.write(w, r, calendarID, eventID, body) {
		return
	}
	current, ok := s.lookup(log, eventID)
	if !ok {
		apiError(w, http.StatusNotFound, "Not Found")
		return
	}
	if match := r.Header.Get("If-Match"); match != "" && match != current.ETag {
		apiError(w, http.StatusPreconditionFailed, "Precondition Failed")
		return
	}
	if r.Method == http.MethodDelete {
		if current.Cancelled() {
			apiError(w, http.StatusGone, "Resource has been deleted")
			return
		}
		s.gone[calendarID+"/"+eventID] = current
		gone := google.Event{ID: eventID, Status: "cancelled", RecurringEventID: current.RecurringEventID, OriginalStartTime: current.OriginalStartTime}
		s.store(log, gone)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if current.Cancelled() && current.RecurringEventID == "" {
		if previous, ok := s.gone[calendarID+"/"+eventID]; ok {
			current = previous
		}
	}
	patched, err := mergePatch(current, body)
	if err != nil {
		apiError(w, http.StatusBadRequest, err.Error())
		return
	}
	patched.ID, patched.RecurringEventID, patched.OriginalStartTime = current.ID, current.RecurringEventID, current.OriginalStartTime
	writeJSON(w, http.StatusOK, s.store(log, patched))
}

// mergePatch applies a PATCH body to an event as Google does: objects are
// merged, null removes a field, and arrays are replaced.
func mergePatch(event google.Event, body map[string]any) (google.Event, error) {
	raw, err := json.Marshal(event)
	if err != nil {
		return google.Event{}, err
	}
	var target map[string]any
	if err := json.Unmarshal(raw, &target); err != nil {
		return google.Event{}, err
	}
	merge(target, body)
	return toEvent(target)
}

func merge(target, patch map[string]any) {
	for key, value := range patch {
		if value == nil {
			delete(target, key)
			continue
		}
		if object, ok := value.(map[string]any); ok {
			existing, _ := target[key].(map[string]any)
			if existing == nil {
				existing = map[string]any{}
			}
			merge(existing, object)
			target[key] = existing
			continue
		}
		target[key] = value
	}
}

func (s *Server) instances(w http.ResponseWriter, r *http.Request, calendarID, eventID string) {
	if !s.access[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")] {
		apiError(w, http.StatusUnauthorized, "Request had invalid authentication credentials.")
		return
	}
	log := s.log(calendarID)
	master, ok := log.events[eventID]
	if !ok || master.event.Cancelled() {
		apiError(w, http.StatusNotFound, "Not Found")
		return
	}
	original := r.URL.Query().Get("originalStart")
	suffix := ""
	if day, err := time.Parse(time.DateOnly, original); err == nil {
		suffix = day.Format("20060102")
	} else if at, err := time.Parse(time.RFC3339, original); err == nil {
		suffix = at.UTC().Format("20060102T150405Z")
	}
	items := []google.Event{}
	if instance, ok := s.lookup(log, eventID+"_"+suffix); ok && suffix != "" {
		items = append(items, instance)
	}
	writeJSON(w, http.StatusOK, map[string]any{"kind": "calendar#events", "items": items})
}
