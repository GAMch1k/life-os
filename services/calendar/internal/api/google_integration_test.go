package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/calendar/internal/api/generated"
	"github.com/GAMch1k/life-os/services/calendar/internal/google"
	"github.com/GAMch1k/life-os/services/calendar/internal/google/fakegoogle"
	store "github.com/GAMch1k/life-os/services/calendar/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/calendar/internal/testdb"
)

// These tests drive the Google endpoints against a fake Google (never the
// real one) and a fresh schema on CALENDAR_TEST_DATABASE_URL.

var googleTestKey = bytes.Repeat([]byte{42}, google.TokenKeySize)

var (
	ownerCalendar   = fakegoogle.Calendar{ID: "owner@example.com", Summary: "Owner", Color: "#9fe1e7", AccessRole: "owner", Primary: true}
	familyCalendar  = fakegoogle.Calendar{ID: "family@group.calendar.google.com", Summary: "Family", Color: "#f83a22", AccessRole: "writer"}
	holidayCalendar = fakegoogle.Calendar{ID: "de.german#holiday@group.v.calendar.google.com", Summary: "Holidays in Germany", AccessRole: "reader"}
)

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

type googleClient struct {
	apiClient
	t        *testing.T
	pool     *pgxpool.Pool
	fake     *fakegoogle.Server
	clock    *testClock
	recorder *activityRecorder
	emitter  *activity.Emitter
	// service and syncer are the running Google connection and sync.
	service *google.Service
	syncer  *google.Syncer
	// bodies are every response, checked for leaked secrets.
	bodies *[]string
}

func googleConfig() google.Config {
	return google.Config{ClientID: fakegoogle.ClientID, ClientSecret: fakegoogle.ClientSecret, RedirectURI: google.DefaultRedirectURI, TokenKey: googleTestKey}
}

func newGoogleClient(t *testing.T, calendars ...fakegoogle.Calendar) *googleClient {
	t.Helper()
	recorder := &activityRecorder{}
	server := httptest.NewServer(recorder)
	t.Cleanup(server.Close)
	c := &googleClient{
		t: t, pool: testdb.Open(t, ""), fake: fakegoogle.New(t, calendars...), clock: &testClock{now: fixedNow},
		recorder: recorder, emitter: activity.NewEmitter(server.URL, "calendar", slog.New(slog.NewTextHandler(io.Discard, nil))),
		bodies: &[]string{},
	}
	c.restart(googleConfig())
	t.Cleanup(c.checkNoSecretLeaked)
	return c
}

// restart is a Calendar restart: a new service (nothing in memory) on the
// same database, with config.
func (c *googleClient) restart(config google.Config) {
	queries := store.New(c.pool)
	handler := NewHandler(c.pool, "Europe/Berlin")
	handler.now = func() time.Time { return fixedNow }
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	service := google.NewService(config, c.pool, c.fake.Endpoints(), c.emitter, logger).WithClock(c.clock.Now)
	berlin, _ := time.LoadLocation("Europe/Berlin")
	c.service, c.syncer = service, google.NewSyncer(service, berlin, 2*time.Minute, logger).WithClock(c.clock.Now)
	handler.WithActivity(c.emitter).WithGoogle(service)
	c.apiClient = apiClient{t: c.t, app: newTestApp(handler), handler: handler, queries: queries}
}

func (c *googleClient) call(method, target string, body any, wantStatus int, out any) []byte {
	c.t.Helper()
	status, raw := c.do(method, target, body, activity.ActorHeader, "user")
	*c.bodies = append(*c.bodies, string(raw))
	if status != wantStatus {
		c.t.Fatalf("%s %s: %d %s, want %d", method, target, status, raw, wantStatus)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			c.t.Fatalf("%s %s: %v in %s", method, target, err, raw)
		}
	}
	return raw
}

// failure expects an error response with code.
func (c *googleClient) failure(method, target string, body any, wantStatus int, code string) string {
	c.t.Helper()
	var envelope struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	c.call(method, target, body, wantStatus, &envelope)
	if envelope.Error.Code != code {
		c.t.Fatalf("%s %s: code %q (%s), want %q", method, target, envelope.Error.Code, envelope.Error.Message, code)
	}
	return envelope.Error.Message
}

func (c *googleClient) connection() generated.GoogleConnection {
	c.t.Helper()
	var out generated.GoogleConnection
	c.call(http.MethodGet, "/v1/google/connection", nil, http.StatusOK, &out)
	return out
}

