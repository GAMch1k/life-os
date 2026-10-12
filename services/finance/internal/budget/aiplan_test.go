package budget

import (
	"strings"
	"testing"
	"time"
)

func TestAPlanIsForThisMonthEarlyInItElseForTheNext(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	november := time.Date(2026, time.November, 1, 0, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		now  time.Time
		want time.Time
	}{
		{time.Date(2026, time.October, 1, 6, 0, 0, 0, berlin), october},
		{time.Date(2026, time.October, 7, 23, 59, 0, 0, berlin), october},
		// 22:30 UTC on the 7th is already the 8th in Berlin.
		{time.Date(2026, time.October, 7, 22, 30, 0, 0, time.UTC), november},
		{time.Date(2026, time.October, 12, 9, 0, 0, 0, berlin), november},
		// The 31st of December plans January of the next year.
		{time.Date(2026, time.December, 31, 12, 0, 0, 0, berlin), time.Date(2027, time.January, 1, 0, 0, 0, 0, time.UTC)},
	} {
		if got := DefaultPlanMonth(test.now, berlin); !got.Equal(test.want) {
			t.Errorf("DefaultPlanMonth(%v) = %v, want %v", test.now, got, test.want)
		}
	}
}

// planningInput is made-up history: bank data from 14 July 2026 (so July
// is partial), planning November on 12 October.
func planningInput() PlanningInput {
	start := time.Date(2026, time.July, 14, 0, 0, 0, 0, time.UTC)
	july, august, september := month(7), month(8), month(9)
	shopping := id(9)
	archived := id(10)
	all := append(append([]Category{}, categories...),
		Category{ID: shopping, Name: "Shopping", Kind: KindExpense},
		Category{ID: archived, Name: "Old hobby", Kind: KindExpense, Archived: true})
	return PlanningInput{
		Month: month(11), Current: october, Currency: "EUR", Exponent: 2, HistoryStart: &start, Categories: all,
		History: []HistoryRow{
			{Month: july, CategoryID: ref(groceriesID), Out: 12000},
			{Month: august, CategoryID: ref(groceriesID), Out: 30000},
			{Month: september, CategoryID: ref(groceriesID), Out: 33000, In: 1000},
			{Month: july, CategoryID: ref(diningID), Out: 5000},
			{Month: august, CategoryID: ref(diningID), Out: 10000},
			{Month: september, CategoryID: ref(diningID), Out: 16000},
			{Month: august, CategoryID: ref(rentID), Out: 80000},
			{Month: september, CategoryID: ref(rentID), Out: 80000},
			{Month: august, CategoryID: ref(salaryID), In: 200000},
			{Month: september, CategoryID: ref(salaryID), In: 210000},
			{Month: september, CategoryID: ref(archived), Out: 4000},
			{Month: september, Out: 2500},
		},
		// Latest first.
		Spending: []SpendingRow{
			{CategoryID: diningID, Amount: 2350, Counterparty: "KNEIPE41 DARMSTADT"},
			{CategoryID: diningID, Amount: 1800, Counterparty: "Kneipe41 Darmstadt"},
			{CategoryID: diningID, Amount: 9000, Counterparty: "Pizzeria Roma GmbH"},
			{CategoryID: diningID, Amount: 450, Counterparty: "Bäckerei Schmidt", Description: "Kartenzahlung"},
			{CategoryID: diningID, Amount: 450, Counterparty: "Bäckerei Schmidt"},
			{CategoryID: groceriesID, Amount: 6000, Counterparty: "REWE Markt 1234"},
			{CategoryID: groceriesID, Amount: 4000, Counterparty: "Lidl"},
			{CategoryID: groceriesID, Amount: 100, Counterparty: "Kiosk 1"},
			{CategoryID: groceriesID, Amount: 100, Counterparty: "Kiosk 2"},
			{CategoryID: groceriesID, Amount: 100, Counterparty: "Spätkauf"},
			{CategoryID: groceriesID, Amount: 100, Counterparty: "Netto"},
			{CategoryID: groceriesID, Amount: 50, Counterparty: "Aldi"},
		},
		Plan: &Plan{Scope: ScopeRegular, Month: month(9), Currency: "EUR", ExpectedIncome: 205000, SavingsGoal: 10000,
			Lines: map[[16]byte]int64{groceriesID: 30000, rentID: 80000}},
		Subscriptions: []PlanningSubscription{{Name: "Netflix", Monthly: 1399}},
		Balance:       10570,
	}
}

