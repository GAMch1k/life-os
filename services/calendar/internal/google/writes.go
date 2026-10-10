package google

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Writer is the part of Google's Calendar API that two-way sync writes
// through (P11-03): the Life OS calendar and events in it, and changes to
// events of the calendars chosen to sync.
type Writer interface {
	// InsertCalendar creates a secondary calendar owned by the account.
	InsertCalendar(ctx context.Context, accessToken, summary, timeZone string) (CalendarEntry, error)
	// GetEvent reads one event (a deleted one has status cancelled).
	GetEvent(ctx context.Context, accessToken, calendarID, eventID string) (Event, error)
	// InsertEvent creates an event; body may name its id.
	InsertEvent(ctx context.Context, accessToken, calendarID string, body map[string]any) (Event, error)
	// PatchEvent changes the fields in body; a non-empty etag makes Google
	// refuse the change when the event changed since (412).
	PatchEvent(ctx context.Context, accessToken, calendarID, eventID, etag string, body map[string]any) (Event, error)
	// DeleteEvent deletes an event (or cancels an instance), with etag as
	// for PatchEvent.
	DeleteEvent(ctx context.Context, accessToken, calendarID, eventID, etag string) error
	// Instance reads the instance of a recurring event that originally
	// starts at originalStart (RFC 3339, or a date for an all-day event).
	Instance(ctx context.Context, accessToken, calendarID, eventID, originalStart string) (Event, error)
}

// maxWriteBody bounds what Calendar reads of a write's answer.
const maxWriteBody = 1 << 20

func (c *Client) calendarURL(calendarID string) string {
	return c.endpoints.CalendarAPI + "/calendars/" + url.PathEscape(calendarID)
}

func (c *Client) eventURL(calendarID, eventID string) string {
	return c.calendarURL(calendarID) + "/events/" + url.PathEscape(eventID)
}

func (c *Client) InsertCalendar(ctx context.Context, accessToken, summary, timeZone string) (CalendarEntry, error) {
	body := map[string]any{"summary": summary, "description": "Events created in Life OS. Life OS keeps this calendar up to date; hide or remove it in Google Calendar at any time."}
	if timeZone != "" {
		body["timeZone"] = timeZone
	}
	var created struct {
		ID      string `json:"id"`
		Summary string `json:"summary"`
	}
	if err := c.send(ctx, http.MethodPost, c.endpoints.CalendarAPI+"/calendars", accessToken, "", body, &created); err != nil {
		return CalendarEntry{}, err
	}
	if created.ID == "" {
		return CalendarEntry{}, &UnavailableError{Err: errors.New("Google created a calendar without an id")}
	}
	return CalendarEntry{ID: created.ID, Summary: firstNonEmpty(created.Summary, summary), AccessRole: "owner"}, nil
}

func (c *Client) GetEvent(ctx context.Context, accessToken, calendarID, eventID string) (Event, error) {
	var event Event
	err := c.send(ctx, http.MethodGet, c.eventURL(calendarID, eventID), accessToken, "", nil, &event)
	return event, err
}

func (c *Client) InsertEvent(ctx context.Context, accessToken, calendarID string, body map[string]any) (Event, error) {
	var event Event
	err := c.send(ctx, http.MethodPost, c.calendarURL(calendarID)+"/events", accessToken, "", body, &event)
	return event, err
}

func (c *Client) PatchEvent(ctx context.Context, accessToken, calendarID, eventID, etag string, body map[string]any) (Event, error) {
	var event Event
	err := c.send(ctx, http.MethodPatch, c.eventURL(calendarID, eventID), accessToken, etag, body, &event)
	return event, err
}

func (c *Client) DeleteEvent(ctx context.Context, accessToken, calendarID, eventID, etag string) error {
	return c.send(ctx, http.MethodDelete, c.eventURL(calendarID, eventID), accessToken, etag, nil, nil)
}

func (c *Client) Instance(ctx context.Context, accessToken, calendarID, eventID, originalStart string) (Event, error) {
	query := url.Values{"originalStart": {originalStart}, "showDeleted": {"true"}, "maxResults": {"5"}}
	var page struct {
		Items []Event `json:"items"`
	}
	if err := c.send(ctx, http.MethodGet, c.eventURL(calendarID, eventID)+"/instances?"+query.Encode(), accessToken, "", nil, &page); err != nil {
		return Event{}, err
	}
	for _, item := range page.Items {
		if sameOriginalStart(item.OriginalStartTime, originalStart) {
			return item, nil
		}
	}
	return Event{}, &APIError{Status: http.StatusNotFound, Message: "the recurring event has no occurrence at " + originalStart}
}

// sameOriginalStart reports whether an instance's original start is the
// one asked for: the same date, or the same instant.
func sameOriginalStart(original *EventTime, want string) bool {
	if original == nil {
		return false
	}
	if original.Date != "" {
		return original.Date == want
	}
	got, err := time.Parse(time.RFC3339, original.DateTime)
	wanted, wantErr := time.Parse(time.RFC3339, want)
	return err == nil && wantErr == nil && got.Equal(wanted)
}

// send makes one Calendar API request with a JSON body (when body is not
// nil) and decodes a JSON answer into out (when out is not nil). etag, when
// set, goes as If-Match. A 5xx or 429 is UnavailableError, another refusal
// APIError.
func (c *Client) send(ctx context.Context, method, target, accessToken, etag string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode Google request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if etag != "" {
		request.Header.Set("If-Match", etag)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return &UnavailableError{Err: transportError(err)}
	}
	defer response.Body.Close()
	limited := io.LimitReader(response.Body, maxWriteBody)
	switch {
	case response.StatusCode >= 500:
		return &UnavailableError{Err: fmt.Errorf("Calendar API returned HTTP %d", response.StatusCode)}
	case response.StatusCode == http.StatusTooManyRequests:
		return &UnavailableError{Err: errors.New("Google asked Life OS to slow down (HTTP 429)")}
	case response.StatusCode < 200 || response.StatusCode > 299:
		var refusal apiErrorResponse
		_ = json.NewDecoder(limited).Decode(&refusal)
		return &APIError{Status: response.StatusCode, Message: clip(refusal.Error.Message, 200)}
	}
	if out == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(limited).Decode(out); err != nil {
		return &UnavailableError{Err: errors.New("Calendar API returned an unreadable response")}
	}
	return nil
}
