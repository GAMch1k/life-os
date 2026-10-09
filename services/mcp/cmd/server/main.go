package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/mcp/internal/config"
	"github.com/GAMch1k/life-os/services/mcp/internal/coreclient"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type query struct {
	Query string `json:"query,omitempty" jsonschema:"URL query without leading question mark"`
}
type inboxAction struct {
	ItemID         string                   `json:"itemId,omitempty" jsonschema:"Inbox item UUID"`
	AttemptID      string                   `json:"attemptId,omitempty" jsonschema:"Processing-attempt UUID"`
	IdempotencyKey string                   `json:"idempotencyKey,omitempty"`
	AgentName      string                   `json:"agentName,omitempty"`
	ModelProfile   string                   `json:"modelProfile,omitempty"`
	Status         string                   `json:"status,omitempty"`
	Actions        []map[string]interface{} `json:"actions,omitempty"`
	ProposedAction map[string]interface{}   `json:"proposedAction,omitempty"`
	Candidates     []map[string]interface{} `json:"candidates,omitempty"`
	ErrorSummary   *string                  `json:"errorSummary,omitempty"`
}
type taskAction struct {
	IdempotencyKey string  `json:"idempotencyKey,omitempty"`
	Title          string  `json:"title" jsonschema:"Task title"`
	Notes          *string `json:"notes,omitempty"`
	DueAt          *string `json:"dueAt,omitempty" jsonschema:"RFC3339 due time"`
}
type taskMutation struct {
	TaskID     string  `json:"taskId" jsonschema:"Existing Task UUID selected after task_search"`
	Title      *string `json:"title,omitempty"`
	Notes      *string `json:"notes,omitempty"`
	DueAt      *string `json:"dueAt,omitempty" jsonschema:"RFC3339 due time"`
	ClearDueAt *bool   `json:"clearDueAt,omitempty"`
}
type eventAction struct {
	IdempotencyKey string  `json:"idempotencyKey,omitempty"`
	Title          string  `json:"title" jsonschema:"Event title"`
	Description    *string `json:"description,omitempty"`
	AllDay         bool    `json:"allDay"`
	StartAt        *string `json:"startAt,omitempty" jsonschema:"RFC3339 start time"`
	EndAt          *string `json:"endAt,omitempty" jsonschema:"RFC3339 end time"`
	StartDate      *string `json:"startDate,omitempty" jsonschema:"ISO date for all-day event"`
	EndDate        *string `json:"endDate,omitempty" jsonschema:"ISO date for all-day event"`
}
type financeAction struct {
	IdempotencyKey string         `json:"idempotencyKey,omitempty"`
	AccountID      string         `json:"accountId" jsonschema:"Explicit Finance account UUID from trusted source metadata"`
	CategoryID     *string        `json:"categoryId,omitempty"`
	Amount         map[string]any `json:"amount" jsonschema:"Money object with signed integer amount and currency"`
	OccurredOn     string         `json:"occurredOn" jsonschema:"ISO transaction date"`
	Note           *string        `json:"note,omitempty"`
}
type Output struct {
	Data map[string]any `json:"data"`
}

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(ctx context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	hc := &http.Client{Timeout: 10 * time.Second}
	emitter := activity.NewEmitter(cfg.ActivityURL, "mcp", nil)
	s := mcp.NewServer(&mcp.Implementation{Name: "life-os", Version: "0.1.0"}, nil)
	add := func(n, d, b, p string) {
		mcp.AddTool(s, &mcp.Tool{Name: n, Description: d}, func(ctx context.Context, req *mcp.CallToolRequest, in query) (*mcp.CallToolResult, Output, error) {
			headers := http.Header{}
			traceOf(req).Apply(headers)
			body, e := coreclient.Client{BaseURL: b, HTTP: hc}.DoWithHeaders(ctx, http.MethodGet, p+in.Query, nil, headers)
			if e != nil {
				return nil, Output{}, e
			}
			out, e := output(body)
			return nil, out, e
		})
	}
	add("finance_month_summary", "Get Finance monthly summary", cfg.FinanceURL, "/v1/summaries/monthly?")
	add("finance_find_transactions", "Find Finance transactions", cfg.FinanceURL, "/v1/transactions?")
	add("task_list", "List Tasks", cfg.TasksURL, "/v1/tasks?")
	add("task_search", "Search Tasks by title or notes before mutating one", cfg.TasksURL, "/v1/tasks?")
	add("inbox_list", "List Inbox items", cfg.InboxURL, "/v1/inbox-items?")
	add("calendar_list", "List Calendar events", cfg.CalendarURL, "/v1/events?")
	// Inbox mutations are exposed as MCP tools so processors never bypass the
	// service API or write the Inbox database directly.
	mcp.AddTool(s, &mcp.Tool{Name: "inbox_start_processing_attempt", Description: "Start an auditable Inbox processing attempt"}, func(ctx context.Context, req *mcp.CallToolRequest, in inboxAction) (*mcp.CallToolResult, Output, error) {
		return post(ctx, hc, traceOf(req), cfg.InboxURL, "/v1/inbox-items/"+in.ItemID+"/processing-attempts", map[string]string{"idempotencyKey": in.IdempotencyKey, "agentName": in.AgentName, "modelProfile": in.ModelProfile})
	})
	mcp.AddTool(s, &mcp.Tool{Name: "inbox_list_processing_attempts", Description: "List auditable processing attempts for an Inbox item"}, func(ctx context.Context, req *mcp.CallToolRequest, in inboxAction) (*mcp.CallToolResult, Output, error) {
		headers := http.Header{}
		traceOf(req).Apply(headers)
		body, err := coreclient.Client{BaseURL: cfg.InboxURL, HTTP: hc}.DoWithHeaders(ctx, http.MethodGet, "/v1/inbox-items/"+in.ItemID+"/processing-attempts", nil, headers)
		if err != nil {
			return nil, Output{}, err
		}
		out, err := output(body)
		return nil, out, err
	})
	mcp.AddTool(s, &mcp.Tool{Name: "inbox_record_processing_attempt", Description: "Record an Inbox processing attempt result"}, func(ctx context.Context, req *mcp.CallToolRequest, in inboxAction) (*mcp.CallToolResult, Output, error) {
		return patch(ctx, hc, traceOf(req), cfg.InboxURL, "/v1/inbox-items/"+in.ItemID+"/processing-attempts/"+in.AttemptID, map[string]interface{}{"status": in.Status, "actions": in.Actions, "errorSummary": in.ErrorSummary})
	})
	mcp.AddTool(s, &mcp.Tool{Name: "inbox_set_human_review", Description: "Retain an ambiguous Inbox action for explicit Human Review"}, func(ctx context.Context, req *mcp.CallToolRequest, in inboxAction) (*mcp.CallToolResult, Output, error) {
		return post(ctx, hc, traceOf(req), cfg.InboxURL, "/v1/inbox-items/"+in.ItemID+"/human-review", map[string]interface{}{"attemptId": in.AttemptID, "proposedAction": in.ProposedAction, "candidates": in.Candidates, "errorSummary": in.ErrorSummary})
	})
	mcp.AddTool(s, &mcp.Tool{Name: "inbox_resolve", Description: "Resolve an Inbox item after successful processing"}, func(ctx context.Context, req *mcp.CallToolRequest, in inboxAction) (*mcp.CallToolResult, Output, error) {
		return post(ctx, hc, traceOf(req), cfg.InboxURL, "/v1/inbox-items/"+in.ItemID+"/resolve", nil)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "task_create", Description: "Create a Life OS task"}, func(ctx context.Context, req *mcp.CallToolRequest, in taskAction) (*mcp.CallToolResult, Output, error) {
		return audited(ctx, emitter, req, "task_create", in, func(trace activity.Trace) (*mcp.CallToolResult, Output, error) {
			return post(ctx, hc, trace, cfg.TasksURL, "/v1/tasks", in, in.IdempotencyKey)
		})
	})
	mcp.AddTool(s, &mcp.Tool{Name: "task_update", Description: "Rename, edit, reschedule, or unschedule one existing Task"}, func(ctx context.Context, req *mcp.CallToolRequest, in taskMutation) (*mcp.CallToolResult, Output, error) {
		return audited(ctx, emitter, req, "task_update", in, func(trace activity.Trace) (*mcp.CallToolResult, Output, error) {
			return patch(ctx, hc, trace, cfg.TasksURL, "/v1/tasks/"+in.TaskID, map[string]any{"title": in.Title, "notes": in.Notes, "dueAt": in.DueAt, "clearDueAt": in.ClearDueAt})
		})
	})
	mcp.AddTool(s, &mcp.Tool{Name: "task_complete", Description: "Mark one existing Task completed"}, func(ctx context.Context, req *mcp.CallToolRequest, in taskMutation) (*mcp.CallToolResult, Output, error) {
		return audited(ctx, emitter, req, "task_complete", in, func(trace activity.Trace) (*mcp.CallToolResult, Output, error) {
			return post(ctx, hc, trace, cfg.TasksURL, "/v1/tasks/"+in.TaskID+"/complete", nil)
		})
	})
	mcp.AddTool(s, &mcp.Tool{Name: "task_reopen", Description: "Reopen one completed Task"}, func(ctx context.Context, req *mcp.CallToolRequest, in taskMutation) (*mcp.CallToolResult, Output, error) {
		return audited(ctx, emitter, req, "task_reopen", in, func(trace activity.Trace) (*mcp.CallToolResult, Output, error) {
			return post(ctx, hc, trace, cfg.TasksURL, "/v1/tasks/"+in.TaskID+"/reopen", nil)
		})
	})
	mcp.AddTool(s, &mcp.Tool{Name: "calendar_create", Description: "Create a Life OS calendar event"}, func(ctx context.Context, req *mcp.CallToolRequest, in eventAction) (*mcp.CallToolResult, Output, error) {
		return audited(ctx, emitter, req, "calendar_create", in, func(trace activity.Trace) (*mcp.CallToolResult, Output, error) {
			return post(ctx, hc, trace, cfg.CalendarURL, "/v1/events", in, in.IdempotencyKey)
		})
	})
	mcp.AddTool(s, &mcp.Tool{Name: "finance_create_manual_transaction", Description: "Record a manual finance transaction only when account, amount, currency, and date are explicit"}, func(ctx context.Context, req *mcp.CallToolRequest, in financeAction) (*mcp.CallToolResult, Output, error) {
		return audited(ctx, emitter, req, "finance_create_manual_transaction", in, func(trace activity.Trace) (*mcp.CallToolResult, Output, error) {
			return post(ctx, hc, trace, cfg.FinanceURL, "/v1/transactions", in, in.IdempotencyKey)
		})
	})
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, nil))
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"status":"ok"}`)) })
	mux.HandleFunc("/ready", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"status":"ready"}`)) })
	return http.ListenAndServe(fmt.Sprintf(":%d", cfg.Port), mux)
}

