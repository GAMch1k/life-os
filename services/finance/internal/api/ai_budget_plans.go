package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	googleuuid "github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/oapi-codegen/runtime/types"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/finance/internal/api/generated"
	"github.com/GAMch1k/life-os/services/finance/internal/budget"
	financemoney "github.com/GAMch1k/life-os/services/finance/internal/money"
	store "github.com/GAMch1k/life-os/services/finance/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/finance/internal/subscriptions"
)

// AI budget plans (P13-09). The owner asks the AI to plan a month, and the
// processor's AI budget plan job asks on the 1st of each month too; Finance
// keeps each plan durably from the request on, so a restarted processor
// loses nothing. The job claims a plan, reads the month's planning data
// (aggregates, never transactions), and leaves the AI's proposal, which
// Finance checks against the same data before keeping it with its own
// evidence. Nothing changes the budget until the owner applies a plan,
// through the same save as the plan editor.

const (
	// aiPlanLease is how long a claimed plan runs before another claim may
	// take it over (the processor stopped during the run): well beyond a
	// Codex run's deadline.
	aiPlanLease = 15 * time.Minute
	// aiPlanRequestsPerDay bounds the owner's requests of a day (a
	// development tool's are not counted).
	aiPlanRequestsPerDay = 10
	// aiPlanMonthsAhead is how far ahead a plan may be asked for.
	aiPlanMonthsAhead   = 12
	aiPlansDefaultLimit = 10
	aiPlansMaxLimit     = 50
	// maxPlanError bounds a failed run's error.
	maxPlanError = 500
)

// The plan states as stored.
const (
	planRequested     = "requested"
	planRunning       = "running"
	planReady         = "ready"
	planFailed        = "failed"
	planApplied       = "applied"
	planPartlyApplied = "partly_applied"
	planDismissed     = "dismissed"
)

// appliedRecord is what the owner applied last, as stored.
type appliedRecord struct {
	At             time.Time     `json:"at"`
	Scope          string        `json:"scope"`
	Lines          []appliedLine `json:"lines"`
	ExpectedIncome *int64        `json:"expectedIncomeMinor"`
	SavingsGoal    *int64        `json:"savingsGoalMinor"`
}

type appliedLine struct {
	CategoryID string `json:"categoryId"`
	Amount     int64  `json:"amountMinor"`
}

// sameChoice reports whether two applications chose the same.
func (a appliedRecord) sameChoice(b appliedRecord) bool {
	return a.Scope == b.Scope && slices.Equal(a.Lines, b.Lines) && equalPointer(a.ExpectedIncome, b.ExpectedIncome) && equalPointer(a.SavingsGoal, b.SavingsGoal)
}

// key identifies the choice, for the Activity event of applying it.
func (a appliedRecord) key() string {
	a.At = time.Time{}
	raw, _ := json.Marshal(a)
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("%x", sum[:16])
}