func month(number int) time.Time { return time.Date(2026, time.Month(number), 1, 0, 0, 0, 0, time.UTC) }

func planningCategory(data PlanningData, name string) PlanningCategory {
	for _, category := range data.Categories {
		if category.Category.Name == name {
			return category
		}
	}
	return PlanningCategory{}
}

func TestPlanningDataAggregatesTheHistory(t *testing.T) {
	data := Planning(planningInput())
	if len(data.Months) != 3 || !data.Months[0].Equal(month(7)) || !data.Months[2].Equal(month(9)) ||
		!data.FirstMonthPartial || data.FullMonths != 2 || data.Rough || data.MerchantMonths != 3 {
		t.Fatalf("window = %v partial %v full %d rough %v merchants %d", data.Months, data.FirstMonthPartial, data.FullMonths, data.Rough, data.MerchantMonths)
	}
	if got := data.Income; got[0] != 0 || got[1] != 200000 || got[2] != 210000 || *data.IncomeMedian != 200000 {
		t.Fatalf("income = %v, median %v", got, *data.IncomeMedian)
	}
	// All spending counts uncategorized money; the archived category is
	// not a line, but its money was spent.
	if got := data.Spending; got[2] != 32000+16000+80000+4000+2500 || data.Uncategorized[2] != 2500 {
		t.Fatalf("spending = %v uncategorized %v", got, data.Uncategorized)
	}
	names := []string{}
	for _, category := range data.Categories {
		names = append(names, category.Category.Name)
	}
	if strings.Join(names, ",") != "eating out,Groceries,Rent,Shopping" {
		t.Fatalf("categories = %v (active expense ones, not Savings, by name)", names)
	}
	groceries := planningCategory(data, "Groceries")
	// The median is the budget's suggestion over July to September; the
	// trend leaves out partial July: September (320) against August (300).
	if groceries.Monthly[2] != 32000 || *groceries.Median != 30000 || *groceries.LastMonth != 32000 || groceries.Trend != TrendFlat ||
		*groceries.TrendPercent != 7 || groceries.Planned != 30000 || groceries.Baseline() != 32000 {
		t.Fatalf("groceries = %+v", groceries)
	}
	dining := planningCategory(data, "eating out")
	if dining.Trend != TrendUp || *dining.TrendPercent != 60 || dining.Planned != 0 {
		t.Fatalf("eating out = %+v", dining)
	}
	// Merchants are grouped as subscriptions group them, largest first,
	// named as last paid, at most five, with their monthly share of the
	// three months.
	if len(dining.Merchants) != 3 || dining.Merchants[0].Name != "Pizzeria Roma GmbH" || dining.Merchants[1].Name != "KNEIPE41 DARMSTADT" ||
		dining.Merchants[1].Count != 2 || dining.Merchants[1].Total != 4150 || dining.Merchants[1].Monthly != 1383 || dining.Merchants[2].Count != 2 {
		t.Fatalf("eating out merchants = %+v", dining.Merchants)
	}
	if merchants := groceries.Merchants; len(merchants) != PlanningMerchants || merchants[0].Name != "REWE Markt 1234" || merchants[4].Name != "Netto" {
		t.Fatalf("groceries merchants = %+v", merchants)
	}
	shopping := planningCategory(data, "Shopping")
	if shopping.History() || shopping.Median != nil || *shopping.LastMonth != 0 || shopping.Trend != TrendFlat || len(shopping.Merchants) != 0 {
		t.Fatalf("shopping = %+v", shopping)
	}
}

