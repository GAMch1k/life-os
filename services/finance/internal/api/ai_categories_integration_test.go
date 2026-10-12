package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/finance/internal/api/generated"
)

const aiActor = "agent:codex-finance-categories"

func (c apiClient) candidates(query string) generated.AiCategoryCandidates {
	c.t.Helper()
	return expect[generated.AiCategoryCandidates](c, http.MethodGet, "/v1/categories/ai/candidates?"+query, nil, http.StatusOK)
}

// choose sends the AI's choices: pairs of a transaction and a category (nil
// for none).
func (c apiClient) choose(choices ...any) generated.AiCategoryChoicesResult {
	c.t.Helper()
	body := []map[string]any{}
	for index := 0; index+1 < len(choices); index += 2 {
		body = append(body, map[string]any{"transactionId": choices[index], "categoryId": choices[index+1]})
	}
	return expect[generated.AiCategoryChoicesResult](c, http.MethodPost, "/v1/categories/ai/choices", map[string]any{"choices": body}, http.StatusOK, activity.ActorHeader, aiActor)
}

func (c apiClient) transaction(id string) generated.Transaction {
	c.t.Helper()
	return expect[generated.Transaction](c, http.MethodGet, "/v1/transactions/"+id, nil, http.StatusOK)
}

func outcomes(result generated.AiCategoryChoicesResult) []string {
	out := make([]string, 0, len(result.Results))
	for _, item := range result.Results {
		value := string(item.Outcome)
		if item.Reason != nil {
			value += ":" + string(*item.Reason)
		}
		out = append(out, value)
	}
	return out
}