func equalPointer(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// refuseOwner refuses the owner an operation that is the AI's job's.
func refuseOwner(c fiber.Ctx, message string) error {
	if actor, ok := activity.ParseActor(c.Get(activity.ActorHeader)); ok && actor.Kind == "user" {
		return apiError(fiber.StatusForbidden, "actor_not_allowed", message)
	}
	return nil
}

// Planning data.

func (h *Handler) GetBudgetPlanningData(c fiber.Ctx, month generated.Month) error {
	parsed, err := parseBudgetMonth(month)
	if err != nil {
		return err
	}
	data, err := h.planningData(c.Context(), h.queries, parsed)
	if err != nil {
		return internal(err)
	}
	return c.JSON(planningView(data))
}

// planningData reads and computes a month's planning data in the budget
// currency.
func (h *Handler) planningData(ctx context.Context, q *store.Queries, month time.Time) (budget.PlanningData, error) {
	currency := h.defaultCurrency
	in := budget.PlanningInput{Month: month, Current: budget.CurrentMonth(h.now(), h.location), Currency: currency, Exponent: financemoney.Exponent(currency)}
	start, err := q.GetHistoryStart(ctx, currency)
	if err != nil {
		return budget.PlanningData{}, err
	}
	if start.Valid {
		day := start.Time
		in.HistoryStart = &day
	}
	from, to := budget.PlanningWindow(month, in.Current, in.HistoryStart)
	if from.Before(to) {
		rows, err := q.GetBudgetHistory(ctx, store.GetBudgetHistoryParams{Currency: currency, FromMonth: budget.Date(from), ToMonth: budget.Date(to)})
		if err != nil {
			return budget.PlanningData{}, err
		}
		for _, row := range rows {
			item := budget.HistoryRow{Month: row.Month.Time, Out: row.OutMinor, In: row.InMinor}
			if row.CategoryID.Valid {
				id := row.CategoryID.Bytes
				item.CategoryID = &id
			}
			in.History = append(in.History, item)
		}
		merchantFrom, merchantTo := budget.MerchantWindow(month, in.Current, in.HistoryStart)
		spending, err := q.ListPlanningSpending(ctx, store.ListPlanningSpendingParams{Currency: currency, FromDay: budget.Date(merchantFrom), ToDay: budget.Date(merchantTo)})
		if err != nil {
			return budget.PlanningData{}, err
		}
		for _, row := range spending {
			in.Spending = append(in.Spending, budget.SpendingRow{CategoryID: row.CategoryID.Bytes, Amount: -row.AmountMinor,
				Counterparty: deref(row.Counterparty), Description: deref(row.Description)})
		}
	}
	categories, err := q.ListFinancialCategories(ctx, true)
	if err != nil {
		return budget.PlanningData{}, err
	}
	in.Categories = budget.Categories(categories)
	if in.Plan, err = budget.LoadPlan(ctx, q, month); err != nil {
		return budget.PlanningData{}, err
	}
	confirmed := "confirmed"
	subs, err := q.ListSubscriptions(ctx, &confirmed)
	if err != nil {
		return budget.PlanningData{}, err
	}
	for _, sub := range subs {
		if sub.Currency != currency {
			continue
		}
		item := budget.PlanningSubscription{Name: sub.Name, Monthly: subscriptions.MonthlyEquivalent(sub.AmountMinor, sub.Cadence)}
		if sub.CategoryID.Valid {
			id := sub.CategoryID.Bytes
			item.CategoryID = &id
		}
		in.Subscriptions = append(in.Subscriptions, item)
	}
	accounts, err := q.ListAccountsWithBalance(ctx, store.ListAccountsWithBalanceParams{IncludeArchived: true})
	if err != nil {
		return budget.PlanningData{}, err
	}
	for _, account := range accounts {
		if account.Currency == currency {
			in.Balance += account.BookedMinor + account.PendingMinor
		}
	}
	return budget.Planning(in), nil
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func monthKeys(months []time.Time) []string {
	keys := make([]string, 0, len(months))
	for _, month := range months {
		keys = append(keys, budget.MonthKey(month))
	}
	return keys
}

func planningView(data budget.PlanningData) generated.BudgetPlanningData {
	view := generated.BudgetPlanningData{Month: budget.MonthKey(data.Month), Currency: data.Currency, Months: monthKeys(data.Months),
		FirstMonthPartial: data.FirstMonthPartial, FullMonths: data.FullMonths, Rough: data.Rough, MedianMonths: monthKeys(data.MedianMonths),
		MerchantMonths: data.MerchantMonths, IncomeMinor: data.Income, IncomeMedianMinor: data.IncomeMedian, SpendingMinor: data.Spending,
		UncategorizedMinor: data.Uncategorized, Categories: make([]generated.BudgetPlanningCategory, 0, len(data.Categories)),
		Subscriptions: make([]generated.BudgetPlanningSubscription, 0, len(data.Subscriptions)), BalanceMinor: data.Balance}
	for _, category := range data.Categories {
		item := generated.BudgetPlanningCategory{CategoryId: types.UUID(category.Category.ID), Name: category.Category.Name, MonthlyMinor: category.Monthly,
			MedianMinor: category.Median, LastMonthMinor: category.LastMonth, TrendPercent: category.TrendPercent, PlannedMinor: category.Planned,
			Merchants: make([]generated.BudgetPlanningMerchant, 0, len(category.Merchants))}
		if category.Trend != "" {
			item.Trend = pointer(generated.BudgetPlanningCategoryTrend(category.Trend))
		}
		for _, merchant := range category.Merchants {
			item.Merchants = append(item.Merchants, generated.BudgetPlanningMerchant{Name: merchant.Name, Count: merchant.Count,
				TotalMinor: merchant.Total, MonthlyMinor: merchant.Monthly})
		}
		view.Categories = append(view.Categories, item)
	}
	if plan := data.Plan; plan != nil {
		source := generated.BudgetPlanSourceRegular
		if plan.Scope == budget.ScopeMonth {
			source = generated.BudgetPlanSourceMonth
		}
		var planned int64
		for _, amount := range plan.Lines {
			planned += amount
		}
		view.Plan = &generated.BudgetPlanningPlan{Source: source, ExpectedIncomeMinor: plan.ExpectedIncome, SavingsGoalMinor: plan.SavingsGoal, PlannedMinor: planned}
	}
	for _, sub := range data.Subscriptions {
		item := generated.BudgetPlanningSubscription{Name: sub.Name, MonthlyMinor: sub.Monthly}
		if sub.CategoryID != nil {
			item.CategoryId = pointer(types.UUID(*sub.CategoryID))
		}
		view.Subscriptions = append(view.Subscriptions, item)
	}
	return view
}

// Plans.

func (h *Handler) ListAiBudgetPlans(c fiber.Ctx, params generated.ListAiBudgetPlansParams) error {
	query := store.ListAIBudgetPlansParams{PageLimit: aiPlansDefaultLimit}
	if params.Month != nil {
		month, err := budget.ParseMonth(*params.Month)
		if err != nil {
			return invalid(err.Error())
		}
		query.Month = budget.Date(month)
	}
	if params.State != nil {
		if !params.State.Valid() {
			return invalid("state must be requested, running, ready, failed, applied, partly_applied, or dismissed")
		}
		state := string(*params.State)
		query.State = &state
	}
	if params.Limit != nil {
		if *params.Limit < 1 || *params.Limit > aiPlansMaxLimit {
			return invalid("limit must be between 1 and 50")
		}
		query.PageLimit = int32(*params.Limit)
	}
	ctx := c.Context()
	rows, err := h.queries.ListAIBudgetPlans(ctx, query)
	if err != nil {
		return internal(err)
	}
	now := h.now()
	waiting, err := h.queries.CountWaitingAIBudgetPlans(ctx, store.CountWaitingAIBudgetPlansParams{Now: timestamp(now), LapsedBefore: timestamp(now.Add(-aiPlanLease))})
	if err != nil {
		return internal(err)
	}
	result := generated.AiBudgetPlanList{Items: make([]generated.AiBudgetPlan, 0, len(rows)), Waiting: int(waiting)}
	for _, row := range rows {
		view, err := planView(row)
		if err != nil {
			return internal(err)
		}
		result.Items = append(result.Items, view)
	}
	return c.JSON(result)
}

func (h *Handler) GetAiBudgetPlan(c fiber.Ctx, planID generated.PlanId) error {
	row, err := h.queries.GetAIBudgetPlan(c.Context(), pgUUID(planID))
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound("AI budget plan")
	}
	if err != nil {
		return internal(err)
	}
	return h.sendPlan(c, fiber.StatusOK, row)
}

func (h *Handler) sendPlan(c fiber.Ctx, status int, row store.AiBudgetPlan) error {
	view, err := planView(row)
	if err != nil {
		return internal(err)
	}
	return c.Status(status).JSON(view)
}

func (h *Handler) RequestAiBudgetPlan(c fiber.Ctx) error {
	var body generated.RequestAiBudgetPlanRequest
	if _, err := decodeObject(c.Body(), &body); err != nil {
		return invalid(err.Error())
	}
	now := h.now()
	current := budget.CurrentMonth(now, h.location)
	month := budget.DefaultPlanMonth(now, h.location)
	if body.Month != nil && strings.TrimSpace(*body.Month) != "" {
		parsed, err := budget.ParseMonth(strings.TrimSpace(*body.Month))
		if err != nil {
			return invalid(err.Error())
		}
		month = parsed
	}
	if month.Before(current) || month.After(budget.AddMonths(current, aiPlanMonthsAhead)) {
		return invalid(fmt.Sprintf("a plan is for this month or one of the next %d", aiPlanMonthsAhead))
	}
	if body.SavingsGoalMinor != nil && *body.SavingsGoalMinor < 0 {
		return invalid("savingsGoalMinor must not be negative")
	}
	trigger := generated.AiBudgetPlanTriggerRequest
	if body.Trigger != nil {
		if !body.Trigger.Valid() {
			return invalid("trigger must be request or schedule")
		}
		trigger = generated.AiBudgetPlanTrigger(*body.Trigger)
	}
	actor, _ := activity.ParseActor(c.Get(activity.ActorHeader))
	ctx := c.Context()
	var row store.AiBudgetPlan
	created := false
	err := h.inTx(ctx, func(q *store.Queries) error {
		if err := q.LockAIBudgetPlans(ctx); err != nil {
			return err
		}
		var err error
		if trigger == generated.AiBudgetPlanTriggerSchedule {
			// One scheduled plan a month; none when the salary asked for
			// one (that did not fail), or the owner did since the month
			// began.
			row, err = q.GetScheduledAIBudgetPlan(ctx, budget.Date(month))
			if err == nil || !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			row, err = q.GetSalaryAIBudgetPlan(ctx, budget.Date(month))
			if err == nil || !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			latest, err := q.ListAIBudgetPlans(ctx, store.ListAIBudgetPlansParams{Month: budget.Date(month), PageLimit: 1})
			if err != nil {
				return err
			}
			began := time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, h.location)
			if len(latest) > 0 && !latest[0].CreatedAt.Time.Before(began) && latest[0].State != planDismissed {
				row = latest[0]
				return nil
			}
		} else {
			row, err = q.GetActiveAIBudgetPlan(ctx, budget.Date(month))
			if err == nil || !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			local := now.In(h.location)
			midnight := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, h.location)
			count, err := q.CountAIBudgetPlanRequestsSince(ctx, timestamp(midnight))
			if err != nil {
				return err
			}
			if actor.Kind == "user" && count >= aiPlanRequestsPerDay {
				return apiError(fiber.StatusTooManyRequests, "daily_limit", fmt.Sprintf("The AI plans a budget %d times a day: ask again tomorrow.", aiPlanRequestsPerDay))
			}
		}
		params := store.CreateAIBudgetPlanParams{Month: budget.Date(month), Currency: h.defaultCurrency, SavingsGoalMinor: body.SavingsGoalMinor,
			Trigger: string(trigger), RequestedByKind: actor.Kind}
		if actor.Name != "" {
			params.RequestedByName = &actor.Name
		}
		row, err = q.CreateAIBudgetPlan(ctx, params)
		created = err == nil
		return err
	})
	if err != nil {
		return passThrough(err)
	}
	if !created {
		return h.sendPlan(c, fiber.StatusOK, row)
	}
	h.recordPlanRequested(c, row)
	return h.sendPlan(c, fiber.StatusAccepted, row)
}