func TestShortHistoryMakesARoughPlan(t *testing.T) {
	in := planningInput()
	start := time.Date(2026, time.September, 20, 0, 0, 0, 0, time.UTC)
	in.HistoryStart = &start
	data := Planning(in)
	if len(data.Months) != 1 || !data.FirstMonthPartial || data.FullMonths != 0 || !data.Rough {
		t.Fatalf("one partial month: %+v", data)
	}
	if dining := planningCategory(data, "eating out"); dining.Trend != "" || dining.TrendPercent != nil || *dining.LastMonth != 16000 {
		t.Fatalf("eating out = %+v", dining)
	}
	in.HistoryStart, in.History, in.Spending = nil, nil, nil
	none := Planning(in)
	if len(none.Months) != 0 || !none.Rough || len(none.Categories) != 4 || none.Categories[0].Median != nil || none.Categories[0].LastMonth != nil {
		t.Fatalf("no history: %+v", none)
	}
	// A plan for this month looks at the months before it.
	in = planningInput()
	in.Month = october
	if data := Planning(in); len(data.Months) != 3 || !data.Months[2].Equal(month(9)) {
		t.Fatalf("this month: %v", data.Months)
	}
}

// proposal is a valid proposal for planningInput's data.
func proposal() Proposal {
	return Proposal{
		Currency: "EUR", Headline: "Money is tight: cut eating out and keep groceries steady.",
		Lines: []ProposedLine{
			{CategoryID: groceriesID, Amount: 30000, Reason: "Close to your usual groceries."},
			{CategoryID: diningID, Amount: 9000, Reason: "Eating out grew fast; bring it back down."},
			{CategoryID: rentID, Amount: 80000, Reason: "Rent stays the same."},
		},
		Suggestions: []ProposedSuggestion{
			{Title: "Cook instead of pizza", Detail: "Pizzeria Roma is your biggest eating out spend; cook twice a week.", CategoryID: ref(diningID), Merchants: []string{"pizzeria roma gmbh"}, MonthlySaving: 2000, Link: LinkFoodRecipes},
			{Title: "Fewer bar nights", Detail: "Two evenings at Kneipe41 a month are enough.", CategoryID: ref(diningID), Merchants: []string{"KNEIPE41  DARMSTADT"}, MonthlySaving: 1000},
		},
		ProjectedTotal: 119000, IncomeEstimate: 205000, ProjectedSavings: 86000,
	}
}

