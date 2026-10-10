package processor

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/GAMch1k/life-os/pkg/activity"
)

type planner interface {
	Plan(context.Context, string) (string, error)
}
type toolCaller interface {
	Call(context.Context, string, map[string]any) (json.RawMessage, error)
}

// Runner turns raw Inbox items into approved Life OS MCP actions. It has no
// database client and resolves an item only after every approved tool succeeds.
type Runner struct {
	Planner planner
	Tools   toolCaller
	Model   string
	DryRun  bool
	// Retry schedules technical retries; ambiguity goes to Human Review.
	Retry RetryPolicy
	// Owner identifies this processor instance on attempt runtime records.
	Owner string
	// OnRuntime, when set, receives every runtime state the runner records.
	OnRuntime func(RuntimeEvent)
	// OnTokens, when set, receives the token usage Codex reports per run.
	OnTokens func(TokenUsage)
}
type inboxList struct {
	Items []inboxItem `json:"items"`
}
type inboxItem struct {
	ID                 string         `json:"id"`
	CorrelationID      string         `json:"correlationId"`
	Content            string         `json:"content"`
	Source             string         `json:"source"`
	SourceMetadata     map[string]any `json:"sourceMetadata"`
	ProcessingMetadata map[string]any `json:"processingMetadata"`
	CreatedAt          time.Time      `json:"createdAt"`
}
type plan struct {
	Actions    []plannedAction `json:"actions"`
	Ambiguous  bool            `json:"ambiguous"`
	Confidence float64         `json:"confidence"`
	Reason     string          `json:"reason"`
}
type plannedAction struct {
	Tool      string         `json:"tool"`
	Arguments map[string]any `json:"arguments"`
}

type Proposal struct {
	ItemID  string          `json:"itemId"`
	Actions []plannedAction `json:"actions"`
	Reason  string          `json:"reason,omitempty"`
}