func (h *Handler) ClaimAiBudgetPlan(c fiber.Ctx) error {
	if err := refuseOwner(c, "Plans are run by the AI budget plan job"); err != nil {
		return err
	}
	ctx := c.Context()
	now := h.now()
	var claimed *store.AiBudgetPlan
	err := h.inTx(ctx, func(q *store.Queries) error {
		if err := q.LockAIBudgetPlans(ctx); err != nil {
			return err
		}
		lapsed := timestamp(now.Add(-aiPlanLease))
		running, err := q.AIBudgetPlanRunning(ctx, lapsed)
		if err != nil || running {
			return err
		}
		row, err := q.ClaimAIBudgetPlan(ctx, store.ClaimAIBudgetPlanParams{Now: timestamp(now), LapsedBefore: lapsed})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		claimed = &row
		return err
	})
	if err != nil {
		return internal(err)
	}
	result := generated.AiBudgetPlanClaim{}
	if claimed != nil {
		view, err := planView(*claimed)
		if err != nil {
			return internal(err)
		}
		result.Plan = &view
	}
	return c.JSON(result)
}

// runningPlan locks a plan that is requested or running, for its job's
// result or failure.
func runningPlan(ctx context.Context, q *store.Queries, id types.UUID) (store.AiBudgetPlan, error) {
	row, err := q.GetAIBudgetPlanForUpdate(ctx, pgUUID(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return row, notFound("AI budget plan")
	}
	if err != nil {
		return row, err
	}
	if row.State != planRequested && row.State != planRunning {
		return row, apiError(fiber.StatusConflict, "plan_not_running", "The plan is "+strings.ReplaceAll(row.State, "_", " ")+", not requested or running")
	}
	return row, nil
}

func (h *Handler) SetAiBudgetPlanResult(c fiber.Ctx, planID generated.PlanId) error {
	if err := refuseOwner(c, "The AI's proposals come from the AI budget plan job"); err != nil {
		return err
	}
	var body generated.AiBudgetPlanProposal
	properties, err := decodeObject(c.Body(), &body)
	if err != nil {
		return invalid(err.Error())
	}
	for _, name := range []string{"currency", "headline", "lines", "suggestions", "projectedTotalMinor", "incomeEstimateMinor", "projectedSavingsMinor"} {
		if _, ok := properties[name]; !ok || isNull(properties, name) {
			return invalid(name + " is required")
		}
	}
	if len(body.Lines) > 500 || len(body.Suggestions) > 20 {
		return invalid("a proposal has at most 500 lines and 20 suggestions")
	}
	proposal := budget.Proposal{Currency: body.Currency, Headline: body.Headline, ProjectedTotal: body.ProjectedTotalMinor,
		IncomeEstimate: body.IncomeEstimateMinor, ProjectedSavings: body.ProjectedSavingsMinor}
	for _, line := range body.Lines {
		proposal.Lines = append(proposal.Lines, budget.ProposedLine{CategoryID: [16]byte(line.CategoryId), Amount: line.ProposedMinor, Reason: line.Reason})
	}
	for _, item := range body.Suggestions {
		suggestion := budget.ProposedSuggestion{Title: item.Title, Detail: item.Detail, Merchants: item.Merchants, MonthlySaving: item.MonthlySavingMinor}
		if item.CategoryId != nil {
			id := [16]byte(*item.CategoryId)
			suggestion.CategoryID = &id
		}
		if item.Link != nil {
			suggestion.Link = *item.Link
		}
		proposal.Suggestions = append(proposal.Suggestions, suggestion)
	}
	ctx := c.Context()
	var row store.AiBudgetPlan
	err = h.inTx(ctx, func(q *store.Queries) error {
		plan, err := runningPlan(ctx, q, planID)
		if err != nil {
			return err
		}
		data, err := h.planningData(ctx, q, plan.Month.Time)
		if err != nil {
			return err
		}
		result, err := budget.CheckProposal(data, proposal, plan.SavingsGoalMinor)
		if budget.IsProposalError(err) {
			return apiError(fiber.StatusBadRequest, "invalid_plan", "The proposal does not pass the checks: "+err.Error())
		}
		if err != nil {
			return err
		}
		raw, err := json.Marshal(result)
		if err != nil {
			return err
		}
		row, err = q.SetAIBudgetPlanResult(ctx, store.SetAIBudgetPlanResultParams{Result: raw, Now: timestamp(h.now()), ID: plan.ID})
		return err
	})
	if err != nil {
		return passThrough(err)
	}
	h.recordPlanCreated(c, row)
	// A plan the owner did not ask for is notified (budget.Alerts).
	h.budgetChanged()
	return h.sendPlan(c, fiber.StatusOK, row)
}

func (h *Handler) FailAiBudgetPlan(c fiber.Ctx, planID generated.PlanId) error {
	if err := refuseOwner(c, "Plans are run by the AI budget plan job"); err != nil {
		return err
	}
	var body generated.AiBudgetPlanFailure
	if _, err := decodeObject(c.Body(), &body); err != nil {
		return invalid(err.Error())
	}
	message := strings.TrimSpace(body.Error)
	if message == "" {
		return invalid("error is required")
	}
	message = activity.Preview(message, maxPlanError)
	ctx := c.Context()
	var row store.AiBudgetPlan
	err := h.inTx(ctx, func(q *store.Queries) error {
		plan, err := runningPlan(ctx, q, planID)
		if err != nil {
			return err
		}
		params := store.FailAIBudgetPlanParams{Error: message, Now: timestamp(h.now()), ID: plan.ID}
		if body.RetryAt != nil {
			params.RetryAt = timestamp(*body.RetryAt)
		}
		row, err = q.FailAIBudgetPlan(ctx, params)
		return err
	})
	if err != nil {
		return passThrough(err)
	}
	if row.State == planFailed {
		h.recordPlanFailed(c, row)
	}
	return h.sendPlan(c, fiber.StatusOK, row)
}

func (h *Handler) ApplyAiBudgetPlan(c fiber.Ctx, planID generated.PlanId) error {
	// Only the owner accepts a plan (a development tool may, for tests);
	// the AI never applies its own.
	if actor, ok := activity.ParseActor(c.Get(activity.ActorHeader)); ok && actor.Kind == "agent" {
		return apiError(fiber.StatusForbidden, "actor_not_allowed", "Only the owner applies an AI budget plan")
	}
	var body generated.ApplyAiBudgetPlanRequest
	properties, err := decodeObject(c.Body(), &body)
	if err != nil {
		return invalid(err.Error())
	}
	if _, ok := properties["lines"]; !ok || len(body.Lines) == 0 || len(body.Lines) > 500 {
		return invalid("lines must have 1 to 500 lines")
	}
	scope := generated.BudgetScopeMonth
	if body.Scope != nil {
		switch *body.Scope {
		case generated.BudgetScopeMonth, generated.BudgetScopeFollowing:
			scope = *body.Scope
		default:
			return invalid("scope must be month or following")
		}
	}
	for name, value := range map[string]*int64{"expectedIncomeMinor": body.ExpectedIncomeMinor, "savingsGoalMinor": body.SavingsGoalMinor} {
		if value != nil && *value < 0 {
			return invalid(name + " must not be negative")
		}
	}
	record := appliedRecord{Scope: string(scope), ExpectedIncome: body.ExpectedIncomeMinor, SavingsGoal: body.SavingsGoalMinor, Lines: make([]appliedLine, 0, len(body.Lines))}
	chosen := map[[16]byte]int64{}
	for _, line := range body.Lines {
		if line.AmountMinor < 0 {
			return invalid("a line's amountMinor must not be negative")
		}
		id := [16]byte(line.CategoryId)
		if _, seen := chosen[id]; seen {
			return invalid("category " + line.CategoryId.String() + " is named twice")
		}
		chosen[id] = line.AmountMinor
		record.Lines = append(record.Lines, appliedLine{CategoryID: line.CategoryId.String(), Amount: line.AmountMinor})
	}
	slices.SortFunc(record.Lines, func(a, b appliedLine) int { return strings.Compare(a.CategoryID, b.CategoryID) })
	ctx := c.Context()
	var (
		row       store.AiBudgetPlan
		change    planChange
		changed   bool
		repeated  bool
		view      generated.BudgetMonth
		result    budget.PlanResult
		planLines int
	)
	err = h.inTx(ctx, func(q *store.Queries) error {
		var err error
		row, err = q.GetAIBudgetPlanForUpdate(ctx, pgUUID(planID))
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound("AI budget plan")
		}
		if err != nil {
			return err
		}
		switch row.State {
		case planReady, planApplied, planPartlyApplied:
		case planDismissed:
			return apiError(fiber.StatusConflict, "plan_dismissed", "The plan was dismissed")
		default:
			return apiError(fiber.StatusConflict, "plan_not_ready", "The plan is "+row.State+", not ready to apply")
		}
		if err := json.Unmarshal(row.Result, &result); err != nil {
			return err
		}
		planLines = len(result.Lines)
		inPlan := map[string]bool{}
		for _, line := range result.Lines {
			inPlan[line.CategoryID] = true
		}
		for _, line := range record.Lines {
			if !inPlan[line.CategoryID] {
				return invalid("category " + line.CategoryID + " is not in the plan")
			}
		}
		month := row.Month.Time
		if len(row.Applied) > 0 {
			var before appliedRecord
			if err := json.Unmarshal(row.Applied, &before); err != nil {
				return err
			}
			if before.sameChoice(record) {
				// The same choice again changes nothing.
				repeated = true
				view, err = h.budgetView(ctx, q, month)
				return err
			}
		}
		current, err := budget.LoadPlan(ctx, q, month)
		if err != nil {
			return err
		}
		lines := map[[16]byte]int64{}
		var income, goal int64
		if current != nil {
			for id, amount := range current.Lines {
				lines[id] = amount
			}
			income, goal = current.ExpectedIncome, current.SavingsGoal
		}
		for id, amount := range chosen {
			lines[id] = amount
		}
		if body.ExpectedIncomeMinor != nil {
			income = *body.ExpectedIncomeMinor
		}
		if body.SavingsGoalMinor != nil {
			goal = *body.SavingsGoalMinor
		}
		if change, changed, view, err = h.savePlan(ctx, q, month, scope, income, goal, lines); err != nil {
			return err
		}
		state := planApplied
		if len(record.Lines) < planLines {
			state = planPartlyApplied
		}
		record.At = h.now().UTC()
		raw, err := json.Marshal(record)
		if err != nil {
			return err
		}
		row, err = q.ApplyAIBudgetPlan(ctx, store.ApplyAIBudgetPlanParams{State: state, Applied: raw, Now: timestamp(record.At), ID: row.ID})
		return err
	})
	if err != nil {
		return passThrough(err)
	}
	if changed {
		h.recordBudgetSaved(c, change)
		h.budgetChanged()
	}
	if !repeated {
		h.recordPlanApplied(c, row, record, planLines)
	}
	plan, err := planView(row)
	if err != nil {
		return internal(err)
	}
	return c.JSON(generated.AiBudgetPlanApplyResult{Plan: plan, Budget: view})
}

