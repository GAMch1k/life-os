// Package calendar is how the Google Calendar integration talks to the
// Calendar service: only through Calendar's public API
// (services/calendar/api/openapi.yaml), as system:google-calendar. The
// integration registers its sources and syncs their events
// (/v1/integrations/google-calendar/...), follows the change feed of Life
// OS events and of its own sources' events (/v1/integrations/changes), and
// applies changes made in Google to exported Life OS events through the
// events API. The types mirror that contract; this service never imports
// Calendar's code or reads its database.
package calendar

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/GAMch1k/life-os/pkg/activity"
)

// Integration is this integration's name in Calendar.
const Integration = "google-calendar"

// Actor is who this service's requests to Calendar are attributed to.
const Actor = "system:" + Integration

// Reminder is Calendar's Reminder.
type Reminder struct {
	Kind          string  `json:"kind"`
	MinutesBefore *int    `json:"minutesBefore,omitempty"`
	DaysBefore    *int    `json:"daysBefore,omitempty"`
	LocalTime     *string `json:"localTime,omitempty"`
}

// Recurrence is Calendar's Recurrence.
type Recurrence struct {
	Frequency string     `json:"frequency"`
	Interval  int        `json:"interval"`
	Until     *time.Time `json:"until,omitempty"`
	Count     *int       `json:"count,omitempty"`
	ByWeekday []string   `json:"byWeekday,omitempty"`
}

// Registration is a RegisterIntegrationSourceRequest. DefaultReminders is
// raw JSON: absent says nothing, null says none.
type Registration struct {
	Name             string          `json:"name"`
	Provider         string          `json:"provider"`
	ReadOnly         bool            `json:"readOnly"`
	DefaultReminders json.RawMessage `json:"defaultReminders,omitempty"`
}

// Source is an IntegrationSource.
type Source struct {
	ID         string `json:"id"`
	ExternalID string `json:"externalId"`
	Provider   string `json:"provider"`
	Name       string `json:"name"`
	Enabled    bool   `json:"enabled"`
	ReadOnly   bool   `json:"readOnly"`
	InProcess  bool   `json:"inProcess"`
}

// SourceEvent is one event of a source as the integration has it.
type SourceEvent struct {
	ExternalUID         string            `json:"externalUid"`
	PreviousExternalUID string            `json:"previousExternalUid,omitempty"`
	Title               string            `json:"title"`
	Description         *string           `json:"description,omitempty"`
	Location            *string           `json:"location,omitempty"`
	AllDay              bool              `json:"allDay"`
	StartAt             *time.Time        `json:"startAt,omitempty"`
	EndAt               *time.Time        `json:"endAt,omitempty"`
	StartDate           string            `json:"startDate,omitempty"`
	EndDate             string            `json:"endDate,omitempty"`
	TimeZone            *string           `json:"timeZone,omitempty"`
	Status              string            `json:"status,omitempty"`
	Recurrence          *Recurrence       `json:"recurrence,omitempty"`
	Exceptions          []SourceException `json:"exceptions,omitempty"`
	Reminders           *SourceReminders  `json:"reminders,omitempty"`
	Priority            string            `json:"priority,omitempty"`
	ReadOnlyReason      string            `json:"readOnlyReason,omitempty"`
}

// SourceException is one changed or skipped occurrence of a series.
// Changes is Calendar's OccurrenceChanges.
type SourceException struct {
	RecurrenceID string          `json:"recurrenceId"`
	Cancelled    bool            `json:"cancelled,omitempty"`
	Changes      json.RawMessage `json:"changes,omitempty"`
}

// SourceReminders are the reminders the source gives an event.
type SourceReminders struct {
	UseDefault bool       `json:"useDefault"`
	Reminders  []Reminder `json:"reminders"`
}

// Window says which stored events a sync batch speaks for.
type Window struct {
	Start *time.Time `json:"start,omitempty"`
}

// SyncRequest is a SyncIntegrationSourceRequest.
type SyncRequest struct {
	Mode       string        `json:"mode"`
	Items      []SourceEvent `json:"items"`
	Deleted    []string      `json:"deleted,omitempty"`
	Window     *Window       `json:"window,omitempty"`
	FeedCursor string        `json:"feedCursor,omitempty"`
}

// SyncResult is what a batch changed.
type SyncResult struct {
	Mode      string `json:"mode"`
	Received  int    `json:"received"`
	Inserted  int    `json:"inserted"`
	Updated   int    `json:"updated"`
	Removed   int    `json:"removed"`
	Restored  int    `json:"restored"`
	Unchanged int    `json:"unchanged"`
}

