package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/GAMch1k/life-os/pkg/activity"
)

// The owner's categories in these tests.
const (
	eatingOutID = "c0000000-0000-4000-8000-000000000001"
	shoppingID  = "c0000000-0000-4000-8000-000000000002"
	incomeID    = "c0000000-0000-4000-8000-000000000003"
	transfersID = "c0000000-0000-4000-8000-000000000004"
	archivedID  = "c0000000-0000-4000-8000-000000000005"
)

// fakeTransaction is a made-up transaction Finance holds.
type fakeTransaction struct {
	ID           string
	Amount       int64
	Counterparty string
	Description  string
	// Category and By are what Finance holds; Reviewed is set once the AI
	// looked.
	Category, By string
	Reviewed     bool
}

// financeTools is a fake MCP for the AI categories job: Finance's
// candidates (what nobody categorized and the AI has not looked at), the
// owner's categories, and finance_set_ai_categories with Finance's guard
// (a transaction someone else categorized is skipped as chosen).
type financeTools struct {
	transactions []*fakeTransaction
	examples     string
	fail         string
	calls        []string
	limits       []any
	choices      [][]map[string]any
	actors       []activity.Actor
}

func newFinance(transactions ...*fakeTransaction) *financeTools {
	return &financeTools{transactions: transactions, examples: `[]`}
}

func (f *financeTools) Call(ctx context.Context, name string, args map[string]any) (json.RawMessage, error) {
	f.calls = append(f.calls, name)
	f.actors = append(f.actors, activity.TraceFrom(ctx).Actor)
	if name == f.fail {
		return nil, errors.New("MCP tool " + name + " error: unavailable")
	}
	switch name {
	case "finance_list_ai_category_candidates":
		f.limits = append(f.limits, args["limit"])
		limit := args["limit"].(int)
		items, total := []map[string]any{}, 0
		for _, item := range f.transactions {
			if item.Category != "" || item.By != "" || item.Reviewed {
				continue
			}
			total++
			if len(items) < limit {
				entry := map[string]any{"id": item.ID, "accountKind": "bank", "amount": map[string]any{"amount": item.Amount, "currency": "EUR"}, "bookingDate": "2026-10-11"}
				if item.Counterparty != "" {
					entry["counterparty"] = item.Counterparty
				}
				if item.Description != "" {
					entry["description"] = item.Description
				}
				items = append(items, entry)
			}
		}
		raw, _ := json.Marshal(map[string]any{"total": total, "items": items, "examples": json.RawMessage(f.examples)})
		return raw, nil
	case "finance_list_categories":
		return json.RawMessage(`{"items":[
			{"id":"` + eatingOutID + `","name":"Eating out","kind":"expense","archived":false},
			{"id":"` + shoppingID + `","name":"Shopping","kind":"expense","archived":false},
			{"id":"` + incomeID + `","name":"Income","kind":"income","archived":false},
			{"id":"` + transfersID + `","name":"Transfers","kind":"transfer","archived":false}]}`), nil
	case "finance_set_ai_categories":
		raw, _ := json.Marshal(args["choices"])
		var choices []map[string]any
		_ = json.Unmarshal(raw, &choices)
		f.choices = append(f.choices, choices)
		result := map[string]any{"categorized": 0, "unchanged": 0, "left": 0, "skipped": 0}
		results := []map[string]any{}
		for _, choice := range choices {
			var item *fakeTransaction
			for _, candidate := range f.transactions {
				if candidate.ID == choice["transactionId"] {
					item = candidate
				}
			}
			category, _ := choice["categoryId"].(string)
			switch {
			case item == nil:
				results = append(results, map[string]any{"outcome": "skipped", "reason": "not_found"})
				result["skipped"] = result["skipped"].(int) + 1
			case item.By != "" && item.By != "ai":
				results = append(results, map[string]any{"outcome": "skipped", "reason": "chosen"})
				result["skipped"] = result["skipped"].(int) + 1
			case category == "":
				item.Reviewed = true
				results = append(results, map[string]any{"outcome": "left"})
				result["left"] = result["left"].(int) + 1
			default:
				item.Category, item.By, item.Reviewed = category, "ai", true
				results = append(results, map[string]any{"outcome": "categorized"})
				result["categorized"] = result["categorized"].(int) + 1
			}
		}
		result["results"] = results
		raw, _ = json.Marshal(result)
		return raw, nil
	}
	return json.RawMessage(`{}`), nil
}

// fakeChooser answers with the given JSON (or answer, when set) and records
// what it was asked.
type fakeChooser struct {
	answer  string
	answers func(prompt string) string
	err     error
	prompts []string
	schemas []string
}

