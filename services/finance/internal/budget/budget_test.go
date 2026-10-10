package budget

import (
	"testing"
	"time"
)

func id(b byte) [16]byte { return [16]byte{b} }

var (
	groceriesID, diningID, rentID, salaryID, savingsID, transfersID = id(1), id(2), id(3), id(4), id(5), id(6)
	categories                                                      = []Category{
		{ID: groceriesID, Name: "Groceries", Kind: KindExpense, SystemKey: "groceries"},
		{ID: diningID, Name: "eating out", Kind: KindExpense},
		{ID: rentID, Name: "Rent", Kind: KindExpense},
		{ID: salaryID, Name: "Salary", Kind: KindIncome},
		{ID: savingsID, Name: "Savings", Kind: KindTransfer, SystemKey: KeySavings},
		{ID: transfersID, Name: "Transfers", Kind: KindTransfer},
	}
	october = time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC)
)

func ref(value [16]byte) *[16]byte { return &value }

func TestComputeCountsBookedAndPendingSpendingAgainstThePlan(t *testing.T) {
	plan := &Plan{Scope: ScopeRegular, Month: october, Currency: "EUR", ExpectedIncome: 300000, SavingsGoal: 40000,
		Lines: map[[16]byte]int64{groceriesID: 30000, diningID: 10000, rentID: 120000}}
	totals := []Total{
		// Groceries: 250 booked + 20 pending out, a 10 refund: 260 of 300.
		{CategoryID: ref(groceriesID), OutBooked: 25000, OutPending: 2000, InBooked: 1000, Count: 5},
		// Eating out: 120 of 100, over.
		{CategoryID: ref(diningID), OutBooked: 12000, Count: 3},
		// Shopping is not in the plan: an unplanned line.
		{CategoryID: ref(id(9)), OutBooked: 5000, Count: 1},
		{CategoryID: ref(salaryID), InBooked: 280000, InPending: 20000, Count: 2},
		// 500 moved to savings from the giro, 100 moved back.
		{CategoryID: ref(savingsID), OutBooked: 50000, InBooked: 10000, NetOutsideSavings: -40000, Count: 2},
		{CategoryID: ref(transfersID), OutBooked: 99999, InBooked: 99999, Count: 2},
		{OutBooked: 3000, OutPending: 500, InBooked: 1500, Count: 3},
	}
	all := append(categories, Category{ID: id(9), Name: "Shopping", Kind: KindExpense})
	month := Compute(october, "EUR", plan, all, totals)

	if len(month.Lines) != 4 {
		t.Fatalf("lines = %+v", month.Lines)
	}
	names := []string{}
	for _, line := range month.Lines {
		names = append(names, line.Category.Name)
	}
	if got := names[0] + "," + names[1] + "," + names[2] + "," + names[3]; got != "eating out,Groceries,Rent,Shopping" {
		t.Fatalf("order = %s, want the plan's lines by name, then unplanned", got)
	}
	dining, groceries, rent, shopping := month.Lines[0], month.Lines[1], month.Lines[2], month.Lines[3]
	if groceries.Spent != 26000 || groceries.Pending != 2000 || groceries.Remaining() != 4000 || *groceries.Percent() != 86 || groceries.Status() != Near {
		t.Fatalf("groceries = %+v (%d%%, %s)", groceries, *groceries.Percent(), groceries.Status())
	}
	if !groceries.Reached(NearPercent) || groceries.Reached(FullPercent) {
		t.Fatal("groceries reached 80% only")
	}
	if dining.Status() != Over || dining.Remaining() != -2000 || !dining.Reached(FullPercent) {
		t.Fatalf("dining = %+v", dining)
	}
	if rent.Spent != 0 || rent.Status() != Under || *rent.Percent() != 0 {
		t.Fatalf("rent = %+v", rent)
	}
	if shopping.Planned != 0 || shopping.Status() != Unplanned || shopping.Percent() != nil {
		t.Fatalf("shopping = %+v", shopping)
	}
	if month.Uncategorized.Spent != 3500 || month.Uncategorized.Pending != 500 || month.Uncategorized.Count != 3 {
		t.Fatalf("uncategorized = %+v", month.Uncategorized)
	}
	// Planned 1600; spent 260 + 120 + 50 + 35 uncategorized.
	if month.Planned != 160000 || month.Spent != 46500 || month.Pending != 2500 || month.Remaining() != 113500 {
		t.Fatalf("totals: planned %d spent %d pending %d", month.Planned, month.Spent, month.Pending)
	}
	// Income: the salary and 15 uncategorized money in.
	if month.Income != 301500 || month.IncomePending != 20000 {
		t.Fatalf("income %d pending %d", month.Income, month.IncomePending)
	}
	if month.MovedToSavings != 40000 || month.Saved() != 255000 || month.Leftover() != 215000 || *month.SavedPercent() != 637 {
		t.Fatalf("savings: moved %d saved %d leftover %d", month.MovedToSavings, month.Saved(), month.Leftover())
	}
	if month.Unallocated() != 300000-160000-40000 {
		t.Fatalf("unallocated = %d", month.Unallocated())
	}
	if over := month.OverBudget(); len(over) != 1 || over[0].Category.ID != diningID {
		t.Fatalf("over budget = %+v", over)
	}
}

