package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/mcp/internal/coreclient"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// reminder is Calendar reminder intent: relative (minutesBefore the start) or
// localTime (HH:MM on the owner's wall clock, daysBefore the start date).
type reminder struct {
	Kind          string  `json:"kind" jsonschema:"relative or localTime"`
	MinutesBefore *int    `json:"minutesBefore,omitempty" jsonschema:"relative only: minutes before the start"`
	DaysBefore    *int    `json:"daysBefore,omitempty" jsonschema:"localTime only: calendar days before the start date (0 is the same day)"`
	LocalTime     *string `json:"localTime,omitempty" jsonschema:"localTime only: HH:MM on the owner's wall clock"`
}

type eventAction struct {
	IdempotencyKey string      `json:"idempotencyKey,omitempty"`
	Title          string      `json:"title" jsonschema:"Event title"`
	Description    *string     `json:"description,omitempty" jsonschema:"Notes or details"`
	Location       *string     `json:"location,omitempty" jsonschema:"Where the event takes place"`
	AllDay         bool        `json:"allDay"`
	StartAt        *string     `json:"startAt,omitempty" jsonschema:"RFC3339 start time"`
	EndAt          *string     `json:"endAt,omitempty" jsonschema:"RFC3339 end time"`
	StartDate      *string     `json:"startDate,omitempty" jsonschema:"ISO date for all-day event"`
	EndDate        *string     `json:"endDate,omitempty" jsonschema:"ISO date for all-day event (inclusive)"`
	TimeZone       *string     `json:"timeZone,omitempty" jsonschema:"IANA timezone a timed event is scheduled in"`
	Reminders      *[]reminder `json:"reminders,omitempty" jsonschema:"The event's own reminders, added to the owner's default reminders; leave out for none of its own"`
	UseDefault     *bool       `json:"useDefaultReminders,omitempty" jsonschema:"Whether the owner's default reminders apply as well (default true); false without reminders means no reminders at all"`
	Priority       *string     `json:"priority,omitempty" jsonschema:"low, medium, or high: how important the event is, the importance of its reminder notifications; leave out for medium"`
}

// eventMutation changes one existing event. Only the fields given change;
// Clear names optional fields to remove (sent to Calendar as null).
type eventMutation struct {
	EventID     string      `json:"eventId" jsonschema:"Existing Calendar event UUID selected after calendar_search"`
	Title       *string     `json:"title,omitempty"`
	Description *string     `json:"description,omitempty"`
	Location    *string     `json:"location,omitempty"`
	AllDay      *bool       `json:"allDay,omitempty" jsonschema:"Switches between timed and all-day; then give the new kind's start and end"`
	StartAt     *string     `json:"startAt,omitempty" jsonschema:"RFC3339 start time"`
	EndAt       *string     `json:"endAt,omitempty" jsonschema:"RFC3339 end time"`
	StartDate   *string     `json:"startDate,omitempty" jsonschema:"ISO date for all-day event"`
	EndDate     *string     `json:"endDate,omitempty" jsonschema:"ISO date for all-day event (inclusive)"`
	TimeZone    *string     `json:"timeZone,omitempty" jsonschema:"IANA timezone of a timed event"`
	Reminders   *[]reminder `json:"reminders,omitempty" jsonschema:"Replaces the event's own reminders, which are added to its defaults"`
	UseDefault  *bool       `json:"useDefaultReminders,omitempty" jsonschema:"Turns the event's default reminders on or off; its own reminders stay"`
	Priority    *string     `json:"priority,omitempty" jsonschema:"low, medium, or high: how important the event is; medium is the default"`
	Clear       []string    `json:"clear,omitempty" jsonschema:"Fields to remove: description, location, timeZone, or reminders (back to the defaults alone)"`
	Move        *move       `json:"move,omitempty" jsonschema:"Move the event relative to its current start, keeping its length, worked out on the owner's wall clock; instead of allDay, startAt, endAt, startDate, and endDate"`
	// RecurrenceID names the one occurrence of a recurring event a move
	// moves (This event); the series is changed only in the Calendar.
	RecurrenceID string `json:"recurrenceId,omitempty" jsonschema:"With move, for a recurring event: the occurrence.recurrenceId (from calendar_search or calendar_list) of the one occurrence to move; the series and its other occurrences stay"`
}

type eventRef struct {
	EventID string `json:"eventId" jsonschema:"Calendar event UUID"`
}

// eventRead reads one event, or one occurrence of a recurring one.
type eventRead struct {
	EventID      string `json:"eventId" jsonschema:"Calendar event UUID (for a recurring event, its series id)"`
	RecurrenceID string `json:"recurrenceId,omitempty" jsonschema:"For a recurring event: the occurrence.recurrenceId of one occurrence, to read that occurrence with its own changes"`
	TimeZone     string `json:"timeZone,omitempty" jsonschema:"IANA timezone to show the times in; default the owner's (LIFE_OS_TIMEZONE)"`
}

