package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/httpserver"
	store "github.com/GAMch1k/life-os/services/inbox/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/inbox/internal/testdb"
)

// recentAPI serves the Inbox API on a fresh schema, with the pool to place
// captures in time.
type recentAPI struct {
	t      *testing.T
	app    *httpserver.App
	pool   *pgxpool.Pool
	events *fakeService
}

func newRecentAPI(t *testing.T) *recentAPI {
	t.Helper()
	pool := testdb.Open(t, "inbox_recent_test")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	events := newFakeService(t, nil)
	app := httpserver.New("inbox", httpserver.WithLogger(logger))
	Register(app, NewHandler(store.New(pool), activity.NewEmitter(events.url, "inbox", logger), nil))
	return &recentAPI{t: t, app: app, pool: pool, events: events}
}

// request makes one request as the processor and returns the status and
// the decoded answer.
func (a *recentAPI) request(method, path string, body any) (int, map[string]any) {
	a.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			a.t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	request := httptest.NewRequest(method, path, reader)
	request.Header.Set("content-type", "application/json")
	request.Header.Set(activity.ActorHeader, "agent:codex-inbox-processor")
	response, err := a.app.Test(request, fiber.TestConfig{Timeout: 5 * time.Second})
	if err != nil {
		a.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer response.Body.Close()
	var decoded map[string]any
	_ = json.NewDecoder(response.Body).Decode(&decoded)
	return response.StatusCode, decoded
}

func (a *recentAPI) call(method, path string, body any) map[string]any {
	a.t.Helper()
	status, decoded := a.request(method, path, body)
	if status/100 != 2 {
		a.t.Fatalf("%s %s = %d %v", method, path, status, decoded)
	}
	return decoded
}

// capture makes a capture from source, created at the given time.
func (a *recentAPI) capture(content, source string, at time.Time) string {
	a.t.Helper()
	id := a.call(http.MethodPost, "/v1/inbox-items", map[string]any{"content": content, "source": source})["id"].(string)
	if _, err := a.pool.Exec(context.Background(), "UPDATE inbox_items SET created_at = $1 WHERE id = $2", at, id); err != nil {
		a.t.Fatal(err)
	}
	return id
}

func (a *recentAPI) attempt(itemID, key string) string {
	a.t.Helper()
	return a.call(http.MethodPost, "/v1/inbox-items/"+itemID+"/processing-attempts", map[string]any{"idempotencyKey": key, "agentName": "codex-inbox-processor", "modelProfile": "test"})["id"].(string)
}

func (a *recentAPI) finish(itemID, attemptID, status string, actions []map[string]any, reason string) {
	a.t.Helper()
	if actions == nil {
		actions = []map[string]any{}
	}
	body := map[string]any{"status": status, "actions": actions}
	if reason != "" {
		body["errorSummary"] = reason
	}
	a.call(http.MethodPatch, "/v1/inbox-items/"+itemID+"/processing-attempts/"+attemptID, body)
}

// processed records a processed attempt with actions and resolves the
// capture, as the processor does.
func (a *recentAPI) processed(itemID string, actions []map[string]any) string {
	a.t.Helper()
	attemptID := a.attempt(itemID, "op-"+itemID)
	a.finish(itemID, attemptID, "processing", nil, "")
	a.finish(itemID, attemptID, "processed", actions, "")
	a.call(http.MethodPost, "/v1/inbox-items/"+itemID+"/resolve", nil)
	return attemptID
}

func (a *recentAPI) recent(query string) []map[string]any {
	a.t.Helper()
	raw, _ := a.call(http.MethodGet, "/v1/recent-captures?"+query, nil)["items"].([]any)
	items := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		items = append(items, item.(map[string]any))
	}
	return items
}

func contents(items []map[string]any) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item["content"].(string))
	}
	return out
}

