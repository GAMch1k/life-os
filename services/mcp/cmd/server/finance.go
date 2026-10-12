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

// aiCandidates asks which transactions the AI may categorize.
type aiCandidates struct {
	Limit int `json:"limit,omitempty" jsonschema:"1 to 100 (default 40)"`
}

// aiChoices are the AI's category choices for a batch of transactions.
type aiChoices struct {
	Choices []aiChoice `json:"choices" jsonschema:"1 to 100 choices, each transaction once"`
}

type aiChoice struct {
	TransactionID string  `json:"transactionId" jsonschema:"Finance transaction UUID (from finance_list_ai_category_candidates)"`
	CategoryID    *string `json:"categoryId" jsonschema:"Finance category UUID (from finance_list_categories), or null to leave the transaction without a category"`
}

// budgetPlanRequest asks Finance for an AI budget plan: the monthly
// schedule's.
type budgetPlanRequest struct {
	Month            string `json:"month,omitempty" jsonschema:"YYYY-MM; leave out for Finance's default: this month during its first 7 days, else the next"`
	SavingsGoalMinor *int64 `json:"savingsGoalMinor,omitempty" jsonschema:"How much to save a month, in minor units (cents)"`
}

// budgetPlanQuery lists AI budget plans.
type budgetPlanQuery struct {
	Month string `json:"month,omitempty" jsonschema:"Only this month's plans, YYYY-MM"`
	State string `json:"state,omitempty" jsonschema:"Only plans in this state: requested, running, ready, failed, applied, partly_applied, or dismissed"`
	Limit int    `json:"limit,omitempty" jsonschema:"1 to 50 (default 10)"`
}

// budgetPlanMonth names the month to plan.
type budgetPlanMonth struct {
	Month string `json:"month" jsonschema:"The month to plan, YYYY-MM"`
}

// budgetPlanClaim claims the next plan to run; it takes nothing.
type budgetPlanClaim struct{}

// budgetPlanResult is the AI's checked proposal for a claimed plan.
type budgetPlanResult struct {
	PlanID                string                 `json:"planId" jsonschema:"The plan's UUID (from finance_claim_budget_plan)"`
	Currency              string                 `json:"currency" jsonschema:"The plan's currency, such as EUR"`
	Headline              string                 `json:"headline" jsonschema:"One short sentence on the plan"`
	Lines                 []budgetPlanLine       `json:"lines" jsonschema:"An amount for every active expense category of the planning data"`
	Suggestions           []budgetPlanSuggestion `json:"suggestions" jsonschema:"2 to 5 ways to spend less"`
	ProjectedTotalMinor   int64                  `json:"projectedTotalMinor" jsonschema:"The sum of the lines"`
	IncomeEstimateMinor   int64                  `json:"incomeEstimateMinor" jsonschema:"The month's expected income"`
	ProjectedSavingsMinor int64                  `json:"projectedSavingsMinor" jsonschema:"The income estimate minus the projected total"`
}

type budgetPlanLine struct {
	CategoryID    string `json:"categoryId" jsonschema:"An active expense category's UUID (from finance_get_budget_planning_data)"`
	ProposedMinor int64  `json:"proposedMinor" jsonschema:"The amount for the month, 0 or more, in minor units"`
	Reason        string `json:"reason" jsonschema:"One sentence"`
}

type budgetPlanSuggestion struct {
	Title              string   `json:"title"`
	Detail             string   `json:"detail"`
	CategoryID         *string  `json:"categoryId" jsonschema:"The category the saving comes from (UUID), or null"`
	Merchants          []string `json:"merchants" jsonschema:"Names of that category's merchants in the planning data"`
	MonthlySavingMinor int64    `json:"monthlySavingMinor" jsonschema:"What it saves a month, in minor units"`
	Link               *string  `json:"link" jsonschema:"food_recipes to point to Food's recipes, or null"`
}

// budgetPlanFailure notes a failed run of a plan.
type budgetPlanFailure struct {
	PlanID  string `json:"planId" jsonschema:"The plan's UUID"`
	Error   string `json:"error" jsonschema:"What went wrong"`
	RetryAt string `json:"retryAt,omitempty" jsonschema:"RFC3339 time to try again; leave out to fail the plan for good"`
}

var (
	currencyCode = regexp.MustCompile(`^[A-Z]{3}$`)
	monthKey     = regexp.MustCompile(`^\d{4}-(0[1-9]|1[0-2])$`)
	planStates   = map[string]bool{"requested": true, "running": true, "ready": true, "failed": true, "applied": true, "partly_applied": true, "dismissed": true}
)

