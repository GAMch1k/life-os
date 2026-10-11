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
	NextRetryAt    string                   `json:"nextRetryAt,omitempty" jsonschema:"RFC3339 time a retryable item may be retried"`
	RuntimeState   string                   `json:"runtimeState,omitempty" jsonschema:"spawning, running, completed, failed, timed_out, or cancelled"`
	RuntimeOwner   string                   `json:"runtimeOwner,omitempty" jsonschema:"Processor instance running the attempt"`
	SessionID      string                   `json:"sessionId,omitempty" jsonschema:"Agent session id, when known"`
	StaleAfter     int                      `json:"staleAfterSeconds,omitempty" jsonschema:"Heartbeat age after which an unfinished attempt is abandoned"`
	RetryReason    string                   `json:"retryReason,omitempty" jsonschema:"Why the retry is scheduled then, e.g. usage_limited or backoff"`
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
	add := func(n, d, b, p string) { addReadTool(s, hc, n, d, b, p) }
	add("task_list", "List Tasks", cfg.TasksURL, "/v1/tasks?")
	add("task_search", "Search Tasks by title or notes before mutating one", cfg.TasksURL, "/v1/tasks?")
	add("inbox_list", "List Inbox items", cfg.InboxURL, "/v1/inbox-items?")
	addCalendarTools(s, hc, cfg.CalendarURL, emitter)
	addFinanceTools(s, hc, cfg.FinanceURL, emitter)
	addFoodTools(s, hc, cfg.FoodURL, emitter)
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
		return patch(ctx, hc, traceOf(req), cfg.InboxURL, "/v1/inbox-items/"+in.ItemID+"/processing-attempts/"+in.AttemptID, attemptUpdate(in))
	})
	mcp.AddTool(s, &mcp.Tool{Name: "inbox_record_attempt_runtime", Description: "Record the agent runtime state of an unfinished Inbox processing attempt (refreshes its heartbeat)"}, func(ctx context.Context, req *mcp.CallToolRequest, in inboxAction) (*mcp.CallToolResult, Output, error) {
		body := map[string]interface{}{"state": in.RuntimeState, "owner": in.RuntimeOwner}
		if in.SessionID != "" {
			body["sessionId"] = in.SessionID
		}
		return put(ctx, hc, traceOf(req), cfg.InboxURL, "/v1/inbox-items/"+in.ItemID+"/processing-attempts/"+in.AttemptID+"/runtime", body)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "inbox_list_stale_attempts", Description: "List unfinished Inbox processing attempts whose runtime stopped reporting"}, func(ctx context.Context, req *mcp.CallToolRequest, in inboxAction) (*mcp.CallToolResult, Output, error) {
		headers := http.Header{}
		traceOf(req).Apply(headers)
		body, err := coreclient.Client{BaseURL: cfg.InboxURL, HTTP: hc}.DoWithHeaders(ctx, http.MethodGet, fmt.Sprintf("/v1/stale-processing-attempts?staleAfterSeconds=%d", in.StaleAfter), nil, headers)
		if err != nil {
			return nil, Output{}, err
		}
		out, err := output(body)
		return nil, out, err
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
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, nil))
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"status":"ok"}`)) })
	mux.HandleFunc("/ready", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"status":"ready"}`)) })
	return http.ListenAndServe(fmt.Sprintf(":%d", cfg.Port), mux)
}

// addReadTool registers a read tool that GETs path plus the caller's URL
// query from the owning service.
func addReadTool(s *mcp.Server, hc *http.Client, name, description, baseURL, path string) {
	mcp.AddTool(s, &mcp.Tool{Name: name, Description: description}, func(ctx context.Context, req *mcp.CallToolRequest, in query) (*mcp.CallToolResult, Output, error) {
		headers := http.Header{}
		traceOf(req).Apply(headers)
		body, err := coreclient.Client{BaseURL: baseURL, HTTP: hc}.DoWithHeaders(ctx, http.MethodGet, path+in.Query, nil, headers)
		if err != nil {
			return nil, Output{}, err
		}
		out, err := output(body)
		return nil, out, err
	})
}

// traceOf reads the workflow trace an MCP client sent with its HTTP request.
// Whoever calls MCP is an agent, so an unnamed caller (or one claiming to be
// the user) is recorded as "mcp". The trace always carries an actor, so the
// owning services' actor gate accepts MCP's calls.
func traceOf(req *mcp.CallToolRequest) activity.Trace {
	get := func(string) string { return "" }
	if req != nil && req.Extra != nil && req.Extra.Header != nil {
		get = req.Extra.Header.Get
	}
	trace := activity.TraceFromHeaders(get)
	if trace.Actor.IsZero() || trace.Actor.Kind == "user" {
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

func patch(ctx context.Context, hc *http.Client, trace activity.Trace, baseURL, path string, input interface{}, idempotencyKey ...string) (*mcp.CallToolResult, Output, error) {
	return write(ctx, hc, trace, http.MethodPatch, baseURL, path, input, idempotencyKey...)
}

func put(ctx context.Context, hc *http.Client, trace activity.Trace, baseURL, path string, input interface{}) (*mcp.CallToolResult, Output, error) {
	return write(ctx, hc, trace, http.MethodPut, baseURL, path, input)
}

func write(ctx context.Context, hc *http.Client, trace activity.Trace, method, baseURL, path string, input interface{}, idempotencyKey ...string) (*mcp.CallToolResult, Output, error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, Output{}, err
	}
	headers := http.Header{}
	trace.Apply(headers)
	if len(idempotencyKey) > 0 && idempotencyKey[0] != "" {
		headers.Set("Idempotency-Key", idempotencyKey[0])
	}
	body, err := coreclient.Client{BaseURL: baseURL, HTTP: hc}.DoWithHeaders(ctx, method, path, raw, headers)
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

// attemptUpdate forwards a processing outcome, including its retry schedule
// when the attempt is retryable.
func attemptUpdate(in inboxAction) map[string]interface{} {
	body := map[string]interface{}{"status": in.Status, "actions": in.Actions, "errorSummary": in.ErrorSummary}
	if in.NextRetryAt != "" {
		body["nextRetryAt"] = in.NextRetryAt
	}
	if in.RetryReason != "" {
		body["retryReason"] = in.RetryReason
	}
	return body
}
