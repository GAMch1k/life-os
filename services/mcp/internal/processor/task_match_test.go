package processor

import (
	"context"
	"encoding/json"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

// searchTools answers task_search per term, the way the Tasks API matches a
// substring, and records each search and the Human Review it leads to.
type searchTools struct {
	*fakeTools
	// matches maps a search term to the ids of the Tasks it finds.
	matches map[string][]string
	// searches are the task_search queries, decoded.
	searches   []url.Values
	review     map[string]any
	toolInputs map[string]map[string]any
}

func (s *searchTools) Call(ctx context.Context, name string, args map[string]any) (json.RawMessage, error) {
	switch name {
	case "task_search":
		s.calls = append(s.calls, name)
		query, _ := url.ParseQuery(args["query"].(string))
		s.searches = append(s.searches, query)
		items := []map[string]any{}
		for _, id := range s.matches[query.Get("query")] {
			items = append(items, map[string]any{"id": id, "title": "Task " + id})
		}
		raw, _ := json.Marshal(map[string]any{"items": items})
		return raw, nil
	case "inbox_set_human_review":
		s.review = args
	case "task_complete", "task_update", "task_reopen":
		if s.toolInputs == nil {
			s.toolInputs = map[string]map[string]any{}
		}
		s.toolInputs[name] = args
	}
	return s.fakeTools.Call(ctx, name, args)
}

func (s *searchTools) terms() []string {
	terms := make([]string, 0, len(s.searches))
	for _, search := range s.searches {
		terms = append(terms, search.Get("query"))
	}
	return terms
}

func (s *searchTools) candidateIDs() []string {
	ids := []string{}
	for _, candidate := range s.review["candidates"].([]map[string]any) {
		ids = append(ids, candidate["id"].(string))
	}
	return ids
}

func runPlan(t *testing.T, tools *searchTools, plan string) {
	t.Helper()
	if _, err := (Runner{Planner: fakePlanner{output: plan}, Tools: tools, Model: "test"}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
}

const chickenPlan = `{"actions":[{"tool":"task_complete","arguments":{"taskQuery":"chicken","taskQueryAlternatives":["куриц","Chicken"," Hähnchen ","Huhn"]}}],"confidence":0.95}`

func TestTaskSearchFindsTheTaskInAnotherLanguageThroughAnAlternative(t *testing.T) {
	// "I already bought the chicken" and the only Task is "Купить курицу на
	// ужин": the English term finds nothing, the Russian stem finds it, and
	// the German terms find it again.
	tools := &searchTools{fakeTools: &fakeTools{}, matches: map[string][]string{"куриц": {"task-ru"}, "Hähnchen": {"task-ru"}}}
	runPlan(t, tools, chickenPlan)

	// "Chicken" repeats "chicken"; every term searches open Tasks only.
	if got, want := tools.terms(), []string{"chicken", "куриц", "Hähnchen", "Huhn"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("searched %q, want %q", got, want)
	}
	for _, search := range tools.searches {
		if search.Get("completed") != "false" || search.Get("limit") != "10" {
			t.Fatalf("search %v must ask for open Tasks, at most 10", search)
		}
	}
	input := tools.toolInputs["task_complete"]
	if input["taskId"] != "task-ru" {
		t.Fatalf("task_complete input = %#v, calls = %#v", input, tools.calls)
	}
	if _, ok := input["taskQuery"]; ok {
		t.Fatalf("search terms reached the Tasks operation: %#v", input)
	}
	if _, ok := input["taskQueryAlternatives"]; ok {
		t.Fatalf("search terms reached the Tasks operation: %#v", input)
	}
	if !contains(tools.calls, "inbox_resolve") || tools.review != nil {
		t.Fatalf("calls = %#v, review = %#v", tools.calls, tools.review)
	}
}

func TestTaskSearchSendsSeveralMatchesAcrossLanguagesToHumanReview(t *testing.T) {
	// "Buy chicken wings" and "Купить курицу для супа" are both open: each
	// term alone matches one Task, but together they are two.
	tools := &searchTools{fakeTools: &fakeTools{}, matches: map[string][]string{"chicken": {"task-en"}, "куриц": {"task-ru"}, "Huhn": {"task-en"}}}
	runPlan(t, tools, chickenPlan)

	if contains(tools.calls, "task_complete") || contains(tools.calls, "inbox_resolve") {
		t.Fatalf("an ambiguous Task was mutated: %#v", tools.calls)
	}
	if tools.review == nil || tools.review["errorSummary"] != "ambiguous or missing Task selection" {
		t.Fatalf("review = %#v", tools.review)
	}
	if got, want := tools.candidateIDs(), []string{"task-en", "task-ru"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %q, want %q", got, want)
	}
	// The proposed action keeps the terms, so the review shows what was searched.
	if proposed := tools.review["proposedAction"].(plannedAction); proposed.Arguments["taskQueryAlternatives"] == nil {
		t.Fatalf("proposed action = %#v", proposed)
	}
}

func TestTaskSearchWithNoMatchGoesToHumanReviewWithoutCandidates(t *testing.T) {
	tools := &searchTools{fakeTools: &fakeTools{}, matches: map[string][]string{}}
	runPlan(t, tools, chickenPlan)

	if contains(tools.calls, "task_complete") || contains(tools.calls, "task_create") || contains(tools.calls, "inbox_resolve") {
		t.Fatalf("calls = %#v", tools.calls)
	}
	if tools.review == nil || len(tools.candidateIDs()) != 0 {
		t.Fatalf("review = %#v", tools.review)
	}
}

func TestTaskSearchWithOnlyATaskQueryStillWorks(t *testing.T) {
	// Plans from before alternatives existed, and literal titles.
	tools := &searchTools{fakeTools: &fakeTools{}, matches: map[string][]string{"electrician": {"task-1"}}}
	runPlan(t, tools, `{"actions":[{"tool":"task_update","arguments":{"taskQuery":"electrician","dueAt":"2026-10-20T09:00:00+02:00"}}],"confidence":0.9}`)

	if got := tools.terms(); !reflect.DeepEqual(got, []string{"electrician"}) {
		t.Fatalf("searched %q", got)
	}
	if input := tools.toolInputs["task_update"]; input["taskId"] != "task-1" || input["dueAt"] != "2026-10-20T09:00:00+02:00" {
		t.Fatalf("task_update input = %#v", input)
	}
}

func TestTaskSearchForReopenLooksAtCompletedTasks(t *testing.T) {
	tools := &searchTools{fakeTools: &fakeTools{}, matches: map[string][]string{"мусор": {"task-1"}}}
	runPlan(t, tools, `{"actions":[{"tool":"task_reopen","arguments":{"taskQuery":"trash","taskQueryAlternatives":["мусор"]}}],"confidence":0.9}`)

	for _, search := range tools.searches {
		if search.Get("completed") != "true" {
			t.Fatalf("reopen searched %v", search)
		}
	}
	if tools.toolInputs["task_reopen"]["taskId"] != "task-1" {
		t.Fatalf("calls = %#v", tools.calls)
	}
}

func TestLowConfidenceTaskMutationOffersEveryMatchingTask(t *testing.T) {
	tools := &searchTools{fakeTools: &fakeTools{}, matches: map[string][]string{"chicken": {"task-en"}, "Hähnchen": {"task-de"}}}
	runPlan(t, tools, `{"actions":[{"tool":"task_complete","arguments":{"taskQuery":"chicken","taskQueryAlternatives":["Hähnchen"]}}],"confidence":0.6}`)

	if contains(tools.calls, "task_complete") {
		t.Fatalf("calls = %#v", tools.calls)
	}
	if got, want := tools.candidateIDs(), []string{"task-en", "task-de"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %q, want %q", got, want)
	}
}

func TestApprovedReviewSelectionDropsTheSearchTerms(t *testing.T) {
	// Inbox removes taskQuery from the approved action but keeps the rest.
	tools := &searchTools{fakeTools: &fakeTools{}, matches: map[string][]string{}}
	item := inboxItem{ID: "item-1", Content: "I already bought the chicken.", ProcessingMetadata: map[string]any{"approvedAction": map[string]any{"tool": "task_complete", "arguments": map[string]any{"taskId": "task-2", "taskQueryAlternatives": []any{"куриц"}}}}}
	if _, err := (Runner{Planner: fakePlanner{err: context.Canceled}, Tools: tools, Model: "test"}).process(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	if len(tools.searches) != 0 {
		t.Fatalf("an approved Task was searched again: %v", tools.searches)
	}
	if got := tools.toolInputs["task_complete"]; !reflect.DeepEqual(got, map[string]any{"taskId": "task-2"}) {
		t.Fatalf("task_complete input = %#v", got)
	}
}

func TestTaskQueriesTrimDeduplicateAndBoundTheTerms(t *testing.T) {
	got := taskQueries(map[string]any{
		"taskQuery": " Milk ",
		"taskQueryAlternatives": []any{
			"milk", "", "  ", "мо", // a repeat, blanks, and a fragment too short to mean anything
			"молок", "Milch", "молоко", "lait", "latte", "leche", "mleko",
		},
	})
	want := []string{"Milk", "молок", "Milch", "молоко", "lait", "latte", "leche"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("taskQueries = %q, want %q", got, want)
	}
	// The taskQuery itself has no minimum length, as before.
	if got := taskQueries(map[string]any{"taskQuery": "TV"}); !reflect.DeepEqual(got, []string{"TV"}) {
		t.Fatalf("taskQueries = %q", got)
	}
}

func TestValidActionChecksTaskQueryAlternatives(t *testing.T) {
	for _, tc := range []struct {
		alternatives any
		valid        bool
	}{
		{[]any{"куриц", "Hähnchen"}, true},
		{[]any{}, true},
		{nil, true},
		{"куриц", false},
		{[]any{"куриц", 3.0}, false},
	} {
		action := plannedAction{Tool: "task_complete", Arguments: map[string]any{"taskQuery": "chicken", "taskQueryAlternatives": tc.alternatives}}
		if got := validAction(action); got != tc.valid {
			t.Errorf("validAction(alternatives %#v) = %v, want %v", tc.alternatives, got, tc.valid)
		}
	}
	// Alternatives belong to existing Task mutations only.
	if validAction(plannedAction{Tool: "task_create", Arguments: map[string]any{"title": "Buy milk", "taskQueryAlternatives": []any{"Milch"}}}) {
		t.Error("task_create accepted taskQueryAlternatives")
	}
}

func TestPlanPromptAsksForSearchTermsInTheOwnersLanguages(t *testing.T) {
	for _, want := range []string{
		"taskQueryAlternatives",
		"English, Russian, and German",
		"translations into the other two languages and word stems",
		`taskQuery "chicken", taskQueryAlternatives ["куриц", "Hähnchen", "Huhn"]`,
		"proceeds only when exactly one Task matches in total",
		"task_complete/task_reopen may only use taskQuery, taskQueryAlternatives.",
	} {
		if !strings.Contains(planPrompt, want) {
			t.Errorf("planPrompt lacks %q", want)
		}
	}
}
