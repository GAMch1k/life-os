// Package diagnostics records server diagnostics: where something went
// wrong, as a code from a fixed list, ids, counts, durations, and times,
// never content (docs/architecture/multi-user.md#logs). They are for the
// server's admins, who manage accounts, never content, so an Event has no
// free-text field: a title, an amount, or a message cannot be written into
// one by accident. The Activity service keeps them (diagnostic_events) and
// checks every field again when it stores them.
package diagnostics

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Code is what went wrong, from a fixed list: services' own 5xx error
// codes, and a generic one for each 5xx status.
type Code string

// The codes. services/activity/api/openapi.yaml lists the same ones; a test
// keeps the two equal.
const (
	InternalError        Code = "internal_error"
	BadGateway           Code = "bad_gateway"
	ServiceUnavailable   Code = "service_unavailable"
	GatewayTimeout       Code = "gateway_timeout"
	ServerError          Code = "server_error"
	Panic                Code = "panic"
	NotReady             Code = "not_ready"
	ProviderUnavailable  Code = "provider_unavailable"
	MediaUnavailable     Code = "media_unavailable"
	GoogleRejected       Code = "google_rejected"
	GoogleUnavailable    Code = "google_unavailable"
	GoogleNotConfigured  Code = "google_not_configured"
	BankingNotConfigured Code = "banking_not_configured"
	FoodUnavailable      Code = "food_unavailable"
	CalendarUnavailable  Code = "calendar_unavailable"
)

// Codes is the fixed list, in a stable order.
var Codes = []Code{
	InternalError, BadGateway, ServiceUnavailable, GatewayTimeout, ServerError,
	Panic, NotReady, ProviderUnavailable, MediaUnavailable, GoogleRejected,
	GoogleUnavailable, GoogleNotConfigured, BankingNotConfigured, FoodUnavailable,
	CalendarUnavailable,
}

var known = func() map[Code]bool {
	set := make(map[Code]bool, len(Codes))
	for _, code := range Codes {
		set[code] = true
	}
	return set
}()

// Valid reports whether c is on the list.
func (c Code) Valid() bool { return known[c] }

// ForStatus is the code of an HTTP answer: the service's own error code when
// it is on the list, else the generic code of its status.
func ForStatus(status int, code string) Code {
	if known[Code(code)] {
		return Code(code)
	}
	switch status {
	case http.StatusInternalServerError:
		return InternalError
	case http.StatusBadGateway:
		return BadGateway
	case http.StatusServiceUnavailable:
		return ServiceUnavailable
	case http.StatusGatewayTimeout:
		return GatewayTimeout
	}
	return ServerError
}

// Event is one diagnostic: a count of one code at one place in one service,
// between FirstAt and LastAt. Every string in it is a name from Life OS's
// own code (a service, a route pattern) or a code from the list; ids are
// UUIDs. Nothing a person wrote fits.
type Event struct {
	// Service is the service's name, such as tasks.
	Service string `json:"service"`
	Code    Code   `json:"code"`
	// Route is the route pattern that answered, such as
	// /v1/tasks/:taskId, never a request's path (which may hold a search).
	Route string `json:"route,omitempty"`
	// Status is the HTTP status, for an answer.
	Status int `json:"status,omitempty"`
	// SubjectID is the entity concerned, when there is one.
	SubjectID *uuid.UUID `json:"subjectId,omitempty"`
	// RequestID is the last request counted, to find it in the service's
	// log; only a UUID (the ids the services make themselves).
	RequestID *uuid.UUID `json:"requestId,omitempty"`
	Count     int        `json:"count"`
	// DurationMs is the longest of the requests counted.
	DurationMs int64     `json:"durationMs,omitempty"`
	FirstAt    time.Time `json:"firstAt"`
	LastAt     time.Time `json:"lastAt"`
}

var (
	servicePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
	routePattern   = regexp.MustCompile(`^(/[A-Za-z0-9_.:{}*+-]*)+$`)
)

// RoutePattern reports whether route has the form of a route pattern.
func RoutePattern(route string) bool {
	return len(route) <= 128 && routePattern.MatchString(route)
}

// Validate checks every field; the Activity service runs the same rules.
func (e Event) Validate() error {
	switch {
	case !servicePattern.MatchString(e.Service):
		return errors.New("service must be a service name such as tasks")
	case !e.Code.Valid():
		return errors.New("code must be one of the diagnostic codes")
	case e.Route != "" && !RoutePattern(e.Route):
		return errors.New("route must be a route pattern such as /v1/tasks/:taskId")
	case e.Status != 0 && (e.Status < 100 || e.Status > 599):
		return errors.New("status must be an HTTP status")
	case e.Count < 1 || e.Count > 1_000_000_000:
		return errors.New("count must be at least 1")
	case e.DurationMs < 0:
		return errors.New("durationMs must not be negative")
	case e.FirstAt.IsZero() || e.LastAt.IsZero() || e.LastAt.Before(e.FirstAt):
		return errors.New("firstAt and lastAt are required, firstAt first")
	}
	return nil
}

// Batch is what the Recorder posts to the Activity service.
type Batch struct {
	Events []Event `json:"events"`
}

// Path is the Activity service's route for diagnostics.
const Path = "/v1/diagnostic-events"

// FlushInterval is how often a Recorder sends what it counted.
const FlushInterval = time.Minute