func TestTheAIFillsTheGapsTheRulesLeave(t *testing.T) {
	c := newAPIClient(t)
	giro := c.account("Girokonto", "bank", "EUR", 0)
	eatingOut := c.category("Eating out", "expense")
	shopping := c.category("Shopping", "expense")
	groceries := c.category("Groceries", "expense")
	income := c.category("Income", "income")
	c.rule(groceries.Id.String(), "counterparty", "contains", "REWE")
	in := func(body map[string]any) generated.Transaction {
		body["accountId"] = giro.Id
		return c.spend(body)
	}
	kneipe := in(map[string]any{"amount": eur(-2350), "counterparty": "Kneipe41", "bookingDate": "2026-10-05"})
	ionos := in(map[string]any{"amount": eur(-1000), "counterparty": "IONOS SE", "description": "Webhosting", "bookingDate": "2026-10-04"})
	salary := in(map[string]any{"amount": eur(250000), "counterparty": "ACME GmbH", "description": "Gehalt", "bookingDate": "2026-10-01"})
	rewe := in(map[string]any{"amount": eur(-1234), "counterparty": "REWE Markt", "bookingDate": "2026-10-06"})
	// Not candidates: pending, cleared by hand, or chosen by hand (which is
	// an example of the owner's taste).
	in(map[string]any{"amount": eur(-500), "counterparty": "Bäcker", "status": "pending", "bookingDate": "2026-10-07"})
	kiosk := in(map[string]any{"amount": eur(-700), "counterparty": "Kiosk", "categoryId": eatingOut.Id, "bookingDate": "2026-10-02"})
	expect[generated.Transaction](c, http.MethodPatch, "/v1/transactions/"+kiosk.Id.String(), map[string]any{"categoryId": nil}, http.StatusOK)
	in(map[string]any{"amount": eur(-1500), "counterparty": "Pizzeria Roma", "categoryId": eatingOut.Id, "bookingDate": "2026-10-03"})
	// Nor transactions of an archived account.
	old := c.account("Altes Konto", "bank", "EUR", 0)
	c.spend(map[string]any{"accountId": old.Id, "amount": eur(-900), "counterparty": "Somewhere"})
	expect[generated.Account](c, http.MethodPatch, "/v1/accounts/"+old.Id.String(), map[string]any{"archived": true}, http.StatusOK)

	got := c.candidates("limit=2")
	if got.Total != 3 || len(got.Items) != 2 || got.Items[0].Id != kneipe.Id || got.Items[1].Id != ionos.Id {
		t.Fatalf("candidates = %+v", got)
	}
	first := got.Items[0]
	if first.AccountKind != generated.AccountKindBank || first.Amount.Amount != -2350 || first.BookingDate.Format(time.DateOnly) != "2026-10-05" || *first.Counterparty != "Kneipe41" {
		t.Fatalf("candidate = %+v", first)
	}
	if len(got.Examples) != 1 || got.Examples[0].Counterparty != "Pizzeria Roma" || got.Examples[0].CategoryId != eatingOut.Id {
		t.Fatalf("examples = %+v", got.Examples)
	}
	c.expectError(http.MethodGet, "/v1/categories/ai/candidates?limit=101", nil, http.StatusBadRequest, "invalid_request")

	// The owner never chooses as the AI; the choices are checked.
	status, raw := c.do(http.MethodPost, "/v1/categories/ai/choices", map[string]any{"choices": []map[string]any{{"transactionId": kneipe.Id, "categoryId": eatingOut.Id}}}, activity.ActorHeader, "user")
	if status != http.StatusForbidden {
		t.Fatalf("as the owner: %d %s", status, raw)
	}
	c.expectError(http.MethodPost, "/v1/categories/ai/choices", map[string]any{"choices": []map[string]any{}}, http.StatusBadRequest, "invalid_request")
	c.expectError(http.MethodPost, "/v1/categories/ai/choices", map[string]any{"choices": []map[string]any{
		{"transactionId": kneipe.Id, "categoryId": eatingOut.Id}, {"transactionId": kneipe.Id, "categoryId": nil}}}, http.StatusBadRequest, "invalid_request")

	var seen int
	c.recorder.since(&seen)
	result := c.choose(
		kneipe.Id, eatingOut.Id,
		ionos.Id, nil, // unsure
		salary.Id, shopping.Id, // money in to an expense category
		rewe.Id, shopping.Id, // a rule chose
		kiosk.Id, shopping.Id, // cleared by hand
		"7f8d2b1e-1c1a-4b7e-9a51-2f0c6d1e9a10", eatingOut.Id,
	)
	want := []string{"categorized", "left", "skipped:kind_mismatch", "skipped:chosen", "skipped:chosen", "skipped:not_found"}
	if got := outcomes(result); len(got) != len(want) || result.Categorized != 1 || result.Left != 1 || result.Skipped != 4 {
		t.Fatalf("result = %+v %v", result, got)
	}
	for index, outcome := range outcomes(result) {
		if outcome != want[index] {
			t.Fatalf("outcome %d = %s, want %s", index, outcome, want[index])
		}
	}
	if got := c.transaction(kneipe.Id.String()); *got.CategoryId != eatingOut.Id || *got.CategorizedBy != generated.TransactionCategorizedByAi || got.CategoryRuleId != nil {
		t.Fatalf("kneipe = %+v", got)
	}
	if got := c.transaction(rewe.Id.String()); *got.CategoryId != groceries.Id || *got.CategorizedBy != generated.TransactionCategorizedByRule {
		t.Fatalf("rewe = %+v", got)
	}
	if got := c.transaction(kiosk.Id.String()); got.CategoryId != nil || *got.CategorizedBy != generated.TransactionCategorizedByManual {
		t.Fatalf("kiosk = %+v", got)
	}
	// One event for the request, by the AI, with counts only.
	events := c.recorder.since(&seen)
	if len(events) != 1 || events[0].Type != "finance.ai_categories.applied" || events[0].Actor != activity.Agent("codex-finance-categories") ||
		events[0].Summary != "AI categorized 1 transaction: Eating out 1 (1 left without a category)" || events[0].Subject.Type != "finance_ai_categories" {
		t.Fatalf("events = %+v", events)
	}
	if events[0].Data["categorized"] != float64(1) || events[0].Data["left"] != float64(1) || events[0].Data["skipped"] != float64(4) {
		t.Fatalf("event data = %+v", events[0].Data)
	}

	// The AI looked at both: only the salary is left. The same choice again
	// changes and records nothing.
	if got := c.candidates(""); got.Total != 1 || got.Items[0].Id != salary.Id {
		t.Fatalf("candidates after = %+v", got)
	}
	if again := c.choose(kneipe.Id, eatingOut.Id, salary.Id, income.Id); outcomes(again)[0] != "unchanged" || outcomes(again)[1] != "categorized" {
		t.Fatalf("again = %v", outcomes(again))
	}
	if events := c.recorder.since(&seen); len(events) != 1 || events[0].Summary != "AI categorized 1 transaction: Income 1" {
		t.Fatalf("events again = %+v", events)
	}
	// A new text lets the AI look again.
	expect[generated.Transaction](c, http.MethodPatch, "/v1/transactions/"+ionos.Id.String(), map[string]any{"description": "Webhosting Oktober"}, http.StatusOK)
	if got := c.candidates(""); got.Total != 1 || got.Items[0].Id != ionos.Id {
		t.Fatalf("candidates after the edit = %+v", got)
	}
	// The list finds what the AI chose.
	if listed := c.list("categorizedBy=ai").Items; len(listed) != 2 {
		t.Fatalf("by AI = %+v", listed)
	}
	c.expectError(http.MethodGet, "/v1/transactions?categorizedBy=robot", nil, http.StatusBadRequest, "invalid_request")
}