func (h *Handler) DismissAiBudgetPlan(c fiber.Ctx, planID generated.PlanId) error {
	ctx := c.Context()
	var row store.AiBudgetPlan
	dismissed := false
	err := h.inTx(ctx, func(q *store.Queries) error {
		var err error
		row, err = q.GetAIBudgetPlanForUpdate(ctx, pgUUID(planID))
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound("AI budget plan")
		}
		if err != nil || row.State == planDismissed {
			return err
		}
		row, err = q.DismissAIBudgetPlan(ctx, store.DismissAIBudgetPlanParams{Now: timestamp(h.now()), ID: row.ID})
		dismissed = err == nil
		return err
	})
	if err != nil {
		return passThrough(err)
	}
	if dismissed {
		h.recordPlanDismissed(c, row)
	}
	return h.sendPlan(c, fiber.StatusOK, row)
}

// planView is a stored plan as the API shows it.
func planView(row store.AiBudgetPlan) (generated.AiBudgetPlan, error) {
	currency := row.Currency
	view := generated.AiBudgetPlan{Id: uuid(row.ID), Month: budget.MonthKey(row.Month.Time), Currency: currency,
		Trigger: generated.AiBudgetPlanTrigger(row.Trigger), State: generated.AiBudgetPlanState(row.State),
		RequestedBy: generated.Actor{Kind: generated.ActorKind(row.RequestedByKind), Name: row.RequestedByName}, Attempts: int(row.Attempts),
		ClaimedAt: nullableTime(row.ClaimedAt), RetryAt: nullableTime(row.RetryAt), Error: row.Error, FinishedAt: nullableTime(row.FinishedAt),
		DismissedAt: nullableTime(row.DismissedAt), CreatedAt: row.CreatedAt.Time.UTC(), UpdatedAt: row.UpdatedAt.Time.UTC()}
	optional := func(amount *int64) *generated.Money {
		if amount == nil {
			return nil
		}
		return pointer(money(*amount, currency))
	}
	view.SavingsGoal = optional(row.SavingsGoalMinor)
	if row.SalaryBookedOn.Valid {
		view.Salary = &generated.AiBudgetPlanSalary{BookingDate: types.Date{Time: row.SalaryBookedOn.Time}, TransactionId: nullableUUID(row.SalaryTransactionID)}
	}
	if len(row.Result) > 0 {
		var result budget.PlanResult
		if err := json.Unmarshal(row.Result, &result); err != nil {
			return view, err
		}
		out := &generated.AiBudgetPlanResult{Headline: result.Headline, Rough: result.Rough, FullMonths: result.FullMonths,
			Lines: make([]generated.AiBudgetPlanLine, 0, len(result.Lines)), Suggestions: make([]generated.AiBudgetSuggestion, 0, len(result.Suggestions)),
			ProjectedTotal: money(result.ProjectedTotal, currency), IncomeEstimate: money(result.IncomeEstimate, currency),
			ProjectedSavings: money(result.ProjectedSavings, currency), SavingsGoal: optional(result.SavingsGoal)}
		if result.SavingsGoal != nil {
			out.GoalGap = pointer(money(max(0, *result.SavingsGoal-result.ProjectedSavings), currency))
		}
		for _, line := range result.Lines {
			id, err := googleuuid.Parse(line.CategoryID)
			if err != nil {
				return view, err
			}
			item := generated.AiBudgetPlanLine{CategoryId: id, CategoryName: line.CategoryName, Proposed: money(line.Proposed, currency), Reason: line.Reason,
				Current: money(line.Current, currency), Median: optional(line.Median), LastMonth: optional(line.LastMonth), TrendPercent: line.TrendPercent}
			if line.Trend != "" {
				item.Trend = pointer(generated.AiBudgetPlanLineTrend(line.Trend))
			}
			out.Lines = append(out.Lines, item)
		}
		for _, suggestion := range result.Suggestions {
			item := generated.AiBudgetSuggestion{Title: suggestion.Title, Detail: suggestion.Detail, Merchants: suggestion.Merchants,
				MonthlySaving: money(suggestion.MonthlySaving, currency)}
			if suggestion.CategoryID != nil {
				id, err := googleuuid.Parse(*suggestion.CategoryID)
				if err != nil {
					return view, err
				}
				item.CategoryId, item.CategoryName = &id, &suggestion.CategoryName
			}
			if suggestion.Link != "" {
				item.Link = pointer(generated.AiBudgetSuggestionLink(suggestion.Link))
			}
			if item.Merchants == nil {
				item.Merchants = []string{}
			}
			out.Suggestions = append(out.Suggestions, item)
		}
		view.Result = out
	}
	if len(row.Applied) > 0 {
		var record appliedRecord
		if err := json.Unmarshal(row.Applied, &record); err != nil {
			return view, err
		}
		applied := &generated.AiBudgetPlanApplied{At: record.At, Scope: generated.BudgetScope(record.Scope), Lines: make([]generated.AiBudgetPlanAppliedLine, 0, len(record.Lines)),
			ExpectedIncome: optional(record.ExpectedIncome), SavingsGoal: optional(record.SavingsGoal)}
		for _, line := range record.Lines {
			id, err := googleuuid.Parse(line.CategoryID)
			if err != nil {
				return view, err
			}
			applied.Lines = append(applied.Lines, generated.AiBudgetPlanAppliedLine{CategoryId: id, Amount: money(line.Amount, currency)})
		}
		view.Applied = applied
	}
	return view, nil
}