func (r Runner) Run(ctx context.Context) ([]Proposal, error) {
	// Only raw items with no scheduled retry, or whose retry time has come.
	raw, err := r.Tools.Call(ctx, "inbox_list", map[string]any{"query": "state=raw&due=true&limit=50"})
	if err != nil {
		return nil, err
	}
	var items inboxList
	if err := json.Unmarshal(toolData(raw), &items); err != nil {
		return nil, fmt.Errorf("decode Inbox MCP result: %w", err)
	}
	proposals := make([]Proposal, 0, len(items.Items))
	for _, item := range items.Items {
		proposal, err := r.process(ctx, item)
		if err != nil {
			return nil, err
		}
		proposals = append(proposals, proposal)
	}
	return proposals, nil
}
func (r Runner) process(ctx context.Context, item inboxItem) (Proposal, error) {
	if r.DryRun {
		return r.plan(ctx, item)
	}
	key := idempotencyKey(item)
	completed, attemptKey, previous, err := r.nextAttemptKey(ctx, item.ID, key)
	if err != nil {
		return Proposal{}, err
	}
	if completed {
		return Proposal{ItemID: item.ID, Reason: "already processed"}, nil
	}
	// Every MCP call for this item carries its workflow correlation and the
	// processor's identity, so Inbox, MCP, and owning services record their
	// activity in the same trace.
	trace := activity.Trace{CorrelationID: item.CorrelationID, Actor: activity.Agent("codex-inbox-processor")}
	if trace.CorrelationID == "" {
		trace.CorrelationID = item.ID
	}
	ctx = activity.WithTrace(ctx, trace)
	attempt, err := r.Tools.Call(ctx, "inbox_start_processing_attempt", map[string]any{"itemId": item.ID, "idempotencyKey": attemptKey, "agentName": "codex-inbox-processor", "modelProfile": r.Model})
	if err != nil {
		return Proposal{}, fmt.Errorf("start attempt for %s: %w", item.ID, err)
	}
	var started struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(toolData(attempt), &started); err != nil || started.ID == "" {
		return Proposal{}, fmt.Errorf("decode attempt for %s: %w", item.ID, err)
	}
	startedEvent := activity.EventID("inbox.processing.started", started.ID)
	trace.CausationID = &startedEvent
	ctx = activity.WithTrace(ctx, trace)
	if err := r.record(ctx, item.ID, started.ID, "processing", nil, nil); err != nil {
		return Proposal{}, err
	}
	parsed, err := approvedReviewPlan(item)
	if err != nil {
		// The attempt is durable before Codex is spawned; its runtime record
		// heartbeats until the outcome is stored.
		runtime := r.trackRuntime(ctx, item.ID, started.ID, RuntimeSpawning)
		defer runtime.close()
		prompt, _ := json.Marshal(map[string]any{"content": item.Content, "source": item.Source, "sourceMetadata": item.SourceMetadata})
		rawPlan, planErr := r.Planner.Plan(planContext(WithRunObserver(ctx, runtime), item), string(prompt))
		runtime.finish(ctx, runtimeOutcome(ctx, planErr))
		if planErr != nil {
			if ctx.Err() != nil {
				// Shutting down: hand the capture back instead of leaving the
				// attempt open.
				return Proposal{}, r.schedule(ctx, item.ID, started.ID, nil, fmt.Sprintf("cancelled: %v", planErr), time.Now(), reasonCancelled)
			}
			return Proposal{}, r.retryLater(ctx, item.ID, started.ID, nil, fmt.Sprintf("planning failed: %v", planErr), failureCodex, previous)
		}
		parsed, err = decodePlan(rawPlan)
	}
	if err == nil && (parsed.Ambiguous || parsed.Confidence < 0.8) && len(parsed.Actions) == 1 && isTaskMutation(parsed.Actions[0]) && validAction(parsed.Actions[0]) {
		query, _ := parsed.Actions[0].Arguments["taskQuery"].(string)
		if candidates, searchErr := r.taskCandidates(ctx, query); searchErr == nil {
			return Proposal{}, r.humanReview(ctx, item.ID, started.ID, parsed.Actions[0], candidates, "ambiguous or low-confidence Task selection")
		}
	}
	if err != nil {
		return Proposal{}, r.retryLater(ctx, item.ID, started.ID, nil, "invalid action plan: "+err.Error(), failureModelOutput, previous)
	}
	if parsed.Ambiguous || parsed.Confidence < 0.8 || len(parsed.Actions) == 0 {
		// Codex understood the capture but found nothing it can safely do.
		// That is a decision for the user, never a technical retry.
		reason := "no executable action"
		if parsed.Reason != "" {
			reason = parsed.Reason
		}
		return Proposal{}, r.humanReview(ctx, item.ID, started.ID, plannedAction{Tool: "none", Arguments: map[string]any{}}, []map[string]any{}, reason)
	}
	actions := make([]map[string]any, 0, len(parsed.Actions))
	for _, action := range parsed.Actions {
		if !validAction(action) {
			return Proposal{}, r.retryLater(ctx, item.ID, started.ID, actions, "invalid proposed action: "+action.Tool, failureModelOutput, previous)
		}
		arguments := make(map[string]any, len(action.Arguments)+1)
		for field, value := range action.Arguments {
			arguments[field] = value
		}
		if (action.Tool == "task_update" || action.Tool == "task_complete" || action.Tool == "task_reopen") && arguments["taskId"] == nil {
			query, _ := arguments["taskQuery"].(string)
			raw, findErr := r.Tools.Call(ctx, "task_search", map[string]any{"query": "query=" + url.QueryEscape(query) + "&limit=10"})
			if findErr != nil {
				return Proposal{}, r.retryLater(ctx, item.ID, started.ID, actions, "task search failed: "+findErr.Error(), failureDependency, previous)
			}
			var found struct {
				Items []map[string]any `json:"items"`
			}
			if json.Unmarshal(toolData(raw), &found) != nil || len(found.Items) != 1 || found.Items[0]["id"] == nil {
				candidates := make([]map[string]any, 0, len(found.Items))
				for _, candidate := range found.Items {
					if id, ok := candidate["id"].(string); ok && id != "" {
						candidates = append(candidates, candidate)
					}
				}
				return Proposal{}, r.humanReview(ctx, item.ID, started.ID, action, candidates, "ambiguous or missing Task selection")
			}
			arguments["taskId"] = found.Items[0]["id"]
			delete(arguments, "taskQuery")
		}
		if action.Tool == "task_create" || action.Tool == "calendar_create" || action.Tool == "finance_create_manual_transaction" {
			arguments["idempotencyKey"] = actionIdempotencyKey(item, len(actions), action)
		}
		result, err := r.Tools.Call(ctx, action.Tool, arguments)
		audit := map[string]any{"tool": action.Tool, "arguments": arguments}
		if err != nil {
			actions = append(actions, audit)
			return Proposal{}, r.retryLater(ctx, item.ID, started.ID, actions, fmt.Sprintf("%s failed: %v", action.Tool, err), classifyActionError(err), previous)
		}
		audit["result"] = json.RawMessage(toolData(result))
		actions = append(actions, audit)
	}
	if err := r.record(ctx, item.ID, started.ID, "processed", actions, nil); err != nil {
		return Proposal{}, err
	}
	completedEvent := activity.EventID("inbox.processing.completed", started.ID)
	trace.CausationID = &completedEvent
	_, err = r.Tools.Call(activity.WithTrace(ctx, trace), "inbox_resolve", map[string]any{"itemId": item.ID})
	return Proposal{}, err
}
func isTaskMutation(action plannedAction) bool {
	return action.Tool == "task_update" || action.Tool == "task_complete" || action.Tool == "task_reopen"
}
func (r Runner) taskCandidates(ctx context.Context, query string) ([]map[string]any, error) {
	raw, err := r.Tools.Call(ctx, "task_search", map[string]any{"query": "query=" + url.QueryEscape(query) + "&limit=10"})
	if err != nil {
		return nil, err
	}
	var found struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(toolData(raw), &found); err != nil {
		return nil, err
	}
	candidates := make([]map[string]any, 0, len(found.Items))
	for _, candidate := range found.Items {
		if id, ok := candidate["id"].(string); ok && id != "" {
			candidates = append(candidates, candidate)
		}
	}
	return candidates, nil
}