// eventCancellation cancels one event, or one occurrence of a recurring
// event. Recurring events are otherwise changed only in the Calendar.
type eventCancellation struct {
	EventID      string `json:"eventId" jsonschema:"Calendar event UUID (for a recurring event, its series id)"`
	RecurrenceID string `json:"recurrenceId,omitempty" jsonschema:"For a recurring event: the occurrence.recurrenceId of the one occurrence to cancel (skip); the series and its other occurrences stay"`
}

// clearable are the optional event fields calendar_update may remove.
var clearable = map[string]bool{"description": true, "location": true, "timeZone": true, "reminders": true}

// addCalendarTools registers the Calendar tools. Each one is a Calendar API
// operation, the same one the Dashboard uses; search runs before any change
// so callers act on an event id, never on a guess.
func addCalendarTools(s *mcp.Server, hc *http.Client, baseURL string, emitter *activity.Emitter, o owner) {
	client := func() coreclient.Client { return coreclient.Client{BaseURL: baseURL, HTTP: hc} }
	list := func(name, description, path string) {
		mcp.AddTool(s, &mcp.Tool{Name: name, Description: description}, func(ctx context.Context, req *mcp.CallToolRequest, in query) (*mcp.CallToolResult, Output, error) {
			// The times are shown in the zone the query names, as Calendar
			// reads its dates in it, else in the owner's.
			values, _ := url.ParseQuery(in.Query)
			zone, err := o.location(values.Get("timeZone"))
			if err != nil {
				zone, _ = o.location("")
			}
			data, err := getJSON(ctx, client(), traceOf(req), path+in.Query)
			if err != nil {
				return nil, Output{}, err
			}
			return nil, Output{Data: withItems(data, zone, func(event map[string]any) map[string]any { return eventDetails(event, zone) })}, nil
		})
	}
	list("calendar_list", "List Calendar events in a time range (from, to, optional timeZone, includeCancelled, limit, cursor); a recurring event is listed as its occurrences. "+eventFields, "/v1/events?")
	list("calendar_search", "Search Calendar events by words in their title, location, or notes (q, optional from/to, timeZone, includeCancelled, editable=true for only the events that can be changed, limit, cursor) before reading or changing one; a recurring event comes as an occurrence. "+eventFields, "/v1/events/search?")
	mcp.AddTool(s, &mcp.Tool{Name: "calendar_get", Description: "Get one Calendar event by id, whatever its status, with every detail: when it starts and ends (in the owner's timezone and UTC, or its dates when allDay), how long it lasts, where, its notes, whether and how it repeats, its reminders and priority, and its source. With recurrenceId, one occurrence of a recurring event. " + eventFields}, func(ctx context.Context, req *mcp.CallToolRequest, in eventRead) (*mcp.CallToolResult, Output, error) {
		path, err := eventPath(in.EventID)
		if err != nil {
			return nil, Output{}, err
		}
		if in.RecurrenceID != "" {
			if path, err = occurrencePath(path, in.RecurrenceID); err != nil {
				return nil, Output{}, err
			}
		}
		zone, err := o.location(in.TimeZone)
		if err != nil {
			return nil, Output{}, err
		}
		event, err := getJSON(ctx, client(), traceOf(req), path)
		if err != nil {
			return nil, Output{}, err
		}
		return nil, Output{Data: eventDetails(event, zone)}, nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "calendar_create", Description: "Create a Life OS calendar event. Answers the event with its details"}, func(ctx context.Context, req *mcp.CallToolRequest, in eventAction) (*mcp.CallToolResult, Output, error) {
		return audited(ctx, emitter, req, "calendar_create", in, func(trace activity.Trace) (*mcp.CallToolResult, Output, error) {
			result, out, err := post(ctx, hc, trace, baseURL, "/v1/events", in, in.IdempotencyKey)
			return result, withEventDetails(out, err, o), err
		})
	})
	mcp.AddTool(s, &mcp.Tool{Name: "calendar_update", Description: "Reschedule or edit one existing Calendar event: only the given fields change, and clear removes optional fields. " +
		"To reschedule relative to when it is now, give move instead of new times, and MCP works the new start out on the owner's wall clock (daylight saving included) and keeps the event's length: " +
		"{\"hours\":1} an hour later, {\"days\":1} a day later at the same time, {\"toDate\":\"2026-10-23\"} to that date at the same time ('to tomorrow, same time', 'to Friday'), " +
		"{\"toDate\":\"2026-10-23\",\"toTime\":\"15:00\"} to that date and time, {\"toTime\":\"18:00\"} to that time on the same date; an all-day event moves by days or to a date. " +
		"A recurring event moves only one occurrence at a time: give move with its recurrenceId (This event); the series is changed only in the Calendar. " +
		"The change is stored in Calendar; the integrations follow it afterwards (a Life OS event is exported to Google, and an event of a calendar an integration syncs that accepts changes, such as a Google calendar the owner may edit, is written to its provider). Events of read-only sources, such as feeds, allow only their reminders and priority. " +
		"Answers the event with its details, and for a move what moved (from, to)"}, func(ctx context.Context, req *mcp.CallToolRequest, in eventMutation) (*mcp.CallToolResult, Output, error) {
		return audited(ctx, emitter, req, "calendar_update", in, func(trace activity.Trace) (*mcp.CallToolResult, Output, error) {
			path, err := eventPath(in.EventID)
			if err != nil {
				return nil, Output{}, err
			}
			if in.Move != nil {
				return moveEvent(ctx, client(), trace, path, in, o)
			}
			if in.RecurrenceID != "" {
				return nil, Output{}, errors.New("recurrenceId goes with move: other changes to one occurrence of a recurring event are made in the Calendar")
			}
			body, err := eventUpdateBody(in)
			if err != nil {
				return nil, Output{}, err
			}
			result, out, err := patch(ctx, hc, trace, baseURL, path, body)
			return result, withEventDetails(out, err, o), err
		})
	})
	mcp.AddTool(s, &mcp.Tool{Name: "calendar_cancel", Description: "Cancel one existing Calendar event, or with recurrenceId only that occurrence of a recurring event; it stays readable as history and can be restored. The integrations follow the cancellation afterwards (such as a Google event being cancelled in Google, an occurrence skipped there)"}, func(ctx context.Context, req *mcp.CallToolRequest, in eventCancellation) (*mcp.CallToolResult, Output, error) {
		return audited(ctx, emitter, req, "calendar_cancel", in, func(trace activity.Trace) (*mcp.CallToolResult, Output, error) {
			path, err := eventPath(in.EventID)
			if err != nil {
				return nil, Output{}, err
			}
			if in.RecurrenceID != "" {
				if path, err = occurrencePath(path, in.RecurrenceID); err != nil {
					return nil, Output{}, err
				}
			}
			result, out, err := patch(ctx, hc, trace, baseURL, path, map[string]any{"status": "cancelled"})
			return result, withEventDetails(out, err, o), err
		})
	})
	mcp.AddTool(s, &mcp.Tool{Name: "calendar_delete", Description: "Permanently delete one Calendar event, only on the owner's explicit request; prefer calendar_cancel, which keeps history"}, func(ctx context.Context, req *mcp.CallToolRequest, in eventRef) (*mcp.CallToolResult, Output, error) {
		return audited(ctx, emitter, req, "calendar_delete", in, func(trace activity.Trace) (*mcp.CallToolResult, Output, error) {
			path, err := eventPath(in.EventID)
			if err != nil {
				return nil, Output{}, err
			}
			headers := http.Header{}
			trace.Apply(headers)
			// Calendar answers 204 with no body.
			if _, err := (coreclient.Client{BaseURL: baseURL, HTTP: hc}).DoWithHeaders(ctx, http.MethodDelete, path, nil, headers); err != nil {
				return nil, Output{}, err
			}
			return nil, Output{Data: map[string]any{"id": strings.TrimPrefix(path, "/v1/events/"), "deleted": true}}, nil
		})
	})
}

