package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

var clock = time.Date(2026, 10, 9, 20, 0, 0, 0, time.UTC)

func fixedPolicy(recovery func(context.Context) (time.Time, bool)) RetryPolicy {
	return RetryPolicy{RecoveryAt: recovery, Now: func() time.Time { return clock }, Jitter: func() float64 { return 0.5 }}
}

func TestBackoffDoublesWithJitterAndIsCapped(t *testing.T) {
	policy := fixedPolicy(nil).withDefaults()
	for previous, want := range []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute} {
		if got := policy.backoff(previous); got != want {
			t.Fatalf("backoff(%d) = %s, want %s", previous, got, want)
		}
	}
	if got := policy.backoff(8000); got != time.Hour {
		t.Fatalf("backoff after 8000 attempts = %s, want the 1h cap", got)
	}
	policy.Jitter = func() float64 { return 0 }
	if got := policy.backoff(0); got != 24*time.Second {
		t.Fatalf("lowest jitter = %s", got)
	}
	policy.Jitter = func() float64 { return 0.999999 }
	if got := policy.backoff(0); got < 35*time.Second || got > 36*time.Second {
		t.Fatalf("highest jitter = %s", got)
	}
}

func TestUsageLimitWaitsForTheReportedResetTime(t *testing.T) {
	reset := clock.Add(3 * time.Hour)
	policy := fixedPolicy(func(context.Context) (time.Time, bool) { return reset, true })
	at, why := policy.next(context.Background(), failureCodex, 0, "planning failed: ERROR: You’ve hit your usage limit. try again at 11:00 PM.")
	if why != reasonUsageLimited || at.Before(reset) || at.After(reset.Add(time.Minute)) {
		t.Fatalf("next = %s (%s), want just after %s", at, why, reset)
	}
	// Without a reported reset, or for other failures, it backs off.
	unknown := fixedPolicy(func(context.Context) (time.Time, bool) { return time.Time{}, false })
	if at, why := unknown.next(context.Background(), failureCodex, 0, "ERROR: You’ve hit your usage limit."); why != reasonBackoff || at != clock.Add(30*time.Second) {
		t.Fatalf("unknown reset = %s (%s)", at, why)
	}
	past := fixedPolicy(func(context.Context) (time.Time, bool) { return clock.Add(-time.Minute), true })
	if _, why := past.next(context.Background(), failureCodex, 0, "usage limit"); why != reasonBackoff {
		t.Fatalf("a reset in the past was honored")
	}
	if _, why := policy.next(context.Background(), failureDependency, 0, "usage limit"); why != reasonBackoff {
		t.Fatalf("a dependency failure used the Codex reset")
	}
}

func TestUsageRecoveryUsesTheLatestExhaustedWindow(t *testing.T) {
	soon, later := clock.Add(time.Hour), clock.Add(48*time.Hour)
	usage := Usage{Limits: []UsageLimit{
		{ID: "codex", Reached: "rate_limit_reached", Windows: []UsageWindow{{UsedPercent: 100, ResetsAt: &soon}, {UsedPercent: 100, ResetsAt: &later}}},
		{ID: "base_model_inference", Windows: []UsageWindow{{UsedPercent: 24, ResetsAt: &soon}}},
	}}
	if at, ok := usage.UsageRecovery(); !ok || !at.Equal(later) {
		t.Fatalf("recovery = %s, %v", at, ok)
	}
	if _, ok := (Usage{Limits: []UsageLimit{{ID: "codex", Windows: []UsageWindow{{UsedPercent: 40, ResetsAt: &soon}}}}}).UsageRecovery(); ok {
		t.Fatal("an unexhausted limit produced a recovery time")
	}
}

// scheduleTools records what the runner asks Inbox to do with one item.
type scheduleTools struct {
	previous int
	action   error
	calls    []string
	records  []map[string]any
	query    string
}

func (s *scheduleTools) Call(_ context.Context, name string, args map[string]any) (json.RawMessage, error) {
	s.calls = append(s.calls, name)
	switch name {
	case "inbox_list":
		s.query, _ = args["query"].(string)
		return json.RawMessage(`{"items":[{"id":"item-1","content":"Call insurer","source":"manual","sourceMetadata":{}}]}`), nil
	case "inbox_list_processing_attempts":
		attempts := make([]string, 0, s.previous)
		for i := 0; i < s.previous; i++ {
			attempts = append(attempts, fmt.Sprintf(`{"idempotencyKey":%q,"status":"retryable"}`, idempotencyKey(inboxItem{ID: "item-1", Content: "Call insurer", Source: "manual", SourceMetadata: map[string]any{}})+fmt.Sprintf(".retry.%d", i+1)))
		}
		return json.RawMessage(`{"items":[` + strings.Join(attempts, ",") + `]}`), nil
	case "inbox_start_processing_attempt":
		return json.RawMessage(`{"id":"attempt-1"}`), nil
	case "inbox_record_processing_attempt":
		s.records = append(s.records, args)
		return json.RawMessage(`{}`), nil
	case "task_create":
		if s.action != nil {
			return nil, s.action
		}
		return json.RawMessage(`{"id":"task-1"}`), nil
	}
	return json.RawMessage(`{}`), nil
}

