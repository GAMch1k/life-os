package budget

import (
	"cmp"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/GAMch1k/life-os/services/finance/internal/subscriptions"
)

// AI budget plans (P13-09). The AI proposes a month's budget from what the
// owner spent: Finance gives it the month's planning data, aggregates only
// (spending per category and month, the top merchants per category, the
// income, the confirmed subscriptions, the plan that applies), and checks
// the proposal it gets back before keeping it. The evidence of each line
// (the 3-month median, last month, the trend) is Finance's own arithmetic,
// never the AI's.

const (
	// PlanningMonths is how many complete months planning data looks back
	// at most.
	PlanningMonths = 12
	// PlanningMerchants is how many merchants per category it names.
	PlanningMerchants = 5
	// EarlyDays: until this day of a month, a plan is for the current month
	// unless the owner names another; later, for the next month.
	EarlyDays = 7
	// RoughBelow: a plan on fewer full months of history is rough.
	RoughBelow = 2
	// TrendPercent is how far recent spending must move from earlier
	// spending to be a trend up or down.
	TrendPercent = 10
)

// Trends of a category's spending.
const (
	TrendUp   = "up"
	TrendDown = "down"
	TrendFlat = "flat"
)

// LinkFoodRecipes is a suggestion's link to Food's recipes (cooking more).
const LinkFoodRecipes = "food_recipes"

// DefaultPlanMonth is the month a plan is for when the owner names none:
// the current month during its first EarlyDays days, else the next month.
func DefaultPlanMonth(now time.Time, location *time.Location) time.Time {
	current := CurrentMonth(now, location)
	if now.In(location).Day() <= EarlyDays {
		return current
	}
	return AddMonths(current, 1)
}

// SpendingRow is one booked payment out in an expense category, as a
// positive amount, for the merchants.
type SpendingRow struct {
	CategoryID                [16]byte
	Amount                    int64
	Counterparty, Description string
}

// PlanningSubscription is a confirmed subscription in the budget currency.
type PlanningSubscription struct {
	Name       string
	Monthly    int64
	CategoryID *[16]byte
}

// PlanningInput is what planning data is computed from.
type PlanningInput struct {
	// Month is the month to plan; Current the owner's current month.
	Month, Current time.Time
	Currency       string
	Exponent       int
	// HistoryStart is the day of the first booked transaction in the
	// currency; nil without any.
	HistoryStart *time.Time
	// Categories are all categories, archived ones included.
	Categories []Category
	// History is the booked money per month and category of the planning
	// window (PlanningWindow).
	History []HistoryRow
	// Spending is the booked money out of the merchant window
	// (MerchantWindow), latest first.
	Spending      []SpendingRow
	Plan          *Plan
	Subscriptions []PlanningSubscription
	// Balance is the money there is now: the balance of the accounts in
	// the currency, pending payments included.
	Balance int64
}

// PlanningMerchant is a merchant of a category over the merchant window.
type PlanningMerchant struct {
	Name  string
	Count int
	// Total is its spending over the window, Monthly that per month.
	Total, Monthly int64
}

// PlanningCategory is one active expense category's history.
type PlanningCategory struct {
	Category Category
	// Monthly is its spending (less refunds) per month of the window.
	Monthly []int64
	// Median is the median of the last three complete months, as the
	// budget's suggestions compute it; nil without spending there.
	Median *int64
	// LastMonth is the last complete month's spending; nil when the history
	// does not reach it.
	LastMonth *int64
	// Trend is up, down, or flat (TrendPercent from the recent months'
	// average against the earlier ones'); empty with fewer than two full
	// months.
	Trend        string
	TrendPercent *int
	// Planned is its amount in the plan that applies to the month.
	Planned   int64
	Merchants []PlanningMerchant
}

// History reports whether the category had any spending in the window.
func (c PlanningCategory) History() bool {
	return slices.ContainsFunc(c.Monthly, func(amount int64) bool { return amount > 0 })
}

// Baseline is what the category typically spends in a month: the larger
// of its median and last month. A saving cannot be larger.
func (c PlanningCategory) Baseline() int64 {
	var baseline int64
	if c.Median != nil {
		baseline = *c.Median
	}
	if c.LastMonth != nil && *c.LastMonth > baseline {
		baseline = *c.LastMonth
	}
	return baseline
}