func TestRecentCapturesListTheSettledCapturesBeforeOne(t *testing.T) {
	api := newRecentAPI(t)
	at := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	minutes := func(n int) time.Time { return at.Add(time.Duration(n) * time.Minute) }

	tooEarly := api.capture("Too early", "web", minutes(-25))
	api.processed(tooEarly, []map[string]any{{"tool": "task_create"}})
	milk := api.capture("Buy milk", "voice", minutes(-15))
	// A retry first, then the processed attempt: the latest finished one counts.
	retried := api.attempt(milk, "op-milk")
	api.finish(milk, retried, "retryable", []map[string]any{}, "Codex timed out")
	shopping := []map[string]any{{"tool": "food_add_shopping_items", "arguments": map[string]any{"items": []any{map[string]any{"name": "milk"}}}, "result": map[string]any{"items": []any{map[string]any{"item": map[string]any{"id": "7d1f0c2e-1111-4a5b-8c9d-0e1f2a3b4c5d", "name": "milk"}, "merged": false}}}}}
	processedAttempt := api.attempt(milk, "op-milk.retry.2")
	api.finish(milk, processedAttempt, "processing", nil, "")
	api.finish(milk, processedAttempt, "processed", shopping, "")
	api.call(http.MethodPost, "/v1/inbox-items/"+milk+"/resolve", nil)
	failed := api.capture("Pay the thing", "watch", minutes(-10))
	api.finish(failed, api.attempt(failed, "op-failed"), "failed", []map[string]any{}, "gave up")
	review := api.capture("Meeting with Anna at 5", "web", minutes(-5))
	reviewAttempt := api.attempt(review, "op-review")
	api.call(http.MethodPost, "/v1/inbox-items/"+review+"/human-review", map[string]any{"attemptId": reviewAttempt, "proposedAction": map[string]any{"tool": "none", "arguments": map[string]any{}}, "candidates": []any{}, "errorSummary": "Which meeting?"})
	discarded := api.capture("Discarded", "web", minutes(-4))
	api.call(http.MethodPost, "/v1/inbox-items/"+discarded+"/discard", nil)
	api.capture("Still raw", "web", minutes(-3))
	processing := api.capture("Still processing", "web", minutes(-2))
	api.attempt(processing, "op-processing")
	// Resolved by hand: no finished attempt.
	byHand := api.capture("Resolved by hand", "manual", minutes(-1))
	api.call(http.MethodPost, "/v1/inbox-items/"+byHand+"/resolve", nil)
	followUp := api.capture("Actually oat milk", "web", at)
	api.attempt(followUp, "op-follow-up")
	later := api.capture("Captured later", "web", minutes(1))
	api.processed(later, []map[string]any{{"tool": "task_create"}})

	items := api.recent("before=" + followUp)
	if got, want := contents(items), []string{"Resolved by hand", "Meeting with Anna at 5", "Pay the thing", "Buy milk"}; !equalStrings(got, want) {
		t.Fatalf("recent captures = %q, want %q (newest first, settled only, within 20 minutes)", got, want)
	}
	if items[0]["outcome"] != nil || items[0]["state"] != "resolved" || items[0]["source"] != "manual" {
		t.Errorf("resolved by hand = %v, want no outcome", items[0])
	}
	reviewOutcome, _ := items[1]["outcome"].(map[string]any)
	if items[1]["state"] != "human_review" || reviewOutcome["status"] != "human_review" || reviewOutcome["errorSummary"] != "Which meeting?" || reviewOutcome["attemptId"] != reviewAttempt {
		t.Errorf("review = %v", items[1])
	}
	failedOutcome, _ := items[2]["outcome"].(map[string]any)
	if items[2]["state"] != "failed" || failedOutcome["status"] != "failed" || items[2]["source"] != "watch" {
		t.Errorf("failed = %v", items[2])
	}
	outcome, _ := items[3]["outcome"].(map[string]any)
	if items[3]["state"] != "resolved" || items[3]["source"] != "voice" || items[3]["correlationId"] != milk || outcome["status"] != "processed" || outcome["attemptId"] != processedAttempt || outcome["completedAt"] == nil {
		t.Fatalf("milk = %v", items[3])
	}
	actions, _ := outcome["actions"].([]any)
	if len(actions) != 1 || actions[0].(map[string]any)["tool"] != "food_add_shopping_items" || actions[0].(map[string]any)["result"] == nil {
		t.Fatalf("milk actions = %v, want the processed attempt's", outcome["actions"])
	}
	if created, err := time.Parse(time.RFC3339, items[3]["createdAt"].(string)); err != nil || !created.Equal(minutes(-15)) {
		t.Errorf("createdAt = %v (%v), want %v", items[3]["createdAt"], err, minutes(-15))
	}
	// Each names its images, none as an empty list (older captures stored
	// JSON null), so the processor can tell a capture sent again (P5-23).
	photo := "inbox-media:" + strings.Repeat("ab", 32)
	if _, err := api.pool.Exec(context.Background(), `UPDATE inbox_items SET media_references = jsonb_build_array($1::text) WHERE id = $2`, photo, review); err != nil {
		t.Fatal(err)
	}
	if _, err := api.pool.Exec(context.Background(), `UPDATE inbox_items SET media_references = 'null'::jsonb WHERE id = $1`, failed); err != nil {
		t.Fatal(err)
	}
	items = api.recent("before=" + followUp)
	for index, want := range [][]any{{}, {photo}, {}, {}} {
		if got, ok := items[index]["mediaReferences"].([]any); !ok || !reflect.DeepEqual(got, want) {
			t.Errorf("%s: mediaReferences = %#v, want %v", items[index]["content"], items[index]["mediaReferences"], want)
		}
	}

	// The latest captures only, and another window.
	if got := contents(api.recent("before=" + followUp + "&limit=2")); !equalStrings(got, []string{"Resolved by hand", "Meeting with Anna at 5"}) {
		t.Errorf("limit 2 = %q", got)
	}
	if got := contents(api.recent("before=" + followUp + "&minutes=30")); len(got) != 5 || got[4] != "Too early" {
		t.Errorf("30 minutes = %q, want the earlier capture too", got)
	}
	if got := contents(api.recent("before=" + followUp + "&minutes=6")); !equalStrings(got, []string{"Resolved by hand", "Meeting with Anna at 5"}) {
		t.Errorf("6 minutes = %q", got)
	}
	// The window ends at the capture's own creation, whatever its state.
	if got := contents(api.recent("before=" + milk)); !equalStrings(got, []string{"Too early"}) {
		t.Errorf("before the milk = %q, want only the capture 10 minutes before it", got)
	}

	for query, want := range map[string]int{
		"before=" + followUp + "&minutes=0":           http.StatusBadRequest,
		"before=" + followUp + "&minutes=121":         http.StatusBadRequest,
		"before=" + followUp + "&limit=0":             http.StatusBadRequest,
		"before=" + followUp + "&limit=51":            http.StatusBadRequest,
		"before=not-a-uuid":                           http.StatusBadRequest,
		"":                                            http.StatusBadRequest,
		"before=0f3c2a10-7d4e-4b8a-9c61-2e5f8a9b1c0d": http.StatusNotFound,
	} {
		if status, _ := api.request(http.MethodGet, "/v1/recent-captures?"+query, nil); status != want {
			t.Errorf("GET ?%s = %d, want %d", query, status, want)
		}
	}
}