func (c *googleClient) calendars() []generated.GoogleCalendar {
	c.t.Helper()
	var out generated.GoogleCalendarList
	c.call(http.MethodGet, "/v1/google/calendars", nil, http.StatusOK, &out)
	return out.Items
}

// authorize starts an authorization and returns its consent page query.
func (c *googleClient) authorize() url.Values {
	c.t.Helper()
	var out generated.GoogleAuthorization
	c.call(http.MethodPost, "/v1/google/connection/authorize", nil, http.StatusOK, &out)
	parsed, err := url.Parse(out.Url)
	if err != nil || !strings.HasPrefix(out.Url, c.fake.URL+"/o/oauth2/v2/auth?") {
		c.t.Fatalf("consent URL %q: %v", out.Url, err)
	}
	return parsed.Query()
}

// consent is the owner agreeing on Google's page: Google returns a code
// for the authorization's state.
func (c *googleClient) consent() map[string]string {
	c.t.Helper()
	query := c.authorize()
	return map[string]string{"code": c.fake.IssueCode(query.Get("code_challenge")), "state": query.Get("state")}
}

func (c *googleClient) connect() generated.GoogleConnection {
	c.t.Helper()
	var out generated.GoogleConnection
	c.call(http.MethodPost, "/v1/google/connection", c.consent(), http.StatusOK, &out)
	return out
}

// checkNoSecretLeaked: no response, Activity event, or stored column holds
// a token Google issued, the client secret, or a raw state.
func (c *googleClient) checkNoSecretLeaked() {
	secrets := append(c.fake.Tokens(), fakegoogle.ClientSecret)
	events, _ := json.Marshal(c.recorder.all())
	var stored string
	_ = c.pool.QueryRow(context.Background(), `SELECT coalesce((SELECT string_agg(row_to_json(g)::text, ' ') FROM google_connections g), '')`).Scan(&stored)
	var ciphertexts [][]byte
	rows, err := c.pool.Query(context.Background(), `SELECT refresh_token_ciphertext FROM google_connections`)
	if err == nil {
		for rows.Next() {
			var value []byte
			_ = rows.Scan(&value)
			ciphertexts = append(ciphertexts, value)
		}
		rows.Close()
	}
	for _, secret := range secrets {
		for _, body := range *c.bodies {
			if strings.Contains(body, secret) {
				c.t.Errorf("a response contains %q: %s", secret, body)
			}
		}
		if strings.Contains(string(events), secret) {
			c.t.Errorf("an Activity event contains %q", secret)
		}
		if strings.Contains(stored, secret) {
			c.t.Errorf("a stored connection contains %q", secret)
		}
		for _, ciphertext := range ciphertexts {
			if bytes.Contains(ciphertext, []byte(secret)) {
				c.t.Errorf("a stored ciphertext contains %q", secret)
			}
		}
	}
}

func summaries(items []generated.GoogleCalendar) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		mark := " "
		if item.SyncEnabled {
			mark = "x"
		}
		out = append(out, "["+mark+"] "+item.Summary)
	}
	return out
}

