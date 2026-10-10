package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Event is one Calendar event or occurrence on a day, as the summary names
// it.
type Event struct {
	Title  string
	AllDay bool
	// Start and End bound a timed event (End exclusive, may be zero).
	Start, End time.Time
}

// label is the event as the summary on day names it: "09:00 Dentist";
// "Birthday (all day)" for an all-day event, or a timed one lasting the
// whole day; "Party (until 01:00)" for one that started the day before.
func (e Event) label(day time.Time, owner *time.Location) string {
	title := itemTitle(e.Title)
	if e.AllDay {
		return title + " (all day)"
	}
	start := e.Start.In(owner)
	if dateOf(e.Start, owner).Equal(day) {
		return start.Format("15:04") + " " + title
	}
	if !e.End.IsZero() && e.End.Before(startOfDay(day.AddDate(0, 0, 1), owner)) {
		return title + " (until " + e.End.In(owner).Format("15:04") + ")"
	}
	return title + " (all day)"
}

// Events reads the owner's events for a day.
type Events interface {
	Enabled() bool
	EventsOn(ctx context.Context, day time.Time) ([]Event, error)
}

// Calendar reads events from the Calendar service's listing API
// (GET /v1/events): recurring events already expanded into occurrences,
// with their own changes, and cancelled ones left out.
type Calendar struct {
	URL   string
	Owner *time.Location
	HTTP  *http.Client
	// Timeout bounds one day's listing (default 10 s).
	Timeout time.Duration
}

// Calendar listing bounds: pages of the largest size, and at most this many.
const (
	calendarPageSize = 100
	calendarMaxPages = 5
)

// NewCalendar returns a Calendar client for baseURL; an empty URL leaves it
// off.
func NewCalendar(baseURL string, owner *time.Location) *Calendar {
	if owner == nil {
		owner = time.UTC
	}
	return &Calendar{URL: strings.TrimRight(strings.TrimSpace(baseURL), "/"), Owner: owner, HTTP: &http.Client{}}
}

// Enabled reports whether a Calendar is configured.
func (c *Calendar) Enabled() bool { return c != nil && c.URL != "" }

// EventsOn lists the events that overlap the owner-local date day, soonest
// start first (all-day events first), as Calendar orders them.
func (c *Calendar) EventsOn(ctx context.Context, day time.Time) ([]Event, error) {
	if !c.Enabled() {
		return nil, nil
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	query := url.Values{
		"from":     {startOfDay(day, c.Owner).UTC().Format(time.RFC3339)},
		"to":       {startOfDay(day.AddDate(0, 0, 1), c.Owner).UTC().Format(time.RFC3339)},
		"timeZone": {c.Owner.String()},
		"limit":    {fmt.Sprint(calendarPageSize)},
	}
	var events []Event
	for page := 0; page < calendarMaxPages; page++ {
		list, err := c.page(ctx, query)
		if err != nil {
			return nil, err
		}
		for _, item := range list.Items {
			if event, ok := item.event(c.Owner); ok {
				events = append(events, event)
			}
		}
		if list.NextCursor == nil || *list.NextCursor == "" {
			break
		}
		query.Set("cursor", *list.NextCursor)
	}
	return events, nil
}

type calendarList struct {
	Items      []calendarEvent `json:"items"`
	NextCursor *string         `json:"nextCursor"`
}

type calendarEvent struct {
	Title     string     `json:"title"`
	AllDay    bool       `json:"allDay"`
	StartAt   *time.Time `json:"startAt"`
	EndAt     *time.Time `json:"endAt"`
	StartDate *string    `json:"startDate"`
	Status    string     `json:"status"`
}

func (e calendarEvent) event(owner *time.Location) (Event, bool) {
	if e.Status == "cancelled" {
		return Event{}, false
	}
	if e.AllDay {
		return Event{Title: e.Title, AllDay: true}, e.StartDate != nil
	}
	if e.StartAt == nil {
		return Event{}, false
	}
	event := Event{Title: e.Title, Start: *e.StartAt}
	if e.EndAt != nil {
		event.End = *e.EndAt
	}
	return event, true
}

// page reads one listing page, trying once more after a network error or
// a 5xx.
func (c *Calendar) page(ctx context.Context, query url.Values) (calendarList, error) {
	target := c.URL + "/v1/events?" + query.Encode()
	var err error
	for attempt, delay := range []time.Duration{0, 300 * time.Millisecond} {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return calendarList{}, errors.Join(err, ctx.Err())
			case <-time.After(delay):
			}
		}
		var list calendarList
		var retry bool
		list, retry, err = c.get(ctx, target)
		if err == nil {
			return list, nil
		}
		if !retry {
			break
		}
	}
	return calendarList{}, err
}

func (c *Calendar) get(ctx context.Context, target string) (calendarList, bool, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return calendarList{}, false, err
	}
	request.Header.Set("accept", "application/json")
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return calendarList{}, true, fmt.Errorf("list calendar events: %w", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return calendarList{}, true, fmt.Errorf("read calendar events: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return calendarList{}, response.StatusCode >= 500, fmt.Errorf("calendar returned %d listing events", response.StatusCode)
	}
	var list calendarList
	if err := json.Unmarshal(raw, &list); err != nil {
		return calendarList{}, false, fmt.Errorf("decode calendar events: %w", err)
	}
	return list, false, nil
}
