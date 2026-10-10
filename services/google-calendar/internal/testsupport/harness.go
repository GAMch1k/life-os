// Package testsupport wires the Google Calendar integration to a fake
// Google (fakegoogle) and the real Calendar service (run as a process on a
// private schema of the test database) for integration tests, with a
// recorder standing in for Activity and a controllable clock. Tests skip
// without GOOGLE_CALENDAR_TEST_DATABASE_URL.
package testsupport

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/services/google-calendar/internal/api"
	"github.com/GAMch1k/life-os/services/google-calendar/internal/calendar"
	"github.com/GAMch1k/life-os/services/google-calendar/internal/google"
	"github.com/GAMch1k/life-os/services/google-calendar/internal/google/fakegoogle"
	"github.com/GAMch1k/life-os/services/google-calendar/internal/testdb"
)

// TokenKey seals refresh tokens in tests.
var TokenKey = bytes.Repeat([]byte{42}, google.TokenKeySize)

// FixedNow is when the tests run.
var FixedNow = time.Date(2026, 10, 10, 10, 0, 0, 0, time.UTC)

// The fake account's calendars.
var (
	OwnerCalendar   = fakegoogle.Calendar{ID: "owner@example.com", Summary: "Owner", Color: "#9fe1e7", AccessRole: "owner", Primary: true}
	FamilyCalendar  = fakegoogle.Calendar{ID: "family@group.calendar.google.com", Summary: "Family", Color: "#f83a22", AccessRole: "writer"}
	HolidayCalendar = fakegoogle.Calendar{ID: "de.german#holiday@group.v.calendar.google.com", Summary: "Holidays in Germany", AccessRole: "reader"}
)

// Config is the Google configuration the fake accepts.
func Config() google.Config {
	return google.Config{ClientID: fakegoogle.ClientID, ClientSecret: fakegoogle.ClientSecret, RedirectURI: google.DefaultRedirectURI, TokenKey: TokenKey}
}

// Clock is a settable clock.
type Clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// Recorder stands in for Activity and keeps the events posted to it.
type Recorder struct {
	*httptest.Server
	mu     sync.Mutex
	events []activity.Event
}

func newRecorder(t testing.TB) *Recorder {
	r := &Recorder{}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var event activity.Event
		if err := json.NewDecoder(req.Body).Decode(&event); err == nil {
			r.mu.Lock()
			r.events = append(r.events, event)
			r.mu.Unlock()
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(r.Close)
	return r
}

// Events returns the recorded events.
func (r *Recorder) Events() []activity.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.events)
}

// OfType returns the recorded events of eventType.
func (r *Recorder) OfType(eventType string) []activity.Event {
	var out []activity.Event
	for _, event := range r.Events() {
		if event.Type == eventType {
			out = append(out, event)
		}
	}
	return out
}

// Harness is the service under test with its fakes.
type Harness struct {
	T           *testing.T
	DatabaseURL string
	// Pool is the service's own (google_calendar_db) schema.
	Pool *pgxpool.Pool
	// CalendarPool is the real Calendar's schema.
	CalendarPool *pgxpool.Pool
	Calendar     *Calendar
	Fake         *fakegoogle.Server
	Clock        *Clock
	Recorder     *Recorder
	Service      *google.Service
	Syncer       *google.Syncer
	app          *httpserver.App
	bodies       []string
}

// Options shape a harness.
type Options struct {
	// UpTo stops Calendar's migrations at a version ("" for all) before
	// Prepare runs on Calendar's schema (such as seeding the legacy tables
	// for the handoff); the rest are applied after it, then Before runs
	// with both schemas, and then Calendar starts.
	UpTo    string
	Prepare func(h *Harness)
	Before  func(h *Harness)
}

// New starts a harness with the fake account holding calendars.
func New(t *testing.T, calendars ...fakegoogle.Calendar) *Harness {
	return NewWith(t, Options{}, calendars...)
}