// PlanningData is what the AI is told to plan a month.
type PlanningData struct {
	Month    time.Time
	Currency string
	Exponent int
	// Months are the complete months of the window, oldest first: up to
	// PlanningMonths before the month (never after the current month), from
	// the month the history starts.
	Months []time.Time
	// FirstMonthPartial: the history starts after the first month's 1st.
	FirstMonthPartial bool
	// FullMonths are the months the history covers whole; Rough when there
	// are fewer than RoughBelow.
	FullMonths int
	Rough      bool
	// MedianMonths are the months the medians look at.
	MedianMonths []time.Time
	// MerchantMonths is how many months the merchants cover.
	MerchantMonths int
	// Income, Spending, and Uncategorized are per month of the window:
	// income, all spending, and spending without a category.
	Income, Spending, Uncategorized []int64
	IncomeMedian                    *int64
	// Categories are the active expense categories (not Savings), by name.
	Categories    []PlanningCategory
	Plan          *Plan
	Subscriptions []PlanningSubscription
	Balance       int64
}

// PlanningWindow is the first month and the month after the last of the
// months planning data looks at for month: up to PlanningMonths complete
// months before it, never after the current month, from the month the
// history starts (from equals to without history).
func PlanningWindow(month, current time.Time, historyStart *time.Time) (from, to time.Time) {
	to = month
	if to.After(current) {
		to = current
	}
	from = AddMonths(to, -PlanningMonths)
	if historyStart == nil {
		return to, to
	}
	if start := time.Date(historyStart.Year(), historyStart.Month(), 1, 0, 0, 0, 0, time.UTC); start.After(from) {
		from = start
	}
	if from.After(to) {
		from = to
	}
	return from, to
}

// MerchantWindow is the first and the after-last day of the payments the
// merchants are counted over: the months the medians look at.
func MerchantWindow(month, current time.Time, historyStart *time.Time) (from, to time.Time) {
	from, to = PlanningWindow(month, current, historyStart)
	if earliest := AddMonths(to, -SuggestionMonths); earliest.After(from) {
		from = earliest
	}
	return from, to
}

// Planning computes a month's planning data.
func Planning(in PlanningInput) PlanningData {
	from, to := PlanningWindow(in.Month, in.Current, in.HistoryStart)
	data := PlanningData{Month: in.Month, Currency: in.Currency, Exponent: in.Exponent, Plan: in.Plan, Balance: in.Balance,
		Months: []time.Time{}, MedianMonths: []time.Time{}, Income: []int64{}, Spending: []int64{}, Uncategorized: []int64{},
		Categories: []PlanningCategory{}, Subscriptions: in.Subscriptions}
	if data.Subscriptions == nil {
		data.Subscriptions = []PlanningSubscription{}
	}
	for month := from; month.Before(to); month = AddMonths(month, 1) {
		data.Months = append(data.Months, month)
	}
	if len(data.Months) > 0 && in.HistoryStart != nil && in.HistoryStart.Day() > 1 &&
		in.HistoryStart.Year() == data.Months[0].Year() && in.HistoryStart.Month() == data.Months[0].Month() {
		data.FirstMonthPartial = true
	}
	data.FullMonths = len(data.Months)
	if data.FirstMonthPartial {
		data.FullMonths--
	}
	data.Rough = data.FullMonths < RoughBelow

	byMonth := map[time.Time][]Total{}
	var medianRows []HistoryRow
	medianFrom := AddMonths(to, -SuggestionMonths)
	for _, row := range in.History {
		byMonth[row.Month] = append(byMonth[row.Month], Total{CategoryID: row.CategoryID, OutBooked: row.Out, InBooked: row.In})
		if !row.Month.Before(medianFrom) && row.Month.Before(to) {
			medianRows = append(medianRows, row)
		}
	}
	spent := map[[16]byte][]int64{}
	for index, month := range data.Months {
		computed := Compute(month, in.Currency, nil, in.Categories, byMonth[month])
		data.Income = append(data.Income, computed.Income)
		data.Spending = append(data.Spending, computed.Spent)
		data.Uncategorized = append(data.Uncategorized, computed.Uncategorized.Spent)
		for _, line := range computed.Lines {
			if spent[line.Category.ID] == nil {
				spent[line.Category.ID] = make([]int64, len(data.Months))
			}
			spent[line.Category.ID][index] = line.Spent
		}
	}
	suggestions := Suggest(in.Categories, medianRows, in.Exponent)
	data.MedianMonths = append(data.MedianMonths, suggestions.Months...)
	data.IncomeMedian = suggestions.Income

	merchantFrom, merchantTo := MerchantWindow(in.Month, in.Current, in.HistoryStart)
	for month := merchantFrom; month.Before(merchantTo); month = AddMonths(month, 1) {
		data.MerchantMonths++
	}
	merchants := merchantsByCategory(in.Spending, data.MerchantMonths)

	last := AddMonths(to, -1)
	for _, category := range in.Categories {
		if category.Kind != KindExpense || category.Archived || category.SystemKey == KeySavings {
			continue
		}
		item := PlanningCategory{Category: category, Monthly: spent[category.ID], Merchants: merchants[category.ID]}
		if item.Monthly == nil {
			item.Monthly = make([]int64, len(data.Months))
		}
		if item.Merchants == nil {
			item.Merchants = []PlanningMerchant{}
		}
		item.Median = suggestions.Categories[category.ID].Amount
		if n := len(data.Months); n > 0 && data.Months[n-1].Equal(last) {
			value := item.Monthly[n-1]
			item.LastMonth = &value
		}
		full := item.Monthly
		if data.FirstMonthPartial {
			full = full[1:]
		}
		item.Trend, item.TrendPercent = trend(full)
		if in.Plan != nil {
			item.Planned = in.Plan.Lines[category.ID]
		}
		data.Categories = append(data.Categories, item)
	}
	slices.SortFunc(data.Categories, func(a, b PlanningCategory) int { return compareNames(a.Category, b.Category) })
	return data
}