func (f *fakeChooser) Categories(_ context.Context, prompt, schema string) (string, error) {
	f.prompts, f.schemas = append(f.prompts, prompt), append(f.schemas, schema)
	if f.answers != nil {
		return f.answers(prompt), f.err
	}
	return f.answer, f.err
}

// pickAll answers every transaction of a prompt with one category.
func pickAll(category string, confidence float64) func(string) string {
	refs := regexp.MustCompile(`"ref":"(t\d+)"`)
	return func(prompt string) string {
		choices := []map[string]any{}
		for _, match := range refs.FindAllStringSubmatch(prompt, -1) {
			choices = append(choices, map[string]any{"ref": match[1], "categoryId": category, "confidence": confidence})
		}
		raw, _ := json.Marshal(map[string]any{"choices": choices})
		return string(raw)
	}
}

func newCategoriesJob(finance *financeTools, chooser *fakeChooser, now *time.Time) *CategoriesJob {
	return &CategoriesJob{Chooser: chooser, Tools: finance, Enabled: true, TimeZone: ownerZone, Now: func() time.Time { return *now },
		Retry: RetryPolicy{Now: func() time.Time { return *now }, Jitter: func() float64 { return 0.5 }}}
}

func spending(count int) []*fakeTransaction {
	items := make([]*fakeTransaction, 0, count)
	for index := range count {
		items = append(items, &fakeTransaction{ID: fmt.Sprintf("a0000000-0000-4000-8000-%012d", index+1), Amount: -1000 - int64(index), Counterparty: fmt.Sprintf("Restaurant %d", index+1)})
	}
	return items
}

func sentCategory(choices []map[string]any, id string) any {
	for _, choice := range choices {
		if choice["transactionId"] == id {
			return choice["categoryId"]
		}
	}
	return "missing"
}

func TestCategoriesGoInBatchesAndABacklogDrainsPaced(t *testing.T) {
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, ownerZone)
	finance := newFinance(spending(95)...)
	chooser := &fakeChooser{answers: pickAll(eatingOutID, 0.9)}
	job := newCategoriesJob(finance, chooser, &now)

	job.Tick(context.Background())
	if len(chooser.prompts) != 1 || finance.limits[0] != categoriesBatch || len(finance.choices[0]) != categoriesBatch {
		t.Fatalf("first batch: %d prompts, limits %v, %d choices", len(chooser.prompts), finance.limits, len(finance.choices[0]))
	}
	if !strings.Contains(chooser.prompts[0], `"ref":"t40"`) || strings.Contains(chooser.prompts[0], `"ref":"t41"`) || chooser.schemas[0] != categoriesSchema {
		t.Fatalf("prompt = %s", chooser.prompts[0])
	}
	status := job.Status()
	if *status.Waiting != 55 || status.LastRun.Categorized != 40 || status.LastRun.Outcome != "categorized" || status.BatchesToday != 1 ||
		!status.NextRunAt.Equal(now.Add(categoriesDrainGap)) {
		t.Fatalf("status = %+v run %+v", status, status.LastRun)
	}
	// Paced: nothing before the gap, the next batch after it.
	now = now.Add(time.Minute)
	job.Tick(context.Background())
	if len(chooser.prompts) != 1 {
		t.Fatal("a batch ran before the gap")
	}
	now = now.Add(time.Minute)
	job.Tick(context.Background())
	now = now.Add(categoriesDrainGap)
	job.Tick(context.Background())
	if len(chooser.prompts) != 3 || len(finance.choices[2]) != 15 || *job.Status().Waiting != 0 {
		t.Fatalf("after three batches: %d prompts, last %d choices, status %+v", len(chooser.prompts), len(finance.choices[2]), job.Status())
	}
	// Drained: the next check is a poll, and it finds nothing to send.
	if next := job.Status().NextRunAt; !next.Equal(now.Add(categoriesPoll)) {
		t.Fatalf("next = %v", next)
	}
	now = now.Add(categoriesPoll)
	job.Tick(context.Background())
	if len(chooser.prompts) != 3 || job.Status().LastCheckAt == nil || !job.Status().LastCheckAt.Equal(now.UTC()) {
		t.Fatalf("an empty check asked Codex: %d, %+v", len(chooser.prompts), job.Status())
	}
	for _, actor := range finance.actors {
		if actor != activity.Agent("codex-finance-categories") {
			t.Fatalf("a call by %+v", actor)
		}
	}
}

