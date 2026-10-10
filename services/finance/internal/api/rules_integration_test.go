package api

import (
	"net/http"
	"testing"

	"github.com/GAMch1k/life-os/services/finance/internal/api/generated"
)

func TestCategoriesAreCreatedChangedAndArchived(t *testing.T) {
	c := newAPIClient(t)
	groceries := expect[generated.Category](c, http.MethodPost, "/v1/categories", map[string]any{"name": "Groceries", "kind": "expense", "color": "#22aa55"}, http.StatusCreated)
	if groceries.Kind != generated.CategoryKindExpense || *groceries.Color != "#22aa55" || groceries.Archived {
		t.Fatalf("groceries = %+v", groceries)
	}
	transfer := c.category("Savings", "transfer")
	c.category("Groceries", "income") // the same name of another kind is fine
	c.expectError(http.MethodPost, "/v1/categories", map[string]any{"name": "Groceries", "kind": "expense"}, http.StatusConflict, "category_exists")
	c.expectError(http.MethodPost, "/v1/categories", map[string]any{"name": "X", "kind": "spending"}, http.StatusBadRequest, "invalid_request")
	c.expectError(http.MethodPost, "/v1/categories", map[string]any{"name": "X", "kind": "expense", "color": "green"}, http.StatusBadRequest, "invalid_request")

	target := "/v1/categories/" + groceries.Id.String()
	changed := expect[generated.Category](c, http.MethodPatch, target, map[string]any{"name": "Food", "color": nil}, http.StatusOK)
	if changed.Name != "Food" || changed.Color != nil {
		t.Fatalf("changed = %+v", changed)
	}
	c.expectError(http.MethodPatch, "/v1/categories/"+transfer.Id.String(), map[string]any{"name": "Food", "kind": "expense"}, http.StatusConflict, "category_exists")
	c.expectError(http.MethodPatch, "/v1/categories/7f8d2b1e-1c1a-4b7e-9a51-2f0c6d1e9a10", map[string]any{"name": "Y"}, http.StatusNotFound, "not_found")
	archived := expect[generated.Category](c, http.MethodPatch, target, map[string]any{"archived": true}, http.StatusOK)
	if !archived.Archived || archived.ArchivedAt == nil {
		t.Fatalf("archived = %+v", archived)
	}
	if listed := expect[generated.CategoryList](c, http.MethodGet, "/v1/categories", nil, http.StatusOK).Items; len(listed) != 2 {
		t.Fatalf("active categories = %+v", listed)
	}
	if listed := expect[generated.CategoryList](c, http.MethodGet, "/v1/categories?includeArchived=true", nil, http.StatusOK).Items; len(listed) != 3 {
		t.Fatalf("all categories = %+v", listed)
	}
	c.expectError(http.MethodPost, "/v1/transactions", map[string]any{"amount": eur(-1), "categoryId": groceries.Id}, http.StatusConflict, "category_archived")
	c.expectError(http.MethodPost, "/v1/categories/rules", map[string]any{"categoryId": groceries.Id, "match": map[string]any{"field": "any", "operator": "contains", "text": "x"}}, http.StatusConflict, "category_archived")
}