func timestamp(at time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: at, Valid: true} }

// Activity: a plan asked for, made, failed, applied, or dismissed; counts
// and amounts only, never the AI's texts.

func planSubject(row store.AiBudgetPlan) activity.Subject {
	return activity.Subject{Type: "finance_ai_budget_plan", ID: uuid(row.ID).String()}
}

// recordPlanRequested records a plan asked for. The salary's is Finance's
// own (system:finance), in the trace of the write that booked the salary.
func (h *Handler) recordPlanRequested(c fiber.Ctx, row store.AiBudgetPlan) {
	month := budget.MonthKey(row.Month.Time)
	summary := fmt.Sprintf("AI budget plan for %s requested", budget.MonthLabel(row.Month.Time))
	data := map[string]any{"month": month, "trigger": row.Trigger, "currency": row.Currency}
	var actor activity.Actor
	switch row.Trigger {
	case string(generated.AiBudgetPlanTriggerSchedule):
		summary = fmt.Sprintf("AI budget plan for %s scheduled", budget.MonthLabel(row.Month.Time))
	case salaryTrigger:
		summary += " when your salary arrived"
		actor = activity.System("finance")
		data["salaryBookedOn"] = row.SalaryBookedOn.Time.Format(time.DateOnly)
		if row.SalaryTransactionID.Valid {
			data["salaryTransactionId"] = uuid(row.SalaryTransactionID).String()
		}
	}
	if row.SavingsGoalMinor != nil {
		summary += ", savings goal " + financemoney.Format(*row.SavingsGoalMinor, row.Currency)
		data["savingsGoal"] = *row.SavingsGoalMinor
	}
	h.emit(c.Context(), c, activity.Event{ID: activity.EventID(aiBudgetPlanRequested, uuid(row.ID).String()), Type: aiBudgetPlanRequested,
		Subject: planSubject(row), Summary: summary, Data: data, Actor: actor})
}