// TestRecentCapturesNameTheUnfinishedCapturesBefore covers unfinished
// (P5-22): the same window's captures still on their way, oldest first,
// which the processor plans a capture after.
func TestRecentCapturesNameTheUnfinishedCapturesBefore(t *testing.T) {
	api := newRecentAPI(t)
	at := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	minutes := func(n int) time.Time { return at.Add(time.Duration(n) * time.Minute) }

	api.processed(api.capture("Too early", "web", minutes(-25)), nil)
	retrying := api.capture("Coffee with Lukas tomorrow at 5 pm", "voice", minutes(-8))
	retryAt := time.Now().Add(time.Minute).UTC().Truncate(time.Second)
	api.call(http.MethodPatch, "/v1/inbox-items/"+retrying+"/processing-attempts/"+api.attempt(retrying, "op-retrying"),
		map[string]any{"status": "retryable", "actions": []any{}, "errorSummary": "Codex timed out", "nextRetryAt": retryAt, "retryReason": "backoff"})
	api.processed(api.capture("Settled", "web", minutes(-6)), []map[string]any{{"tool": "task_create"}})
	discarded := api.capture("Discarded", "web", minutes(-5))
	api.call(http.MethodPost, "/v1/inbox-items/"+discarded+"/discard", nil)
	review := api.capture("Waiting for review", "web", minutes(-4))
	api.call(http.MethodPost, "/v1/inbox-items/"+review+"/human-review", map[string]any{"attemptId": api.attempt(review, "op-review"), "proposedAction": map[string]any{"tool": "none", "arguments": map[string]any{}}, "candidates": []any{}, "errorSummary": "Which one?"})
	fresh := api.capture("Still raw", "watch", minutes(-3))
	processing := api.capture("Still processing", "web", minutes(-2))
	api.attempt(processing, "op-processing")
	item := api.capture("Actually, make it 6", "web", at)
	api.attempt(item, "op-item")
	api.capture("Captured later", "web", minutes(1))

	answer := api.call(http.MethodGet, "/v1/recent-captures?before="+item, nil)
	if got := contents(itemsOf(answer["items"])); !equalStrings(got, []string{"Waiting for review", "Settled"}) {
		t.Fatalf("settled = %q", got)
	}
	unfinished := itemsOf(answer["unfinished"])
	if got, want := contents(unfinished), []string{"Coffee with Lukas tomorrow at 5 pm", "Still raw", "Still processing"}; !equalStrings(got, want) {
		t.Fatalf("unfinished = %q, want %q (oldest first, raw or processing, in the window, not the item)", got, want)
	}
	first := unfinished[0]
	if first["id"] != retrying || first["state"] != "raw" || first["source"] != "voice" || first["nextRetryAt"] == nil || first["createdAt"] == nil || first["updatedAt"] == nil {
		t.Errorf("waiting for a retry = %v", first)
	}
	if next, err := time.Parse(time.RFC3339, first["nextRetryAt"].(string)); err != nil || !next.Equal(retryAt) {
		t.Errorf("nextRetryAt = %v, want %v", first["nextRetryAt"], retryAt)
	}
	if unfinished[1]["id"] != fresh || unfinished[1]["state"] != "raw" || unfinished[1]["nextRetryAt"] != nil {
		t.Errorf("new capture = %v", unfinished[1])
	}
	if unfinished[2]["id"] != processing || unfinished[2]["state"] != "processing" {
		t.Errorf("processing = %v", unfinished[2])
	}
	// The same limit and window as the settled ones; none is an empty list.
	if got := contents(itemsOf(api.call(http.MethodGet, "/v1/recent-captures?before="+item+"&limit=1", nil)["unfinished"])); !equalStrings(got, []string{"Coffee with Lukas tomorrow at 5 pm"}) {
		t.Errorf("limit 1 = %q", got)
	}
	if got := contents(itemsOf(api.call(http.MethodGet, "/v1/recent-captures?before="+item+"&minutes=5", nil)["unfinished"])); !equalStrings(got, []string{"Still raw", "Still processing"}) {
		t.Errorf("5 minutes = %q", got)
	}
	if none, ok := api.call(http.MethodGet, "/v1/recent-captures?before="+retrying, nil)["unfinished"].([]any); !ok || len(none) != 0 {
		t.Errorf("before the first capture: unfinished = %v, want []", none)
	}
}