func (s *scheduleTools) last() map[string]any { return s.records[len(s.records)-1] }

const createPlan = `{"actions":[{"tool":"task_create","arguments":{"title":"Call insurer"}}],"ambiguous":false,"confidence":0.95}`

func TestRunnerPollsOnlyDueWork(t *testing.T) {
	tools := &scheduleTools{}
	_, _ = (Runner{Planner: fakePlanner{output: createPlan}, Tools: tools}).Run(context.Background())
	if !strings.Contains(tools.query, "due=true") || !strings.Contains(tools.query, "state=raw") {
		t.Fatalf("inbox_list query = %q", tools.query)
	}
}

func TestRunnerSchedulesEachKindOfFailure(t *testing.T) {
	reset := clock.Add(2 * time.Hour)
	policy := fixedPolicy(func(context.Context) (time.Time, bool) { return reset, true })
	cases := []struct {
		name     string
		planner  fakePlanner
		action   error
		previous int
		status   string
		reason   string
		after    time.Time
	}{
		{"usage limit waits for the reset", fakePlanner{err: errors.New("run Codex CLI: exit status 1: ERROR: You’ve hit your usage limit.")}, nil, 3, "retryable", reasonUsageLimited, reset},
		{"other Codex failures back off", fakePlanner{err: errors.New("run Codex CLI: exit status 1: ERROR: stream disconnected")}, nil, 2, "retryable", reasonBackoff, clock.Add(2 * time.Minute)},
		{"unavailable owning service backs off", fakePlanner{output: createPlan}, errors.New("call MCP tool task_create: dial tcp: connection refused"), 0, "retryable", reasonBackoff, clock.Add(30 * time.Second)},
		{"unusable model output backs off", fakePlanner{output: "not json"}, nil, 1, "retryable", reasonBackoff, clock.Add(time.Minute)},
		{"unusable model output eventually needs an operator", fakePlanner{output: "not json"}, nil, maxModelOutputAttempts - 1, "failed", "", time.Time{}},
		{"a rejected action is model output", fakePlanner{output: createPlan}, errors.New("MCP tool task_create rejected: dueAt must be RFC3339"), maxModelOutputAttempts - 1, "failed", "", time.Time{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tools := &scheduleTools{previous: tc.previous, action: tc.action}
			if _, err := (Runner{Planner: tc.planner, Tools: tools, Retry: policy}).Run(context.Background()); err != nil {
				t.Fatal(err)
			}
			last := tools.last()
			if last["status"] != tc.status {
				t.Fatalf("status = %v, records = %#v", last["status"], tools.records)
			}
			if tc.status == "failed" {
				if _, scheduled := last["nextRetryAt"]; scheduled {
					t.Fatalf("a failed item was scheduled: %#v", last)
				}
				return
			}
			at, err := time.Parse(time.RFC3339Nano, last["nextRetryAt"].(string))
			if err != nil || last["retryReason"] != tc.reason {
				t.Fatalf("schedule = %#v, err = %v", last, err)
			}
			if tc.reason == reasonUsageLimited {
				if at.Before(tc.after) || at.After(tc.after.Add(time.Minute)) {
					t.Fatalf("nextRetryAt = %s, want just after %s", at, tc.after)
				}
			} else if !at.Equal(tc.after) {
				t.Fatalf("nextRetryAt = %s, want %s", at, tc.after)
			}
		})
	}
}

func TestNothingToDoGoesToHumanReviewNotRetry(t *testing.T) {
	tools := &scheduleTools{}
	plan := `{"actions":[],"ambiguous":false,"confidence":0.95,"reason":"a quotation, not a request"}`
	if _, err := (Runner{Planner: fakePlanner{output: plan}, Tools: tools, Retry: fixedPolicy(nil)}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !contains(tools.calls, "inbox_set_human_review") {
		t.Fatalf("calls = %v", tools.calls)
	}
	for _, record := range tools.records {
		if record["status"] == "retryable" {
			t.Fatalf("nothing-to-do entered technical retry: %#v", tools.records)
		}
	}
}