func TestIBANsAndCardNumbersNeverReachThePrompt(t *testing.T) {
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, ownerZone)
	finance := newFinance(
		&fakeTransaction{ID: "a1", Amount: -5000, Counterparty: "Max Mustermann", Description: "SEPA-Überweisung IBAN DE89 3704 0044 0532 0130 00 BIC COBADEFFXXX Miete"},
		&fakeTransaction{ID: "a2", Amount: -2350, Counterparty: "KNEIPE41 DARMSTADT", Description: "Kartenzahlung 4111 1111 1111 1111 girocard"},
		&fakeTransaction{ID: "a3", Amount: -1999, Counterparty: "IKEA", Description: "Karte 5168 41** **** 1234 Einkauf, Kundennr 123456789"},
		&fakeTransaction{ID: "a4", Amount: 12000, Counterparty: "Eva", Description: "Gutschrift von GB33BUKB20201555555555"},
	)
	finance.examples = `[{"counterparty":"Pizzeria DE44500105175407324931","categoryId":"` + eatingOutID + `"},{"counterparty":"Archived","categoryId":"` + archivedID + `"}]`
	chooser := &fakeChooser{answer: `{"choices":[]}`}
	job := newCategoriesJob(finance, chooser, &now)
	job.Tick(context.Background())
	if len(chooser.prompts) != 1 {
		t.Fatalf("prompts = %d", len(chooser.prompts))
	}
	prompt := chooser.prompts[0]
	for _, secret := range []string{"3704", "0532 0130", "4111", "1111 1111", "5168", "1234", "123456789", "BUKB2020", "20201555555555", "500105175407324931", "DE89", "GB33"} {
		if strings.Contains(prompt, secret) {
			t.Errorf("the prompt holds %q: %s", secret, prompt)
		}
	}
	for _, kept := range []string{"[IBAN]", "[card]", "KNEIPE41 DARMSTADT", "Miete", "girocard", "IKEA", "50.00 EUR", `"direction":"in"`, `"Pizzeria [IBAN]"`, `"category":"Eating out"`} {
		if !strings.Contains(prompt, kept) {
			t.Errorf("the prompt lacks %q: %s", kept, prompt)
		}
	}
	// An example in a category that is not the owner's any more is left out.
	if strings.Contains(prompt, "Archived") {
		t.Errorf("an example of an unknown category: %s", prompt)
	}
}

