package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/mcp/internal/coreclient"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type taskAction struct {
	IdempotencyKey string  `json:"idempotencyKey,omitempty"`
	Title          string  `json:"title" jsonschema:"Task title"`
	Notes          *string `json:"notes,omitempty"`
	DueAt          *string `json:"dueAt,omitempty" jsonschema:"RFC3339 due time; the owner-local midnight for a date without a time"`
}

type taskMutation struct {
	TaskID     string  `json:"taskId" jsonschema:"Existing Task UUID selected after task_search"`
	Title      *string `json:"title,omitempty"`
	Notes      *string `json:"notes,omitempty"`
	DueAt      *string `json:"dueAt,omitempty" jsonschema:"RFC3339 new due time (the owner-local midnight for a date without a time)"`
	ClearDueAt *bool   `json:"clearDueAt,omitempty" jsonschema:"true makes the Task unscheduled"`
	Move       *move   `json:"move,omitempty" jsonschema:"Move the due date and time relative to the current one, worked out on the owner's wall clock; instead of dueAt or clearDueAt"`
}

type taskRef struct {
	TaskID   string `json:"taskId" jsonschema:"Task UUID, from task_search or task_list"`
	TimeZone string `json:"timeZone,omitempty" jsonschema:"IANA timezone to show the times in; default the owner's (LIFE_OS_TIMEZONE)"`
}

const taskFields = "Each Task comes as Tasks stores it (dueAt and the other instants in UTC) plus timeZone (the owner's), " +
	"due (null when unscheduled; else date, weekday, hasTime, and for a Task with a time also time, local, and utc; overdue for an open Task; text), " +
	"createdAtLocal, updatedAtLocal, completedAtLocal, recurring, and recurrenceText"

