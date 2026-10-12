package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GAMch1k/life-os/pkg/activity"
)

const accountUUID = "0d5c8a3e-2b4f-4e6a-9c1d-7e8f9a0b1c2d"

// financeSession connects an MCP client to a server with only the Finance
// tools, backed by a fake Finance API that records every request.
func financeSession(t *testing.T, status int, response string) (*mcp.ClientSession, *[]calendarRequest) {
	t.Helper()
	requests := &[]calendarRequest{}
	finance := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		request := calendarRequest{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Actor: r.Header.Get(activity.ActorHeader), Idempotency: r.Header.Get("Idempotency-Key"), RawBody: string(raw)}
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &request.Body)
		}
		*requests = append(*requests, request)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(finance.Close)
	server := mcp.NewServer(&mcp.Implementation{Name: "life-os-test", Version: "0"}, nil)
	addFinanceTools(server, finance.Client(), finance.URL, activity.NewEmitter("", "mcp", nil))
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := server.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session, requests
}

func TestCashSpendingNeedsNoAccount(t *testing.T) {
	session, requests := financeSession(t, http.StatusCreated, `{"id":"tx-1"}`)
	// "Spent 12 euros at the bakery": no account, no date.
	result := callTool(t, session, "finance_create_manual_transaction", map[string]any{
		"amount": map[string]any{"amount": -1200, "currency": "eur"}, "counterparty": "Bäckerei",
	})
	if result.IsError {
		t.Fatalf("create failed: %s", resultText(result))
	}
	got := (*requests)[0]
	if got.Method != http.MethodPost || got.Path != "/v1/transactions" || got.Actor != "agent:mcp" {
		t.Fatalf("Finance saw %+v", got)
	}
	amount, _ := got.Body["amount"].(map[string]any)
	if amount["amount"] != -1200.0 || amount["currency"] != "EUR" || got.Body["counterparty"] != "Bäckerei" {
		t.Fatalf("body = %s", got.RawBody)
	}
	for _, field := range []string{"accountId", "bookingDate", "occurredOn", "note", "notes", "categoryId"} {
		if _, present := got.Body[field]; present {
			t.Fatalf("%s was sent without being given: %s", field, got.RawBody)
		}
	}
}

func TestPlannedTransactionsMapToTheFinanceFields(t *testing.T) {
	session, requests := financeSession(t, http.StatusCreated, `{"id":"tx-1"}`)
	// The processor plans with accountId, amount, occurredOn, and note.
	result := callTool(t, session, "finance_create_manual_transaction", map[string]any{
		"idempotencyKey": "action-7", "accountId": accountUUID, "amount": map[string]any{"amount": -2000, "currency": "EUR"},
		"occurredOn": "2026-10-07", "note": "Lidl", "description": "weekly shop",
	})
	if result.IsError {
		t.Fatalf("create failed: %s", resultText(result))
	}
	got := (*requests)[0]
	if got.Idempotency != "action-7" || got.Body["accountId"] != accountUUID || got.Body["bookingDate"] != "2026-10-07" ||
		got.Body["notes"] != "Lidl" || got.Body["description"] != "weekly shop" || got.Body["occurredOn"] != nil || got.Body["note"] != nil {
		t.Fatalf("Finance saw %+v %s", got, got.RawBody)
	}
	for name, arguments := range map[string]map[string]any{
		"zero amount":    {"amount": map[string]any{"amount": 0, "currency": "EUR"}},
		"bad currency":   {"amount": map[string]any{"amount": -1, "currency": "euro"}},
		"account name":   {"accountId": "account-cash", "amount": map[string]any{"amount": -1, "currency": "EUR"}},
		"bad date":       {"occurredOn": "yesterday", "amount": map[string]any{"amount": -1, "currency": "EUR"}},
		"category name":  {"categoryId": "Groceries", "amount": map[string]any{"amount": -1, "currency": "EUR"}},
		"decimal amount": {"amount": map[string]any{"amount": -12.5, "currency": "EUR"}},
	} {
		if result := callTool(t, session, "finance_create_manual_transaction", arguments); !result.IsError {
			t.Errorf("%s was accepted", name)
		}
	}
	if len(*requests) != 1 {
		t.Fatalf("refused calls reached Finance: %d requests", len(*requests))
	}
}