// maxPending bounds the distinct counters kept between two flushes; a count
// for one more is dropped (and the drop counted under server_error).
const maxPending = 512

type key struct {
	code   Code
	route  string
	status int
}

// Recorder counts diagnostics in memory and sends them to the Activity
// service once a minute, so that a burst of errors is one row, not
// thousands. A nil Recorder, or one without a URL, counts nothing.
type Recorder struct {
	url     string
	service string
	client  *http.Client
	logger  *slog.Logger
	now     func() time.Time

	mu      sync.Mutex
	pending map[key]*Event
	dropped int
	start   sync.Once
}

// NewRecorder sends the diagnostics of service to the Activity service at url.
func NewRecorder(url, service string, logger *slog.Logger) *Recorder {
	if logger == nil {
		logger = slog.Default()
	}
	return &Recorder{
		url:     strings.TrimRight(url, "/"),
		service: service,
		client:  &http.Client{Timeout: 5 * time.Second},
		logger:  logger,
		now:     time.Now,
		pending: map[key]*Event{},
	}
}

// FromEnv is the Recorder of a service whose ACTIVITY_SERVICE_URL is set,
// else nil: the Activity service itself, the tools, and the tests record
// none. ACTIVITY_DIAGNOSTICS=off turns it off.
func FromEnv(service string, logger *slog.Logger) *Recorder {
	url := strings.TrimSpace(os.Getenv("ACTIVITY_SERVICE_URL"))
	if url == "" || strings.EqualFold(os.Getenv("ACTIVITY_DIAGNOSTICS"), "off") || !servicePattern.MatchString(service) {
		return nil
	}
	return NewRecorder(url, service, logger)
}

// Answer counts one answered request: its status, the error code it
// answered with, the route pattern, the request id, and how long it took.
// Only server errors (5xx) are diagnostics.
func (r *Recorder) Answer(status int, code, route, requestID string, took time.Duration) {
	if status < 500 {
		return
	}
	r.Count(ForStatus(status, code), route, status, requestID, took)
}

// Count adds one occurrence of code at route.
func (r *Recorder) Count(code Code, route string, status int, requestID string, took time.Duration) {
	if r == nil || r.url == "" {
		return
	}
	if !code.Valid() {
		code = ServerError
	}
	if !RoutePattern(route) {
		route = ""
	}
	at := r.now().UTC()
	var id *uuid.UUID
	if parsed, err := uuid.Parse(requestID); err == nil {
		id = &parsed
	}
	k := key{code: code, route: route, status: status}
	r.mu.Lock()
	event, ok := r.pending[k]
	switch {
	case ok:
		event.Count++
		event.LastAt = at
		if id != nil {
			event.RequestID = id
		}
		event.DurationMs = max(event.DurationMs, took.Milliseconds())
	case len(r.pending) >= maxPending:
		r.dropped++
	default:
		r.pending[k] = &Event{Service: r.service, Code: code, Route: route, Status: status,
			RequestID: id, Count: 1, DurationMs: max(took.Milliseconds(), 0), FirstAt: at, LastAt: at}
	}
	r.mu.Unlock()
	r.start.Do(func() { go r.loop() })
}

// Pending is what was counted and not sent yet (tests).
func (r *Recorder) Pending() []Event {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	events := make([]Event, 0, len(r.pending))
	for _, event := range r.pending {
		events = append(events, *event)
	}
	return events
}

func (r *Recorder) loop() {
	ticker := time.NewTicker(FlushInterval)
	defer ticker.Stop()
	for range ticker.C {
		_ = r.Flush(context.Background())
	}
}

// Flush sends what was counted. On failure the counts are kept for the next
// flush (merged with new ones); the service's work never waits for it.
func (r *Recorder) Flush(ctx context.Context) error {
	if r == nil || r.url == "" {
		return nil
	}
	r.mu.Lock()
	if r.dropped > 0 {
		at := r.now().UTC()
		k := key{code: ServerError}
		if event, ok := r.pending[k]; ok {
			event.Count += r.dropped
		} else {
			r.pending[k] = &Event{Service: r.service, Code: ServerError, Count: r.dropped, FirstAt: at, LastAt: at}
		}
		r.dropped = 0
	}
	batch := Batch{Events: make([]Event, 0, len(r.pending))}
	for _, event := range r.pending {
		batch.Events = append(batch.Events, *event)
	}
	r.pending = map[key]*Event{}
	r.mu.Unlock()
	if len(batch.Events) == 0 {
		return nil
	}
	err := r.post(ctx, batch)
	if err != nil {
		r.mu.Lock()
		for _, event := range batch.Events {
			k := key{code: event.Code, route: event.Route, status: event.Status}
			if current, ok := r.pending[k]; ok {
				current.Count += event.Count
				current.FirstAt = event.FirstAt
				current.DurationMs = max(current.DurationMs, event.DurationMs)
			} else if len(r.pending) < maxPending {
				copied := event
				r.pending[k] = &copied
			}
		}
		r.mu.Unlock()
		r.logger.Debug("diagnostics not sent", "events", len(batch.Events), "error", err)
	}
	return err
}

func (r *Recorder) post(ctx context.Context, batch Batch) error {
	body, err := json.Marshal(batch)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, r.url+Path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Life-OS-Actor", "system:"+r.service)
	response, err := r.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		return fmt.Errorf("activity answered %s", response.Status)
	}
	return nil
}
