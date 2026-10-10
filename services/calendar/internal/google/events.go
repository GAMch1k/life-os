package google

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Event is a Google Calendar event as Calendar reads it from events.list:
// only the fields it maps (and stores in google_events, see sync.go).
// Attendees, conference data, colors, transparency, and visibility are not
// read.
type Event struct {
	ID     string `json:"id"`
	Status string `json:"status,omitempty"`
	// EventType is default, birthday, focusTime, outOfOffice, fromGmail,
	// or workingLocation (not imported, see mapping.go).
	EventType   string `json:"eventType,omitempty"`
	Summary     string `json:"summary,omitempty"`
	Description string `json:"description,omitempty"`
	Location    string `json:"location,omitempty"`
	// ICalUID is the event's iCalendar UID (shared by a series' instances
	// and by copies of an invitation in other calendars); Calendar keys
	// events by ID.
	ICalUID            string     `json:"iCalUID,omitempty"`
	Start              *EventTime `json:"start,omitempty"`
	End                *EventTime `json:"end,omitempty"`
	EndTimeUnspecified bool       `json:"endTimeUnspecified,omitempty"`
	// Recurrence holds a recurring event's RRULE, EXRULE, RDATE, and EXDATE
	// lines (RFC 5545).
	Recurrence []string `json:"recurrence,omitempty"`
	// RecurringEventID and OriginalStartTime are set on an instance of a
	// recurring event that was changed or cancelled on its own.
	RecurringEventID  string     `json:"recurringEventId,omitempty"`
	OriginalStartTime *EventTime `json:"originalStartTime,omitempty"`
	Reminders         *Reminders `json:"reminders,omitempty"`
	Updated           string     `json:"updated,omitempty"`
}

// Cancelled reports whether the event was deleted (or, for an instance,
// skipped).
func (e Event) Cancelled() bool { return e.Status == "cancelled" }

// EventTime is a start or end: Date (YYYY-MM-DD) for an all-day event,
// else DateTime (RFC 3339) with the IANA TimeZone it was scheduled in.
type EventTime struct {
	Date     string `json:"date,omitempty"`
	DateTime string `json:"dateTime,omitempty"`
	TimeZone string `json:"timeZone,omitempty"`
}

// Reminders are an event's notifications: the calendar's defaults
// (UseDefault) or its own Overrides.
type Reminders struct {
	UseDefault bool       `json:"useDefault"`
	Overrides  []Reminder `json:"overrides,omitempty"`
}

// Reminder is one notification, Minutes before the start, by Method
// (popup or email).
type Reminder struct {
	Method  string `json:"method"`
	Minutes int    `json:"minutes"`
}

// EventsQuery is one events.list request. A full listing gives TimeMin; an
// incremental one SyncToken (Google refuses both together). PageToken asks
// for the next page of either.
type EventsQuery struct {
	SyncToken string
	TimeMin   time.Time
	PageToken string
}

// EventsPage is one page of events.list. The last page of a listing has
// NextSyncToken instead of NextPageToken.
type EventsPage struct {
	Items            []Event    `json:"items"`
	NextPageToken    string     `json:"nextPageToken"`
	NextSyncToken    string     `json:"nextSyncToken"`
	Summary          string     `json:"summary"`
	TimeZone         string     `json:"timeZone"`
	DefaultReminders []Reminder `json:"defaultReminders"`
}

// eventsPageSize is how many events Calendar asks for per page.
const eventsPageSize = 100

// Events reads one page of a calendar's events, recurring events as their
// master (singleEvents=false) and deleted ones included (showDeleted=true),
// so incremental listings report deletions.
func (c *Client) Events(ctx context.Context, accessToken, calendarID string, query EventsQuery) (EventsPage, error) {
	values := url.Values{
		"singleEvents": {"false"},
		"showDeleted":  {"true"},
		"maxResults":   {strconv.Itoa(eventsPageSize)},
	}
	if query.SyncToken != "" {
		values.Set("syncToken", query.SyncToken)
	} else if !query.TimeMin.IsZero() {
		values.Set("timeMin", query.TimeMin.UTC().Format(time.RFC3339))
	}
	if query.PageToken != "" {
		values.Set("pageToken", query.PageToken)
	}
	var page EventsPage
	err := c.getJSONLimited(ctx, c.endpoints.CalendarAPI+"/calendars/"+url.PathEscape(calendarID)+"/events?"+values.Encode(), accessToken, &page, maxEventsResponseBytes)
	return page, err
}

// IsSyncTokenGone reports whether Google no longer accepts a sync token
// (410 Gone): the calendar has to be listed in full again.
func IsSyncTokenGone(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusGone
}

// isUnauthorized reports whether Google refused the access token (401).
func isUnauthorized(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusUnauthorized
}
