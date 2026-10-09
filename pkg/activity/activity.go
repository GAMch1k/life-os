// Package activity is the infrastructure client services use to record
// user-facing activity events in the Activity service.
//
// It carries no domain logic: each owning service decides which of its
// operations are worth recording and what they mean. The Activity OpenAPI
// contract (services/activity/api/openapi.yaml) is the source of truth for the
// event shape mirrored by Event.
package activity

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"

	"github.com/GAMch1k/life-os/pkg/httpserver"
)

const (
	// CorrelationIDHeader names the workflow an operation belongs to. It is the
	// same header the shared HTTP runtime already propagates.
	CorrelationIDHeader = "X-Correlation-ID"
	// CausationIDHeader carries the id of the activity event that directly
	// caused the request.
	CausationIDHeader = "X-Causation-ID"
	// ActorHeader identifies a non-user caller as "agent:<name>" or
	// "system:<name>". Requests without it were made by the user.
	ActorHeader = "X-Life-OS-Actor"
)

// Outcomes recorded on events.
const (
	Succeeded = "succeeded"
	Pending   = "pending"
	Failed    = "failed"
)

type Actor struct {
	Kind string `json:"kind"`
	Name string `json:"name,omitempty"`
}

func User() Actor              { return Actor{Kind: "user"} }
func Agent(name string) Actor  { return Actor{Kind: "agent", Name: name} }
func System(name string) Actor { return Actor{Kind: "system", Name: name} }
func (a Actor) IsZero() bool   { return a.Kind == "" }
func (a Actor) headerValue() string {
	if a.Kind == "" || a.Kind == "user" {
		return ""
	}
	return a.Kind + ":" + a.Name
}

// ParseActor reads an ActorHeader value. Unknown or empty values mean the user.
func ParseActor(value string) Actor {
	kind, name, _ := strings.Cut(sanitize(value), ":")
	if kind == "agent" || kind == "system" {
		return Actor{Kind: kind, Name: name}
	}
	return User()
}

type Subject struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type Event struct {
	ID            uuid.UUID      `json:"id"`
	Type          string         `json:"type"`
	Source        string         `json:"source"`
	Subject       Subject        `json:"subject"`
	CorrelationID string         `json:"correlationId"`
	CausationID   *uuid.UUID     `json:"causationId,omitempty"`
	Actor         Actor          `json:"actor"`
	Outcome       string         `json:"outcome"`
	Summary       string         `json:"summary"`
	Data          map[string]any `json:"data,omitempty"`
	OccurredAt    time.Time      `json:"occurredAt"`
}

var namespace = uuid.MustParse("6f1c7a52-6a3e-4c55-9f0d-8a1e6f2b7c11")

// EventID derives a stable event id from the event type and the identifiers
// that make the occurrence unique, for example EventID("task.created",
// taskID). Re-emitting the same occurrence then deduplicates in the store,
// and other services can name it as a causation without looking it up.
func EventID(eventType string, keys ...string) uuid.UUID {
	return uuid.NewSHA1(namespace, []byte(eventType+"\x00"+strings.Join(keys, "\x00")))
}

// Trace is the workflow context received with a request and forwarded on
// outgoing calls.
type Trace struct {
	CorrelationID string
	CausationID   *uuid.UUID
	Actor         Actor
}

// TraceFromHeaders reads Trace from request headers via get, for example a
// Fiber context's Get method or http.Header.Get.
func TraceFromHeaders(get func(string) string) Trace {
	trace := Trace{CorrelationID: sanitize(get(CorrelationIDHeader)), Actor: ParseActor(get(ActorHeader))}
	if id, err := uuid.Parse(sanitize(get(CausationIDHeader))); err == nil {
		trace.CausationID = &id
	}
	return trace
}

// FromRequest reads the Trace of a request served by the shared HTTP runtime,
// whose middleware has already validated or assigned the correlation id.
func FromRequest(c fiber.Ctx) Trace {
	trace := TraceFromHeaders(func(key string) string { return c.Get(key) })
	if id := httpserver.CorrelationID(c); id != "" {
		trace.CorrelationID = id
	}
	return trace
}

// Apply writes the Trace onto outgoing request headers.
func (t Trace) Apply(header http.Header) {
	if t.CorrelationID != "" {
		header.Set(CorrelationIDHeader, t.CorrelationID)
	}
	if t.CausationID != nil {
		header.Set(CausationIDHeader, t.CausationID.String())
	}
	if value := t.Actor.headerValue(); value != "" {
		header.Set(ActorHeader, value)
	}
}

type traceKey struct{}

func WithTrace(ctx context.Context, trace Trace) context.Context {
	return context.WithValue(ctx, traceKey{}, trace)
}

func TraceFrom(ctx context.Context) Trace {
	trace, _ := ctx.Value(traceKey{}).(Trace)
	return trace
}

// Emitter records events in the Activity service. A nil Emitter or one without
// a URL is a no-op, so services run unchanged when Activity is not configured.
//
// Emission is best effort: it retries briefly and logs failure, but never fails
// the domain operation that already happened.
type Emitter struct {
	URL    string
	Source string
	HTTP   *http.Client
	Logger *slog.Logger
}

func NewEmitter(url, source string, logger *slog.Logger) *Emitter {
	if logger == nil {
		logger = slog.Default()
	}
	return &Emitter{URL: strings.TrimRight(url, "/"), Source: source, HTTP: &http.Client{Timeout: 2 * time.Second}, Logger: logger}
}

var retryDelays = []time.Duration{0, 100 * time.Millisecond, 400 * time.Millisecond}

func (e *Emitter) Emit(ctx context.Context, event Event) {
	if e == nil || e.URL == "" {
		return
	}
	if event.ID == uuid.Nil {
		event.ID = uuid.New()
	}
	if event.Source == "" {
		event.Source = e.Source
	}
	if event.Actor.IsZero() {
		event.Actor = User()
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now().UTC()
	}
	if event.CorrelationID == "" {
		event.CorrelationID = event.ID.String()
	}
	body, err := json.Marshal(event)
	if err != nil {
		e.Logger.Warn("activity event not encodable", "type", event.Type, "error", err)
		return
	}
	// The caller's request may already be finishing; the event still describes
	// something that happened.
	ctx = context.WithoutCancel(ctx)
	for _, delay := range retryDelays {
		time.Sleep(delay)
		if err = e.post(ctx, body); err == nil {
			return
		}
	}
	e.Logger.Warn("activity event not recorded", "type", event.Type, "id", event.ID, "error", err)
}

func (e *Emitter) post(ctx context.Context, body []byte) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, e.URL+"/v1/activity-events", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("content-type", "application/json")
	client := e.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		return fmt.Errorf("activity service returned %s", response.Status)
	}
	return nil
}

// Preview shortens user text for event data and summaries.
func Preview(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	if runes := []rune(text); len(runes) > limit {
		return string(runes[:limit-1]) + "…"
	}
	return text
}

func sanitize(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 128 {
		return ""
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return ""
		}
	}
	return value
}