func TestRedactionKeepsWordsAndAmounts(t *testing.T) {
	for in, want := range map[string]string{
		"DE89370400440532013000":                   "[IBAN]",
		"IBAN: de89 3704 0044 0532 0130 00, danke": "IBAN: [IBAN], danke",
		"DE89 3704 0044 0532 0130 0000 REWE":       "[IBAN] REWE",
		"4111-1111-1111-1111":                      "[card]",
		"XXXX XXXX XXXX 1234 Kartenzahlung":        "[card] Kartenzahlung",
		"Mandat 20261012345 Ref 99":                "Mandat [number] Ref 99",
		"REWE Markt 1234 Darmstadt 12,50 EUR":      "REWE Markt 1234 Darmstadt 12,50 EUR",
		"Lastschrift AB12 Rechnung 2026-10":        "Lastschrift AB12 Rechnung 2026-10",
		"Kneipe41":                                 "Kneipe41",
	} {
		if got := redactFinanceText(in); got != want {
			t.Errorf("redactFinanceText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPicksMustFitTheMoneyAndBeSure(t *testing.T) {
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, ownerZone)
	out, in := int64(-1000), int64(2000)
	finance := newFinance(
		&fakeTransaction{ID: "t-out-income", Amount: out, Counterparty: "Kabul Restaurant"},
		&fakeTransaction{ID: "t-in-expense", Amount: in, Counterparty: "IKEA Erstattung"},
		&fakeTransaction{ID: "t-in-transfer", Amount: in, Counterparty: "Tagesgeld"},
		&fakeTransaction{ID: "t-out-expense", Amount: out, Counterparty: "Kneipe41"},
		&fakeTransaction{ID: "t-unsure", Amount: out, Counterparty: "Max Mustermann"},
		&fakeTransaction{ID: "t-null", Amount: out, Counterparty: "Hans"},
		&fakeTransaction{ID: "t-unknown", Amount: out, Counterparty: "Blume 2000"},
		&fakeTransaction{ID: "t-archived", Amount: out, Counterparty: "MHG Vaping"},
		&fakeTransaction{ID: "t-forgotten", Amount: out, Counterparty: "FIGMA"},
		&fakeTransaction{ID: "t-taken", Amount: out, Counterparty: "SMYTHS TOYS"},
	)
	chooser := &fakeChooser{answer: "```json\n" + `{"choices":[
		{"ref":"t1","categoryId":"` + incomeID + `","confidence":0.95},
		{"ref":"t2","categoryId":"` + shoppingID + `","confidence":0.9},
		{"ref":"t3","categoryId":"` + transfersID + `","confidence":0.9},
		{"ref":"t4","categoryId":"` + eatingOutID + `","confidence":0.92},
		{"ref":"t4","categoryId":"` + shoppingID + `","confidence":0.99},
		{"ref":"t5","categoryId":"` + eatingOutID + `","confidence":0.6},
		{"ref":"t6","categoryId":null,"confidence":0.2},
		{"ref":"t7","categoryId":"c0000000-0000-4000-8000-00000000ffff","confidence":0.9},
		{"ref":"t8","categoryId":"` + archivedID + `","confidence":0.9},
		{"ref":"t10","categoryId":"` + shoppingID + `","confidence":0.9},
		{"ref":"t99","categoryId":"` + shoppingID + `","confidence":0.9}
	]}` + "\n```"}
	job := newCategoriesJob(finance, chooser, &now)
	// The owner picks a category for SMYTHS TOYS while Codex thinks.
	chooser.answers = func(string) string {
		finance.transactions[9].Category, finance.transactions[9].By = eatingOutID, "manual"
		return chooser.answer
	}
	job.Tick(context.Background())
	if len(finance.choices) != 1 {
		t.Fatalf("choices sent = %d", len(finance.choices))
	}
	sent := finance.choices[0]
	want := map[string]any{"t-out-income": nil, "t-in-expense": nil, "t-in-transfer": transfersID, "t-out-expense": eatingOutID, "t-unsure": nil,
		"t-null": nil, "t-unknown": nil, "t-archived": nil, "t-forgotten": nil, "t-taken": shoppingID}
	for id, category := range want {
		if got := sentCategory(sent, id); got != category {
			t.Errorf("%s was sent %v, want %v", id, got, category)
		}
	}
	run := job.Status().LastRun
	if run.Examined != 10 || run.Categorized != 2 || run.Unsure != 3 || run.Rejected != 4 || run.ChosenMeanwhile != 1 || run.Skipped != 8 || run.Outcome != "categorized" {
		t.Fatalf("run = %+v", run)
	}
	// The owner's choice stays.
	if taken := finance.transactions[9]; taken.Category != eatingOutID || taken.By != "manual" {
		t.Fatalf("taken = %+v", taken)
	}
}

func TestEachTransactionIsSentOnce(t *testing.T) {
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, ownerZone)
	finance := newFinance(spending(3)...)
	chooser := &fakeChooser{answer: `{"choices":[{"ref":"t1","categoryId":"` + eatingOutID + `","confidence":0.9}]}`}
	job := newCategoriesJob(finance, chooser, &now)
	job.Tick(context.Background())
	// Picked or not, Finance knows the AI looked: the next checks send
	// nothing, also after a restart.
	for range 3 {
		now = now.Add(categoriesPoll)
		job.Tick(context.Background())
	}
	restarted := newCategoriesJob(finance, chooser, &now)
	restarted.Tick(context.Background())
	if len(chooser.prompts) != 1 || len(finance.choices) != 1 {
		t.Fatalf("%d prompts, %d choices", len(chooser.prompts), len(finance.choices))
	}
	if run := job.Status().LastRun; run.Categorized != 1 || run.Unsure != 2 {
		t.Fatalf("run = %+v", run)
	}
	// A run never overlaps another.
	job.running = true
	now = now.Add(categoriesPoll)
	job.Tick(context.Background())
	if len(finance.calls) != 3+3+1 {
		t.Fatalf("a running job checked again: %v", finance.calls)
	}
}

func TestSwitchedOffNothingRuns(t *testing.T) {
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, ownerZone)
	finance := newFinance(spending(5)...)
	chooser := &fakeChooser{answers: pickAll(eatingOutID, 0.9)}
	job := newCategoriesJob(finance, chooser, &now)
	job.Enabled = false
	var published []CategoriesStatus
	job.OnStatus = func(status CategoriesStatus) { published = append(published, status) }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	job.Loop(ctx)
	job.Tick(context.Background())
	if len(finance.calls) != 0 || len(chooser.prompts) != 0 {
		t.Fatalf("switched off, it called %v and asked %d times", finance.calls, len(chooser.prompts))
	}
	if len(published) != 1 || published[0].Enabled || published[0].NextRunAt != nil {
		t.Fatalf("published = %+v", published)
	}

	t.Setenv("LIFE_OS_AI_CATEGORIES", "")
	if cfg, err := LoadConfig(); err != nil || !cfg.CategoriesEnabled {
		t.Fatalf("default: %+v %v", cfg.CategoriesEnabled, err)
	}
	t.Setenv("LIFE_OS_AI_CATEGORIES", "off")
	if cfg, err := LoadConfig(); err != nil || cfg.CategoriesEnabled {
		t.Fatalf("off: %+v %v", cfg.CategoriesEnabled, err)
	}
	t.Setenv("LIFE_OS_AI_CATEGORIES", "sometimes")
	if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "LIFE_OS_AI_CATEGORIES") {
		t.Fatalf("an unknown switch: %v", err)
	}
}