func TestGoogleConnectionLifecycle(t *testing.T) {
	c := newGoogleClient(t, ownerCalendar, familyCalendar, holidayCalendar)
	seen := 0

	initial := c.connection()
	if !initial.Configured || initial.Status != "not_connected" || initial.AccountEmail != nil || len(initial.Scopes) != 0 || len(c.calendars()) != 0 {
		t.Fatalf("initial = %+v", initial)
	}

	// The consent page asks for offline access, forced consent, the three
	// scopes, a state, and a PKCE challenge, and returns to the Dashboard.
	query := c.authorize()
	for name, want := range map[string]string{
		"client_id": fakegoogle.ClientID, "redirect_uri": "https://lifeos.gamch1k.com/calendar/google/callback", "response_type": "code",
		"access_type": "offline", "prompt": "consent", "code_challenge_method": "S256",
		"scope": strings.Join(google.Scopes, " "),
	} {
		if got := query.Get(name); got != want {
			t.Errorf("consent %s = %q, want %q", name, got, want)
		}
	}
	if len(query.Get("state")) < 43 || len(query.Get("code_challenge")) != 43 || query.Has("client_secret") {
		t.Fatalf("consent query = %v", query)
	}
	// Only the state's hash is stored.
	var stored int
	if err := c.pool.QueryRow(context.Background(), `SELECT count(*) FROM google_oauth_states WHERE state_hash = $1`, google.HashState(query.Get("state"))).Scan(&stored); err != nil || stored != 1 {
		t.Fatalf("stored state: %d %v", stored, err)
	}

	// The owner consents; the Dashboard posts the code and state.
	consent := map[string]string{"code": c.fake.IssueCode(query.Get("code_challenge")), "state": query.Get("state")}
	var connected generated.GoogleConnection
	c.call(http.MethodPost, "/v1/google/connection", consent, http.StatusOK, &connected)
	if connected.Status != "connected" || connected.AccountEmail == nil || *connected.AccountEmail != "owner@example.com" || !connected.Configured ||
		len(connected.Scopes) != 3 || len(connected.MissingScopes) != 0 || connected.ConnectedAt == nil || connected.LastError != nil {
		t.Fatalf("connected = %+v", connected)
	}
	event := expectRecorded(t, c.recorder, &seen, google.EventConnected)[0]
	if event.Actor != activity.User() || event.Subject.Type != "google_connection" || event.Summary != "Google Calendar connected: owner@example.com (3 calendars)" ||
		event.Data["accountEmail"] != "owner@example.com" {
		t.Fatalf("connected event = %+v", event)
	}
	// The state was used: posting it again fails.
	c.failure(http.MethodPost, "/v1/google/connection", consent, http.StatusBadRequest, "invalid_state")

	// The primary calendar starts with sync on, the others off.
	calendars := c.calendars()
	equal(t, "calendars", summaries(calendars), []string{"[x] Owner", "[ ] Family", "[ ] Holidays in Germany"})
	if calendars[0].GoogleCalendarId != "owner@example.com" || !calendars[0].Primary || *calendars[0].Color != "#9fe1e7" || calendars[0].AccessRole != "owner" ||
		calendars[2].Color != nil || calendars[2].GoogleCalendarId != holidayCalendar.ID {
		t.Fatalf("calendars = %+v", calendars)
	}
	var family generated.GoogleCalendar
	c.call(http.MethodPatch, "/v1/google/calendars/"+calendars[1].Id.String(), map[string]any{"syncEnabled": true}, http.StatusOK, &family)
	if !family.SyncEnabled || family.Summary != "Family" {
		t.Fatalf("family = %+v", family)
	}
	c.failure(http.MethodPatch, "/v1/google/calendars/"+calendars[1].Id.String(), map[string]any{}, http.StatusBadRequest, "bad_request")
	c.failure(http.MethodPatch, "/v1/google/calendars/7f8d2b1e-1c1a-4b7e-9a51-2f0c6d1e9a10", map[string]any{"syncEnabled": true}, http.StatusNotFound, "not_found")

	// Refreshing the list uses the access token from the exchange: no token
	// refresh while it is fresh.
	c.fake.Lock()
	c.fake.Calendars = []fakegoogle.Calendar{ownerCalendar, {ID: familyCalendar.ID, Summary: "Family (shared)", AccessRole: "writer"}, {ID: "work@example.com", Summary: "Work", AccessRole: "reader"}}
	c.fake.Unlock()
	var refreshed generated.GoogleCalendarList
	c.call(http.MethodPost, "/v1/google/calendars/refresh", nil, http.StatusOK, &refreshed)
	equal(t, "refreshed", summaries(refreshed.Items), []string{"[x] Owner", "[x] Family (shared)", "[ ] Work"})
	if _, refreshes, _ := c.fake.Counts(); refreshes != 0 {
		t.Fatalf("refreshes = %d, want 0", refreshes)
	}

	// 56 minutes later the token is within the margin of its expiry: it is
	// refreshed before use.
	c.clock.Advance(56 * time.Minute)
	c.call(http.MethodPost, "/v1/google/calendars/refresh", nil, http.StatusOK, nil)
	if _, refreshes, _ := c.fake.Counts(); refreshes != 1 {
		t.Fatalf("refreshes after 56 min = %d, want 1", refreshes)
	}
	c.call(http.MethodPost, "/v1/google/calendars/refresh", nil, http.StatusOK, nil)
	if _, refreshes, _ := c.fake.Counts(); refreshes != 1 {
		t.Fatalf("refreshes with a fresh token = %d, want 1", refreshes)
	}

	// After a restart only the encrypted refresh token is left: it is
	// decrypted and refreshed.
	c.restart(googleConfig())
	equal(t, "after restart", summaries(c.calendars()), []string{"[x] Owner", "[x] Family (shared)", "[ ] Work"})
	c.call(http.MethodPost, "/v1/google/calendars/refresh", nil, http.StatusOK, nil)
	if _, refreshes, _ := c.fake.Counts(); refreshes != 2 {
		t.Fatalf("refreshes after restart = %d, want 2", refreshes)
	}

	// The owner removes Life OS in their Google Account: the next refresh
	// finds the authorization gone and the connection expired.
	c.fake.RevokeAll()
	c.restart(googleConfig())
	message := c.failure(http.MethodPost, "/v1/google/calendars/refresh", nil, http.StatusConflict, "google_expired")
	if !strings.Contains(message, "reconnect Google in Calendar") {
		t.Fatalf("expired message = %q", message)
	}
	expired := c.connection()
	if expired.Status != "expired" || expired.LastError == nil || !strings.Contains(*expired.LastError, "invalid_grant") ||
		!strings.Contains(*expired.LastError, "reconnect Google in Calendar") || *expired.AccountEmail != "owner@example.com" {
		t.Fatalf("expired = %+v", expired)
	}
	event = expectRecorded(t, c.recorder, &seen, google.EventExpired)[0]
	if event.Actor != activity.System("calendar") || event.Outcome != activity.Failed || !strings.Contains(event.Summary, "reconnect Google in Calendar") {
		t.Fatalf("expired event = %+v", event)
	}
	// Expired stays expired without asking Google again, and is recorded once.
	_, refreshesBefore, _ := c.fake.Counts()
	c.failure(http.MethodPost, "/v1/google/calendars/refresh", nil, http.StatusConflict, "google_expired")
	if _, refreshes, _ := c.fake.Counts(); refreshes != refreshesBefore {
		t.Fatal("an expired connection asked Google again")
	}
	expectRecorded(t, c.recorder, &seen)
	// The stored list stays readable.
	if len(c.calendars()) != 3 {
		t.Fatal("calendars dropped on expiry")
	}

	// Reconnecting the same account makes it usable and keeps the choices;
	// the consent page preselects the account.
	if hint := c.authorize().Get("login_hint"); hint != "owner@example.com" {
		t.Fatalf("login_hint = %q", hint)
	}
	reconnected := c.connect()
	if reconnected.Status != "connected" || reconnected.LastError != nil {
		t.Fatalf("reconnected = %+v", reconnected)
	}
	expectRecorded(t, c.recorder, &seen, google.EventConnected)
	equal(t, "after reconnect", summaries(c.calendars()), []string{"[x] Owner", "[x] Family (shared)", "[ ] Work"})

	// Disconnect revokes the refresh token at Google and forgets everything.
	c.call(http.MethodDelete, "/v1/google/connection", nil, http.StatusNoContent, nil)
	if revoked := c.fake.RevokedTokens(); len(revoked) != 1 || !strings.HasPrefix(revoked[0], "ya29.fake-refresh-token-") {
		t.Fatalf("revoked = %v", revoked)
	}
	event = expectRecorded(t, c.recorder, &seen, google.EventDisconnected)[0]
	if event.Summary != "Google Calendar disconnected: owner@example.com" || event.Data["revokedAtGoogle"] != true || event.Actor != activity.User() {
		t.Fatalf("disconnected event = %+v", event)
	}
	if after := c.connection(); after.Status != "not_connected" || after.AccountEmail != nil || len(c.calendars()) != 0 {
		t.Fatalf("after disconnect = %+v", after)
	}
	c.failure(http.MethodPost, "/v1/google/calendars/refresh", nil, http.StatusConflict, "google_not_connected")
	// Disconnecting again is fine and records nothing.
	c.call(http.MethodDelete, "/v1/google/connection", nil, http.StatusNoContent, nil)
	expectRecorded(t, c.recorder, &seen)
}