// planContext carries what planning needs to resolve the item's relative
// dates: when it was captured, and the timezone it was captured in (the
// capturing browser's, sourceMetadata.timeZone, when the client sent one).
func planContext(ctx context.Context, item inboxItem) context.Context {
	timeZone, _ := item.SourceMetadata["timeZone"].(string)
	return WithCaptureTimeZone(WithCapturedAt(ctx, item.CreatedAt), timeZone)
}

func approvedReviewPlan(item inboxItem) (plan, error) {
	raw, ok := item.ProcessingMetadata["approvedAction"].(map[string]any)
	if !ok {
		return plan{}, fmt.Errorf("no approved review action")
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return plan{}, err
	}
	var action plannedAction
	if err := json.Unmarshal(encoded, &action); err != nil {
		return plan{}, err
	}
	return plan{Actions: []plannedAction{action}, Confidence: 1}, nil
}

// nextAttemptKey preserves every retry as a separate audit row while keeping
// the deterministic operation key recognizable. A completed operation stops
// retries; retryable/failed rows receive a new, unique attempt identity.
// nextAttemptKey returns whether the item was already processed, the key for
// its next attempt, and how many attempts it had before.
func (r Runner) nextAttemptKey(ctx context.Context, itemID, key string) (bool, string, int, error) {
	raw, err := r.Tools.Call(ctx, "inbox_list_processing_attempts", map[string]any{"itemId": itemID})
	if err != nil {
		return false, "", 0, err
	}
	var attempts struct {
		Items []struct {
			IdempotencyKey string `json:"idempotencyKey"`
			Status         string `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(toolData(raw), &attempts); err != nil {
		return false, "", 0, err
	}
	count := 0
	for _, attempt := range attempts.Items {
		if attempt.IdempotencyKey == key || strings.HasPrefix(attempt.IdempotencyKey, key+".retry.") {
			count++
			if attempt.Status == "processed" {
				return true, "", count, nil
			}
		}
	}
	if count == 0 {
		return false, key, 0, nil
	}
	return false, fmt.Sprintf("%s.retry.%d", key, count+1), count, nil
}
func (r Runner) plan(ctx context.Context, item inboxItem) (Proposal, error) {
	prompt, _ := json.Marshal(map[string]any{"content": item.Content, "source": item.Source, "sourceMetadata": item.SourceMetadata})
	raw, err := r.Planner.Plan(planContext(ctx, item), string(prompt))
	if err != nil {
		return Proposal{}, err
	}
	parsed, err := decodePlan(raw)
	if err != nil {
		return Proposal{}, err
	}
	return Proposal{ItemID: item.ID, Actions: parsed.Actions, Reason: parsed.Reason}, nil
}

// retryLater records a technical failure. It schedules the next attempt, or
// marks the item failed (operator attention) once unusable model output has
// used up its attempts.
func (r Runner) retryLater(ctx context.Context, itemID, attemptID string, actions []map[string]any, reason string, kind failureKind, previous int) error {
	if kind == failureModelOutput && previous+1 >= maxModelOutputAttempts {
		reason = fmt.Sprintf("gave up after %d attempts: %s", previous+1, reason)
		return r.record(ctx, itemID, attemptID, "failed", actions, &reason)
	}
	at, why := r.Retry.next(ctx, kind, previous, reason)
	return r.schedule(ctx, itemID, attemptID, actions, reason, at, why)
}

// schedule records a retryable outcome with the time the capture may be
// retried. Like every outcome, it is stored even while shutting down.
func (r Runner) schedule(ctx context.Context, itemID, attemptID string, actions []map[string]any, reason string, at time.Time, why string) error {
	if actions == nil {
		actions = []map[string]any{}
	}
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	_, err := r.Tools.Call(recordCtx, "inbox_record_processing_attempt", map[string]any{"itemId": itemID, "attemptId": attemptID, "status": "retryable", "actions": actions, "errorSummary": reason, "nextRetryAt": at.UTC().Format(time.RFC3339Nano), "retryReason": why})
	return err
}
func (r Runner) humanReview(ctx context.Context, itemID, attemptID string, action plannedAction, candidates []map[string]any, reason string) error {
	_, err := r.Tools.Call(ctx, "inbox_set_human_review", map[string]any{"itemId": itemID, "attemptId": attemptID, "proposedAction": action, "candidates": candidates, "errorSummary": reason})
	return err
}
func (r Runner) record(ctx context.Context, itemID, attemptID, status string, actions []map[string]any, summary *string) error {
	if actions == nil {
		actions = []map[string]any{}
	}
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	_, err := r.Tools.Call(recordCtx, "inbox_record_processing_attempt", map[string]any{"itemId": itemID, "attemptId": attemptID, "status": status, "actions": actions, "errorSummary": summary})
	return err
}
func allowed(name string) bool {
	return name == "task_create" || name == "task_update" || name == "task_complete" || name == "task_reopen" || name == "calendar_create" || name == "finance_create_manual_transaction"
}
func validAction(action plannedAction) bool {
	if !allowed(action.Tool) || action.Arguments == nil {
		return false
	}
	if action.Tool == "task_update" || action.Tool == "task_complete" || action.Tool == "task_reopen" {
		if taskQuery, ok := action.Arguments["taskQuery"].(string); (!ok || strings.TrimSpace(taskQuery) == "") && action.Arguments["taskId"] == nil {
			return false
		}
	} else if action.Tool != "finance_create_manual_transaction" {
		if title, ok := action.Arguments["title"].(string); !ok || strings.TrimSpace(title) == "" {
			return false
		}
	}
	allowedFields := map[string]map[string]bool{
		"task_create":                       {"title": true, "notes": true, "dueAt": true},
		"task_update":                       {"taskQuery": true, "taskId": true, "title": true, "notes": true, "dueAt": true, "clearDueAt": true},
		"task_complete":                     {"taskQuery": true, "taskId": true},
		"task_reopen":                       {"taskQuery": true, "taskId": true},
		"calendar_create":                   {"title": true, "description": true, "allDay": true, "startAt": true, "endAt": true, "startDate": true, "endDate": true},
		"finance_create_manual_transaction": {"accountId": true, "categoryId": true, "amount": true, "occurredOn": true, "note": true},
	}
	for field := range action.Arguments {
		if !allowedFields[action.Tool][field] {
			return false
		}
	}
	if action.Tool == "finance_create_manual_transaction" {
		account, accountOK := action.Arguments["accountId"].(string)
		amount, amountOK := action.Arguments["amount"].(map[string]any)
		occurred, occurredOK := action.Arguments["occurredOn"].(string)
		money, moneyOK := amount["amount"].(float64)
		currency, currencyOK := amount["currency"].(string)
		if !accountOK || account == "" || !amountOK || !moneyOK || money == 0 || !currencyOK || currency == "" || !occurredOK || occurred == "" {
			return false
		}
	}
	return true
}
func idempotencyKey(item inboxItem) string {
	sum := sha256.Sum256([]byte(item.ID + "\x00" + item.Content))
	return fmt.Sprintf("codex-%x", sum[:12])
}
func actionIdempotencyKey(item inboxItem, index int, action plannedAction) string {
	raw, _ := json.Marshal(map[string]any{"operation": idempotencyKey(item), "index": index, "tool": action.Tool, "arguments": action.Arguments})
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("action-%x", sum[:16])
}
func decodePlan(raw string) (plan, error) {
	clean := strings.TrimSpace(raw)
	clean = strings.TrimPrefix(clean, "```json")
	clean = strings.TrimPrefix(clean, "```")
	clean = strings.TrimSuffix(strings.TrimSpace(clean), "```")
	var parsed plan
	if err := json.Unmarshal([]byte(clean), &parsed); err != nil {
		return plan{}, err
	}
	return parsed, nil
}
func toolData(raw json.RawMessage) json.RawMessage {
	var result struct {
		StructuredContent struct {
			Data json.RawMessage `json:"data"`
		} `json:"structuredContent"`
	}
	if json.Unmarshal(raw, &result) == nil && len(result.StructuredContent.Data) > 0 {
		return result.StructuredContent.Data
	}
	return raw
}
