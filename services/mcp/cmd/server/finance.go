package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/mcp/internal/coreclient"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// moneyInput is a Finance amount: signed minor units and a currency.
type moneyInput struct {
	Amount   int64  `json:"amount" jsonschema:"Signed integer in minor units (cents): -1200 is 12.00 spent, 250000 is 2500.00 received"`
	Currency string `json:"currency" jsonschema:"Three-letter ISO currency such as EUR"`
}

// financeAction records one manual transaction. The field names stay those
// the Inbox processor plans with (occurredOn, note); MCP maps them to the
// Finance API's bookingDate and notes.
type financeAction struct {
	IdempotencyKey string     `json:"idempotencyKey,omitempty"`
	AccountID      string     `json:"accountId,omitempty" jsonschema:"Finance account UUID (from finance_list_accounts); leave out for cash spending, which goes to the built-in Cash account"`
	CategoryID     *string    `json:"categoryId,omitempty" jsonschema:"Finance category UUID (from finance_list_categories); leave out to let the category rules choose"`
	Amount         moneyInput `json:"amount" jsonschema:"Signed amount in minor units with its currency; the currency must be the account's"`
	OccurredOn     string     `json:"occurredOn,omitempty" jsonschema:"The owner's local date (YYYY-MM-DD); leave out for today in the owner's timezone"`
	Note           *string    `json:"note,omitempty" jsonschema:"The owner's own note"`
	Counterparty   *string    `json:"counterparty,omitempty" jsonschema:"Who was paid, or who paid, such as Bäckerei or REWE"`
	Description    *string    `json:"description,omitempty" jsonschema:"What it was for"`
}

// transactionSearch finds Finance transactions; every field is optional.
type transactionSearch struct {
	Q          string `json:"q,omitempty" jsonschema:"Words to find in the counterparty, description, or notes (every word must match), such as bakery or REWE"`
	AccountID  string `json:"accountId,omitempty" jsonschema:"Only this account (UUID)"`
	CategoryID string `json:"categoryId,omitempty" jsonschema:"Only this category (UUID), or uncategorized"`
	From       string `json:"from,omitempty" jsonschema:"Earliest booking date, YYYY-MM-DD (inclusive)"`
	To         string `json:"to,omitempty" jsonschema:"Latest booking date, YYYY-MM-DD (inclusive)"`
	Direction  string `json:"direction,omitempty" jsonschema:"in (money received) or out (money spent)"`
	AmountMin  *int64 `json:"amountMin,omitempty" jsonschema:"Smallest absolute amount in minor units"`
	AmountMax  *int64 `json:"amountMax,omitempty" jsonschema:"Largest absolute amount in minor units"`
	Status     string `json:"status,omitempty" jsonschema:"booked or pending"`
	Source     string `json:"source,omitempty" jsonschema:"manual, csv, or bank"`
	Limit      int    `json:"limit,omitempty" jsonschema:"1 to 100 (default 20)"`
	Cursor     string `json:"cursor,omitempty" jsonschema:"The previous result's nextCursor, with the same filters"`
}

var currencyCode = regexp.MustCompile(`^[A-Z]{3}$`)

// addFinanceTools registers the Finance tools. Each is a Finance API
// operation, the same one the Dashboard uses.
func addFinanceTools(s *mcp.Server, hc *http.Client, baseURL string, emitter *activity.Emitter) {
	addReadTool(s, hc, "finance_month_summary", "Get the Finance summary of a month (month=YYYY-MM, optional currency and accountId): income, spending, net, the balance now (totalBalance, pending card payments included; totalBookedBalance and totalPending split it), and totals per category", baseURL, "/v1/summaries/monthly?")
	addReadTool(s, hc, "finance_list_accounts", "List Finance accounts with their balances (the built-in Cash account first; includeArchived=true for archived ones too). balance is the money there is to spend: bookedBalance plus pending, the card payments the bank has not booked yet", baseURL, "/v1/accounts?")
	addReadTool(s, hc, "finance_list_categories", "List Finance categories (expense, income, transfer)", baseURL, "/v1/categories?")
	addReadTool(s, hc, "finance_find_transactions", "List Finance transactions with a raw URL query (q, accountId, categoryId, from, to, direction, amountMin, amountMax, status, source, limit, cursor); prefer finance_search_transactions", baseURL, "/v1/transactions?")
	mcp.AddTool(s, &mcp.Tool{Name: "finance_search_transactions", Description: "Search Finance transactions by words in their counterparty, description, or notes, and filter by account, category, dates, direction, amount, status, or source; newest first"}, func(ctx context.Context, req *mcp.CallToolRequest, in transactionSearch) (*mcp.CallToolResult, Output, error) {
		query, err := searchQuery(in)
		if err != nil {
			return nil, Output{}, err
		}
		headers := http.Header{}
		traceOf(req).Apply(headers)
		body, err := coreclient.Client{BaseURL: baseURL, HTTP: hc}.DoWithHeaders(ctx, http.MethodGet, "/v1/transactions?"+query, nil, headers)
		if err != nil {
			return nil, Output{}, err
		}
		out, err := output(body)
		return nil, out, err
	})
	mcp.AddTool(s, &mcp.Tool{Name: "finance_create_manual_transaction", Description: "Record a manual Finance transaction. Only the amount (signed minor units and currency) is required: without accountId it goes to the built-in Cash account, so \"spent 12 euros at the bakery\" is {amount: {amount: -1200, currency: EUR}, counterparty: Bäckerei}; without occurredOn it is dated today in the owner's timezone; without categoryId the category rules choose"}, func(ctx context.Context, req *mcp.CallToolRequest, in financeAction) (*mcp.CallToolResult, Output, error) {
		return audited(ctx, emitter, req, "finance_create_manual_transaction", in, func(trace activity.Trace) (*mcp.CallToolResult, Output, error) {
			body, err := transactionBody(in)
			if err != nil {
				return nil, Output{}, err
			}
			return post(ctx, hc, trace, baseURL, "/v1/transactions", body, in.IdempotencyKey)
		})
	})
}