func TestGoogleStateIsShortLivedAndMustMatch(t *testing.T) {
	c := newGoogleClient(t, ownerCalendar)
	consent := c.consent()
	c.clock.Advance(google.StateLifetime)
	c.failure(http.MethodPost, "/v1/google/connection", consent, http.StatusBadRequest, "invalid_state")

	consent = c.consent()
	forged := map[string]string{"code": consent["code"], "state": "forged-" + consent["state"]}
	c.failure(http.MethodPost, "/v1/google/connection", forged, http.StatusBadRequest, "invalid_state")
	if exchanges, _, _ := c.fake.Counts(); exchanges != 0 {
		t.Fatalf("a code was exchanged without a valid state (%d)", exchanges)
	}
	for _, body := range []any{`{`, map[string]string{"code": "x"}, map[string]string{"state": "y"}, map[string]string{"code": "", "state": ""}} {
		c.failure(http.MethodPost, "/v1/google/connection", body, http.StatusBadRequest, "bad_request")
	}
	// Expired states are cleaned up when the next authorization starts.
	c.clock.Advance(time.Hour)
	c.authorize()
	var pending int
	if err := c.pool.QueryRow(context.Background(), `SELECT count(*) FROM google_oauth_states`).Scan(&pending); err != nil || pending != 1 {
		t.Fatalf("pending states = %d, %v", pending, err)
	}
	if c.connection().Status != "not_connected" {
		t.Fatal("connected without a valid state")
	}
}