func TestFinanceSearchBuildsAFilteredQuery(t *testing.T) {
	session, requests := financeSession(t, http.StatusOK, `{"items":[]}`)
	amountMin := 1000
	result := callTool(t, session, "finance_search_transactions", map[string]any{
		"q": "bakery", "accountId": accountUUID, "categoryId": "uncategorized", "from": "2026-10-01", "to": "2026-10-31",
		"direction": "out", "amountMin": amountMin, "status": "booked", "source": "manual", "limit": 10,
	})
	if result.IsError {
		t.Fatalf("search failed: %s", resultText(result))
	}
	got := (*requests)[0]
	query, err := url.ParseQuery(got.Query)
	if err != nil || got.Method != http.MethodGet || got.Path != "/v1/transactions" {
		t.Fatalf("Finance saw %+v", got)
	}
	want := map[string]string{"q": "bakery", "accountId": accountUUID, "categoryId": "uncategorized", "from": "2026-10-01", "to": "2026-10-31",
		"direction": "out", "amountMin": "1000", "status": "booked", "source": "manual", "limit": "10"}
	for key, value := range want {
		if query.Get(key) != value {
			t.Errorf("%s = %q, want %q (query %s)", key, query.Get(key), value, got.Query)
		}
	}
	callTool(t, session, "finance_search_transactions", map[string]any{})
	if query := (*requests)[1].Query; query != "limit=20" {
		t.Fatalf("empty search query = %q", query)
	}
	for name, arguments := range map[string]map[string]any{
		"direction": {"direction": "sideways"}, "account": {"accountId": "cash"}, "date": {"from": "October"},
		"category": {"categoryId": "food"}, "limit": {"limit": 500}, "status": {"status": "done"},
	} {
		if result := callTool(t, session, "finance_search_transactions", arguments); !result.IsError {
			t.Errorf("%s was accepted", name)
		}
	}
	if len(*requests) != 2 {
		t.Fatalf("refused searches reached Finance: %d", len(*requests))
	}
}

func TestFinanceReadToolsPassTheirQuery(t *testing.T) {
	session, requests := financeSession(t, http.StatusOK, `{"items":[]}`)
	for tool, path := range map[string]string{"finance_list_accounts": "/v1/accounts", "finance_list_categories": "/v1/categories", "finance_month_summary": "/v1/summaries/monthly", "finance_find_transactions": "/v1/transactions"} {
		before := len(*requests)
		if result := callTool(t, session, tool, map[string]any{"query": "month=2026-10"}); result.IsError {
			t.Fatalf("%s failed: %s", tool, resultText(result))
		}
		got := (*requests)[before]
		if got.Path != path || !strings.Contains(got.Query, "month=2026-10") {
			t.Fatalf("%s reached %s?%s", tool, got.Path, got.Query)
		}
	}
}

func TestTheAIsCategoryToolsCallFinance(t *testing.T) {
	session, requests := financeSession(t, http.StatusOK, `{"total":0,"items":[],"examples":[]}`)
	if result := callTool(t, session, "finance_list_ai_category_candidates", map[string]any{}); result.IsError {
		t.Fatalf("candidates failed: %s", resultText(result))
	}
	if got := (*requests)[0]; got.Method != http.MethodGet || got.Path != "/v1/categories/ai/candidates" || got.Query != "limit=40" {
		t.Fatalf("Finance saw %+v", got)
	}
	if result := callTool(t, session, "finance_list_ai_category_candidates", map[string]any{"limit": 500}); !result.IsError {
		t.Fatal("limit 500 was accepted")
	}

	const transaction, category = "4c0b7c1e-8f2a-4d7b-9e6a-1a2b3c4d5e6f", "9a8b7c6d-5e4f-4a3b-8c2d-1e0f9a8b7c6d"
	result := callTool(t, session, "finance_set_ai_categories", map[string]any{"choices": []map[string]any{
		{"transactionId": transaction, "categoryId": category},
		{"transactionId": accountUUID, "categoryId": nil},
	}})
	if result.IsError {
		t.Fatalf("choices failed: %s", resultText(result))
	}
	got := (*requests)[1]
	if got.Method != http.MethodPost || got.Path != "/v1/categories/ai/choices" || got.Actor != "agent:mcp" {
		t.Fatalf("Finance saw %+v", got)
	}
	if got.RawBody != `{"choices":[{"categoryId":"`+category+`","transactionId":"`+transaction+`"},{"categoryId":null,"transactionId":"`+accountUUID+`"}]}` {
		t.Fatalf("body = %s", got.RawBody)
	}
	for name, arguments := range map[string]map[string]any{
		"no choices":    {"choices": []map[string]any{}},
		"category name": {"choices": []map[string]any{{"transactionId": transaction, "categoryId": "Eating out"}}},
		"transaction":   {"choices": []map[string]any{{"transactionId": "kneipe", "categoryId": category}}},
	} {
		if result := callTool(t, session, "finance_set_ai_categories", arguments); !result.IsError {
			t.Errorf("%s was accepted", name)
		}
	}
	if len(*requests) != 2 {
		t.Fatalf("refused calls reached Finance: %d requests", len(*requests))
	}
}

