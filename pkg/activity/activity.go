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
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"

	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/pkg/identity"
)

const (
	// CorrelationIDHeader names the workflow an operation belongs to. It is the
	// same header the shared HTTP runtime already propagates.
	CorrelationIDHeader = "X-Correlation-ID"
	// CausationIDHeader carries the id of the activity event that directly
	// caused the request.
	CausationIDHeader = "X-Causation-ID"
	// ActorHeader says who makes a request: "user" (the owner),
	// "agent:<name>" (the Life OS AI), "system:<name>" (a Life OS component),
	// or "dev:<name>" (a development tool or agent working on Life OS, such as
	// Claude Code). Writes without a valid value are rejected (RequireActor),
	// so nothing is attributed to the owner by default.
	ActorHeader = "X-Life-OS-Actor"
	// ActorRequiredCode is the error code of a write rejected by RequireActor.
	ActorRequiredCode = "actor_required"
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

// Dev is a development tool or agent working on Life OS, for example
// Dev("claude-code"). It is neither the owner nor the Life OS AI, so its
// writes to live data stay distinguishable from both.
func Dev(name string) Actor { return Actor{Kind: "dev", Name: name} }

// headerValue is the ActorHeader value for a, or "" for the zero Actor. The
// user is sent explicitly, so a forwarded user trace still passes
// RequireActor.
func (a Actor) headerValue() string {
	switch a.Kind {
	case "":
		return ""
	case "user":
		return "user"
	}
	return a.Kind + ":" + a.Name
}

// actorName is what may follow "agent:", "system:", or "dev:". The gateway
// applies the same rule to the dev claims it lets through.
var actorName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// ParseActor reads an ActorHeader value: "user", or "agent:", "system:", or
// "dev:" followed by a name of 1-64 letters, digits, '.', '_', or '-'. ok is
// false for anything else, including an empty value; it never falls back to
// the user.
func ParseActor(value string) (actor Actor, ok bool) {
	value = strings.TrimSpace(value)
	if value == "user" {
		return User(), true
	}
	kind, name, _ := strings.Cut(value, ":")
	if (kind == "agent" || kind == "system" || kind == "dev") && actorName.MatchString(name) {
		return Actor{Kind: kind, Name: name}, true
	}
	return Actor{}, false
}

// RequireActor is middleware that rejects writes (any method but GET, HEAD,
// and OPTIONS) without a valid ActorHeader with 400 actor_required, so no
// change is silently attributed to the owner. Reads stay open, as do the
// runtime's /health and /ready. A request whose verified identity token
// names a person who signed in (pkg/identity) needs no header: the actor is
// that person (PrincipalActor).
func RequireActor() fiber.Handler {
	return func(c fiber.Ctx) error {
		switch c.Method() {
		case fiber.MethodGet, fiber.MethodHead, fiber.MethodOptions:
			return c.Next()
		}
		if path := c.Path(); path == httpserver.HealthPath || path == httpserver.ReadyPath {
			return c.Next()
		}
		if principal, ok := identity.FromFiber(c); ok {
			if _, ok := PrincipalActor(principal); ok {
				return c.Next()
			}
		}
		if _, ok := ParseActor(c.Get(ActorHeader)); !ok {
			return httpserver.NewError(fiber.StatusBadRequest, ActorRequiredCode,
				"Writes must say who makes them: send the "+ActorHeader+" header as user, agent:<name>, system:<name>, or dev:<name>.")
		}
		return c.Next()
	}
}

type Subject struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type Event struct {
	ID            uuid.UUID  `json:"id"`
	Type          string     `json:"type"`
	Source        string     `json:"source"`
	Subject       Subject    `json:"subject"`
	CorrelationID string     `json:"correlationId"`
	CausationID   *uuid.UUID `json:"causationId,omitempty"`
	Actor         Actor      `json:"actor"`
	// ActorUserID is the person the action is for
	// (docs/architecture/multi-user.md#background-work): the user who
	// signed in, or the first account under legacy access, also when a
	// development tool or the AI makes the change for them. Nil for a
	// service's own work. Emit fills it from the request's verified
	// identity when the event leaves it nil.
	ActorUserID *uuid.UUID     `json:"actorUserId,omitempty"`
	Outcome     string         `json:"outcome"`
	Summary     string         `json:"summary"`
	Data        map[string]any `json:"data,omitempty"`
	OccurredAt  time.Time      `json:"occurredAt"`
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
// Fiber context's Get method or http.Header.Get. A missing or invalid actor
// leaves Trace.Actor zero; writes never get that far (RequireActor).
func TraceFromHeaders(get func(string) string) Trace {
	actor, _ := ParseActor(get(ActorHeader))
	trace := Trace{CorrelationID: sanitize(get(CorrelationIDHeader)), Actor: actor}
	if id, err := uuid.Parse(sanitize(get(CausationIDHeader))); err == nil {
		trace.CausationID = &id
	}
	return trace
}

// FromRequest reads the Trace of a request served by the shared HTTP runtime,
// whose middleware has already validated or assigned the correlation id.
// The actor comes from the verified identity token when it names a person
// who signed in, else from ActorHeader.
func FromRequest(c fiber.Ctx) Trace {
	trace := TraceFromHeaders(func(key string) string { return c.Get(key) })
	if id := httpserver.CorrelationID(c); id != "" {
		trace.CorrelationID = id
	}
	if principal, ok := identity.FromFiber(c); ok {
		if actor, ok := PrincipalActor(principal); ok {
			trace.Actor = actor
		}
	}
	return trace
}

// PrincipalActor is the actor a verified identity token makes: the user, for
// a person who signed in. ok is false for legacy access (a request with no
// credential), whose actor still comes from ActorHeader as before accounts
// existed, so that development tools keep their dev:<name>; and for the
// delegations and service assertions of P21-07.
func PrincipalActor(p identity.Principal) (Actor, bool) {
	if p.Kind != identity.KindUser || p.IsLegacy() {
		return Actor{}, false
	}
	return User(), true
}

// ActorUserID is the person a request acts for, from its verified identity
// token: the user for a person (a session, or legacy access as the first
// account) and the subject of a delegation; nil for a service's assertion,
// a space-bound delegation, or a request without a token.
func ActorUserID(ctx context.Context) *uuid.UUID {
	principal, ok := identity.FromContext(ctx)
	if !ok || principal.Kind == identity.KindService || principal.UserID == uuid.Nil {
		return nil
	}
	id := principal.UserID
	return &id
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
		// Never the owner by default: an event without an actor was made by
		// the emitting service itself.
		event.Actor = System(event.Source)
	}
	if event.ActorUserID == nil {
		event.ActorUserID = ActorUserID(ctx)
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