func TestFailuresAreRetriedWithBackoff(t *testing.T) {
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, ownerZone)
	finance := newFinance(spending(2)...)
	chooser := &fakeChooser{err: errors.New("run Codex CLI: exit status 1: stream disconnected")}
	job := newCategoriesJob(finance, chooser, &now)
	job.Tick(context.Background())
	status := job.Status()
	if status.LastRun.Outcome != "failed" || status.LastRun.Skipped != 2 || !status.NextRunAt.Equal(now.Add(30*time.Second)) || len(finance.choices) != 0 {
		t.Fatalf("after a Codex failure: %+v run %+v", status, status.LastRun)
	}
	now = now.Add(30 * time.Second)
	job.Tick(context.Background())
	if next := job.Status().NextRunAt; !next.Equal(now.Add(time.Minute)) {
		t.Fatalf("second backoff: next %v", next)
	}
	// Finance unreachable: nothing goes to Codex.
	finance.fail = "finance_list_ai_category_candidates"
	now = now.Add(time.Minute)
	job.Tick(context.Background())
	if len(chooser.prompts) != 2 || job.Status().LastRun.Outcome != "failed" {
		t.Fatalf("with Finance down: %d prompts", len(chooser.prompts))
	}

	// Unusable answers are retried, and after five in a row the batch is
	// left without categories so the backlog moves on.
	finance.fail = ""
	chooser.err, chooser.answer = nil, "I think these are restaurants."
	for attempt := 1; attempt < maxModelOutputAttempts; attempt++ {
		now = now.Add(2 * time.Hour)
		job.Tick(context.Background())
		if len(finance.choices) != 0 {
			t.Fatalf("attempt %d sent choices", attempt)
		}
	}
	now = now.Add(2 * time.Hour)
	job.Tick(context.Background())
	if len(finance.choices) != 1 || sentCategory(finance.choices[0], spending(1)[0].ID) != nil || job.Status().LastRun.Unsure != 2 {
		t.Fatalf("given up: %v %+v", finance.choices, job.Status().LastRun)
	}
	if next := job.Status().NextRunAt; !next.Equal(now.Add(categoriesPoll)) {
		t.Fatalf("after giving up: next %v", next)
	}
}

func TestTheDailyBatchesAreBounded(t *testing.T) {
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, ownerZone)
	finance := newFinance(spending(categoriesBatch * (categoriesPerDay + 2))...)
	chooser := &fakeChooser{answers: pickAll(shoppingID, 0.8)}
	job := newCategoriesJob(finance, chooser, &now)
	for range categoriesPerDay + 1 {
		job.Tick(context.Background())
		now = now.Add(categoriesDrainGap)
	}
	status := job.Status()
	if len(chooser.prompts) != categoriesPerDay || status.BatchesToday != categoriesPerDay || *status.Waiting != 2*categoriesBatch ||
		!status.NextRunAt.Equal(time.Date(2026, 10, 13, 0, 0, 0, 0, ownerZone)) {
		t.Fatalf("%d batches, status %+v", len(chooser.prompts), status)
	}
	now = time.Date(2026, 10, 13, 0, 0, 30, 0, ownerZone)
	job.Tick(context.Background())
	if len(chooser.prompts) != categoriesPerDay+1 || job.Status().BatchesToday != 1 {
		t.Fatalf("the next day: %d batches", len(chooser.prompts))
	}
}

func TestTheMonitorShowsTheCategoriesJob(t *testing.T) {
	monitor := NewMonitor("gpt-5.6-luna")
	chooser := monitor.MonitoredCategories(&fakeChooser{err: errors.New("ERROR: You've hit your usage limit")})
	if _, err := chooser.Categories(context.Background(), "prompt", categoriesSchema); err == nil {
		t.Fatal("the failure was lost")
	}
	waiting := 12
	monitor.SetCategories(CategoriesStatus{Enabled: true, Waiting: &waiting})
	status := monitor.Snapshot()
	if status.Codex.State != CodexUsageLimited || status.Categories == nil || *status.Categories.Waiting != 12 {
		t.Fatalf("status = %+v", status)
	}
}