// EventSource is a CalendarEvent's source.
type EventSource struct {
	Kind        string  `json:"kind"`
	SourceID    *string `json:"sourceId,omitempty"`
	Name        *string `json:"name,omitempty"`
	Integration *string `json:"integration,omitempty"`
	Provider    *string `json:"provider,omitempty"`
	ReadOnly    *bool   `json:"readOnly,omitempty"`
	ExternalUID *string `json:"externalUid,omitempty"`
}

// Event is a CalendarEvent: the fields the integration reads.
type Event struct {
	ID                  string      `json:"id"`
	Title               string      `json:"title"`
	Description         *string     `json:"description,omitempty"`
	Location            *string     `json:"location,omitempty"`
	AllDay              bool        `json:"allDay"`
	StartAt             *time.Time  `json:"startAt,omitempty"`
	EndAt               *time.Time  `json:"endAt,omitempty"`
	StartDate           *string     `json:"startDate,omitempty"`
	EndDate             *string     `json:"endDate,omitempty"`
	TimeZone            *string     `json:"timeZone,omitempty"`
	Recurrence          *Recurrence `json:"recurrence,omitempty"`
	Reminders           []Reminder  `json:"reminders"`
	UseDefaultReminders bool        `json:"useDefaultReminders"`
	DefaultReminders    []Reminder  `json:"defaultReminders"`
	EffectiveReminders  []Reminder  `json:"effectiveReminders"`
	Priority            string      `json:"priority"`
	DefaultPriority     string      `json:"defaultPriority"`
	Status              string      `json:"status"`
	RemovedFromSourceAt *time.Time  `json:"removedFromSourceAt,omitempty"`
	Source              EventSource `json:"source"`
	Editable            bool        `json:"editable"`
	ReadOnlyReason      *string     `json:"readOnlyReason,omitempty"`
	UpdatedAt           time.Time   `json:"updatedAt"`
}

// OccurrenceException is one occurrence of a series with its own changes
// or skipped; Changes is in Calendar's stored form (a null description or
// location clears it).
type OccurrenceException struct {
	RecurrenceID string          `json:"recurrenceId"`
	Cancelled    bool            `json:"cancelled"`
	Changes      json.RawMessage `json:"changes"`
}

// ChangeSource is where an integration's event in the feed belongs.
type ChangeSource struct {
	SourceID    string `json:"sourceId"`
	Integration string `json:"integration"`
	ExternalID  string `json:"externalId"`
	ExternalUID string `json:"externalUid"`
}

// Change is one ChangeFeedEntry.
type Change struct {
	EventID    string                `json:"eventId"`
	Source     *ChangeSource         `json:"source"`
	Deleted    bool                  `json:"deleted"`
	Event      *Event                `json:"event"`
	Exceptions []OccurrenceException `json:"exceptions"`
	Actors     []string              `json:"actors"`
	ChangedAt  time.Time             `json:"changedAt"`
}

// Own reports whether the change is only this integration's own.
func (c Change) Own() bool {
	return len(c.Actors) == 1 && c.Actors[0] == Actor
}

// Snapshot phase of the change feed.
const (
	PhaseSnapshot = "snapshot"
	PhaseChanges  = "changes"
)

// ChangePage is a ChangeFeedPage.
type ChangePage struct {
	Items      []Change `json:"items"`
	NextCursor string   `json:"nextCursor"`
	HasMore    bool     `json:"hasMore"`
	Phase      string   `json:"phase"`
}

// StatusError is an answer other than success.
type StatusError struct {
	Status  int
	Code    string
	Message string
}

func (e *StatusError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("Calendar returned %d %s: %s", e.Status, e.Code, e.Message)
	}
	return fmt.Sprintf("Calendar returned %d", e.Status)
}

// UnavailableError means Calendar could not be reached or failed on its
// side; trying again later may succeed.
type UnavailableError struct{ Err error }

func (e *UnavailableError) Error() string { return "Calendar could not be reached: " + e.Err.Error() }
func (e *UnavailableError) Unwrap() error { return e.Err }

// ErrNotConfigured means CALENDAR_SERVICE_URL is not set.
var ErrNotConfigured = errors.New("Calendar is not configured (CALENDAR_SERVICE_URL)")

// HasCode reports whether err is Calendar's answer with this status and
// error code ("" for any code).
func HasCode(err error, status int, code string) bool {
	var answer *StatusError
	return errors.As(err, &answer) && answer.Status == status && (code == "" || answer.Code == code)
}

// IsNotFound reports whether Calendar answered 404.
func IsNotFound(err error) bool { return HasCode(err, http.StatusNotFound, "") }