func TestGoogleConnectRefusals(t *testing.T) {
	c := newGoogleClient(t, ownerCalendar, familyCalendar)

	// Google refuses the code (expired, used, or not Google's).
	consent := c.consent()
	consent["code"] = "code-unknown"
	c.failure(http.MethodPost, "/v1/google/connection", consent, http.StatusBadRequest, "google_code_rejected")

	// The owner unticked the calendar list permission: nothing is stored and
	// the grant is withdrawn.
	c.fake.Lock()
	c.fake.GrantedScopes = []string{google.ScopeEvents}
	c.fake.Unlock()
	c.failure(http.MethodPost, "/v1/google/connection", c.consent(), http.StatusForbidden, "google_scope_missing")
	if len(c.fake.RevokedTokens()) != 1 || c.connection().Status != "not_connected" {
		t.Fatalf("revoked %v, status %s", c.fake.RevokedTokens(), c.connection().Status)
	}

	// Fewer permissions than asked, but the list: connected, and the missing
	// ones are reported.
	c.fake.Lock()
	c.fake.GrantedScopes = []string{google.ScopeCalendarList}
	c.fake.Unlock()
	partial := c.connect()
	if partial.Status != "connected" || strings.Join(partial.MissingScopes, " ") != google.ScopeEvents+" "+google.ScopeAppCreated {
		t.Fatalf("partial = %+v", partial)
	}
	c.call(http.MethodDelete, "/v1/google/connection", nil, http.StatusNoContent, nil)

	// No refresh token issued.
	c.fake.Lock()
	c.fake.GrantedScopes, c.fake.OmitRefreshToken = nil, true
	c.fake.Unlock()
	c.failure(http.MethodPost, "/v1/google/connection", c.consent(), http.StatusBadGateway, "google_rejected")

	// A wrong client secret is the setup's fault, not the code's.
	wrong := googleConfig()
	wrong.ClientSecret = "not-the-secret"
	c.restart(wrong)
	c.fake.Lock()
	c.fake.OmitRefreshToken = false
	c.fake.Unlock()
	if message := c.failure(http.MethodPost, "/v1/google/connection", c.consent(), http.StatusBadGateway, "google_rejected"); !strings.Contains(message, "invalid_client") {
		t.Fatalf("wrong secret: %q", message)
	}
	c.restart(googleConfig())

	// Google is down.
	c.fake.Lock()
	c.fake.OmitRefreshToken, c.fake.TokenStatus = false, http.StatusInternalServerError
	c.fake.Unlock()
	c.failure(http.MethodPost, "/v1/google/connection", c.consent(), http.StatusBadGateway, "google_unavailable")
	if c.connection().Status != "not_connected" {
		t.Fatal("a failed connect stored a connection")
	}
}

func TestGoogleConnectingAnotherAccountReplacesTheConnection(t *testing.T) {
	c := newGoogleClient(t, ownerCalendar, familyCalendar)
	first := c.connect()
	calendars := c.calendars()
	c.call(http.MethodPatch, "/v1/google/calendars/"+calendars[1].Id.String(), map[string]any{"syncEnabled": true}, http.StatusOK, nil)

	c.fake.Lock()
	c.fake.Calendars = []fakegoogle.Calendar{{ID: "other@example.com", Summary: "Other", AccessRole: "owner", Primary: true}, familyCalendar}
	c.fake.Unlock()
	second := c.connect()
	if *second.AccountEmail != "other@example.com" || *first.AccountEmail != "owner@example.com" {
		t.Fatalf("second = %+v", second)
	}
	// The other account's calendars start fresh, even one both can see.
	equal(t, "other account", summaries(c.calendars()), []string{"[x] Other", "[ ] Family"})
	var connections int
	if err := c.pool.QueryRow(context.Background(), `SELECT count(*) FROM google_connections`).Scan(&connections); err != nil || connections != 1 {
		t.Fatalf("connections = %d, %v", connections, err)
	}
}