// addFinanceTools registers the Finance tools. Each is a Finance API
// operation, the same one the Dashboard uses.
func addFinanceTools(s *mcp.Server, hc *http.Client, baseURL string, emitter *activity.Emitter) {
	addReadTool(s, hc, "finance_month_summary", "Get the Finance summary of a month (month=YYYY-MM, optional currency and accountId): income, spending, net, the balance now (totalBalance, pending card payments included; totalBookedBalance and totalPending split it), and totals per category", baseURL, "/v1/summaries/monthly?")
	addReadTool(s, hc, "finance_list_accounts", "List Finance accounts with their balances (the built-in Cash account first; includeArchived=true for archived ones too). balance is the money there is to spend: bookedBalance plus pending, the card payments the bank has not booked yet", baseURL, "/v1/accounts?")
	addReadTool(s, hc, "finance_list_categories", "List Finance categories (expense, income, transfer)", baseURL, "/v1/categories?")
	addReadTool(s, hc, "finance_find_transactions", "List Finance transactions with a raw URL query (q, accountId, categoryId, from, to, direction, amountMin, amountMax, status, source, categorizedBy, limit, cursor); prefer finance_search_transactions", baseURL, "/v1/transactions?")
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
	mcp.AddTool(s, &mcp.Tool{Name: "finance_list_ai_category_candidates", Description: "List the Finance transactions the AI may categorize: booked, without a category nobody chose, and not looked at since their text changed, latest first, with how many there are (total) and the owner's latest choices by hand per counterparty as examples"}, func(ctx context.Context, req *mcp.CallToolRequest, in aiCandidates) (*mcp.CallToolResult, Output, error) {
		limit := in.Limit
		if limit == 0 {
			limit = 40
		}
		if limit < 1 || limit > 100 {
			return nil, Output{}, errors.New("limit must be between 1 and 100")
		}
		headers := http.Header{}
		traceOf(req).Apply(headers)
		body, err := coreclient.Client{BaseURL: baseURL, HTTP: hc}.DoWithHeaders(ctx, http.MethodGet, "/v1/categories/ai/candidates?limit="+strconv.Itoa(limit), nil, headers)
		if err != nil {
			return nil, Output{}, err
		}
		out, err := output(body)
		return nil, out, err
	})
	mcp.AddTool(s, &mcp.Tool{Name: "finance_set_ai_categories", Description: "Give Finance transactions the AI's categories (categorizedBy ai), or with categoryId null note that the AI left one without a category. A choice applies only while no one else chose: a choice by hand, a rule, or a merchant category code wins, and rules replace the AI's choice later. Money out goes only to expense or transfer categories, money in only to income or transfer categories. Answers each choice's outcome"}, func(ctx context.Context, req *mcp.CallToolRequest, in aiChoices) (*mcp.CallToolResult, Output, error) {
		return audited(ctx, emitter, req, "finance_set_ai_categories", in, func(trace activity.Trace) (*mcp.CallToolResult, Output, error) {
			body, err := aiChoicesBody(in)
			if err != nil {
				return nil, Output{}, err
			}
			return post(ctx, hc, trace, baseURL, "/v1/categories/ai/choices", body)
		})
	})
	addBudgetPlanTools(s, hc, baseURL, emitter)
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

// aiChoicesBody is the Finance body for the AI's choices, checked before
// Finance is called: 1 to 100 choices with UUIDs.
func aiChoicesBody(in aiChoices) (map[string]any, error) {
	if len(in.Choices) == 0 || len(in.Choices) > 100 {
		return nil, errors.New("choices must have 1 to 100 choices")
	}
	choices := make([]map[string]any, 0, len(in.Choices))
	for _, choice := range in.Choices {
		transaction, err := uuid.Parse(strings.TrimSpace(choice.TransactionID))
		if err != nil {
			return nil, errors.New("transactionId must be a Finance transaction UUID")
		}
		entry := map[string]any{"transactionId": transaction.String(), "categoryId": nil}
		if choice.CategoryID != nil && strings.TrimSpace(*choice.CategoryID) != "" {
			category, err := uuid.Parse(strings.TrimSpace(*choice.CategoryID))
			if err != nil {
				return nil, errors.New("categoryId must be a Finance category UUID or null")
			}
			entry["categoryId"] = category.String()
		}
		choices = append(choices, entry)
	}
	return map[string]any{"choices": choices}, nil
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

// addBudgetPlanTools registers the AI budget plan job's tools (P13-09):
// two reads and four audited writes, never planned by a capture.
func addBudgetPlanTools(s *mcp.Server, hc *http.Client, baseURL string, emitter *activity.Emitter) {
	get := func(ctx context.Context, req *mcp.CallToolRequest, path string) (*mcp.CallToolResult, Output, error) {
		headers := http.Header{}
		traceOf(req).Apply(headers)
		body, err := coreclient.Client{BaseURL: baseURL, HTTP: hc}.DoWithHeaders(ctx, http.MethodGet, path, nil, headers)
		if err != nil {
			return nil, Output{}, err
		}
		out, err := output(body)
		return nil, out, err
	}
	mcp.AddTool(s, &mcp.Tool{Name: "finance_list_budget_plans", Description: "List the AI's budget plans, newest first, of a month and in a state when given, with how many wait for the AI budget plan job (waiting)"}, func(ctx context.Context, req *mcp.CallToolRequest, in budgetPlanQuery) (*mcp.CallToolResult, Output, error) {
		query := url.Values{}
		if in.Month != "" {
			if !monthKey.MatchString(in.Month) {
				return nil, Output{}, errors.New("month must be YYYY-MM")
			}
			query.Set("month", in.Month)
		}
		if in.State != "" {
			if !planStates[in.State] {
				return nil, Output{}, errors.New("state must be requested, running, ready, failed, applied, partly_applied, or dismissed")
			}
			query.Set("state", in.State)
		}
		limit := in.Limit
		if limit == 0 {
			limit = 10
		}
		if limit < 1 || limit > 50 {
			return nil, Output{}, errors.New("limit must be between 1 and 50")
		}
		query.Set("limit", strconv.Itoa(limit))
		return get(ctx, req, "/v1/ai-budget-plans?"+query.Encode())
	})
	mcp.AddTool(s, &mcp.Tool{Name: "finance_get_budget_planning_data", Description: "Get what a month's budget is planned from, aggregates only: up to 12 complete months of income and spending, per active expense category its spending per month, median, last month, trend, planned amount, and top merchants, the plan that applies, confirmed subscriptions, and the balance now"}, func(ctx context.Context, req *mcp.CallToolRequest, in budgetPlanMonth) (*mcp.CallToolResult, Output, error) {
		if !monthKey.MatchString(in.Month) {
			return nil, Output{}, errors.New("month must be YYYY-MM")
		}
		return get(ctx, req, "/v1/budgets/"+in.Month+"/planning-data")
	})
	mcp.AddTool(s, &mcp.Tool{Name: "finance_request_budget_plan", Description: "Ask for the monthly AI budget plan of a month (trigger schedule): one per month, and a repeat answers with it"}, func(ctx context.Context, req *mcp.CallToolRequest, in budgetPlanRequest) (*mcp.CallToolResult, Output, error) {
		return audited(ctx, emitter, req, "finance_request_budget_plan", in, func(trace activity.Trace) (*mcp.CallToolResult, Output, error) {
			body := map[string]any{"trigger": "schedule"}
			if in.Month != "" {
				if !monthKey.MatchString(in.Month) {
					return nil, Output{}, errors.New("month must be YYYY-MM")
				}
				body["month"] = in.Month
			}
			if in.SavingsGoalMinor != nil {
				if *in.SavingsGoalMinor < 0 {
					return nil, Output{}, errors.New("savingsGoalMinor must not be negative")
				}
				body["savingsGoalMinor"] = *in.SavingsGoalMinor
			}
			return post(ctx, hc, trace, baseURL, "/v1/ai-budget-plans", body)
		})
	})
	mcp.AddTool(s, &mcp.Tool{Name: "finance_claim_budget_plan", Description: "Claim the oldest AI budget plan waiting to run (it becomes running); plan is null when none waits or another runs"}, func(ctx context.Context, req *mcp.CallToolRequest, in budgetPlanClaim) (*mcp.CallToolResult, Output, error) {
		return audited(ctx, emitter, req, "finance_claim_budget_plan", in, func(trace activity.Trace) (*mcp.CallToolResult, Output, error) {
			return post(ctx, hc, trace, baseURL, "/v1/ai-budget-plans/claim", nil)
		})
	})
	mcp.AddTool(s, &mcp.Tool{Name: "finance_set_budget_plan_result", Description: "Leave the AI's proposal for a claimed budget plan; Finance checks it against the month's planning data (every active expense category, amounts of 0 or more, savings no larger than the spending they come from, totals that add up) and keeps it for the owner to apply or dismiss"}, func(ctx context.Context, req *mcp.CallToolRequest, in budgetPlanResult) (*mcp.CallToolResult, Output, error) {
		// The audit names the plan and counts; the proposal itself is
		// Finance's to keep.
		summary := map[string]any{"planId": in.PlanID, "lines": len(in.Lines), "suggestions": len(in.Suggestions), "projectedTotalMinor": in.ProjectedTotalMinor}
		return audited(ctx, emitter, req, "finance_set_budget_plan_result", summary, func(trace activity.Trace) (*mcp.CallToolResult, Output, error) {
			id, body, err := budgetPlanResultBody(in)
			if err != nil {
				return nil, Output{}, err
			}
			return put(ctx, hc, trace, baseURL, "/v1/ai-budget-plans/"+id+"/result", body)
		})
	})
	mcp.AddTool(s, &mcp.Tool{Name: "finance_fail_budget_plan", Description: "Note that a run of an AI budget plan failed: with retryAt it waits until then, without it fails for good"}, func(ctx context.Context, req *mcp.CallToolRequest, in budgetPlanFailure) (*mcp.CallToolResult, Output, error) {
		return audited(ctx, emitter, req, "finance_fail_budget_plan", in, func(trace activity.Trace) (*mcp.CallToolResult, Output, error) {
			id, err := uuid.Parse(strings.TrimSpace(in.PlanID))
			if err != nil {
				return nil, Output{}, errors.New("planId must be a plan UUID")
			}
			if strings.TrimSpace(in.Error) == "" {
				return nil, Output{}, errors.New("error is required")
			}
			body := map[string]any{"error": in.Error}
			if in.RetryAt != "" {
				at, err := time.Parse(time.RFC3339, in.RetryAt)
				if err != nil {
					return nil, Output{}, errors.New("retryAt must be an RFC3339 time")
				}
				body["retryAt"] = at.UTC().Format(time.RFC3339)
			}
			return post(ctx, hc, trace, baseURL, "/v1/ai-budget-plans/"+id.String()+"/failure", body)
		})
	})
}

// budgetPlanResultBody is the plan's id and the Finance body of a proposal,
// its UUIDs and counts checked before Finance is called.
func budgetPlanResultBody(in budgetPlanResult) (string, map[string]any, error) {
	id, err := uuid.Parse(strings.TrimSpace(in.PlanID))
	if err != nil {
		return "", nil, errors.New("planId must be a plan UUID")
	}
	if len(in.Lines) > 500 || len(in.Suggestions) > 20 {
		return "", nil, errors.New("a proposal has at most 500 lines and 20 suggestions")
	}
	lines := make([]map[string]any, 0, len(in.Lines))
	for _, line := range in.Lines {
		category, err := uuid.Parse(strings.TrimSpace(line.CategoryID))
		if err != nil {
			return "", nil, errors.New("a line's categoryId must be a category UUID")
		}
		lines = append(lines, map[string]any{"categoryId": category.String(), "proposedMinor": line.ProposedMinor, "reason": line.Reason})
	}
	suggestions := make([]map[string]any, 0, len(in.Suggestions))
	for _, suggestion := range in.Suggestions {
		entry := map[string]any{"title": suggestion.Title, "detail": suggestion.Detail, "categoryId": nil, "merchants": suggestion.Merchants,
			"monthlySavingMinor": suggestion.MonthlySavingMinor, "link": suggestion.Link}
		if suggestion.Merchants == nil {
			entry["merchants"] = []string{}
		}
		if suggestion.CategoryID != nil && strings.TrimSpace(*suggestion.CategoryID) != "" {
			category, err := uuid.Parse(strings.TrimSpace(*suggestion.CategoryID))
			if err != nil {
				return "", nil, errors.New("a suggestion's categoryId must be a category UUID or null")
			}
			entry["categoryId"] = category.String()
		}
		suggestions = append(suggestions, entry)
	}
	return id.String(), map[string]any{"currency": strings.ToUpper(strings.TrimSpace(in.Currency)), "headline": in.Headline, "lines": lines,
		"suggestions": suggestions, "projectedTotalMinor": in.ProjectedTotalMinor, "incomeEstimateMinor": in.IncomeEstimateMinor,
		"projectedSavingsMinor": in.ProjectedSavingsMinor}, nil
}