func TestComputeWithoutAPlanListsSpendingOnly(t *testing.T) {
	month := Compute(october, "EUR", nil, categories, []Total{{CategoryID: ref(groceriesID), OutBooked: 1000}, {CategoryID: ref(rentID), InBooked: 500}})
	if month.Plan != nil || month.Planned != 0 || len(month.Lines) != 1 || month.Lines[0].Status() != Unplanned {
		t.Fatalf("month = %+v", month)
	}
	if month.Percent() != nil || month.SavedPercent() != nil || month.Unallocated() != 0 {
		t.Fatal("no plan, no percentages")
	}
	// A refund larger than the spending never makes spending negative.
	if month.Spent != 1000 {
		t.Fatalf("spent = %d", month.Spent)
	}
}

func TestSuggestTakesTheMedianOfTheMonthsWithHistory(t *testing.T) {
	july, august, september := AddMonths(october, -3), AddMonths(october, -2), AddMonths(october, -1)
	rows := []HistoryRow{
		{Month: july, CategoryID: ref(groceriesID), Out: 30050},
		{Month: august, CategoryID: ref(groceriesID), Out: 28000, In: 1000},
		{Month: september, CategoryID: ref(groceriesID), Out: 41000},
		// Eating out only in one month: the others count as 0.
		{Month: august, CategoryID: ref(diningID), Out: 9000},
		{Month: july, CategoryID: ref(salaryID), In: 300000},
		{Month: august, CategoryID: ref(salaryID), In: 310000},
		{Month: september, CategoryID: ref(salaryID), In: 290050},
	}
	got := Suggest(categories, rows, 2)
	if len(got.Months) != 3 || !got.Months[0].Equal(july) || !got.Months[2].Equal(september) {
		t.Fatalf("months = %v", got.Months)
	}
	if s := got.Categories[groceriesID]; s.Amount == nil || *s.Amount != 30100 || s.MonthsWithSpending != 3 {
		t.Fatalf("groceries = %+v (median 300.50 rounds up to 301)", s)
	}
	if s := got.Categories[diningID]; s.Amount == nil || *s.Amount != 0 || s.MonthsWithSpending != 1 {
		t.Fatalf("eating out = %+v", s)
	}
	if s := got.Categories[rentID]; s.Amount != nil || s.MonthsWithSpending != 0 {
		t.Fatalf("rent = %+v, want no history", s)
	}
	if _, ok := got.Categories[savingsID]; ok {
		t.Fatal("Savings is not an expense line")
	}
	if got.Income == nil || *got.Income != 300000 {
		t.Fatalf("income = %v", got.Income)
	}
	// Saved per month: 2699.50, 2740.00, 2490.50; the median rounds down.
	if got.Saved == nil || *got.Saved != 269900 {
		t.Fatalf("saved = %v", *got.Saved)
	}

	// Two months: the median is between them, rounded up for spending.
	two := Suggest(categories, rows[1:3], 2)
	if s := two.Categories[groceriesID]; *s.Amount != 34000 {
		t.Fatalf("two months: %+v", *s.Amount)
	}
	empty := Suggest(categories, nil, 2)
	if len(empty.Months) != 0 || empty.Income != nil || empty.Saved != nil || empty.Categories[groceriesID].Amount != nil {
		t.Fatalf("no history = %+v", empty)
	}
}

func TestSuggestionWindowEndsAtTheCurrentMonth(t *testing.T) {
	from, to := SuggestionWindow(AddMonths(october, 2), october)
	if !from.Equal(AddMonths(october, -3)) || !to.Equal(october) {
		t.Fatalf("future month: %v %v", from, to)
	}
	from, to = SuggestionWindow(AddMonths(october, -2), october)
	if !from.Equal(AddMonths(october, -5)) || !to.Equal(AddMonths(october, -2)) {
		t.Fatalf("past month: %v %v", from, to)
	}
}

func TestMonthsParseAndFollowTheOwnersZone(t *testing.T) {
	if _, err := ParseMonth("2026-13"); err == nil {
		t.Fatal("2026-13 parsed")
	}
	if _, err := ParseMonth("26-10"); err == nil {
		t.Fatal("26-10 parsed")
	}
	if month, err := ParseMonth("2026-10"); err != nil || !month.Equal(october) || MonthKey(month) != "2026-10" || MonthLabel(month) != "October 2026" {
		t.Fatalf("2026-10 = %v %v", month, err)
	}
	berlin, _ := time.LoadLocation("Europe/Berlin")
	// 23:30 UTC on 31 October is already November in Berlin.
	if got := CurrentMonth(time.Date(2026, time.October, 31, 23, 30, 0, 0, time.UTC), berlin); !got.Equal(AddMonths(october, 1)) {
		t.Fatalf("current month = %v", got)
	}
}
