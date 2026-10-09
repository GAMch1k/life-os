package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/GAMch1k/life-os/services/mcp/internal/config"
	"github.com/GAMch1k/life-os/services/mcp/internal/coreclient"
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
	s := mcp.NewServer(&mcp.Implementation{Name: "life-os", Version: "0.1.0"}, nil)
	add := func(n, d, b, p string) {
		mcp.AddTool(s, &mcp.Tool{Name: n, Description: d}, func(ctx context.Context, _ *mcp.CallToolRequest, in query) (*mcp.CallToolResult, Output, error) {
			body, e := coreclient.Client{BaseURL: b, HTTP: hc}.Do(ctx, http.MethodGet, p+in.Query, nil)
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
	mcp.AddTool(s, &mcp.Tool{Name: "inbox_start_processing_attempt", Description: "Start an auditable Inbox processing attempt"}, func(ctx context.Context, _ *mcp.CallToolRequest, in inboxAction) (*mcp.CallToolResult, Output, error) {
		return post(ctx, hc, cfg.InboxURL, "/v1/inbox-items/"+in.ItemID+"/processing-attempts", map[string]string{"idempotencyKey": in.IdempotencyKey, "agentName": in.AgentName, "modelProfile": in.ModelProfile})
	})
	mcp.AddTool(s, &mcp.Tool{Name: "inbox_list_processing_attempts", Description: "List auditable processing attempts for an Inbox item"}, func(ctx context.Context, _ *mcp.CallToolRequest, in inboxAction) (*mcp.CallToolResult, Output, error) {
		body, err := coreclient.Client{BaseURL: cfg.InboxURL, HTTP: hc}.Do(ctx, http.MethodGet, "/v1/inbox-items/"+in.ItemID+"/processing-attempts", nil)
		if err != nil {
			return nil, Output{}, err
		}
		out, err := output(body)
		return nil, out, err
	})
	mcp.AddTool(s, &mcp.Tool{Name: "inbox_record_processing_attempt", Description: "Record an Inbox processing attempt result"}, func(ctx context.Context, _ *mcp.CallToolRequest, in inboxAction) (*mcp.CallToolResult, Output, error) {
		return patch(ctx, hc, cfg.InboxURL, "/v1/inbox-items/"+in.ItemID+"/processing-attempts/"+in.AttemptID, map[string]interface{}{"status": in.Status, "actions": in.Actions, "errorSummary": in.ErrorSummary})
	})
	mcp.AddTool(s, &mcp.Tool{Name: "inbox_set_human_review", Description: "Retain an ambiguous Inbox action for explicit Human Review"}, func(ctx context.Context, _ *mcp.CallToolRequest, in inboxAction) (*mcp.CallToolResult, Output, error) {
		return post(ctx, hc, cfg.InboxURL, "/v1/inbox-items/"+in.ItemID+"/human-review", map[string]interface{}{"attemptId": in.AttemptID, "proposedAction": in.ProposedAction, "candidates": in.Candidates, "errorSummary": in.ErrorSummary})
	})
	mcp.AddTool(s, &mcp.Tool{Name: "inbox_resolve", Description: "Resolve an Inbox item after successful processing"}, func(ctx context.Context, _ *mcp.CallToolRequest, in inboxAction) (*mcp.CallToolResult, Output, error) {
		return post(ctx, hc, cfg.InboxURL, "/v1/inbox-items/"+in.ItemID+"/resolve", nil)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "task_create", Description: "Create a Life OS task"}, func(ctx context.Context, _ *mcp.CallToolRequest, in taskAction) (*mcp.CallToolResult, Output, error) {
		return post(ctx, hc, cfg.TasksURL, "/v1/tasks", in, in.IdempotencyKey)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "task_update", Description: "Rename, edit, reschedule, or unschedule one existing Task"}, func(ctx context.Context, _ *mcp.CallToolRequest, in taskMutation) (*mcp.CallToolResult, Output, error) {
		return patch(ctx, hc, cfg.TasksURL, "/v1/tasks/"+in.TaskID, map[string]any{"title": in.Title, "notes": in.Notes, "dueAt": in.DueAt, "clearDueAt": in.ClearDueAt})
	})
	mcp.AddTool(s, &mcp.Tool{Name: "task_complete", Description: "Mark one existing Task completed"}, func(ctx context.Context, _ *mcp.CallToolRequest, in taskMutation) (*mcp.CallToolResult, Output, error) {
		return post(ctx, hc, cfg.TasksURL, "/v1/tasks/"+in.TaskID+"/complete", nil)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "task_reopen", Description: "Reopen one completed Task"}, func(ctx context.Context, _ *mcp.CallToolRequest, in taskMutation) (*mcp.CallToolResult, Output, error) {
		return post(ctx, hc, cfg.TasksURL, "/v1/tasks/"+in.TaskID+"/reopen", nil)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "calendar_create", Description: "Create a Life OS calendar event"}, func(ctx context.Context, _ *mcp.CallToolRequest, in eventAction) (*mcp.CallToolResult, Output, error) {
		return post(ctx, hc, cfg.CalendarURL, "/v1/events", in, in.IdempotencyKey)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "finance_create_manual_transaction", Description: "Record a manual finance transaction only when account, amount, currency, and date are explicit"}, func(ctx context.Context, _ *mcp.CallToolRequest, in financeAction) (*mcp.CallToolResult, Output, error) {
		return post(ctx, hc, cfg.FinanceURL, "/v1/transactions", in, in.IdempotencyKey)
	})
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, nil))
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"status":"ok"}`)) })
	mux.HandleFunc("/ready", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"status":"ready"}`)) })
	return http.ListenAndServe(fmt.Sprintf(":%d", cfg.Port), mux)
}

func post(ctx context.Context, hc *http.Client, baseURL, path string, input interface{}, idempotencyKey ...string) (*mcp.CallToolResult, Output, error) {
	var raw []byte
	var err error
	if input != nil {
		raw, err = json.Marshal(input)
		if err != nil {
			return nil, Output{}, err
		}
	}
	headers := http.Header{}
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

func patch(ctx context.Context, hc *http.Client, baseURL, path string, input interface{}) (*mcp.CallToolResult, Output, error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, Output{}, err
	}
	body, err := coreclient.Client{BaseURL: baseURL, HTTP: hc}.Do(ctx, http.MethodPatch, path, raw)
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
