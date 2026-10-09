package processor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// runtimeTools records the runtime and outcome calls for one item.
type runtimeTools struct {
	mu       sync.Mutex
	runtime  []map[string]any
	outcomes []map[string]any
	stale    string
	ctxErrs  []error
}

func (r *runtimeTools) Call(ctx context.Context, name string, args map[string]any) (json.RawMessage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch name {
	case "inbox_list":
		return json.RawMessage(`{"items":[{"id":"item-1","content":"Call insurer","source":"manual","sourceMetadata":{}}]}`), nil
	case "inbox_list_processing_attempts":
		return json.RawMessage(`{"items":[]}`), nil
	case "inbox_start_processing_attempt":
		return json.RawMessage(`{"id":"attempt-1"}`), nil
	case "inbox_record_attempt_runtime":
		r.runtime = append(r.runtime, args)
	case "inbox_record_processing_attempt":
		r.ctxErrs = append(r.ctxErrs, ctx.Err())
		if args["status"] != "processing" {
			r.outcomes = append(r.outcomes, args)
		}
	case "inbox_list_stale_attempts":
		return json.RawMessage(r.stale), nil
	}
	return json.RawMessage(`{}`), nil
}

func (r *runtimeTools) states() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	states := []string{}
	for _, call := range r.runtime {
		states = append(states, fmt.Sprint(call["runtimeState"]))
	}
	return states
}

// observingPlanner behaves like the Codex runtime: it reports the spawn and
// the session before answering.
type observingPlanner struct {
	output string
	err    error
	block  bool
}

func (p observingPlanner) Plan(ctx context.Context, _ string) (string, error) {
	if observer := observerFrom(ctx); observer != nil {
		observer.Spawned()
		observer.Session("thread-42")
	}
	if p.block {
		<-ctx.Done()
		return "", fmt.Errorf("Codex run cancelled: %w", ctx.Err())
	}
	return p.output, p.err
}

func TestRuntimeIsRecordedBeforeSpawnAndThroughTheRun(t *testing.T) {
	tools := &runtimeTools{}
	var events []RuntimeEvent
	runner := Runner{Planner: observingPlanner{output: createPlan}, Tools: tools, Owner: "processor-a", OnRuntime: func(event RuntimeEvent) { events = append(events, event) }}
	if _, err := runner.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(tools.states(), ","); got != "spawning,running,running,completed" {
		t.Fatalf("runtime states = %s", got)
	}
	first, last := tools.runtime[0], tools.runtime[len(tools.runtime)-1]
	if first["runtimeOwner"] != "processor-a" || first["sessionId"] != nil {
		t.Fatalf("spawning record = %#v", first)
	}
	if last["sessionId"] != "thread-42" || last["attemptId"] != "attempt-1" {
		t.Fatalf("completed record = %#v", last)
	}
	if len(events) != 4 || events[3].State != RuntimeCompleted || events[3].SessionID != "thread-42" {
		t.Fatalf("events = %+v", events)
	}
}

func TestShutdownDuringARunHandsTheCaptureBack(t *testing.T) {
	tools := &runtimeTools{}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	_, _ = (Runner{Planner: observingPlanner{block: true}, Tools: tools}).Run(ctx)
	states := tools.states()
	if states[len(states)-1] != RuntimeCancelled {
		t.Fatalf("runtime states = %v", states)
	}
	if len(tools.outcomes) != 1 || tools.outcomes[0]["status"] != "retryable" || tools.outcomes[0]["retryReason"] != reasonCancelled {
		t.Fatalf("outcomes = %#v", tools.outcomes)
	}
	// The outcome is stored even though the poll's context was cancelled.
	for _, err := range tools.ctxErrs {
		if err != nil {
			t.Fatalf("an outcome was recorded with a cancelled context: %v", err)
		}
	}
	at, _ := time.Parse(time.RFC3339Nano, tools.outcomes[0]["nextRetryAt"].(string))
	if time.Since(at) > time.Minute || time.Until(at) > time.Second {
		t.Fatalf("cancelled capture should be retried right away, got %s", at)
	}
}

func TestATimedOutRunBacksOff(t *testing.T) {
	tools := &runtimeTools{}
	planner := observingPlanner{err: fmt.Errorf("%w after 5m0s", ErrRunTimedOut)}
	if _, err := (Runner{Planner: planner, Tools: tools, Retry: fixedPolicy(nil)}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if states := tools.states(); states[len(states)-1] != RuntimeTimedOut {
		t.Fatalf("runtime states = %v", states)
	}
	if outcome := tools.outcomes[0]; outcome["retryReason"] != reasonBackoff || !strings.Contains(outcome["errorSummary"].(string), "timed out") {
		t.Fatalf("outcome = %#v", outcome)
	}
}

func TestReconcileReturnsAbandonedAttemptsToTheQueue(t *testing.T) {
	tools := &runtimeTools{stale: `{"items":[
		{"id":"attempt-running","inboxItemId":"item-1","status":"processing","runtimeState":"running","runtimeSessionId":"thread-9"},
		{"id":"attempt-pending","inboxItemId":"item-2","status":"pending"}]}`}
	recovered, err := (Runner{Tools: tools}).Reconcile(context.Background(), 2*time.Minute)
	if err != nil || recovered != 2 {
		t.Fatalf("recovered = %d, err = %v", recovered, err)
	}
	if len(tools.outcomes) != 2 {
		t.Fatalf("outcomes = %#v", tools.outcomes)
	}
	for _, outcome := range tools.outcomes {
		if outcome["status"] != "retryable" || outcome["retryReason"] != reasonRecovered {
			t.Fatalf("outcome = %#v", outcome)
		}
	}
	if summary := tools.outcomes[0]["errorSummary"].(string); !strings.Contains(summary, "runtime running") || !strings.Contains(summary, "thread-9") {
		t.Fatalf("summary = %q", summary)
	}
	if summary := tools.outcomes[1]["errorSummary"].(string); !strings.Contains(summary, "runtime untracked") {
		t.Fatalf("summary = %q", summary)
	}
}