// trend compares the latest months' average spending with the earlier
// months': the last three against those before with four months or more,
// else the last month against those before it.
func trend(values []int64) (string, *int) {
	n := len(values)
	if n < 2 {
		return "", nil
	}
	recentCount := 1
	if n >= 4 {
		recentCount = 3
	}
	mean := func(part []int64) float64 {
		var sum int64
		for _, value := range part {
			sum += value
		}
		return float64(sum) / float64(len(part))
	}
	recent, earlier := mean(values[n-recentCount:]), mean(values[:n-recentCount])
	if earlier == 0 {
		if recent == 0 {
			zero := 0
			return TrendFlat, &zero
		}
		return TrendUp, nil
	}
	percent := int(math.Round((recent - earlier) * 100 / earlier))
	switch {
	case percent >= TrendPercent:
		return TrendUp, &percent
	case percent <= -TrendPercent:
		return TrendDown, &percent
	}
	return TrendFlat, &percent
}

// merchantsByCategory groups payments by category and merchant (as
// subscriptions identify merchants) and keeps each category's
// PlanningMerchants largest, with their monthly amount over months.
func merchantsByCategory(rows []SpendingRow, months int) map[[16]byte][]PlanningMerchant {
	type group struct {
		merchant PlanningMerchant
		order    int
	}
	groups := map[[16]byte]map[string]*group{}
	for index, row := range rows {
		merchant := subscriptions.MerchantOf(row.Counterparty, row.Description)
		if merchant.Key == "" {
			merchant = subscriptions.Merchant{Key: "", Name: "Unnamed payments"}
		}
		byKey := groups[row.CategoryID]
		if byKey == nil {
			byKey = map[string]*group{}
			groups[row.CategoryID] = byKey
		}
		found := byKey[merchant.Key]
		if found == nil {
			// Rows come latest first: a merchant is named as last paid.
			found = &group{merchant: PlanningMerchant{Name: merchant.Name}, order: index}
			byKey[merchant.Key] = found
		}
		found.merchant.Count++
		found.merchant.Total += row.Amount
	}
	result := map[[16]byte][]PlanningMerchant{}
	for category, byKey := range groups {
		list := make([]*group, 0, len(byKey))
		for _, item := range byKey {
			list = append(list, item)
		}
		slices.SortFunc(list, func(a, b *group) int {
			if order := cmp.Compare(b.merchant.Total, a.merchant.Total); order != 0 {
				return order
			}
			return cmp.Compare(a.order, b.order)
		})
		if len(list) > PlanningMerchants {
			list = list[:PlanningMerchants]
		}
		merchants := make([]PlanningMerchant, 0, len(list))
		for _, item := range list {
			if months > 0 {
				item.merchant.Monthly = roundDiv(item.merchant.Total, int64(months))
			}
			merchants = append(merchants, item.merchant)
		}
		result[category] = merchants
	}
	return result
}