func TestRulesReplaceTheAIsCategoryAndTheOwnersChoiceWins(t *testing.T) {
	c := newAPIClient(t)
	eatingOut := c.category("Eating out", "expense")
	fun := c.category("Fun", "expense")
	shopping := c.category("Shopping", "expense")
	c.syncBank(bankSync([]map[string]any{giroInput(0, 0)}, []map[string]any{
		bankTx("acc-1", "kneipe", "booked", "2026-10-02", -2350, "Kneipe41"),
		bankTx("acc-1", "kabul", "booked", "2026-10-03", -3100, "Kabul Restaurant"),
		bankTx("acc-1", "blume", "booked", "2026-10-04", -1999, "Blume 2000"),
		bankTx("acc-1", "smyths", "booked", "2026-10-05", -4599, "SMYTHS TOYS"),
	}))
	kneipe, kabul, blume, smyths := c.byExternalID("kneipe"), c.byExternalID("kabul"), c.byExternalID("blume"), c.byExternalID("smyths")
	c.choose(kneipe.Id, fun.Id, kabul.Id, fun.Id, blume.Id, shopping.Id, smyths.Id, shopping.Id)

	// The owner corrects one: by hand, and the AI never takes it back.
	fixed := expect[generated.Transaction](c, http.MethodPatch, "/v1/transactions/"+kabul.Id.String(), map[string]any{"categoryId": eatingOut.Id}, http.StatusOK)
	if *fixed.CategorizedBy != generated.TransactionCategorizedByManual {
		t.Fatalf("fixed = %+v", fixed)
	}
	if again := c.choose(kabul.Id, fun.Id); outcomes(again)[0] != "skipped:chosen" {
		t.Fatalf("the AI over the owner: %v", outcomes(again))
	}

	// "Always put Kneipe41 in Eating out": a rule, applied with the default
	// scope, replaces the AI's choice; the others stay.
	c.rule(eatingOut.Id.String(), "counterparty", "contains", "Kneipe41")
	var seen int
	c.recorder.since(&seen)
	applied := expect[generated.ApplyCategoryRulesResult](c, http.MethodPost, "/v1/categories/rules/apply", nil, http.StatusOK)
	if applied.Recategorized != 1 || applied.Categorized != 0 || applied.Cleared != 0 {
		t.Fatalf("applied = %+v", applied)
	}
	if got := c.transaction(kneipe.Id.String()); *got.CategoryId != eatingOut.Id || *got.CategorizedBy != generated.TransactionCategorizedByRule {
		t.Fatalf("kneipe = %+v", got)
	}
	if events := c.recorder.since(&seen); len(events) != 1 || events[0].Summary != "Category rules applied to uncategorized transactions: 0 categorized, 1 recategorized from the AI's choice" {
		t.Fatalf("events = %+v", events)
	}
	// Scope all keeps an AI choice no rule matches; a choice by hand stays.
	applied = expect[generated.ApplyCategoryRulesResult](c, http.MethodPost, "/v1/categories/rules/apply", map[string]any{"scope": "all"}, http.StatusOK)
	if applied.Cleared != 0 || applied.Recategorized != 0 {
		t.Fatalf("applied all = %+v", applied)
	}
	if got := c.transaction(blume.Id.String()); *got.CategoryId != shopping.Id || *got.CategorizedBy != generated.TransactionCategorizedByAi {
		t.Fatalf("blume = %+v", got)
	}
	if got := c.transaction(kabul.Id.String()); *got.CategoryId != eatingOut.Id || *got.CategorizedBy != generated.TransactionCategorizedByManual {
		t.Fatalf("kabul = %+v", got)
	}

	// Re-syncs: the same text keeps the AI's choice; a rule for the new
	// text takes over; a new text no rule matches drops it, and the AI
	// looks again.
	c.rule(shopping.Id.String(), "counterparty", "contains", "Smyths Toys Darmstadt")
	c.syncBank(bankSync([]map[string]any{giroInput(0, 0)}, []map[string]any{
		bankTx("acc-1", "blume", "booked", "2026-10-04", -1999, "Blume 2000"),
		bankTx("acc-1", "smyths", "booked", "2026-10-05", -4599, "SMYTHS TOYS DARMSTADT"),
	}))
	if got := c.byExternalID("blume"); *got.CategorizedBy != generated.TransactionCategorizedByAi {
		t.Fatalf("blume after the same text = %+v", got)
	}
	if got := c.byExternalID("smyths"); *got.CategorizedBy != generated.TransactionCategorizedByRule {
		t.Fatalf("smyths after a matching text = %+v", got)
	}
	c.syncBank(bankSync([]map[string]any{giroInput(0, 0)}, []map[string]any{
		bankTx("acc-1", "blume", "booked", "2026-10-04", -1999, "BLUME 2000 SAGT DANKE"),
	}))
	if got := c.byExternalID("blume"); got.CategoryId != nil || got.CategorizedBy != nil {
		t.Fatalf("blume after a new text = %+v", got)
	}
	if got := c.candidates(""); got.Total != 1 || got.Items[0].Id != blume.Id {
		t.Fatalf("candidates = %+v", got)
	}
}