// TestInboxItemsListOldestFirst covers order=oldest, the processor's order.
func TestInboxItemsListOldestFirst(t *testing.T) {
	api := newRecentAPI(t)
	now := time.Now().UTC().Truncate(time.Second)
	api.capture("Second", "web", now.Add(-2*time.Minute))
	api.capture("First", "web", now.Add(-3*time.Minute))
	api.capture("Third", "web", now.Add(-time.Minute))
	list := func(query string) []string {
		return contents(itemsOf(api.call(http.MethodGet, "/v1/inbox-items?"+query, nil)["items"]))
	}
	if got := list("state=raw&due=true&order=oldest&limit=2"); !equalStrings(got, []string{"First", "Second"}) {
		t.Errorf("oldest first = %q", got)
	}
	for _, query := range []string{"state=raw&due=true&limit=2", "state=raw&order=newest&limit=2"} {
		if got := list(query); !equalStrings(got, []string{"Third", "Second"}) {
			t.Errorf("%s = %q, want the newest first", query, got)
		}
	}
	if status, _ := api.request(http.MethodGet, "/v1/inbox-items?order=sideways", nil); status != http.StatusBadRequest {
		t.Errorf("order=sideways = %d, want 400", status)
	}
}

func itemsOf(raw any) []map[string]any {
	list, _ := raw.([]any)
	items := make([]map[string]any, 0, len(list))
	for _, item := range list {
		items = append(items, item.(map[string]any))
	}
	return items
}