// addTaskTools registers the Task tools. Each one is a Tasks API operation,
// the same one the Dashboard uses; reads and writes answer the Task with its
// due date and time on the owner's clock (taskDetails).
func addTaskTools(s *mcp.Server, hc *http.Client, baseURL string, emitter *activity.Emitter, o owner) {
	client := func() coreclient.Client { return coreclient.Client{BaseURL: baseURL, HTTP: hc} }
	details := func(zone *time.Location) func(map[string]any) map[string]any {
		now := o.clock()
		return func(task map[string]any) map[string]any { return taskDetails(task, zone, now) }
	}
	list := func(name, description string) {
		mcp.AddTool(s, &mcp.Tool{Name: name, Description: description}, func(ctx context.Context, req *mcp.CallToolRequest, in query) (*mcp.CallToolResult, Output, error) {
			zone, _ := o.location("")
			data, err := getJSON(ctx, client(), traceOf(req), "/v1/tasks?"+in.Query)
			if err != nil {
				return nil, Output{}, err
			}
			return nil, Output{Data: withItems(data, zone, details(zone))}, nil
		})
	}
	list("task_list", "List Tasks (URL query: completed, dueBefore, includeUndated, undated, query, limit, cursor): open ones first, soonest due first. "+taskFields)
	list("task_search", "Search Tasks by words in their title or notes (URL query: query=<words>, completed=false for open ones, limit) before reading or changing one. "+taskFields)
	mcp.AddTool(s, &mcp.Tool{Name: "task_get", Description: "Get one Task by id with every detail: title, notes, whether and when it is due (date, and the time only when it has one: due.hasTime), in the owner's timezone and UTC, whether it is overdue, completed and when, its recurrence, and when it was created and changed. " + taskFields}, func(ctx context.Context, req *mcp.CallToolRequest, in taskRef) (*mcp.CallToolResult, Output, error) {
		path, err := taskPath(in.TaskID)
		if err != nil {
			return nil, Output{}, err
		}
		zone, err := o.location(in.TimeZone)
		if err != nil {
			return nil, Output{}, err
		}
		task, err := getJSON(ctx, client(), traceOf(req), path)
		if err != nil {
			return nil, Output{}, err
		}
		return nil, Output{Data: details(zone)(task)}, nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "task_create", Description: "Create a Life OS task: title, notes, and dueAt (RFC3339; the owner-local midnight for a date without a time). Answers the Task with its details"}, func(ctx context.Context, req *mcp.CallToolRequest, in taskAction) (*mcp.CallToolResult, Output, error) {
		return audited(ctx, emitter, req, "task_create", in, func(trace activity.Trace) (*mcp.CallToolResult, Output, error) {
			result, out, err := post(ctx, hc, trace, baseURL, "/v1/tasks", in, in.IdempotencyKey)
			return result, withTaskDetails(out, err, o), err
		})
	})
	mcp.AddTool(s, &mcp.Tool{Name: "task_update", Description: "Change one existing Task (taskId from task_search): only the given fields change. " +
		"title renames it, notes replaces its notes, dueAt (RFC3339) sets a new due date and time, clearDueAt true makes it unscheduled. " +
		"To reschedule relative to when it is due now, give move instead of dueAt, and MCP works the new due date and time out on the owner's wall clock (daylight saving included): " +
		"{\"toDate\":\"2026-10-23\"} moves it to that date at the same time ('to tomorrow, same time', 'to Friday'), {\"days\":2} two days later at the same time, " +
		"{\"hours\":1} an hour later (also past midnight), {\"toDate\":\"2026-10-23\",\"toTime\":\"15:00\"} to that date and time, {\"toTime\":\"18:00\"} to that time on the same date, " +
		"and {\"dateOnly\":true} keeps the date without a time. A Task without a time keeps having none unless toTime gives one, so hours and minutes need a Task with a time, and an unscheduled Task needs toDate. " +
		"Answers the Task with its details and moved (from, to)"}, func(ctx context.Context, req *mcp.CallToolRequest, in taskMutation) (*mcp.CallToolResult, Output, error) {
		return audited(ctx, emitter, req, "task_update", in, func(trace activity.Trace) (*mcp.CallToolResult, Output, error) {
			path, err := taskPath(in.TaskID)
			if err != nil {
				return nil, Output{}, err
			}
			body := taskUpdateBody(in)
			if in.Move == nil {
				if len(body) == 0 {
					return nil, Output{}, errors.New("nothing to change: give title, notes, dueAt, clearDueAt, or move")
				}
				result, out, err := patch(ctx, hc, trace, baseURL, path, body)
				return result, withTaskDetails(out, err, o), err
			}
			if in.DueAt != nil || in.ClearDueAt != nil {
				return nil, Output{}, errors.New("move replaces dueAt and clearDueAt: give either move or one of them")
			}
			plan, err := in.Move.plan(o)
			if err != nil {
				return nil, Output{}, err
			}
			task, err := getJSON(ctx, client(), trace, path)
			if err != nil {
				return nil, Output{}, err
			}
			title, _ := task["title"].(string)
			var current *time.Time
			if due, ok := instant(task["dueAt"]); ok {
				current = &due
			}
			due, err := plan.task(title, current)
			if err != nil {
				return nil, Output{}, err
			}
			moved := map[string]any{"from": dueText(task, plan.zone)}
			if current == nil || !due.Equal(*current) {
				body["dueAt"] = due.Format(time.RFC3339)
			}
			if len(body) == 0 {
				// Already due then: nothing to write.
				moved["unchanged"] = true
			} else {
				_, out, err := patch(ctx, hc, trace, baseURL, path, body)
				if err != nil {
					return nil, Output{}, err
				}
				task = out.Data
			}
			moved["to"] = dueText(task, plan.zone)
			data := taskDetails(task, plan.zone, o.clock())
			data["moved"] = moved
			return nil, Output{Data: data}, nil
		})
	})
	mcp.AddTool(s, &mcp.Tool{Name: "task_complete", Description: "Mark one existing Task completed (taskId from task_search)"}, func(ctx context.Context, req *mcp.CallToolRequest, in taskMutation) (*mcp.CallToolResult, Output, error) {
		return audited(ctx, emitter, req, "task_complete", in, func(trace activity.Trace) (*mcp.CallToolResult, Output, error) {
			path, err := taskPath(in.TaskID)
			if err != nil {
				return nil, Output{}, err
			}
			result, out, err := post(ctx, hc, trace, baseURL, path+"/complete", nil)
			return result, withTaskDetails(out, err, o), err
		})
	})
	mcp.AddTool(s, &mcp.Tool{Name: "task_reopen", Description: "Reopen one completed Task (taskId from task_search with completed=true)"}, func(ctx context.Context, req *mcp.CallToolRequest, in taskMutation) (*mcp.CallToolResult, Output, error) {
		return audited(ctx, emitter, req, "task_reopen", in, func(trace activity.Trace) (*mcp.CallToolResult, Output, error) {
			path, err := taskPath(in.TaskID)
			if err != nil {
				return nil, Output{}, err
			}
			result, out, err := post(ctx, hc, trace, baseURL, path+"/reopen", nil)
			return result, withTaskDetails(out, err, o), err
		})
	})
}

// taskPath is the Tasks path of one Task. The id must be a UUID, so a caller
// cannot reach another path.
func taskPath(id string) (string, error) {
	parsed, err := uuid.Parse(strings.TrimSpace(id))
	if err != nil {
		return "", fmt.Errorf("taskId must be a Task UUID")
	}
	return "/v1/tasks/" + parsed.String(), nil
}

// taskUpdateBody is the Tasks PATCH body: the given fields only.
func taskUpdateBody(in taskMutation) map[string]any {
	body := map[string]any{}
	if in.Title != nil {
		body["title"] = *in.Title
	}
	if in.Notes != nil {
		body["notes"] = *in.Notes
	}
	if in.DueAt != nil {
		body["dueAt"] = *in.DueAt
	}
	if in.ClearDueAt != nil {
		body["clearDueAt"] = *in.ClearDueAt
	}
	return body
}

// withTaskDetails adds the details to a Task a write answered.
func withTaskDetails(out Output, err error, o owner) Output {
	if err != nil || out.Data == nil {
		return out
	}
	zone, _ := o.location("")
	return Output{Data: taskDetails(out.Data, zone, o.clock())}
}

// getJSON reads one JSON object from an owning service.
func getJSON(ctx context.Context, client coreclient.Client, trace activity.Trace, path string) (map[string]any, error) {
	headers := http.Header{}
	trace.Apply(headers)
	body, err := client.DoWithHeaders(ctx, http.MethodGet, path, nil, headers)
	if err != nil {
		return nil, err
	}
	out, err := output(body)
	return out.Data, err
}