func roundDiv(a, b int64) int64 { return floorDiv(2*a+b, 2*b) }

// The AI's proposal and its checks.

// Limits of a proposal.
const (
	minHeadline, maxHeadline = 10, 200
	minReason, maxReason     = 5, 240
	minTitle, maxTitle       = 3, 80
	minDetail, maxDetail     = 10, 300
	minSuggestions           = 2
	maxSuggestions           = 5
	maxSuggestionMerchants   = 5
	noHistoryReason          = "No spending in the last months and nothing in the plan."
	maxProposalAmount        = 1_000_000_000_000
	maxMerchantName          = 100
)

// ProposedLine is the AI's amount for a category.
type ProposedLine struct {
	CategoryID [16]byte
	Amount     int64
	Reason     string
}

// ProposedSuggestion is one way the AI sees to spend less.
type ProposedSuggestion struct {
	Title, Detail string
	CategoryID    *[16]byte
	Merchants     []string
	MonthlySaving int64
	Link          string
}

// Proposal is the AI's plan for a month, as it answers.
type Proposal struct {
	Currency                                         string
	Headline                                         string
	Lines                                            []ProposedLine
	Suggestions                                      []ProposedSuggestion
	ProjectedTotal, IncomeEstimate, ProjectedSavings int64
}

// PlanResult is a checked proposal with Finance's evidence, as Finance
// keeps it. Amounts are minor units of the plan's currency.
type PlanResult struct {
	Headline string `json:"headline"`
	// Rough: the plan rests on fewer than RoughBelow full months.
	Rough       bool               `json:"rough"`
	FullMonths  int                `json:"fullMonths"`
	Lines       []ResultLine       `json:"lines"`
	Suggestions []ResultSuggestion `json:"suggestions"`
	// ProjectedTotal is the sum of the lines; ProjectedSavings the income
	// estimate minus it.
	ProjectedTotal   int64 `json:"projectedTotalMinor"`
	IncomeEstimate   int64 `json:"incomeEstimateMinor"`
	ProjectedSavings int64 `json:"projectedSavingsMinor"`
	// SavingsGoal is the owner's goal for the plan, else the current plan's
	// (nil without one).
	SavingsGoal *int64 `json:"savingsGoalMinor"`
}

// ResultLine is a category of a checked plan.
type ResultLine struct {
	CategoryID   string `json:"categoryId"`
	CategoryName string `json:"categoryName"`
	Proposed     int64  `json:"proposedMinor"`
	Reason       string `json:"reason"`
	// Current is the amount in the plan that applied when the AI planned.
	Current      int64  `json:"currentMinor"`
	Median       *int64 `json:"medianMinor"`
	LastMonth    *int64 `json:"lastMonthMinor"`
	Trend        string `json:"trend,omitempty"`
	TrendPercent *int   `json:"trendPercent"`
}

// ResultSuggestion is a checked way to spend less.
type ResultSuggestion struct {
	Title         string   `json:"title"`
	Detail        string   `json:"detail"`
	CategoryID    *string  `json:"categoryId"`
	CategoryName  string   `json:"categoryName,omitempty"`
	Merchants     []string `json:"merchants"`
	MonthlySaving int64    `json:"monthlySavingMinor"`
	Link          string   `json:"link,omitempty"`
}

// ProposalError says why a proposal was refused.
type ProposalError struct{ Reason string }

func (e ProposalError) Error() string { return e.Reason }

func refuse(format string, args ...any) error {
	return ProposalError{Reason: fmt.Sprintf(format, args...)}
}

// IsProposalError reports whether err refused a proposal.
func IsProposalError(err error) bool {
	var refused ProposalError
	return errors.As(err, &refused)
}

