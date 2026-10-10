// Package calendar hands feed events to the Calendar service through its
// integration API (services/calendar/api/openapi.yaml, /v1/integrations/
// {integration}/sources/...), the only way calendar-feeds touches Calendar.
// The types mirror that contract; calendar-feeds never imports Calendar's
// code or reads its database.
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
	"strings"
	"time"

	"github.com/GAMch1k/life-os/pkg/activity"
)

// Integration is this service's name in Calendar: the integration its
// sources belong to, and the name of its system actor.
const Integration = "calendar-feeds"

// Actor is who calendar-feeds' writes to Calendar are attributed to.
const Actor = "system:" + Integration

// Reminder is a reminder as Calendar takes it.
type Reminder struct {
	Kind          string  `json:"kind"`
	MinutesBefore *int    `json:"minutesBefore,omitempty"`
	DaysBefore    *int    `json:"daysBefore,omitempty"`
	LocalTime     *string `json:"localTime,omitempty"`
}

// Source is one of the integration's sources in Calendar
// (IntegrationSource).
type Source struct {
	ID               string          `json:"id"`
	Integration      string          `json:"integration"`
	ExternalID       string          `json:"externalId"`
	Provider         string          `json:"provider"`
	Name             string          `json:"name"`
	Enabled          bool            `json:"enabled"`
	ReadOnly         bool            `json:"readOnly"`
	InProcess        bool            `json:"inProcess"`
	DefaultReminders json.RawMessage `json:"defaultReminders"`
	DefaultPriority  string          `json:"defaultPriority"`
}

// Registration registers or updates a source
// (RegisterIntegrationSourceRequest). DefaultReminders and DefaultPriority
// are sent only when set: raw JSON (null for none) and a priority; left out,
// they say nothing about the owner's settings.
type Registration struct {
	Name             string          `json:"name"`
	Provider         string          `json:"provider"`
	ReadOnly         bool            `json:"readOnly"`
	DefaultReminders json.RawMessage `json:"defaultReminders,omitempty"`
	DefaultPriority  string          `json:"defaultPriority,omitempty"`
}

// Event is one feed event (SourceEvent): a single event, timed (StartAt and
// EndAt, RFC 3339) or all-day (StartDate and EndDate, inclusive
// YYYY-MM-DD). A feed gives no reminders and no priority, so the owner's
// stay.
type Event struct {
	ExternalUID string     `json:"externalUid"`
	Title       string     `json:"title"`
	Description *string    `json:"description,omitempty"`
	Location    *string    `json:"location,omitempty"`
	AllDay      bool       `json:"allDay"`
	StartAt     *time.Time `json:"startAt,omitempty"`
	EndAt       *time.Time `json:"endAt,omitempty"`
	StartDate   *string    `json:"startDate,omitempty"`
	EndDate     *string    `json:"endDate,omitempty"`
	TimeZone    *string    `json:"timeZone,omitempty"`
	Status      string     `json:"status,omitempty"`
}

// Window says which stored events a batch speaks for (SyncWindow).
type Window struct {
	UpcomingOnly bool `json:"upcomingOnly,omitempty"`
}

// Sync is one batch (SyncIntegrationSourceRequest). Feeds send snapshots.
type Sync struct {
	Mode       string  `json:"mode"`
	Items      []Event `json:"items"`
	Window     *Window `json:"window,omitempty"`
	EmptyGuard bool    `json:"emptyGuard,omitempty"`
}

// SyncResult is what a batch changed (SyncIntegrationSourceResult).
type SyncResult struct {
	Mode      string `json:"mode"`
	Received  int    `json:"received"`
	Inserted  int    `json:"inserted"`
	Updated   int    `json:"updated"`
	Removed   int    `json:"removed"`
	Restored  int    `json:"restored"`
	Unchanged int    `json:"unchanged"`
}

// Error codes of Calendar's answers this service acts on.
const (
	CodeSourceDisabled  = "source_disabled"
	CodeSourceInProcess = "source_in_process"
	CodeEmptySnapshot   = "empty_snapshot"
)

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

// Is reports whether err is Calendar's answer with this status and, when
// code is not empty, this error code.
func Is(err error, status int, code string) bool {
	var answer *StatusError
	return errors.As(err, &answer) && answer.Status == status && (code == "" || answer.Code == code)
}

// IsNotFound reports whether Calendar has no such source.
func IsNotFound(err error) bool { return Is(err, http.StatusNotFound, "") }

// ErrNotConfigured means CALENDAR_SERVICE_URL is not set.
var ErrNotConfigured = errors.New("Calendar is not configured (CALENDAR_SERVICE_URL)")

// Client calls Calendar at URL.
type Client struct {
	URL  string
	HTTP *http.Client
}

// New returns a client for the Calendar service at baseURL. A sync of a
// large feed is one transaction in Calendar; the timeout leaves it room.
func New(baseURL string) *Client {
	return &Client{URL: strings.TrimRight(strings.TrimSpace(baseURL), "/"), HTTP: &http.Client{Timeout: 60 * time.Second}}
}

func sourcePath(externalID string) string {
	return "/v1/integrations/" + Integration + "/sources/" + url.PathEscape(externalID)
}

// ListSources returns every source of the integration, oldest first.
func (c *Client) ListSources(ctx context.Context) ([]Source, error) {
	var list struct {
		Items []Source `json:"items"`
	}
	err := c.do(ctx, http.MethodGet, "/v1/integrations/"+Integration+"/sources", nil, &list)
	return list.Items, err
}

// GetSource returns one source (IsNotFound when there is none).
func (c *Client) GetSource(ctx context.Context, externalID string) (Source, error) {
	var source Source
	err := c.do(ctx, http.MethodGet, sourcePath(externalID), nil, &source)
	return source, err
}

// RegisterSource adds the source or updates it (idempotent by externalID).
func (c *Client) RegisterSource(ctx context.Context, externalID string, registration Registration) (Source, error) {
	var source Source
	err := c.do(ctx, http.MethodPut, sourcePath(externalID), registration, &source)
	return source, err
}

// DeleteSource removes the source and its events (IsNotFound when there is
// none).
func (c *Client) DeleteSource(ctx context.Context, externalID string) error {
	return c.do(ctx, http.MethodDelete, sourcePath(externalID), nil, nil)
}

// SyncSource writes one batch of the source's events.
func (c *Client) SyncSource(ctx context.Context, externalID string, batch Sync) (SyncResult, error) {
	var result SyncResult
	err := c.do(ctx, http.MethodPost, sourcePath(externalID)+"/sync", batch, &result)
	return result, err
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	if c == nil || c.URL == "" {
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
	// The request's correlation carries over; the actor is always this
	// integration, which is the only one Calendar lets use its sources.
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
		return fmt.Errorf("Calendar could not be reached: %w", err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
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