// eventPath is the Calendar path of one event. The id must be a UUID, so a
// caller cannot reach another path.
func eventPath(id string) (string, error) {
	parsed, err := uuid.Parse(strings.TrimSpace(id))
	if err != nil {
		return "", fmt.Errorf("eventId must be a Calendar event UUID")
	}
	return "/v1/events/" + parsed.String(), nil
}

// occurrencePath is the Calendar path of one occurrence of the event at
// eventPath. The recurrence id must be one (an RFC 3339 instant or a date),
// so a caller cannot reach another path.
func occurrencePath(eventPath, recurrenceID string) (string, error) {
	recurrenceID = strings.TrimSpace(recurrenceID)
	_, instantErr := time.Parse(time.RFC3339, recurrenceID)
	_, dateErr := time.Parse(time.DateOnly, recurrenceID)
	if instantErr != nil && dateErr != nil {
		return "", fmt.Errorf("recurrenceId must be an occurrence's recurrenceId: an RFC 3339 instant or a date")
	}
	return eventPath + "/occurrences/" + url.PathEscape(recurrenceID), nil
}

const eventFields = "Each event comes as Calendar stores it (startAt and endAt in UTC; startDate and endDate for allDay; location, description, reminders, priority, status, occurrence, recurrence) plus timeZone (the owner's), " +
	"start and end (date, weekday, text, and for a timed event time, local, and utc; a timed end is exclusive, an all-day end is its last day), durationMinutes or days, when, recurring, and recurrenceText"