// NewWith starts a harness with options.
func NewWith(t *testing.T, options Options, calendars ...fakegoogle.Calendar) *Harness {
	t.Helper()
	databaseURL := os.Getenv("GOOGLE_CALENDAR_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("GOOGLE_CALENDAR_TEST_DATABASE_URL is not set")
	}
	h := &Harness{T: t, DatabaseURL: databaseURL, Pool: testdb.Open(t), Fake: fakegoogle.New(t, calendars...), Clock: &Clock{now: FixedNow}, Recorder: newRecorder(t)}
	var schema string
	h.CalendarPool, schema = CalendarSchema(t, databaseURL, options.UpTo)
	if options.Prepare != nil {
		options.Prepare(h)
	}
	if options.UpTo != "" {
		ApplyCalendarMigrations(t, h.CalendarPool, options.UpTo)
	}
	if options.Before != nil {
		options.Before(h)
	}
	h.Calendar = StartCalendar(t, databaseURL, schema)
	h.Restart(Config())
	t.Cleanup(h.checkNoSecretLeaked)
	return h
}

// Restart is a restart of the service: nothing in memory, the same
// database, with config.
func (h *Harness) Restart(config google.Config) {
	var output io.Writer = io.Discard
	if os.Getenv("GOOGLE_CALENDAR_TEST_LOG") != "" {
		output = os.Stderr
	}
	logger := slog.New(slog.NewTextHandler(output, &slog.HandlerOptions{Level: slog.LevelDebug}))
	emitter := activity.NewEmitter(h.Recorder.URL, calendar.Integration, logger)
	berlin, _ := time.LoadLocation("Europe/Berlin")
	h.Service = google.NewService(config, h.Pool, h.Fake.Endpoints(), calendar.New(h.Calendar.URL), emitter, logger).WithClock(h.Clock.Now)
	h.Syncer = google.NewSyncer(h.Service, berlin, 2*time.Minute, 15*time.Second, logger).WithClock(h.Clock.Now)
	h.app = httpserver.New("google-calendar", httpserver.WithLogger(logger))
	api.Register(h.app, api.NewHandler(h.Service))
}

// Run is one round of the service's work: removals, the change feed, the
// edits and exports written to Google, and the due syncs.
func (h *Harness) Run() {
	h.T.Helper()
	h.Syncer.RunOnce(context.Background())
}

// Do makes a request to the service's API as the owner.
func (h *Harness) Do(method, target string, body any) (int, []byte) {
	h.T.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			h.T.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	request := httptest.NewRequest(method, target, reader)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(activity.ActorHeader, "user")
	response, err := h.app.Test(request)
	if err != nil {
		h.T.Fatal(err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	h.bodies = append(h.bodies, string(raw))
	return response.StatusCode, raw
}

// Call makes a request to the service's API and decodes the answer.
func (h *Harness) Call(method, target string, body any, want int, out any) {
	h.T.Helper()
	status, raw := h.Do(method, target, body)
	if status != want {
		h.T.Fatalf("%s %s: %d %s, want %d", method, target, status, raw, want)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			h.T.Fatalf("%s %s: %v in %s", method, target, err, raw)
		}
	}
}

// Connect goes through the owner's consent and connects the fake account.
func (h *Harness) Connect() map[string]any {
	h.T.Helper()
	var authorization struct{ URL string }
	h.Call(http.MethodPost, "/v1/connection/authorize", nil, http.StatusOK, &authorization)
	parsed, err := url.Parse(authorization.URL)
	if err != nil || !strings.HasPrefix(authorization.URL, h.Fake.URL+"/o/oauth2/v2/auth?") {
		h.T.Fatalf("consent URL %q: %v", authorization.URL, err)
	}
	query := parsed.Query()
	body := map[string]string{"code": h.Fake.IssueCode(query.Get("code_challenge")), "state": query.Get("state")}
	var connection map[string]any
	h.Call(http.MethodPost, "/v1/connection", body, http.StatusOK, &connection)
	return connection
}

// checkNoSecretLeaked: no response, Activity event, or log holds a token
// Google issued or the client secret.
func (h *Harness) checkNoSecretLeaked() {
	secrets := append(h.Fake.Tokens(), fakegoogle.ClientSecret)
	events, _ := json.Marshal(h.Recorder.Events())
	for _, secret := range secrets {
		for _, body := range h.bodies {
			if strings.Contains(body, secret) {
				h.T.Errorf("a response contains a secret: %s", body)
			}
		}
		if strings.Contains(string(events), secret) {
			h.T.Errorf("an Activity event contains a secret")
		}
	}
}