func (h *Handler) recordPlanCreated(c fiber.Ctx, row store.AiBudgetPlan) {
	var result budget.PlanResult
	if err := json.Unmarshal(row.Result, &result); err != nil {
		return
	}
	var savings int64
	for _, suggestion := range result.Suggestions {
		savings += suggestion.MonthlySaving
	}
	format := func(amount int64) string { return financemoney.Format(amount, row.Currency) }
	summary := fmt.Sprintf("AI budget plan for %s ready: %s planned for %d %s, %d %s to spend less (≈ %s a month)", budget.MonthLabel(row.Month.Time),
		format(result.ProjectedTotal), len(result.Lines), categoryWord(len(result.Lines)), len(result.Suggestions), plural(len(result.Suggestions), "way"), format(savings))
	if result.Rough {
		summary += "; rough, on little history"
	}
	h.emit(c.Context(), c, activity.Event{ID: activity.EventID(aiBudgetPlanCreated, uuid(row.ID).String()), Type: aiBudgetPlanCreated,
		Subject: planSubject(row), Summary: summary,
		Data: map[string]any{"month": budget.MonthKey(row.Month.Time), "categories": len(result.Lines), "planned": result.ProjectedTotal,
			"suggestions": len(result.Suggestions), "savings": savings, "incomeEstimate": result.IncomeEstimate, "projectedSavings": result.ProjectedSavings,
			"rough": result.Rough, "attempts": row.Attempts, "currency": row.Currency}})
}