// IsUnavailable reports whether Calendar could not be reached or failed on
// its side (5xx).
func IsUnavailable(err error) bool {
	var unavailable *UnavailableError
	var answer *StatusError
	return errors.As(err, &unavailable) || errors.Is(err, ErrNotConfigured) || errors.As(err, &answer) && answer.Status >= 500
}

// Client calls Calendar at URL.
type Client struct {
	URL  string
	HTTP *http.Client
}

// New returns a client for the Calendar service at baseURL.
func New(baseURL string) *Client {
	return &Client{URL: strings.TrimRight(strings.TrimSpace(baseURL), "/"), HTTP: &http.Client{Timeout: 60 * time.Second}}
}

// Configured reports whether Calendar's address is set.
func (c *Client) Configured() bool { return c != nil && c.URL != "" }

func sourcePath(externalID string) string {
	return "/v1/integrations/" + Integration + "/sources/" + url.PathEscape(externalID)
}

// RegisterSource adds or updates the source of a calendar (idempotent).
func (c *Client) RegisterSource(ctx context.Context, externalID string, registration Registration) (Source, error) {
	var source Source
	err := c.do(ctx, http.MethodPut, sourcePath(externalID), registration, &source)
	return source, err
}

// GetSource reads a source; found is false when Calendar has none.
func (c *Client) GetSource(ctx context.Context, externalID string) (Source, bool, error) {
	var source Source
	err := c.do(ctx, http.MethodGet, sourcePath(externalID), nil, &source)
	if IsNotFound(err) {
		return Source{}, false, nil
	}
	return source, err == nil, err
}

// DeleteSource removes a source with its events; a source Calendar does
// not have is already removed.
func (c *Client) DeleteSource(ctx context.Context, externalID string) error {
	err := c.do(ctx, http.MethodDelete, sourcePath(externalID), nil, nil)
	if IsNotFound(err) {
		return nil
	}
	return err
}

// Sync writes one batch of a source's events.
func (c *Client) Sync(ctx context.Context, externalID string, request SyncRequest) (SyncResult, error) {
	if request.Items == nil {
		request.Items = []SourceEvent{}
	}
	var result SyncResult
	err := c.do(ctx, http.MethodPost, sourcePath(externalID)+"/sync", request, &result)
	return result, err
}

// Changes reads one page of the change feed after cursor ("" starts with
// a snapshot), scope own.
func (c *Client) Changes(ctx context.Context, cursor string, limit int) (ChangePage, error) {
	query := url.Values{"scope": {"own"}, "limit": {strconv.Itoa(limit)}}
	if cursor != "" {
		query.Set("cursor", cursor)
	}
	var page ChangePage
	err := c.do(ctx, http.MethodGet, "/v1/integrations/changes?"+query.Encode(), nil, &page)
	return page, err
}

// UpdateEvent changes the fields in body of an event (All events).
func (c *Client) UpdateEvent(ctx context.Context, eventID string, body map[string]any) (Event, error) {
	var event Event
	err := c.do(ctx, http.MethodPatch, "/v1/events/"+url.PathEscape(eventID), body, &event)
	return event, err
}

// UpdateOccurrence changes one occurrence of a series (This event).
func (c *Client) UpdateOccurrence(ctx context.Context, eventID, recurrenceID string, body map[string]any) error {
	return c.do(ctx, http.MethodPatch, "/v1/events/"+url.PathEscape(eventID)+"/occurrences/"+url.PathEscape(recurrenceID), body, nil)
}

// GetEvent reads an event.
func (c *Client) GetEvent(ctx context.Context, eventID string) (Event, error) {
	var event Event
	err := c.do(ctx, http.MethodGet, "/v1/events/"+url.PathEscape(eventID), nil, &event)
	return event, err
}

// maxResponseBytes bounds a Calendar answer (a feed page of 200 events).
const maxResponseBytes = 32 << 20

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	if !c.Configured() {
		return ErrNotConfigured
	}
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.URL+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Accept", "application/json")
	trace := activity.TraceFrom(ctx)
	trace.Actor = activity.Actor{}
	trace.Apply(request.Header)
	request.Header.Set(activity.ActorHeader, Actor)
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		var urlError *url.Error
		if errors.As(err, &urlError) {
			err = urlError.Err
		}
		return &UnavailableError{Err: err}
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return &UnavailableError{Err: err}
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		status := &StatusError{Status: response.StatusCode}
		var envelope struct {
			Error struct{ Code, Message string } `json:"error"`
		}
		if json.Unmarshal(raw, &envelope) == nil {
			status.Code, status.Message = envelope.Error.Code, envelope.Error.Message
		}
		return status
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("Calendar returned an unreadable answer: %w", err)
	}
	return nil
}