// A choice by hand that lands while the AI's choices wait for the
// transaction wins: the AI's choice is skipped, not written over it.
func TestAnAIChoiceNeverOverwritesAChoiceThatLandedMeanwhile(t *testing.T) {
	c := newAPIClient(t)
	eatingOut := c.category("Eating out", "expense")
	fun := c.category("Fun", "expense")
	item := c.spend(map[string]any{"amount": eur(-2350), "counterparty": "Kneipe41"})

	ctx := context.Background()
	tx, err := c.handler.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	// The owner's change holds the row...
	if _, err := tx.Exec(ctx, `UPDATE transactions SET category_id = $1, categorized_by = 'manual' WHERE id = $2`, eatingOut.Id, item.Id); err != nil {
		t.Fatal(err)
	}
	type answer struct {
		status int
		raw    []byte
	}
	done := make(chan answer, 1)
	go func() {
		body := map[string]any{"choices": []map[string]any{{"transactionId": item.Id, "categoryId": fun.Id}}}
		status, raw := c.do(http.MethodPost, "/v1/categories/ai/choices", body, activity.ActorHeader, aiActor)
		done <- answer{status, raw}
	}()
	select {
	case got := <-done:
		t.Fatalf("the AI's choice did not wait for the row: %d %s", got.status, got.raw)
	case <-time.After(300 * time.Millisecond):
	}
	// ...and commits while the AI's choice waits for it.
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		var result generated.AiCategoryChoicesResult
		if err := json.Unmarshal(got.raw, &result); err != nil || got.status != http.StatusOK {
			t.Fatalf("answer = %d %s %v", got.status, got.raw, err)
		}
		if outcome := outcomes(result); len(outcome) != 1 || outcome[0] != "skipped:chosen" {
			t.Fatalf("result = %v", outcome)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the AI's choice never finished")
	}
	if got := c.transaction(item.Id.String()); *got.CategoryId != eatingOut.Id || *got.CategorizedBy != generated.TransactionCategorizedByManual {
		t.Fatalf("after = %+v", got)
	}
}
