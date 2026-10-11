package processor

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

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
	// TimeZone is the owner's configured timezone; Calendar events are
	// searched from the start of the capture's day in it (or in the
	// capture's own timezone). Nil means UTC.
	TimeZone *time.Location
	// Media reads a capture's images (InboxMedia) for Codex; nil plans
	// without them.
	Media mediaFetcher
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
	MediaReferences    []string       `json:"mediaReferences"`
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
	// The event the owner chose for an approved Calendar change, and the
	// shopping list or pantry item for an approved Food change.
	approvedEvent, approvedFood := "", ""
	if err == nil {
		parsed, approvedEvent = approvedCalendarTarget(parsed)
		parsed, approvedFood = approvedFoodTarget(parsed)
	} else {
		// Without its images a capture could be misread: when Inbox cannot
		// hand them over, it is retried later.
		images, imagesErr := r.captureImages(ctx, item)
		if imagesErr != nil {
			return Proposal{}, r.retryLater(ctx, item.ID, started.ID, nil, "capture images unavailable: "+imagesErr.Error(), failureDependency, previous)
		}
		// The attempt is durable before Codex is spawned; its runtime record
		// heartbeats until the outcome is stored.
		runtime := r.trackRuntime(ctx, item.ID, started.ID, RuntimeSpawning)
		defer runtime.close()
		prompt, _ := json.Marshal(map[string]any{"content": item.Content, "source": item.Source, "sourceMetadata": item.SourceMetadata})
		rawPlan, planErr := r.Planner.Plan(WithImages(planContext(WithRunObserver(ctx, runtime), item), images), string(prompt))
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
	// Fields the AI may not set are dropped and recorded, never an error.
	dropped := dropAIFields(parsed)
	if err == nil && (parsed.Ambiguous || parsed.Confidence < 0.8) && len(parsed.Actions) == 1 && (isTaskMutation(parsed.Actions[0]) || isCalendarMutation(parsed.Actions[0]) || isFoodMutation(parsed.Actions[0])) && validAction(parsed.Actions[0]) {
		if res, searchErr := r.resolve(ctx, item, parsed.Actions[0]); searchErr == nil {
			return Proposal{}, r.reviewResolution(ctx, item, started.ID, res, lowConfidenceReason(res.action))
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
	for index, action := range parsed.Actions {
		// noted adds what was dropped from this action to its audit entry.
		noted := func(audit map[string]any) map[string]any {
			if len(dropped[index]) > 0 {
				audit["droppedFields"] = dropped[index]
			}
			return audit
		}
		if plansRecurrence(action) {
			// Only the owner sets up or changes a repeating event.
			reason := recurringChangeReason
			if action.Tool == "calendar_create" {
				reason = recurringCreateReason
			}
			return Proposal{}, r.humanReview(ctx, item.ID, started.ID, action, []map[string]any{}, reason)
		}
		if missingSlot(action) {
			// "Pasta on Wednesday": which meal of the day is the owner's.
			return Proposal{}, r.humanReview(ctx, item.ID, started.ID, action, []map[string]any{}, noSlotReason)
		}
		if action.Tool == "calendar_update" && len(dropped[index]) > 0 && !changesEvent(action.Arguments) {
			// It only turned the event's default reminders off or on.
			return Proposal{}, r.humanReview(ctx, item.ID, started.ID, action, []map[string]any{}, defaultRemindersReason)
		}
		if !validAction(action) {
			return Proposal{}, r.retryLater(ctx, item.ID, started.ID, actions, "invalid proposed action: "+action.Tool, failureModelOutput, previous)
		}
		// recipe is the recipe a meal or its ingredients name: the one the
		// owner chose in Human Review, else the one the words find. A meal
		// no recipe matches is planned as its words.
		recipe := ""
		if isRecipeAction(action) {
			recipe = approvedFood
			if recipe == "" {
				found, findErr := r.findRecipes(ctx, action)
				if findErr != nil {
					return Proposal{}, r.retryLater(ctx, item.ID, started.ID, actions, "recipe search failed: "+findErr.Error(), classifyActionError(findErr), previous)
				}
				switch {
				case len(found) > 1:
					return Proposal{}, r.humanReview(ctx, item.ID, started.ID, action, recipeCandidates(found), recipeReviewReason(action.Tool))
				case len(found) == 1:
					recipe, _ = found[0]["id"].(string)
				case action.Tool == "food_add_missing_ingredients":
					// "купи всё для борща" without a borscht recipe: the
					// owner expects groceries, so they decide.
					return Proposal{}, r.humanReview(ctx, item.ID, started.ID, action, []map[string]any{}, noRecipeReason(text(action.Arguments["recipeQuery"])))
				}
			}
		}
		// target is the one existing Task or event a search found.
		var target map[string]any
		// restated is the planned calendar_create of an event that already
		// exists, which an update of that event replaces.
		var restated map[string]any
		if action.Tool == "calendar_create" {
			// The event the owner chose in Human Review, else a search.
			var found []map[string]any
			var findErr error
			if approvedEvent != "" {
				event, getErr := r.getEvent(ctx, approvedEvent)
				if getErr != nil {
					return Proposal{}, r.retryLater(ctx, item.ID, started.ID, actions, "calendar read failed: "+getErr.Error(), classifyActionError(getErr), previous)
				}
				found = []map[string]any{event}
			} else if found, findErr = r.findDuplicates(ctx, item, action); findErr != nil {
				reason, kind := failedSearch(findErr)
				return Proposal{}, r.retryLater(ctx, item.ID, started.ID, actions, reason, kind, previous)
			}
			switch {
			case len(found) > 1:
				// Approving a candidate runs this create as an update of it.
				return Proposal{}, r.humanReview(ctx, item.ID, started.ID, action, eventCandidates(found, r.location(item)), duplicateReason)
			case len(found) == 1 && isRecurring(found[0]):
				return Proposal{}, r.humanReview(ctx, item.ID, started.ID, action, []map[string]any{}, recurringChangeReason)
			case len(found) == 1:
				restated, target = action.Arguments, found[0]
				action = plannedAction{Tool: "calendar_update", Arguments: duplicateChange(restated, target)}
				if len(action.Arguments) == 0 {
					actions = append(actions, noted(map[string]any{"tool": "none", "convertedFrom": "calendar_create", "plannedArguments": restated, "duplicateOf": target["id"], "reason": unchangedDuplicateReason}))
					continue
				}
			}
		}
		if target == nil && ((isTaskMutation(action) && action.Arguments["taskId"] == nil) || (isCalendarMutation(action) && approvedEvent == "") || (isFoodMutation(action) && approvedFood == "")) {
			res, findErr := r.resolve(ctx, item, action)
			if findErr != nil {
				reason, kind := failedSearch(findErr)
				return Proposal{}, r.retryLater(ctx, item.ID, started.ID, actions, reason, kind, previous)
			}
			// Nothing to mark bought or used up: nothing is created
			// instead, and the capture's other actions still run.
			if isFoodMutation(action) && len(res.found) == 0 {
				actions = append(actions, noted(map[string]any{"tool": "none", "plannedTool": action.Tool, "plannedArguments": action.Arguments, "reason": unmatchedFoodReason(action.Tool)}))
				continue
			}
			// Every cancellation the AI proposes waits for the owner, even
			// when exactly one event matches; it runs once approved. A
			// recurring event is never changed by capture.
			if res.action.Tool == "calendar_cancel" || len(res.found) != 1 || recurringUpdate(res) {
				return Proposal{}, r.reviewResolution(ctx, item, started.ID, res, reviewReason(res))
			}
			// The equivalent action, when only the other domain matched.
			action, target = res.action, res.found[0]
		}
		arguments := make(map[string]any, len(action.Arguments)+1)
		for field, value := range action.Arguments {
			arguments[field] = value
		}
		if isTaskMutation(action) {
			if target != nil {
				arguments["taskId"] = target["id"]
			}
			// The search terms only identify the Task (a Human Review approval
			// may still carry the alternatives); the Tasks operation takes its id.
			delete(arguments, "taskQuery")
			delete(arguments, "taskQueryAlternatives")
		}
		if isCalendarMutation(action) {
			eventID, event := approvedEvent, target
			if target != nil {
				eventID, _ = target["id"].(string)
			}
			arguments = calendarArguments(action.Tool, arguments, eventID)
			// The owner chose the event: a recurring one is only ever
			// skipped one occurrence at a time, never changed as a whole.
			if target == nil && (action.Tool == "calendar_update" || arguments["recurrenceId"] == nil) {
				var getErr error
				if event, getErr = r.getEvent(ctx, eventID); getErr != nil {
					return Proposal{}, r.retryLater(ctx, item.ID, started.ID, actions, "calendar read failed: "+getErr.Error(), classifyActionError(getErr), previous)
				}
				if isRecurring(event) {
					return Proposal{}, r.humanReview(ctx, item.ID, started.ID, action, []map[string]any{}, recurringChangeReason)
				}
			}
			if action.Tool == "calendar_update" && missingEnd(arguments) {
				if event == nil {
					var getErr error
					if event, getErr = r.getEvent(ctx, eventID); getErr != nil {
						return Proposal{}, r.retryLater(ctx, item.ID, started.ID, actions, "calendar read failed: "+getErr.Error(), classifyActionError(getErr), previous)
					}
				}
				keepTimeOfDay(arguments, event, r.location(item))
				keepDuration(arguments, event)
			}
			// New reminders add to the event's own (a restated event's are
			// merged already).
			if action.Tool == "calendar_update" && restated == nil && !addReminders(arguments, event) {
				actions = append(actions, noted(map[string]any{"tool": "none", "plannedTool": action.Tool, "plannedArguments": action.Arguments, "eventId": eventID, "reason": unchangedRemindersReason}))
				continue
			}
		}
		if isFoodMutation(action) {
			itemID := approvedFood
			if target != nil {
				itemID, _ = target["id"].(string)
			}
			arguments = foodArguments(action.Tool, arguments, itemID)
		}
		if isMealTool(action.Tool) {
			at := item.CreatedAt
			if at.IsZero() {
				at = time.Now()
			}
			arguments = mealArguments(action.Tool, arguments, recipe, at, r.location(item))
		}
		if action.Tool == "food_add_shopping_items" || action.Tool == "food_add_pantry_items" {
			// Food records that the items came from this capture.
			arguments["sourceRef"] = item.ID
		}
		if action.Tool == "calendar_create" {
			completeCreate(arguments)
		}
		switch {
		case action.Tool == "task_create" || action.Tool == "calendar_create" || action.Tool == "finance_create_manual_transaction":
			arguments["idempotencyKey"] = actionIdempotencyKey(item, len(actions), action)
		case isFoodWrite(action.Tool):
			arguments["idempotencyKey"] = foodIdempotencyKey(item, action.Tool, arguments)
		}
		result, err := r.Tools.Call(ctx, action.Tool, arguments)
		audit := noted(map[string]any{"tool": action.Tool, "arguments": arguments})
		if restated != nil {
			audit["convertedFrom"] = "calendar_create"
			audit["plannedArguments"] = restated
		}
		if err != nil && isCalendarMutation(action) && managedBySource(err) {
			// An imported event: a decision for the owner, never a retry.
			return Proposal{}, r.humanReview(ctx, item.ID, started.ID, action, []map[string]any{}, managedBySourceReason)
		}
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

const (
	// taskSearchLimit bounds each Task search and the candidates a Human
	// Review lists.
	taskSearchLimit = 10
	// maxTaskQueryAlternatives bounds the extra search terms one Task
	// mutation may carry.
	maxTaskQueryAlternatives = 6
	// minTaskQueryAlternative is the shortest extra search term used, in
	// characters; a shorter fragment identifies no particular Task.
	minTaskQueryAlternative = 3
)

// taskQueries is every term existing Tasks are searched with for one
// mutation: its taskQuery, then its taskQueryAlternatives (the same thing in
// the owner's other languages, or a word stem that also matches inflected
// forms), trimmed and without duplicates. A title in Russian or German is
// found from an English capture only through an alternative.
func taskQueries(arguments map[string]any) []string {
	return searchTerms(arguments, "taskQuery", "taskQueryAlternatives")
}

// searchTerms is a mutation's primary search term (the primary field) and
// its alternatives, trimmed, deduplicated, and bounded like taskQueries.
func searchTerms(arguments map[string]any, primaryField, alternativesField string) []string {
	var queries []string
	seen := map[string]bool{}
	add := func(term string) bool {
		term = strings.TrimSpace(term)
		if term == "" || seen[strings.ToLower(term)] {
			return false
		}
		seen[strings.ToLower(term)] = true
		queries = append(queries, term)
		return true
	}
	primary, _ := arguments[primaryField].(string)
	add(primary)
	alternatives, _ := arguments[alternativesField].([]any)
	used := 0
	for _, alternative := range alternatives {
		if used == maxTaskQueryAlternatives {
			break
		}
		term, _ := alternative.(string)
		if utf8.RuneCountInString(strings.TrimSpace(term)) >= minTaskQueryAlternative && add(term) {
			used++
		}
	}
	return queries
}

// findTasks returns the distinct Tasks any of a mutation's search terms
// matches, in the order found. Only open Tasks can be completed or updated
// and only completed ones reopened, so a finished Task with the same name
// never makes a capture ambiguous. The caller acts only on exactly one.
func (r Runner) findTasks(ctx context.Context, action plannedAction) ([]map[string]any, error) {
	completed := "false"
	if action.Tool == "task_reopen" {
		completed = "true"
	}
	found := []map[string]any{}
	seen := map[string]bool{}
	for _, term := range taskQueries(action.Arguments) {
		raw, err := r.Tools.Call(ctx, "task_search", map[string]any{"query": fmt.Sprintf("query=%s&completed=%s&limit=%d", url.QueryEscape(term), completed, taskSearchLimit)})
		if err != nil {
			return nil, err
		}
		var result struct {
			Items []map[string]any `json:"items"`
		}
		if err := json.Unmarshal(toolData(raw), &result); err != nil {
			// A partial union could look like exactly one match.
			return nil, fmt.Errorf("decode Task search result: %w", err)
		}
		for _, task := range result.Items {
			if id, ok := task["id"].(string); ok && id != "" && !seen[id] {
				seen[id] = true
				found = append(found, task)
			}
		}
	}
	return found, nil
}

// reviewCandidates is the Tasks a Human Review offers to choose from.
func reviewCandidates(found []map[string]any) []map[string]any {
	if len(found) > taskSearchLimit {
		return found[:taskSearchLimit]
	}
	return found
}

// planContext carries what planning needs to resolve the item's relative
// dates: when it was captured, and the timezone it was captured in (the
// capturing browser's, sourceMetadata.timeZone, when the client sent one).
// captureImages reads the images of a capture that has any.
func (r Runner) captureImages(ctx context.Context, item inboxItem) (CaptureImages, error) {
	if r.Media == nil || len(item.MediaReferences) == 0 {
		return CaptureImages{}, nil
	}
	return r.Media.Fetch(ctx, item.MediaReferences)
}

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
	images, err := r.captureImages(ctx, item)
	if err != nil {
		return Proposal{}, err
	}
	prompt, _ := json.Marshal(map[string]any{"content": item.Content, "source": item.Source, "sourceMetadata": item.SourceMetadata})
	raw, err := r.Planner.Plan(WithImages(planContext(ctx, item), images), string(prompt))
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
	return name == "task_create" || name == "task_update" || name == "task_complete" || name == "task_reopen" || name == "calendar_create" || name == "calendar_update" || name == "calendar_cancel" || name == "finance_create_manual_transaction" || isFoodWrite(name)
}

// validSearchTerms checks a mutation's primary search term and alternatives.
func validSearchTerms(arguments map[string]any, primaryField, alternativesField string) bool {
	if primary, ok := arguments[primaryField].(string); !ok || strings.TrimSpace(primary) == "" {
		return false
	}
	if alternatives, present := arguments[alternativesField]; present && alternatives != nil {
		terms, ok := alternatives.([]any)
		if !ok {
			return false
		}
		for _, term := range terms {
			if _, ok := term.(string); !ok {
				return false
			}
		}
	}
	return true
}
func validAction(action plannedAction) bool {
	if !allowed(action.Tool) || action.Arguments == nil {
		return false
	}
	if action.Tool == "task_update" || action.Tool == "task_complete" || action.Tool == "task_reopen" {
		if taskQuery, ok := action.Arguments["taskQuery"].(string); (!ok || strings.TrimSpace(taskQuery) == "") && action.Arguments["taskId"] == nil {
			return false
		}
		if alternatives, present := action.Arguments["taskQueryAlternatives"]; present && alternatives != nil {
			terms, ok := alternatives.([]any)
			if !ok {
				return false
			}
			for _, term := range terms {
				if _, ok := term.(string); !ok {
					return false
				}
			}
		}
	} else if isCalendarMutation(action) {
		// A planned Calendar change names its event only by search terms.
		if !validSearchTerms(action.Arguments, "eventQuery", "eventQueryAlternatives") || (action.Tool == "calendar_update" && !validCalendarChange(action.Arguments)) {
			return false
		}
		if action.Tool == "calendar_cancel" && !validCancelDay(action.Arguments) {
			return false
		}
	} else if isFoodWrite(action.Tool) {
		if !validFoodAction(action) {
			return false
		}
	} else if action.Tool != "finance_create_manual_transaction" {
		if title, ok := action.Arguments["title"].(string); !ok || strings.TrimSpace(title) == "" {
			return false
		}
		if action.Tool == "calendar_create" && (!validReminders(action.Arguments["reminders"]) || !validPriority(action.Arguments)) {
			return false
		}
	}
	allowedFields := map[string]map[string]bool{
		"task_create":                       {"title": true, "notes": true, "dueAt": true},
		"task_update":                       {"taskQuery": true, "taskQueryAlternatives": true, "taskId": true, "title": true, "notes": true, "dueAt": true, "clearDueAt": true},
		"task_complete":                     {"taskQuery": true, "taskQueryAlternatives": true, "taskId": true},
		"task_reopen":                       {"taskQuery": true, "taskQueryAlternatives": true, "taskId": true},
		"calendar_create":                   {"title": true, "description": true, "location": true, "allDay": true, "startAt": true, "endAt": true, "startDate": true, "endDate": true, "timeZone": true, "reminders": true, "priority": true},
		"calendar_update":                   {"eventQuery": true, "eventQueryAlternatives": true, "title": true, "description": true, "location": true, "allDay": true, "startAt": true, "endAt": true, "startDate": true, "endDate": true, "timeZone": true, "reminders": true, "priority": true, "clear": true},
		"calendar_cancel":                   {"eventQuery": true, "eventQueryAlternatives": true, "onDate": true, "occurrences": true},
		"finance_create_manual_transaction": {"accountId": true, "categoryId": true, "amount": true, "occurredOn": true, "note": true, "counterparty": true, "description": true},
		"food_add_shopping_items":           {"items": true},
		"food_add_pantry_items":             {"items": true},
		"food_mark_bought":                  {"itemQuery": true, "itemQueryAlternatives": true},
		"food_update_pantry_item":           {"itemQuery": true, "itemQueryAlternatives": true, "state": true},
		"food_plan_meal":                    {"recipeQuery": true, "recipeQueryAlternatives": true, "date": true, "slot": true, "servings": true, "note": true},
		"food_clear_meal":                   {"date": true, "slot": true},
		"food_add_missing_ingredients":      {"recipeQuery": true, "recipeQueryAlternatives": true, "week": true, "from": true, "to": true},
	}
	for field := range action.Arguments {
		if !allowedFields[action.Tool][field] {
			return false
		}
	}
	if action.Tool == "finance_create_manual_transaction" {
		// Only the amount is required: Finance records a transaction without
		// an account in the built-in Cash account, dated today.
		amount, amountOK := action.Arguments["amount"].(map[string]any)
		money, moneyOK := amount["amount"].(float64)
		currency, currencyOK := amount["currency"].(string)
		if !amountOK || !moneyOK || money == 0 || money != float64(int64(money)) || !currencyOK || currency == "" {
			return false
		}
		if value, present := action.Arguments["accountId"]; present && value != nil {
			if account, ok := value.(string); !ok || strings.TrimSpace(account) == "" {
				return false
			}
		}
		if value, present := action.Arguments["occurredOn"]; present && value != nil {
			day, ok := value.(string)
			if _, err := time.Parse(time.DateOnly, day); !ok || err != nil {
				return false
			}
		}
	}
	return true
}

// validCancelDay checks a cancellation's optional onDate (an ISO date) and
// the occurrences a Human Review recorded (series id to recurrence id).
func validCancelDay(arguments map[string]any) bool {
	if value, present := arguments["onDate"]; present && value != nil {
		day, ok := value.(string)
		if _, err := time.Parse(time.DateOnly, day); !ok || err != nil {
			return false
		}
	}
	if value, present := arguments["occurrences"]; present && value != nil {
		occurrences, ok := value.(map[string]any)
		if !ok {
			return false
		}
		for _, rid := range occurrences {
			if _, ok := rid.(string); !ok {
				return false
			}
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