func TestProcessedFollowUpLinksTheEarlierCapture(t *testing.T) {
	api := newRecentAPI(t)
	now := time.Now().UTC()
	milk := api.capture("Buy milk", "web", now.Add(-2*time.Minute))
	api.processed(milk, []map[string]any{{"tool": "food_add_shopping_items"}})
	anna := api.capture("Meeting with Anna at 5", "voice", now.Add(-time.Minute))
	api.processed(anna, []map[string]any{{"tool": "calendar_create"}})
	followUp := api.capture("Actually oat milk, and make the meeting 6", "web", now)
	attemptID := api.processed(followUp, []map[string]any{
		{"tool": "food_update_shopping_item", "followsUp": map[string]any{"inboxItemId": milk, "entity": "shopping_item", "entityId": "7d1f0c2e-1111-4a5b-8c9d-0e1f2a3b4c5d"}},
		{"tool": "calendar_update", "followsUp": map[string]any{"inboxItemId": anna, "entity": "event", "entityId": "event-1"}},
		{"tool": "food_add_shopping_items", "followsUp": map[string]any{"inboxItemId": milk}},
		// Neither its own capture nor one Inbox does not have is linked.
		{"tool": "task_create", "followsUp": map[string]any{"inboxItemId": followUp}},
		{"tool": "task_create", "followsUp": map[string]any{"inboxItemId": "0f3c2a10-7d4e-4b8a-9c61-2e5f8a9b1c0d"}},
		{"tool": "task_create", "followsUp": "not an object"},
	})
	// The processor repeats the record it is unsure went through: the
	// events have the same ids, so Activity keeps one each.
	api.finish(followUp, attemptID, "processed", []map[string]any{{"tool": "food_update_shopping_item", "followsUp": map[string]any{"inboxItemId": milk}}}, "")

	events := api.events.ofType("inbox.item.followed_up")
	byID := map[string]sent{}
	for _, event := range events {
		byID[event.body["id"].(string)] = event
	}
	if len(byID) != 2 {
		t.Fatalf("followed_up events = %d distinct (%d sent), want one per earlier capture", len(byID), len(events))
	}
	completed := activity.EventID("inbox.processing.completed", attemptID).String()
	for _, event := range events[:2] {
		body := event.body
		subject, _ := body["subject"].(map[string]any)
		data, _ := body["data"].(map[string]any)
		if body["correlationId"] != followUp || body["causationId"] != completed || subject["type"] != "inbox_item" {
			t.Errorf("event = %v, want the follow-up's workflow, caused by its completed processing", body)
		}
		if data["inboxItemId"] != followUp || data["followedItemId"] != subject["id"] || data["followedCorrelationId"] != subject["id"] || data["attemptId"] != attemptID {
			t.Errorf("event data = %v", data)
		}
		actor, _ := body["actor"].(map[string]any)
		if actor["kind"] != "agent" || actor["name"] != "codex-inbox-processor" {
			t.Errorf("actor = %v", actor)
		}
	}
	first, _ := events[0].body["data"].(map[string]any)
	if first["followedItemId"] != milk || first["followedContent"] != "Buy milk" {
		t.Fatalf("first link = %v, want the milk capture", first)
	}
	changes, _ := first["changes"].([]any)
	if len(changes) != 2 || changes[0].(map[string]any)["entity"] != "shopping_item" || changes[0].(map[string]any)["tool"] != "food_update_shopping_item" {
		t.Errorf("milk changes = %v", changes)
	}
	if summary := events[0].body["summary"]; summary != "“Actually oat milk, and make the meeting 6” changed what “Buy milk” did" {
		t.Errorf("summary = %v", summary)
	}
	if second, _ := events[1].body["data"].(map[string]any); second["followedItemId"] != anna {
		t.Errorf("second link = %v, want the meeting capture", second)
	}
	// A retryable or failed attempt links nothing.
	other := api.capture("Something else", "web", now)
	api.finish(other, api.attempt(other, "op-other"), "retryable", []map[string]any{{"tool": "task_update", "followsUp": map[string]any{"inboxItemId": milk}}}, "later")
	if got := len(api.events.ofType("inbox.item.followed_up")); got != len(events) {
		t.Errorf("a retryable attempt recorded follow-ups: %d events, want %d", got, len(events))
	}
}