// CheckProposal checks the AI's proposal against the month's planning
// data and returns it as Finance keeps it:
//
//   - the plan's currency;
//   - a line for every active expense category (once, a known one, never
//     an archived one or Savings) with an amount of 0 or more and a short
//     reason; a category without spending in the window and nothing in the
//     plan may be left out and gets 0;
//   - 2 to 5 suggestions (none needed without any spending), each with a
//     saving above 0 that is no larger than the spending it comes from:
//     its category's baseline (the larger of the median and last month),
//     the named merchants' monthly spending, or all spending without a
//     category; together no larger than all of it;
//   - totals that add up to within one unit of the currency: the projected
//     total is the sum of the lines and the projected savings the income
//     estimate (0 or more) minus it. The kept totals are the exact sums.
//
// savingsGoal is the owner's goal for the plan, if any.
func CheckProposal(data PlanningData, proposal Proposal, savingsGoal *int64) (PlanResult, error) {
	if proposal.Currency != data.Currency {
		return PlanResult{}, refuse("the plan is in %s, not %q", data.Currency, proposal.Currency)
	}
	result := PlanResult{Rough: data.Rough, FullMonths: data.FullMonths, Lines: []ResultLine{}, Suggestions: []ResultSuggestion{}}
	headline, err := text("headline", proposal.Headline, minHeadline, maxHeadline)
	if err != nil {
		return PlanResult{}, err
	}
	result.Headline = headline
	byID := make(map[[16]byte]PlanningCategory, len(data.Categories))
	for _, category := range data.Categories {
		byID[category.Category.ID] = category
	}
	proposed := map[[16]byte]ProposedLine{}
	for _, line := range proposal.Lines {
		category, known := byID[line.CategoryID]
		switch {
		case !known:
			return PlanResult{}, refuse("category %s is not an active expense category", FormatID(line.CategoryID))
		case line.Amount < 0:
			return PlanResult{}, refuse("the amount for %s is negative", category.Category.Name)
		case line.Amount > maxProposalAmount:
			return PlanResult{}, refuse("the amount for %s is too large", category.Category.Name)
		}
		if _, seen := proposed[line.CategoryID]; seen {
			return PlanResult{}, refuse("%s has two amounts", category.Category.Name)
		}
		reason, err := text("the reason for "+category.Category.Name, line.Reason, minReason, maxReason)
		if err != nil {
			return PlanResult{}, err
		}
		line.Reason = reason
		proposed[line.CategoryID] = line
	}
	for _, category := range data.Categories {
		line, ok := proposed[category.Category.ID]
		if !ok {
			if category.History() || category.Planned > 0 {
				return PlanResult{}, refuse("there is no amount for %s", category.Category.Name)
			}
			line = ProposedLine{CategoryID: category.Category.ID, Reason: noHistoryReason}
		}
		result.Lines = append(result.Lines, ResultLine{CategoryID: FormatID(category.Category.ID), CategoryName: category.Category.Name, Proposed: line.Amount,
			Reason: line.Reason, Current: category.Planned, Median: category.Median, LastMonth: category.LastMonth, Trend: category.Trend,
			TrendPercent: category.TrendPercent})
		result.ProjectedTotal += line.Amount
	}

	var everything int64
	for _, category := range data.Categories {
		everything += category.Baseline()
	}
	uncategorized := lastOrZero(data.Uncategorized)
	everything += uncategorized
	if count := len(proposal.Suggestions); count > maxSuggestions || (everything > 0 && count < minSuggestions) {
		return PlanResult{}, refuse("a plan has %d to %d ways to spend less, not %d", minSuggestions, maxSuggestions, count)
	}
	var savings int64
	for index, suggestion := range proposal.Suggestions {
		checked, err := checkSuggestion(index+1, suggestion, byID, data, uncategorized)
		if err != nil {
			return PlanResult{}, err
		}
		savings += checked.MonthlySaving
		result.Suggestions = append(result.Suggestions, checked)
	}
	if savings > everything {
		return PlanResult{}, refuse("the savings add up to more than all the spending")
	}

	// One unit of the currency (a euro) for rounding.
	tolerance := int64(1)
	for range data.Exponent {
		tolerance *= 10
	}
	if proposal.IncomeEstimate < 0 {
		return PlanResult{}, refuse("the income estimate is negative")
	}
	if diff := proposal.ProjectedTotal - result.ProjectedTotal; diff > tolerance || diff < -tolerance {
		return PlanResult{}, refuse("the projected total is not the sum of the lines")
	}
	result.IncomeEstimate = proposal.IncomeEstimate
	result.ProjectedSavings = result.IncomeEstimate - result.ProjectedTotal
	if diff := proposal.ProjectedSavings - result.ProjectedSavings; diff > tolerance || diff < -tolerance {
		return PlanResult{}, refuse("the projected savings are not the income estimate minus the projected total")
	}
	result.SavingsGoal = savingsGoal
	if result.SavingsGoal == nil && data.Plan != nil && data.Plan.SavingsGoal > 0 {
		goal := data.Plan.SavingsGoal
		result.SavingsGoal = &goal
	}
	return result, nil
}