func TestACheckedProposalCarriesFinancesEvidence(t *testing.T) {
	data := Planning(planningInput())
	in := proposal()
	// Totals within a euro of the sums are accepted, and kept exact.
	in.ProjectedTotal, in.ProjectedSavings = 119050, 85950
	result, err := CheckProposal(data, in, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.ProjectedTotal != 119000 || result.ProjectedSavings != 86000 || result.IncomeEstimate != 205000 || *result.SavingsGoal != 10000 ||
		result.Rough || result.FullMonths != 2 {
		t.Fatalf("result = %+v", result)
	}
	if len(result.Lines) != 4 {
		t.Fatalf("lines = %+v", result.Lines)
	}
	dining, groceries, shopping := result.Lines[0], result.Lines[1], result.Lines[3]
	if dining.CategoryName != "eating out" || dining.Proposed != 9000 || *dining.Median != 10000 || *dining.LastMonth != 16000 || dining.Trend != TrendUp ||
		dining.Current != 0 || groceries.Current != 30000 || groceries.CategoryID != FormatID(groceriesID) {
		t.Fatalf("lines = %+v", result.Lines)
	}
	// Shopping had no spending and no amount: it was left out and gets 0.
	if shopping.CategoryName != "Shopping" || shopping.Proposed != 0 || shopping.Reason != noHistoryReason {
		t.Fatalf("shopping = %+v", shopping)
	}
	first := result.Suggestions[0]
	if len(result.Suggestions) != 2 || first.CategoryName != "eating out" || *first.CategoryID != FormatID(diningID) ||
		first.Merchants[0] != "Pizzeria Roma GmbH" || first.Link != LinkFoodRecipes || result.Suggestions[1].Merchants[0] != "KNEIPE41 DARMSTADT" {
		t.Fatalf("suggestions = %+v", result.Suggestions)
	}
	// The owner's goal wins over the plan's.
	goal := int64(5000)
	if result, err := CheckProposal(data, proposal(), &goal); err != nil || *result.SavingsGoal != 5000 {
		t.Fatalf("goal: %+v %v", result.SavingsGoal, err)
	}
}

func TestBadProposalsAreRefused(t *testing.T) {
	data := Planning(planningInput())
	for name, change := range map[string]func(*Proposal){
		"another currency":     func(p *Proposal) { p.Currency = "USD" },
		"an unknown category":  func(p *Proposal) { p.Lines[0].CategoryID = id(77) },
		"an archived category": func(p *Proposal) { p.Lines[0].CategoryID = id(10) },
		"an income category":   func(p *Proposal) { p.Lines[0].CategoryID = salaryID },
		"Savings":              func(p *Proposal) { p.Lines[0].CategoryID = savingsID },
		"a negative amount":    func(p *Proposal) { p.Lines[1].Amount = -100 },
		"a category twice":     func(p *Proposal) { p.Lines[2] = p.Lines[0] },
		"a missing category":   func(p *Proposal) { p.Lines = p.Lines[:2] },
		"no reason":            func(p *Proposal) { p.Lines[0].Reason = " " },
		"no headline":          func(p *Proposal) { p.Headline = "Plan" },
		"one suggestion":       func(p *Proposal) { p.Suggestions = p.Suggestions[:1] },
		"six suggestions": func(p *Proposal) {
			p.Suggestions = append(p.Suggestions, p.Suggestions[0], p.Suggestions[0], p.Suggestions[0], p.Suggestions[0])
		},
		"a saving of nothing":   func(p *Proposal) { p.Suggestions[1].MonthlySaving = 0 },
		"a negative saving":     func(p *Proposal) { p.Suggestions[1].MonthlySaving = -500 },
		"above the category":    func(p *Proposal) { p.Suggestions[1].Merchants, p.Suggestions[1].MonthlySaving = nil, 16001 },
		"above the merchant":    func(p *Proposal) { p.Suggestions[1].MonthlySaving = 1384 },
		"an unknown merchant":   func(p *Proposal) { p.Suggestions[1].Merchants = []string{"Burger Palace"} },
		"another's merchant":    func(p *Proposal) { p.Suggestions[1].Merchants = []string{"Lidl"} },
		"a suggestion category": func(p *Proposal) { p.Suggestions[0].CategoryID = ref(id(77)) },
		"above everything": func(p *Proposal) {
			p.Suggestions[1].CategoryID, p.Suggestions[1].Merchants, p.Suggestions[1].MonthlySaving = nil, nil, 200000
		},
		"another link":        func(p *Proposal) { p.Suggestions[0].Link = "https://example.com" },
		"a wrong total":       func(p *Proposal) { p.ProjectedTotal = 119101 },
		"wrong savings":       func(p *Proposal) { p.ProjectedSavings = 80000 },
		"a negative income":   func(p *Proposal) { p.IncomeEstimate, p.ProjectedSavings = -1, -119001 },
		"an enormous amount":  func(p *Proposal) { p.Lines[0].Amount = maxProposalAmount + 1 },
		"a too long headline": func(p *Proposal) { p.Headline = strings.Repeat("a", maxHeadline+1) },
	} {
		in := proposal()
		change(&in)
		if _, err := CheckProposal(data, in, nil); err == nil || !IsProposalError(err) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Without any spending, no suggestion is needed.
	empty := planningInput()
	empty.HistoryStart, empty.History, empty.Spending, empty.Plan = nil, nil, nil, nil
	in := Proposal{Currency: "EUR", Headline: "No history yet: a rough start.", IncomeEstimate: 0}
	if result, err := CheckProposal(Planning(empty), in, nil); err != nil || len(result.Lines) != 4 || result.ProjectedTotal != 0 {
		t.Fatalf("no history: %+v %v", result, err)
	}
}