// TestProcessedRepeatLinksTheEarlierCapture covers duplicateOf (P5-23): a
// capture sent again, which the processor resolved without doing anything,
// names the capture it repeats.
func TestProcessedRepeatLinksTheEarlierCapture(t *testing.T) {
	api := newRecentAPI(t)
	now := time.Now().UTC()
	lemons := api.capture("Buy lemons.", "web", now.Add(-time.Minute))
	api.processed(lemons, []map[string]any{{"tool": "food_add_shopping_items"}})
	again := api.capture("Buy lemons.", "voice", now)
	attemptID := api.attempt(again, "op-again")
	api.finish(again, attemptID, "processing", nil, "")
	body := map[string]any{"status": "processed", "actions": []any{}, "errorSummary": "Same as the capture from 17:09, already done", "duplicateOf": lemons}
	attempt := api.call(http.MethodPatch, "/v1/inbox-items/"+again+"/processing-attempts/"+attemptID, body)
	if attempt["status"] != "processed" || attempt["errorSummary"] != "Same as the capture from 17:09, already done" {
		t.Fatalf("attempt = %v", attempt)
	}
	if actions, _ := attempt["actions"].([]any); len(actions) != 0 {
		t.Fatalf("actions = %v, want none", attempt["actions"])
	}
	// The processor repeats a record it is unsure went through: the event
	// has the same id, so Activity keeps one.
	api.call(http.MethodPatch, "/v1/inbox-items/"+again+"/processing-attempts/"+attemptID, body)
	api.call(http.MethodPost, "/v1/inbox-items/"+again+"/resolve", nil)

	events := api.events.ofType("inbox.item.repeated")
	if len(events) != 2 || events[0].body["id"] != events[1].body["id"] {
		t.Fatalf("repeated events = %v, want the same event twice", events)
	}
	event := events[0].body
	subject, _ := event["subject"].(map[string]any)
	data, _ := event["data"].(map[string]any)
	completed := activity.EventID("inbox.processing.completed", attemptID).String()
	if subject["type"] != "inbox_item" || subject["id"] != lemons || event["correlationId"] != again || event["causationId"] != completed || event["outcome"] != "succeeded" {
		t.Errorf("event = %v, want it about the earlier capture, in the repeat's workflow, caused by its completed processing", event)
	}
	want := map[string]any{"inboxItemId": again, "attemptId": attemptID, "content": "Buy lemons.", "repeatedItemId": lemons, "repeatedCorrelationId": lemons, "repeatedContent": "Buy lemons.", "repeatedState": "resolved"}
	if !reflect.DeepEqual(data, want) {
		t.Errorf("data = %v, want %v", data, want)
	}
	if event["summary"] != "“Buy lemons.” repeated “Buy lemons.”: nothing done again" {
		t.Errorf("summary = %v", event["summary"])
	}
	if actor, _ := event["actor"].(map[string]any); actor["kind"] != "agent" || actor["name"] != "codex-inbox-processor" {
		t.Errorf("actor = %v", event["actor"])
	}

	// Only a processed attempt names a repeat, never of the capture itself;
	// a capture Inbox does not have is not linked.
	other := api.capture("Something else", "web", now)
	otherAttempt := api.attempt(other, "op-other")
	for name, update := range map[string]map[string]any{
		"retryable": {"status": "retryable", "actions": []any{}, "duplicateOf": lemons},
		"review":    {"status": "human_review", "actions": []any{}, "duplicateOf": lemons},
		"itself":    {"status": "processed", "actions": []any{}, "duplicateOf": other},
		"not a id":  {"status": "processed", "actions": []any{}, "duplicateOf": "lemons"},
	} {
		if status, answer := api.request(http.MethodPatch, "/v1/inbox-items/"+other+"/processing-attempts/"+otherAttempt, update); status != http.StatusBadRequest {
			t.Errorf("%s: %d %v, want 400", name, status, answer)
		}
	}
	api.call(http.MethodPatch, "/v1/inbox-items/"+other+"/processing-attempts/"+otherAttempt, map[string]any{"status": "processed", "actions": []any{}, "duplicateOf": "0f3c2a10-7d4e-4b8a-9c61-2e5f8a9b1c0d"})
	if got := len(api.events.ofType("inbox.item.repeated")); got != 2 {
		t.Errorf("an unknown capture was linked: %d events", got)
	}
}

func TestMigration000010DownAndUp(t *testing.T) {
	api := newRecentAPI(t)
	ctx := context.Background()
	index := func() int {
		var n int
		if err := api.pool.QueryRow(ctx, "SELECT count(*) FROM pg_indexes WHERE indexname = 'inbox_items_created_idx' AND schemaname = current_schema()").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if index() != 1 {
		t.Fatal("the creation time index is missing")
	}
	for _, step := range []struct {
		name string
		want int
	}{{"000010_recent_captures.down.sql", 0}, {"000010_recent_captures.up.sql", 1}} {
		migration, err := os.ReadFile(filepath.Join("..", "..", "db", "migrations", step.name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := api.pool.Exec(ctx, string(migration)); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		if got := index(); got != step.want {
			t.Fatalf("after %s: %d indexes, want %d", step.name, got, step.want)
		}
	}
	before := api.capture("Before", "web", time.Now().Add(-time.Minute))
	api.processed(before, nil)
	if got := contents(api.recent("before=" + api.capture("After", "web", time.Now()))); !equalStrings(got, []string{"Before"}) {
		t.Fatalf("after down and up: %q", got)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