func (h *Handler) recordPlanFailed(c fiber.Ctx, row store.AiBudgetPlan) {
	reason := ""
	if row.Error != nil {
		reason = *row.Error
	}
	h.emit(c.Context(), c, activity.Event{ID: activity.EventID(aiBudgetPlanFailed, uuid(row.ID).String(), fmt.Sprint(row.Attempts)), Type: aiBudgetPlanFailed,
		Subject: planSubject(row), Outcome: activity.Failed,
		Summary: fmt.Sprintf("AI budget plan for %s failed: %s", budget.MonthLabel(row.Month.Time), activity.Preview(reason, 160)),
		Data:    map[string]any{"month": budget.MonthKey(row.Month.Time), "attempts": row.Attempts, "error": activity.Preview(reason, maxPlanError)}})
}

func (h *Handler) recordPlanApplied(c fiber.Ctx, row store.AiBudgetPlan, record appliedRecord, planLines int) {
	var planned int64
	for _, line := range record.Lines {
		planned += line.Amount
	}
	label := budget.MonthLabel(row.Month.Time)
	scope := fmt.Sprintf("for %s only", label)
	if record.Scope == string(generated.BudgetScopeFollowing) {
		scope = fmt.Sprintf("as the regular plan from %s on", label)
	}
	h.emit(c.Context(), c, activity.Event{ID: activity.EventID(aiBudgetPlanApplied, uuid(row.ID).String(), record.key()), Type: aiBudgetPlanApplied,
		Subject: planSubject(row),
		Summary: fmt.Sprintf("AI budget plan for %s applied: %d of %d %s (%s), %s", label, len(record.Lines), planLines, categoryWord(planLines),
			financemoney.Format(planned, row.Currency), scope),
		Data: map[string]any{"month": budget.MonthKey(row.Month.Time), "state": row.State, "scope": record.Scope, "applied": len(record.Lines),
			"lines": planLines, "planned": planned, "currency": row.Currency}})
}

func (h *Handler) recordPlanDismissed(c fiber.Ctx, row store.AiBudgetPlan) {
	h.emit(c.Context(), c, activity.Event{ID: activity.EventID(aiBudgetPlanDismissed, uuid(row.ID).String()), Type: aiBudgetPlanDismissed,
		Subject: planSubject(row), Summary: fmt.Sprintf("AI budget plan for %s dismissed", budget.MonthLabel(row.Month.Time)),
		Data: map[string]any{"month": budget.MonthKey(row.Month.Time)}})
}