// transactionBody is the Finance create body for a planned or requested
// transaction, checked before Finance is called.
func transactionBody(in financeAction) (map[string]any, error) {
	currency := strings.ToUpper(strings.TrimSpace(in.Amount.Currency))
	if in.Amount.Amount == 0 || !currencyCode.MatchString(currency) {
		return nil, errors.New("amount must be a non-zero integer of minor units with a three-letter currency, such as {\"amount\":-1200,\"currency\":\"EUR\"}")
	}
	body := map[string]any{"amount": map[string]any{"amount": in.Amount.Amount, "currency": currency}}
	if account := strings.TrimSpace(in.AccountID); account != "" {
		id, err := uuid.Parse(account)
		if err != nil {
			return nil, errors.New("accountId must be a Finance account UUID; leave it out for the Cash account")
		}
		body["accountId"] = id.String()
	}
	if in.CategoryID != nil && strings.TrimSpace(*in.CategoryID) != "" {
		id, err := uuid.Parse(strings.TrimSpace(*in.CategoryID))
		if err != nil {
			return nil, errors.New("categoryId must be a Finance category UUID")
		}
		body["categoryId"] = id.String()
	}
	if date := strings.TrimSpace(in.OccurredOn); date != "" {
		if _, err := time.Parse(time.DateOnly, date); err != nil {
			return nil, errors.New("occurredOn must be a date such as 2026-10-10")
		}
		body["bookingDate"] = date
	}
	for field, value := range map[string]*string{"notes": in.Note, "counterparty": in.Counterparty, "description": in.Description} {
		if value != nil && strings.TrimSpace(*value) != "" {
			body[field] = strings.TrimSpace(*value)
		}
	}
	return body, nil
}

// searchQuery is the Finance list query for a search, checked so a caller
// cannot reach another parameter.
func searchQuery(in transactionSearch) (string, error) {
	query := url.Values{}
	if q := strings.TrimSpace(in.Q); q != "" {
		query.Set("q", q)
	}
	if in.AccountID != "" {
		id, err := uuid.Parse(strings.TrimSpace(in.AccountID))
		if err != nil {
			return "", errors.New("accountId must be a Finance account UUID")
		}
		query.Set("accountId", id.String())
	}
	if category := strings.TrimSpace(in.CategoryID); category != "" {
		if category != "uncategorized" {
			id, err := uuid.Parse(category)
			if err != nil {
				return "", errors.New("categoryId must be a Finance category UUID or uncategorized")
			}
			category = id.String()
		}
		query.Set("categoryId", category)
	}
	for name, value := range map[string]string{"from": in.From, "to": in.To} {
		if value = strings.TrimSpace(value); value != "" {
			if _, err := time.Parse(time.DateOnly, value); err != nil {
				return "", fmt.Errorf("%s must be a date such as 2026-10-10", name)
			}
			query.Set(name, value)
		}
	}
	for name, allowed := range map[string][]string{"direction": {"in", "out"}, "status": {"booked", "pending"}, "source": {"manual", "csv", "bank"}} {
		value := map[string]string{"direction": in.Direction, "status": in.Status, "source": in.Source}[name]
		if value == "" {
			continue
		}
		ok := false
		for _, candidate := range allowed {
			ok = ok || value == candidate
		}
		if !ok {
			return "", fmt.Errorf("%s must be %s", name, strings.Join(allowed, " or "))
		}
		query.Set(name, value)
	}
	for name, value := range map[string]*int64{"amountMin": in.AmountMin, "amountMax": in.AmountMax} {
		if value != nil {
			query.Set(name, strconv.FormatInt(*value, 10))
		}
	}
	limit := in.Limit
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > 100 {
		return "", errors.New("limit must be between 1 and 100")
	}
	query.Set("limit", strconv.Itoa(limit))
	if in.Cursor != "" {
		query.Set("cursor", in.Cursor)
	}
	return query.Encode(), nil
}