func TestGoogleTokenRefusalsAndRotation(t *testing.T) {
	c := newGoogleClient(t, ownerCalendar)
	c.connect()

	// A refused client (such as a changed secret) is an error, not expiry,
	// and a later success clears it.
	c.fake.Lock()
	c.fake.RefreshError = "invalid_client"
	c.fake.Unlock()
	c.clock.Advance(time.Hour)
	c.failure(http.MethodPost, "/v1/google/calendars/refresh", nil, http.StatusBadGateway, "google_rejected")
	if got := c.connection(); got.Status != "error" || got.LastError == nil || !strings.Contains(*got.LastError, "invalid_client") {
		t.Fatalf("error = %+v", got)
	}
	// An outage leaves the status alone.
	c.fake.Lock()
	c.fake.RefreshError, c.fake.TokenStatus = "", http.StatusServiceUnavailable
	c.fake.Unlock()
	c.failure(http.MethodPost, "/v1/google/calendars/refresh", nil, http.StatusBadGateway, "google_unavailable")
	if got := c.connection(); got.Status != "error" {
		t.Fatalf("after outage = %+v", got)
	}
	// Google rotates the refresh token: the new one is kept and works after
	// a restart.
	c.fake.Lock()
	c.fake.TokenStatus, c.fake.RotateRefreshToken = 0, true
	c.fake.Unlock()
	c.call(http.MethodPost, "/v1/google/calendars/refresh", nil, http.StatusOK, nil)
	if got := c.connection(); got.Status != "connected" || got.LastError != nil {
		t.Fatalf("recovered = %+v", got)
	}
	c.fake.Lock()
	c.fake.RotateRefreshToken = false
	c.fake.Unlock()
	c.restart(googleConfig())
	c.call(http.MethodPost, "/v1/google/calendars/refresh", nil, http.StatusOK, nil)

	// A changed token key cannot open the stored token.
	other := googleConfig()
	other.TokenKey = bytes.Repeat([]byte{1}, google.TokenKeySize)
	c.restart(other)
	c.failure(http.MethodPost, "/v1/google/calendars/refresh", nil, http.StatusConflict, "google_expired")
	if got := c.connection(); got.Status != "error" || !strings.Contains(*got.LastError, "LIFE_OS_GOOGLE_TOKEN_KEY") {
		t.Fatalf("wrong key = %+v", got)
	}
}

// A server without the Google settings reads as not configured, refuses to
// connect, and can still forget an existing connection.
func TestGoogleWithoutConfiguration(t *testing.T) {
	c := newGoogleClient(t, ownerCalendar)
	c.restart(google.Config{})
	if got := c.connection(); got.Configured || got.Status != "not_connected" {
		t.Fatalf("unconfigured = %+v", got)
	}
	message := c.failure(http.MethodPost, "/v1/google/connection/authorize", nil, http.StatusServiceUnavailable, "google_not_configured")
	if !strings.Contains(message, "LIFE_OS_GOOGLE_CLIENT_ID") {
		t.Fatalf("message = %q", message)
	}
	c.failure(http.MethodPost, "/v1/google/connection", map[string]string{"code": "c", "state": "s"}, http.StatusServiceUnavailable, "google_not_configured")
	c.failure(http.MethodPost, "/v1/google/calendars/refresh", nil, http.StatusServiceUnavailable, "google_not_configured")

	c.restart(googleConfig())
	c.connect()
	c.restart(google.Config{})
	if got := c.connection(); got.Configured || got.Status != "connected" || *got.AccountEmail != "owner@example.com" {
		t.Fatalf("connected but unconfigured = %+v", got)
	}
	c.call(http.MethodDelete, "/v1/google/connection", nil, http.StatusNoContent, nil)
	if len(c.fake.RevokedTokens()) != 0 || c.connection().Status != "not_connected" {
		t.Fatal("unconfigured disconnect")
	}
}
