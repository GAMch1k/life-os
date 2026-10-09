package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

type fakePlanner struct {
	output string
	err    error
}

func (p fakePlanner) Plan(context.Context, string) (string, error) { return p.output, p.err }

type fakeTools struct {
	calls   []string
	fail    string
	records []map[string]any
	starts  []map[string]any
}

func (f *fakeTools) Call(_ context.Context, name string, args map[string]any) (json.RawMessage, error) {
	f.calls = append(f.calls, name)
	if name == f.fail {
		return nil, errors.New("unavailable")
	}
	switch name {
	case "inbox_list":
		return json.RawMessage(`{"items":[{"id":"item-1","content":"Call insurer","source":"manual","sourceMetadata":{}}]}`), nil
	case "inbox_list_processing_attempts":
		return json.RawMessage(`{"items":[]}`), nil
	case "inbox_start_processing_attempt":
		f.starts = append(f.starts, args)
		return json.RawMessage(`{"id":"attempt-1"}`), nil
	case "inbox_record_processing_attempt":
		f.records = append(f.records, args)
		return json.RawMessage(`{}`), nil
	case "inbox_set_human_review":
		f.records = append(f.records, map[string]any{"status": "human_review", "actions": []map[string]any{args["proposedAction"].(plannedAction).Arguments}})
		return json.RawMessage(`{}`), nil
	case "task_search":
		return json.RawMessage(`{"items":[{"id":"task-1","title":"Call insurer"}]}`), nil
	default:
		return json.RawMessage(`{}`), nil
	}
}
func TestRunnerResolvesExactlyOneTaskBeforeMutation(t *testing.T) {
	tools := &fakeTools{}
	_, err := (Runner{Planner: fakePlanner{output: `{"actions":[{"tool":"task_complete","arguments":{"taskQuery":"Call insurer"}}],"confidence":0.9}`}, Tools: tools, Model: "gpt-5.6-terra"}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !contains(tools.calls, "task_search") || !contains(tools.calls, "task_complete") {
		t.Fatalf("calls = %#v", tools.calls)
	}
	if got := tools.records[1]["actions"].([]map[string]any)[0]["arguments"].(map[string]any)["taskId"]; got != "task-1" {
		t.Fatalf("resolved task ID = %#v", got)
	}
}

func TestRunnerDoesNotMutateAmbiguousTaskSelection(t *testing.T) {
	tools := ambiguousTaskTools{fakeTools: &fakeTools{}}
	_, err := (Runner{Planner: fakePlanner{output: `{"actions":[{"tool":"task_complete","arguments":{"taskQuery":"Call insurer"}}],"confidence":0.9}`}, Tools: &tools, Model: "gpt-5.6-terra"}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if contains(tools.calls, "task_complete") || contains(tools.calls, "inbox_resolve") {
		t.Fatalf("ambiguous Task was mutated: %#v", tools.calls)
	}
	if tools.records[1]["status"] != "human_review" {
		t.Fatalf("status = %#v", tools.records[1]["status"])
	}
}

type ambiguousTaskTools struct{ *fakeTools }

func (t *ambiguousTaskTools) Call(ctx context.Context, name string, args map[string]any) (json.RawMessage, error) {
	if name == "task_search" {
		t.calls = append(t.calls, name)
		return json.RawMessage(`{"items":[{"id":"task-1"},{"id":"task-2"}]}`), nil
	}
	return t.fakeTools.Call(ctx, name, args)
}
func TestRunnerExecutesApprovedActionsThenResolves(t *testing.T) {
	tools := &fakeTools{}
	_, err := (Runner{Planner: fakePlanner{output: `{"actions":[{"tool":"task_create","arguments":{"title":"Call insurer"}}],"ambiguous":false,"confidence":0.9}`}, Tools: tools, Model: "gpt-5.6-terra"}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"inbox_list", "inbox_list_processing_attempts", "inbox_start_processing_attempt", "inbox_record_processing_attempt", "task_create", "inbox_record_processing_attempt", "inbox_resolve"}
	if !equal(tools.calls, want) {
		t.Fatalf("calls = %#v, want %#v", tools.calls, want)
	}
	if tools.records[1]["status"] != "processed" {
		t.Fatalf("status = %#v", tools.records[1]["status"])
	}
	initialActions, ok := tools.records[0]["actions"].([]map[string]any)
	if !ok || len(initialActions) != 0 {
		t.Fatalf("initial processing audit must send an empty actions array, got %#v", tools.records[0]["actions"])
	}
}
func TestRunnerLeavesFailedActionRecoverable(t *testing.T) {
	tools := &fakeTools{fail: "task_create"}
	_, err := (Runner{Planner: fakePlanner{output: `{"actions":[{"tool":"task_create","arguments":{"title":"Call insurer"}}],"confidence":0.9}`}, Tools: tools, Model: "gpt-5.6-terra"}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if contains(tools.calls, "inbox_resolve") {
		t.Fatalf("failed item was resolved")
	}
	if tools.records[1]["status"] != "retryable" {
		t.Fatalf("status = %#v", tools.records[1]["status"])
	}
}
func TestRunnerDryRunReturnsProposalsWithoutMutations(t *testing.T) {
	tools := &fakeTools{}
	proposals, err := (Runner{Planner: fakePlanner{output: `{"actions":[{"tool":"task_create","arguments":{"title":"Call insurer"}}],"confidence":0.9}`}, Tools: tools, Model: "gpt-5.6-terra", DryRun: true}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(proposals) != 1 || proposals[0].Actions[0].Tool != "task_create" {
		t.Fatalf("proposals = %#v", proposals)
	}
	if !equal(tools.calls, []string{"inbox_list"}) {
		t.Fatalf("dry-run mutated via %#v", tools.calls)
	}
}
func TestRunnerSkipsPreviouslyProcessedIdempotencyKey(t *testing.T) {
	tools := &fakeTools{}
	duplicate := duplicateTools{fakeTools: tools, key: idempotencyKey(inboxItem{ID: "item-1", Content: "Call insurer"})}
	proposals, err := (Runner{Planner: fakePlanner{output: `{"actions":[{"tool":"task_create","arguments":{"title":"Call insurer"}}],"confidence":0.9}`}, Tools: &duplicate, Model: "gpt-5.6-terra"}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if proposals[0].Reason != "already processed" || contains(tools.calls, "task_create") {
		t.Fatalf("calls=%#v proposals=%#v", tools.calls, proposals)
	}
}

func TestRunnerCreatesDistinctAttemptAfterRetryableFailure(t *testing.T) {
	tools := &fakeTools{}
	key := idempotencyKey(inboxItem{ID: "item-1", Content: "Call insurer"})
	retrying := retryingTools{fakeTools: tools, key: key}
	_, err := (Runner{Planner: fakePlanner{output: `{"actions":[{"tool":"task_create","arguments":{"title":"Call insurer"}}],"confidence":0.9}`}, Tools: &retrying, Model: "gpt-5.6-terra"}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := tools.starts[0]["idempotencyKey"]; got != key+".retry.2" {
		t.Fatalf("retry attempt key = %#v", got)
	}
}

type duplicateTools struct {
	*fakeTools
	key string
}

type retryingTools struct {
	*fakeTools
	key string
}

func (d *retryingTools) Call(ctx context.Context, name string, args map[string]any) (json.RawMessage, error) {
	if name == "inbox_list_processing_attempts" {
		d.calls = append(d.calls, name)
		return json.RawMessage(fmt.Sprintf(`{"items":[{"idempotencyKey":%q,"status":"retryable"}]}`, d.key)), nil
	}
	return d.fakeTools.Call(ctx, name, args)
}

func (d *duplicateTools) Call(ctx context.Context, name string, args map[string]any) (json.RawMessage, error) {
	if name == "inbox_list_processing_attempts" {
		d.calls = append(d.calls, name)
		return json.RawMessage(fmt.Sprintf(`{"items":[{"idempotencyKey":%q,"status":"processed"}]}`, d.key)), nil
	}
	return d.fakeTools.Call(ctx, name, args)
}
func equal(a, b []string) bool {
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
func contains(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}