func checkSuggestion(number int, in ProposedSuggestion, byID map[[16]byte]PlanningCategory, data PlanningData, uncategorized int64) (ResultSuggestion, error) {
	title, err := text(fmt.Sprintf("suggestion %d's title", number), in.Title, minTitle, maxTitle)
	if err != nil {
		return ResultSuggestion{}, err
	}
	detail, err := text(fmt.Sprintf("suggestion %d's detail", number), in.Detail, minDetail, maxDetail)
	if err != nil {
		return ResultSuggestion{}, err
	}
	out := ResultSuggestion{Title: title, Detail: detail, Merchants: []string{}, MonthlySaving: in.MonthlySaving}
	switch in.Link {
	case "", LinkFoodRecipes:
		out.Link = in.Link
	default:
		return ResultSuggestion{}, refuse("suggestion %d links to %q", number, in.Link)
	}
	if in.MonthlySaving <= 0 {
		return ResultSuggestion{}, refuse("suggestion %d saves nothing", number)
	}
	if len(in.Merchants) > maxSuggestionMerchants {
		return ResultSuggestion{}, refuse("suggestion %d names more than %d merchants", number, maxSuggestionMerchants)
	}
	// Where the saving comes from: a category, or everything.
	var pool []PlanningCategory
	limit := uncategorized
	if in.CategoryID != nil {
		category, known := byID[*in.CategoryID]
		if !known {
			return ResultSuggestion{}, refuse("suggestion %d is for category %s, which is not an active expense category", number, FormatID(*in.CategoryID))
		}
		id := FormatID(category.Category.ID)
		out.CategoryID, out.CategoryName = &id, category.Category.Name
		pool, limit = []PlanningCategory{category}, category.Baseline()
	} else {
		pool = data.Categories
		for _, category := range data.Categories {
			limit += category.Baseline()
		}
	}
	if len(in.Merchants) > 0 {
		var merchantsMonthly int64
		for _, name := range in.Merchants {
			found, ok := findMerchant(pool, name)
			if !ok {
				return ResultSuggestion{}, refuse("suggestion %d names %q, which is not a merchant it may name", number, clip(name, maxMerchantName))
			}
			if slices.Contains(out.Merchants, found.Name) {
				continue
			}
			out.Merchants = append(out.Merchants, found.Name)
			merchantsMonthly += found.Monthly
		}
		limit = min(limit, merchantsMonthly)
	}
	if in.MonthlySaving > limit {
		return ResultSuggestion{}, refuse("suggestion %d saves more than the spending it comes from", number)
	}
	return out, nil
}

// findMerchant finds a merchant of the categories by name, compared
// without case or repeated spaces.
func findMerchant(categories []PlanningCategory, name string) (PlanningMerchant, bool) {
	wanted := strings.ToLower(strings.Join(strings.Fields(name), " "))
	for _, category := range categories {
		for _, merchant := range category.Merchants {
			if strings.ToLower(strings.Join(strings.Fields(merchant.Name), " ")) == wanted {
				return merchant, true
			}
		}
	}
	return PlanningMerchant{}, false
}

// text trims a text and checks its length.
func text(name, value string, minimum, maximum int) (string, error) {
	value = strings.Join(strings.Fields(value), " ")
	if count := utf8.RuneCountInString(value); count < minimum || count > maximum {
		return "", refuse("%s must have %d to %d characters", name, minimum, maximum)
	}
	return value, nil
}

func clip(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	return string([]rune(value)[:limit])
}

func lastOrZero(values []int64) int64 {
	if len(values) == 0 {
		return 0
	}
	return values[len(values)-1]
}

// FormatID is a category id as a UUID string.
func FormatID(id [16]byte) string {
	return fmt.Sprintf("%x-%x-%x-%x-%x", id[0:4], id[4:6], id[6:8], id[8:10], id[10:16])
}