func TestTheBudgetPlanToolsCallFinance(t *testing.T) {
	session, requests := financeSession(t, http.StatusOK, `{"items":[],"waiting":0}`)
	const plan, category = "4c0b7c1e-8f2a-4d7b-9e6a-1a2b3c4d5e6f", "9a8b7c6d-5e4f-4a3b-8c2d-1e0f9a8b7c6d"
	calls := []struct {
		tool                string
		arguments           map[string]any
		method, path, query string
	}{
		{"finance_list_budget_plans", map[string]any{"state": "requested", "limit": 1}, http.MethodGet, "/v1/ai-budget-plans", "limit=1&state=requested"},
		{"finance_get_budget_planning_data", map[string]any{"month": "2026-11"}, http.MethodGet, "/v1/budgets/2026-11/planning-data", ""},
		{"finance_request_budget_plan", map[string]any{"month": "2026-11"}, http.MethodPost, "/v1/ai-budget-plans", ""},
		{"finance_claim_budget_plan", map[string]any{}, http.MethodPost, "/v1/ai-budget-plans/claim", ""},
		{"finance_set_budget_plan_result", map[string]any{"planId": plan, "currency": "eur", "headline": "Eat out less.",
			"lines":               []map[string]any{{"categoryId": category, "proposedMinor": 9000, "reason": "Less pizza."}},
			"suggestions":         []map[string]any{{"title": "Cook", "detail": "Cook twice a week.", "categoryId": category, "merchants": []string{"Pizzeria Roma"}, "monthlySavingMinor": 3000, "link": "food_recipes"}},
			"projectedTotalMinor": 9000, "incomeEstimateMinor": 200000, "projectedSavingsMinor": 191000}, http.MethodPut, "/v1/ai-budget-plans/" + plan + "/result", ""},
		{"finance_fail_budget_plan", map[string]any{"planId": plan, "error": "usage limit", "retryAt": "2026-11-01T08:00:00+01:00"}, http.MethodPost, "/v1/ai-budget-plans/" + plan + "/failure", ""},
	}
	for index, call := range calls {
		if result := callTool(t, session, call.tool, call.arguments); result.IsError {
			t.Fatalf("%s failed: %s", call.tool, resultText(result))
		}
		got := (*requests)[index]
		if got.Method != call.method || got.Path != call.path || got.Query != call.query || got.Actor != "agent:mcp" {
			t.Fatalf("%s: Finance saw %+v", call.tool, got)
		}
	}
	if body := (*requests)[2].RawBody; body != `{"month":"2026-11","trigger":"schedule"}` {
		t.Fatalf("request body = %s", body)
	}
	if body := (*requests)[4].RawBody; !strings.Contains(body, `"currency":"EUR"`) || strings.Contains(body, "planId") ||
		!strings.Contains(body, `"categoryId":"`+category+`"`) || !strings.Contains(body, `"link":"food_recipes"`) {
		t.Fatalf("result body = %s", body)
	}
	if body := (*requests)[5].RawBody; body != `{"error":"usage limit","retryAt":"2026-11-01T07:00:00Z"}` {
		t.Fatalf("failure body = %s", body)
	}
	for name, call := range map[string]struct {
		tool      string
		arguments map[string]any
	}{
		"a bad month":           {"finance_get_budget_planning_data", map[string]any{"month": "November"}},
		"an unknown state":      {"finance_list_budget_plans", map[string]any{"state": "thinking"}},
		"a large limit":         {"finance_list_budget_plans", map[string]any{"limit": 500}},
		"a negative goal":       {"finance_request_budget_plan", map[string]any{"savingsGoalMinor": -1}},
		"a plan name":           {"finance_set_budget_plan_result", map[string]any{"planId": "November", "lines": []map[string]any{}}},
		"a category name":       {"finance_set_budget_plan_result", map[string]any{"planId": plan, "lines": []map[string]any{{"categoryId": "Eating out", "proposedMinor": 1, "reason": "x"}}}},
		"a failure without why": {"finance_fail_budget_plan", map[string]any{"planId": plan, "error": " "}},
		"a bad retry time":      {"finance_fail_budget_plan", map[string]any{"planId": plan, "error": "x", "retryAt": "tomorrow"}},
	} {
		if result := callTool(t, session, call.tool, call.arguments); !result.IsError {
			t.Errorf("%s was accepted", name)
		}
	}
	if len(*requests) != len(calls) {
		t.Fatalf("refused calls reached Finance: %d requests", len(*requests))
	}
}
