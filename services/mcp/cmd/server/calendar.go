package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

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
	Clear       []string    `json:"clear,omitempty" jsonschema:"Fields to remove: description, location, timeZone, or reminders (back to the defaults alone)"`
}

type eventRef struct {
	EventID string `json:"eventId" jsonschema:"Calendar event UUID"`
}

// clearable are the optional event fields calendar_update may remove.
var clearable = map[string]bool{"description": true, "location": true, "timeZone": true, "reminders": true}

// addCalendarTools registers the Calendar tools. Each one is a Calendar API
// operation, the same one the Dashboard uses; search runs before any change
// so callers act on an event id, never on a guess.
func addCalendarTools(s *mcp.Server, hc *http.Client, baseURL string, emitter *activity.Emitter) {
	addReadTool(s, hc, "calendar_list", "List Calendar events in a time range (from, to, optional timeZone, includeCancelled, limit, cursor)", baseURL, "/v1/events?")
	addReadTool(s, hc, "calendar_search", "Search Calendar events by words in their title, location, or notes (q, optional from/to, timeZone, includeCancelled, limit, cursor) before changing one", baseURL, "/v1/events/search?")
	mcp.AddTool(s, &mcp.Tool{Name: "calendar_get", Description: "Get one Calendar event by id, whatever its status"}, func(ctx context.Context, req *mcp.CallToolRequest, in eventRef) (*mcp.CallToolResult, Output, error) {
		path, err := eventPath(in.EventID)
		if err != nil {
			return nil, Output{}, err
		}
		headers := http.Header{}
		traceOf(req).Apply(headers)
		body, err := coreclient.Client{BaseURL: baseURL, HTTP: hc}.DoWithHeaders(ctx, http.MethodGet, path, nil, headers)
		if err != nil {
			return nil, Output{}, err
		}
		out, err := output(body)
		return nil, out, err
	})
	mcp.AddTool(s, &mcp.Tool{Name: "calendar_create", Description: "Create a Life OS calendar event"}, func(ctx context.Context, req *mcp.CallToolRequest, in eventAction) (*mcp.CallToolResult, Output, error) {
		return audited(ctx, emitter, req, "calendar_create", in, func(trace activity.Trace) (*mcp.CallToolResult, Output, error) {
			return post(ctx, hc, trace, baseURL, "/v1/events", in, in.IdempotencyKey)
		})
	})
	mcp.AddTool(s, &mcp.Tool{Name: "calendar_update", Description: "Reschedule or edit one existing Calendar event: only the given fields change, and clear removes optional fields"}, func(ctx context.Context, req *mcp.CallToolRequest, in eventMutation) (*mcp.CallToolResult, Output, error) {
		return audited(ctx, emitter, req, "calendar_update", in, func(trace activity.Trace) (*mcp.CallToolResult, Output, error) {
			path, err := eventPath(in.EventID)
			if err != nil {
				return nil, Output{}, err
			}
			body, err := eventUpdateBody(in)
			if err != nil {
				return nil, Output{}, err
			}
			return patch(ctx, hc, trace, baseURL, path, body)
		})
	})
	mcp.AddTool(s, &mcp.Tool{Name: "calendar_cancel", Description: "Cancel one existing Calendar event; it stays readable as history and can be restored"}, func(ctx context.Context, req *mcp.CallToolRequest, in eventRef) (*mcp.CallToolResult, Output, error) {
		return audited(ctx, emitter, req, "calendar_cancel", in, func(trace activity.Trace) (*mcp.CallToolResult, Output, error) {
			path, err := eventPath(in.EventID)
			if err != nil {
				return nil, Output{}, err
			}
			return patch(ctx, hc, trace, baseURL, path, map[string]any{"status": "cancelled"})
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

// eventUpdateBody is the Calendar PATCH body: the given fields, plus null
// for each cleared one.
func eventUpdateBody(in eventMutation) (map[string]any, error) {
	body := map[string]any{}
	for field, value := range map[string]*string{"title": in.Title, "description": in.Description, "location": in.Location, "startAt": in.StartAt, "endAt": in.EndAt, "startDate": in.StartDate, "endDate": in.EndDate, "timeZone": in.TimeZone} {
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
		return nil, errors.New("nothing to change: give at least one field to set or clear")
	}
	return body, nil
}
