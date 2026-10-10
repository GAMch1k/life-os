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