// withEventDetails adds the details to an event a write answered.
func withEventDetails(out Output, err error, o owner) Output {
	if err != nil || out.Data == nil {
		return out
	}
	zone, _ := o.location("")
	return Output{Data: eventDetails(out.Data, zone)}
}

// eventTimeFields are what a move replaces.
var eventTimeFields = []string{"allDay", "startAt", "endAt", "startDate", "endDate"}

// moveEvent moves the event at path, or with a recurrence id one occurrence
// of a recurring event, relative to its current start: it reads the event,
// works out the new start and end on the owner's wall clock (keeping the
// length), and patches them with the update's other fields. A recurring
// event moves only one occurrence at a time, and nothing else of it changes
// here.
func moveEvent(ctx context.Context, client coreclient.Client, trace activity.Trace, path string, in eventMutation, o owner) (*mcp.CallToolResult, Output, error) {
	others, err := eventUpdateBody(in)
	if err != nil && !errors.Is(err, errNothingToChange) {
		return nil, Output{}, err
	}
	for _, field := range eventTimeFields {
		if _, set := others[field]; set {
			return nil, Output{}, errors.New("move replaces allDay, startAt, endAt, startDate, and endDate: give either move or new times")
		}
	}
	plan, err := in.Move.plan(o)
	if err != nil {
		return nil, Output{}, err
	}
	event, err := getJSON(ctx, client, trace, path)
	if err != nil {
		return nil, Output{}, err
	}
	title, _ := event["title"].(string)
	target := path
	if event["recurrence"] != nil {
		if in.RecurrenceID == "" {
			return nil, Output{}, refuse("“%s” is a recurring event: give recurrenceId (its occurrence.recurrenceId from calendar_search or calendar_list) to move that one occurrence; the series is changed only in the Calendar", title)
		}
		if len(others) > 0 {
			return nil, Output{}, refuse("“%s” is a recurring event: a move of one occurrence changes only its time; other changes are made in the Calendar", title)
		}
		if target, err = occurrencePath(path, in.RecurrenceID); err != nil {
			return nil, Output{}, err
		}
		if event, err = getJSON(ctx, client, trace, target); err != nil {
			return nil, Output{}, err
		}
	} else if in.RecurrenceID != "" {
		return nil, Output{}, refuse("“%s” does not repeat: leave out recurrenceId", title)
	}
	times, err := plan.event(event)
	if err != nil {
		return nil, Output{}, err
	}
	for field, value := range times {
		others[field] = value
	}
	raw, err := json.Marshal(others)
	if err != nil {
		return nil, Output{}, err
	}
	headers := http.Header{}
	trace.Apply(headers)
	body, err := client.DoWithHeaders(ctx, http.MethodPatch, target, raw, headers)
	if err != nil {
		return nil, Output{}, err
	}
	out, err := output(body)
	if err != nil {
		return nil, Output{}, err
	}
	data := eventDetails(out.Data, plan.zone)
	data["moved"] = map[string]any{"from": eventWhen(event, plan.zone), "to": eventWhen(out.Data, plan.zone)}
	return nil, Output{Data: data}, nil
}

// errNothingToChange is an update without a field to set or clear, which a
// move does not need.
var errNothingToChange = errors.New("nothing to change: give at least one field to set or clear")

// eventUpdateBody is the Calendar PATCH body: the given fields, plus null
// for each cleared one.
func eventUpdateBody(in eventMutation) (map[string]any, error) {
	body := map[string]any{}
	for field, value := range map[string]*string{"title": in.Title, "description": in.Description, "location": in.Location, "startAt": in.StartAt, "endAt": in.EndAt, "startDate": in.StartDate, "endDate": in.EndDate, "timeZone": in.TimeZone, "priority": in.Priority} {
		if value != nil {
			body[field] = *value
		}
	}
	if in.AllDay != nil {
		body["allDay"] = *in.AllDay
	}
	if in.Reminders != nil {
		body["reminders"] = *in.Reminders
	}
	if in.UseDefault != nil {
		body["useDefaultReminders"] = *in.UseDefault
	}
	for _, field := range in.Clear {
		if !clearable[field] {
			return nil, fmt.Errorf("cannot clear %q: only description, location, timeZone, and reminders can be cleared", field)
		}
		if _, set := body[field]; set {
			return nil, fmt.Errorf("%s is both set and cleared", field)
		}
		body[field] = nil
	}
	if len(body) == 0 {
		return body, errNothingToChange
	}
	return body, nil
}
