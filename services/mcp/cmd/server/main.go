package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
	_ "time/tzdata" // the owner's timezone resolves in any image

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/identity"
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
	DuplicateOf    string                   `json:"duplicateOf,omitempty" jsonschema:"With status processed: the earlier Inbox item UUID this one repeats (same text and images), so nothing was done"`
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
	zone, err := ownerZone(cfg.TimeZone)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	// Task and event times are shown, and moved, on the owner's clock.
	o := owner{zone: zone, now: time.Now}
	hc := &http.Client{Timeout: 10 * time.Second}
	emitter := activity.NewEmitter(cfg.ActivityURL, "mcp", nil)
	// The gateway's identity token, when a request has one (pkg/identity):
	// an invalid one is refused. With it, MCP lists and runs only the tools
	// the person's grant allows, and forwards the token to the services
	// (P21-06); without one (the processor, on the private network) every
	// tool, as before.
	keys, err := identity.KeySetFromEnv()
	if err != nil {
		return err
	}
	if keys.Len() == 0 {
		fmt.Fprintln(os.Stderr, "identity: "+identity.TrustedKeysEnv+" is empty: requests with an identity token are refused")
	}
	tools := newCatalog(func() *mcp.Server { return newServer(cfg, hc, emitter, o) })
	handler := httpHandler(identity.NewVerifier(keys, nil), tools, cfg.PublicURL)
	return http.ListenAndServe(fmt.Sprintf(":%d", cfg.Port), handler)
}

// newServer is the MCP server with every Life OS tool.
func newServer(cfg config.Config, hc *http.Client, emitter *activity.Emitter, o owner) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "life-os", Version: "0.1.0"}, nil)
	add := func(n, d, b, p string) { addReadTool(s, hc, n, d, b, p) }
	add("inbox_list", "List Inbox items", cfg.InboxURL, "/v1/inbox-items?")
	add("inbox_recent_captures", "List the captures made in the minutes before one capture (before=<itemId>, minutes, limit), with what their processing did", cfg.InboxURL, "/v1/recent-captures?")
	addTaskTools(s, hc, cfg.TasksURL, emitter, o)
	addCalendarTools(s, hc, cfg.CalendarURL, emitter, o)
	addFinanceTools(s, hc, cfg.FinanceURL, emitter)
	addFoodTools(s, hc, cfg.FoodURL, emitter)
	// A recipe Codex built from a capture's text or photo goes to Food.
	addRecipeDraftTools(s, hc, cfg.FoodURL, emitter)
	// recipe-import fetches the page while it answers (up to 10 seconds),
	// and a save of a recipe page fetches it again.
	addRecipeImportTools(s, &http.Client{Timeout: 45 * time.Second}, cfg.RecipeImportURL, emitter)
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
	return s
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

// audited records an MCP mutation as requested and then completed, not
// done, or failed. The owning service sees the request event as the
// causation of its change.
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
		emitter.Emit(ctx, done)
		return result, out, err
	}
	if reason, notDone := notDone(out.Data); notDone {
		// The tool answered, but what was asked did not happen (a link with
		// nothing to save): not a success, and the reason says why.
		done.Type, done.Outcome, done.Summary = "mcp.action.not_done", activity.Failed, tool+" not done: "+activity.Preview(reason, 160)
		done.Data["reason"] = activity.Preview(reason, 500)
	} else if out.Data["alreadySaved"] == true {
		done.Summary = "Completed " + tool + ": already saved"
		done.Data["alreadySaved"] = true
	}
	if id, ids := resultIDs(out.Data); id != "" {
		done.Data["resultId"] = id
	} else if len(ids) > 0 {
		done.Data["resultIds"] = ids
	}
	emitter.Emit(ctx, done)
	return result, out, err
}

// notDoneFlags are the answer fields that say whether what a tool was asked
// to do happened; false means it did not, and the answer's reason says why.
var notDoneFlags = []string{"saved"}

// notDone reports whether a tool's answer says that what was asked did not
// happen, and why.
func notDone(data map[string]any) (reason string, ok bool) {
	for _, flag := range notDoneFlags {
		if done, isFlag := data[flag].(bool); isFlag && !done {
			reason, _ = data["reason"].(string)
			if reason = strings.TrimSpace(reason); reason == "" {
				reason = "nothing was " + flag
			}
			return reason, true
		}
	}
	return "", false
}

// resultIDs names what a tool's answer created or changed: its id; else its
// one "…Id" field (recipeId) or nested entity's id (staple.id), when they
// all name the same thing. Several different ones are all named, by field.
func resultIDs(data map[string]any) (string, map[string]string) {
	if id, ok := data["id"].(string); ok && id != "" {
		return id, nil
	}
	found := map[string]string{}
	distinct := map[string]bool{}
	for field, value := range data {
		id := ""
		switch value := value.(type) {
		case string:
			if strings.HasSuffix(field, "Id") {
				id = value
			}
		case map[string]any:
			id, _ = value["id"].(string)
			field += ".id"
		}
		if id != "" {
			found[field] = id
			distinct[id] = true
		}
	}
	switch len(distinct) {
	case 0:
		return "", nil
	case 1:
		for id := range distinct {
			return id, nil
		}
	}
	return "", found
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

func put(ctx context.Context, hc *http.Client, trace activity.Trace, baseURL, path string, input interface{}, idempotencyKey ...string) (*mcp.CallToolResult, Output, error) {
	return write(ctx, hc, trace, http.MethodPut, baseURL, path, input, idempotencyKey...)
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
	if in.DuplicateOf != "" {
		body["duplicateOf"] = in.DuplicateOf
	}
	return body
}