// traceOf reads the workflow trace an MCP client sent with its HTTP request.
// Whoever calls MCP is an agent, so an unnamed caller is recorded as "mcp".
func traceOf(req *mcp.CallToolRequest) activity.Trace {
	get := func(string) string { return "" }
	if req != nil && req.Extra != nil && req.Extra.Header != nil {
		get = req.Extra.Header.Get
	}
	trace := activity.TraceFromHeaders(get)
	if trace.Actor.Kind == "user" {
		trace.Actor = activity.Agent("mcp")
	}
	if trace.CorrelationID == "" {
		trace.CorrelationID = uuid.NewString()
	}
	return trace
}

// audited records an MCP mutation as requested and then completed or failed.
// The owning service sees the request event as the causation of its change.
func audited(ctx context.Context, emitter *activity.Emitter, req *mcp.CallToolRequest, tool string, arguments any, call func(activity.Trace) (*mcp.CallToolResult, Output, error)) (*mcp.CallToolResult, Output, error) {
	trace := traceOf(req)
	requestID := uuid.New()
	subject := activity.Subject{Type: "mcp_action", ID: requestID.String()}
	emitter.Emit(ctx, activity.Event{ID: requestID, Type: "mcp.action.requested", Subject: subject, CorrelationID: trace.CorrelationID, CausationID: trace.CausationID, Actor: trace.Actor, Outcome: activity.Pending, Summary: "Requested " + tool, Data: map[string]any{"tool": tool, "arguments": arguments}})
	result, out, err := call(activity.Trace{CorrelationID: trace.CorrelationID, CausationID: &requestID, Actor: trace.Actor})
	done := activity.Event{Type: "mcp.action.completed", Subject: subject, CorrelationID: trace.CorrelationID, CausationID: &requestID, Actor: trace.Actor, Outcome: activity.Succeeded, Summary: "Completed " + tool, Data: map[string]any{"tool": tool}}
	if err != nil {
		done.Type, done.Outcome, done.Summary = "mcp.action.failed", activity.Failed, tool+" failed: "+activity.Preview(err.Error(), 160)
		done.Data["error"] = activity.Preview(err.Error(), 500)
	} else if id, ok := out.Data["id"].(string); ok {
		done.Data["resultId"] = id
	}
	emitter.Emit(ctx, done)
	return result, out, err
}