func TestRulesCategorizeNewTransactionsAndReapply(t *testing.T) {
	c := newAPIClient(t)
	giro := c.account("Girokonto", "bank", "EUR", 0)
	groceries := c.category("Groceries", "expense")
	dining := c.category("Dining", "expense")
	rewe := c.rule(groceries.Id.String(), "counterparty", "contains", "REWE")
	if rewe.Priority != 10 || rewe.Match.Field != generated.Counterparty || rewe.Match.Text != "REWE" {
		t.Fatalf("rule = %+v", rewe)
	}
	toGo := expect[generated.CategoryRule](c, http.MethodPost, "/v1/categories/rules", map[string]any{"categoryId": dining.Id, "priority": 5,
		"match": map[string]any{"field": "any", "operator": "contains", "text": "to go"}}, http.StatusCreated)
	rules := expect[generated.CategoryRuleList](c, http.MethodGet, "/v1/categories/rules", nil, http.StatusOK).Items
	if len(rules) != 2 || rules[0].Id != toGo.Id || rules[1].Id != rewe.Id {
		t.Fatalf("rule order = %+v", rules)
	}
	for _, bad := range []map[string]any{
		{"categoryId": groceries.Id, "match": map[string]any{"field": "notes", "operator": "contains", "text": "x"}},
		{"categoryId": groceries.Id, "match": map[string]any{"field": "any", "operator": "like", "text": "x"}},
		{"categoryId": groceries.Id, "match": map[string]any{"field": "any", "operator": "contains", "text": "  "}},
		{"categoryId": groceries.Id},
	} {
		c.expectError(http.MethodPost, "/v1/categories/rules", bad, http.StatusBadRequest, "invalid_request")
	}
	c.expectError(http.MethodPost, "/v1/categories/rules", map[string]any{"categoryId": "7f8d2b1e-1c1a-4b7e-9a51-2f0c6d1e9a10", "match": map[string]any{"field": "any", "operator": "contains", "text": "x"}}, http.StatusBadRequest, "unknown_category")

	// New manual transactions: the first matching rule chooses; a category
	// given by hand wins.
	shop := c.spend(map[string]any{"amount": eur(-2345), "counterparty": "rewe markt"})
	if *shop.CategoryId != groceries.Id || *shop.CategorizedBy != generated.TransactionCategorizedByRule || *shop.CategoryRuleId != rewe.Id {
		t.Fatalf("shop = %+v", shop)
	}
	if snack := c.spend(map[string]any{"amount": eur(-450), "counterparty": "REWE To Go"}); *snack.CategoryId != dining.Id {
		t.Fatalf("snack = %+v", snack)
	}
	byHand := c.spend(map[string]any{"amount": eur(-999), "counterparty": "REWE", "categoryId": dining.Id})
	if *byHand.CategoryId != dining.Id || *byHand.CategorizedBy != generated.TransactionCategorizedByManual {
		t.Fatalf("by hand = %+v", byHand)
	}
	// Imported rows too.
	expect[generated.CsvImport](c, http.MethodPost, "/v1/imports/csv?accountId="+giro.Id.String(),
		csvBody("date,amount,counterparty,description\n2026-10-02,-5.00,Lidl,\n2026-10-03,-7.00,Netto,REWE voucher\n"), http.StatusOK)
	lidl := c.list("q=lidl").Items[0]
	if lidl.CategoryId != nil {
		t.Fatalf("lidl = %+v", lidl)
	}

	// A new rule, applied to uncategorized transactions.
	discounters := c.category("Discounters", "expense")
	lidlRule := c.rule(discounters.Id.String(), "counterparty", "equals", "lidl")
	applied := expect[generated.ApplyCategoryRulesResult](c, http.MethodPost, "/v1/categories/rules/apply", nil, http.StatusOK)
	if applied.Scope != generated.Uncategorized || applied.Categorized != 1 || applied.Recategorized != 0 || applied.Cleared != 0 {
		t.Fatalf("applied = %+v", applied)
	}
	if lidl = c.list("q=lidl").Items[0]; *lidl.CategoryId != discounters.Id || *lidl.CategoryRuleId != lidlRule.Id {
		t.Fatalf("lidl after apply = %+v", lidl)
	}

	// Rules change: REWE now means Dining, and to-go is gone. Applying to
	// all moves rule-chosen categories and clears those no rule matches,
	// never a choice by hand.
	expect[generated.CategoryRule](c, http.MethodPatch, "/v1/categories/rules/"+rewe.Id.String(), map[string]any{"categoryId": discounters.Id,
		"match": map[string]any{"field": "counterparty", "operator": "equals", "text": "rewe markt"}}, http.StatusOK)
	if status, raw := c.do(http.MethodDelete, "/v1/categories/rules/"+toGo.Id.String(), nil); status != http.StatusNoContent {
		t.Fatalf("delete rule: %d %s", status, raw)
	}
	c.expectError(http.MethodDelete, "/v1/categories/rules/"+toGo.Id.String(), nil, http.StatusNotFound, "not_found")
	applied = expect[generated.ApplyCategoryRulesResult](c, http.MethodPost, "/v1/categories/rules/apply", map[string]any{"scope": "all"}, http.StatusOK)
	if applied.Scope != generated.All || applied.Recategorized != 1 || applied.Cleared != 1 || applied.Categorized != 0 {
		t.Fatalf("applied all = %+v", applied)
	}
	if got := expect[generated.Transaction](c, http.MethodGet, "/v1/transactions/"+shop.Id.String(), nil, http.StatusOK); *got.CategoryId != discounters.Id {
		t.Fatalf("shop after all = %+v", got)
	}
	if got := expect[generated.Transaction](c, http.MethodGet, "/v1/transactions/"+byHand.Id.String(), nil, http.StatusOK); *got.CategoryId != dining.Id {
		t.Fatalf("by hand after all = %+v", got)
	}
	if snacks := c.list("q=" + "To%20Go").Items; len(snacks) != 1 || snacks[0].CategoryId != nil || snacks[0].CategorizedBy != nil {
		t.Fatalf("snack after all = %+v", snacks)
	}
	c.expectError(http.MethodPost, "/v1/categories/rules/apply", map[string]any{"scope": "some"}, http.StatusBadRequest, "invalid_request")

	// Editing the text of a transaction whose category a rule chose asks the
	// rules again; a category chosen by hand stays.
	moved := expect[generated.Transaction](c, http.MethodPatch, "/v1/transactions/"+shop.Id.String(), map[string]any{"counterparty": "Lidl"}, http.StatusOK)
	if *moved.CategoryId != discounters.Id || *moved.CategoryRuleId != lidlRule.Id {
		t.Fatalf("re-ruled = %+v", moved)
	}
	cleared := expect[generated.Transaction](c, http.MethodPatch, "/v1/transactions/"+shop.Id.String(), map[string]any{"counterparty": "Kiosk"}, http.StatusOK)
	if cleared.CategoryId != nil || cleared.CategorizedBy != nil {
		t.Fatalf("no rule matches = %+v", cleared)
	}
	kept := expect[generated.Transaction](c, http.MethodPatch, "/v1/transactions/"+byHand.Id.String(), map[string]any{"counterparty": "Lidl"}, http.StatusOK)
	if *kept.CategoryId != dining.Id {
		t.Fatalf("by hand after edit = %+v", kept)
	}
	// Cleared by hand: rules leave it uncategorized.
	none := expect[generated.Transaction](c, http.MethodPatch, "/v1/transactions/"+byHand.Id.String(), map[string]any{"categoryId": nil}, http.StatusOK)
	if none.CategoryId != nil || *none.CategorizedBy != generated.TransactionCategorizedByManual {
		t.Fatalf("cleared by hand = %+v", none)
	}
	if applied := expect[generated.ApplyCategoryRulesResult](c, http.MethodPost, "/v1/categories/rules/apply", map[string]any{}, http.StatusOK); applied.Categorized != 0 {
		t.Fatalf("a choice by hand was overridden: %+v", applied)
	}

	// An archived category's rules are skipped.
	expect[generated.Category](c, http.MethodPatch, "/v1/categories/"+discounters.Id.String(), map[string]any{"archived": true}, http.StatusOK)
	if got := c.spend(map[string]any{"amount": eur(-100), "counterparty": "Lidl"}); got.CategoryId != nil {
		t.Fatalf("archived category's rule applied: %+v", got)
	}
}