func post(ctx context.Context, hc *http.Client, trace activity.Trace, baseURL, path string, input interface{}, idempotencyKey ...string) (*mcp.CallToolResult, Output, error) {
	var raw []byte
	var err error
	if input != nil {
		raw, err = json.Marshal(input)
		if err != nil {
			return nil, Output{}, err
		}
	}
	headers := http.Header{}
	trace.Apply(headers)
	if len(idempotencyKey) > 0 && idempotencyKey[0] != "" {
		headers.Set("Idempotency-Key", idempotencyKey[0])
	}
	body, err := coreclient.Client{BaseURL: baseURL, HTTP: hc}.DoWithHeaders(ctx, http.MethodPost, path, raw, headers)
	if err != nil {
		return nil, Output{}, err
	}
	out, err := output(body)
	return nil, out, err
}

func patch(ctx context.Context, hc *http.Client, trace activity.Trace, baseURL, path string, input interface{}) (*mcp.CallToolResult, Output, error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, Output{}, err
	}
	headers := http.Header{}
	trace.Apply(headers)
	body, err := coreclient.Client{BaseURL: baseURL, HTTP: hc}.DoWithHeaders(ctx, http.MethodPatch, path, raw, headers)
	if err != nil {
		return nil, Output{}, err
	}
	out, err := output(body)
	return nil, out, err
}

func output(body []byte) (Output, error) {
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		return Output{}, fmt.Errorf("decode upstream JSON object: %w", err)
	}
	return Output{Data: data}, nil
}
