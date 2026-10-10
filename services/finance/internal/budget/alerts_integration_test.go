package budget

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/GAMch1k/life-os/pkg/notify"
	store "github.com/GAMch1k/life-os/services/finance/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/finance/internal/testdb"
)

type fakeNotifier struct {
	enabled bool
	fail    error
	sent    []notify.Notification
}

func (f *fakeNotifier) Enabled() bool { return f.enabled }

func (f *fakeNotifier) Send(_ context.Context, n notify.Notification) error {
	if f.fail != nil {
		return f.fail
	}
	f.sent = append(f.sent, n)
	return nil
}

func (f *fakeNotifier) take() []notify.Notification {
	sent := f.sent
	f.sent = nil
	return sent
}

type alertWorld struct {
	t        *testing.T
	q        *store.Queries
	account  store.Account
	notifier *fakeNotifier
	alerts   *Alerts
	now      time.Time
}

func newAlertWorld(t *testing.T) *alertWorld {
	t.Helper()
	pool := testdb.Open(t, "")
	q := store.New(pool)
	account, err := q.CreateAccount(t.Context(), store.CreateAccountParams{Name: "Giro", Currency: "EUR", Kind: "bank"})
	if err != nil {
		t.Fatal(err)
	}
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	w := &alertWorld{t: t, q: q, account: account, notifier: &fakeNotifier{enabled: true}}
	w.alerts = NewAlerts(q, w.notifier, AlertConfig{Owner: berlin, Currency: "EUR"}, slog.New(slog.NewTextHandler(io.Discard, nil))).
		WithClock(func() time.Time { return w.now })
	return w
}

func (w *alertWorld) category(name, kind string) store.FinancialCategory {
	w.t.Helper()
	category, err := w.q.CreateFinancialCategory(w.t.Context(), store.CreateFinancialCategoryParams{Name: name, Kind: kind})
	if err != nil {
		w.t.Fatal(err)
	}
	return category
}

func (w *alertWorld) spend(category store.FinancialCategory, amount int64, day, status string) {
	w.t.Helper()
	date, _ := time.Parse(time.DateOnly, day)
	if _, err := w.q.CreateTransaction(w.t.Context(), store.CreateTransactionParams{AccountID: w.account.ID, CategoryID: category.ID, AmountMinor: amount,
		Currency: "EUR", OccurredOn: Date(date), Status: status, SourceMetadata: []byte(`{}`)}); err != nil {
		w.t.Fatal(err)
	}
}

func (w *alertWorld) plan(month string, income, goal int64, lines map[store.FinancialCategory]int64) {
	w.t.Helper()
	parsed, _ := ParseMonth(month)
	plan, err := w.q.UpsertBudgetPlan(w.t.Context(), store.UpsertBudgetPlanParams{Month: Date(parsed), Scope: ScopeRegular, Currency: "EUR",
		ExpectedIncomeMinor: income, SavingsGoalMinor: goal})
	if err != nil {
		w.t.Fatal(err)
	}
	insert := store.InsertBudgetPlanLinesParams{PlanID: plan.ID}
	for category, amount := range lines {
		insert.CategoryIds = append(insert.CategoryIds, category.ID)
		insert.Amounts = append(insert.Amounts, amount)
	}
	if err := w.q.InsertBudgetPlanLines(w.t.Context(), insert); err != nil {
		w.t.Fatal(err)
	}
}

func (w *alertWorld) run(at string) (AlertResult, []notify.Notification) {
	w.t.Helper()
	parsed, err := time.ParseInLocation("2006-01-02 15:04", at, w.alerts.owner)
	if err != nil {
		w.t.Fatal(err)
	}
	w.now = parsed
	result, runErr := w.alerts.RunOnce(w.t.Context())
	if runErr != nil && w.notifier.fail == nil {
		w.t.Fatalf("run at %s: %v", at, runErr)
	}
	return result, w.notifier.take()
}

func (w *alertWorld) claimed(month string) []string {
	w.t.Helper()
	parsed, _ := ParseMonth(month)
	keys, err := w.q.ListBudgetNotifications(w.t.Context(), Date(parsed))
	if err != nil {
		w.t.Fatal(err)
	}
	return keys
}

func titles(sent []notify.Notification) string {
	out := make([]string, 0, len(sent))
	for _, n := range sent {
		out = append(out, n.Title)
	}
	return strings.Join(out, " | ")
}

func TestBudgetAlertsNotifyOncePerCategoryAndThreshold(t *testing.T) {
	w := newAlertWorld(t)
	groceries := w.category("Groceries", KindExpense)
	dining := w.category("Eating out", KindExpense)
	rent := w.category("Rent", KindExpense)
	transport := w.category("Transport", KindExpense)
	fun := w.category("Fun", KindExpense)
	salary := w.category("Salary", KindIncome)
	w.plan("2026-10", 300000, 20000, map[store.FinancialCategory]int64{groceries: 10000, dining: 5000, rent: 100000, transport: 1000, fun: 1000})

	// No plan for September: no summary on 2 October.
	if result, sent := w.run("2026-10-02 10:00"); len(sent) != 0 || result != (AlertResult{}) {
		t.Fatalf("early October: %+v %s", result, titles(sent))
	}
	w.spend(groceries, -8500, "2026-10-03", "booked")
	w.spend(dining, -6000, "2026-10-04", "booked")
	w.spend(salary, 300000, "2026-10-01", "booked")
	result, sent := w.run("2026-10-15 12:00")
	if result.Sent != 2 || titles(sent) != "Eating out is over budget | Groceries budget at 85%" {
		t.Fatalf("first check: %+v %s", result, titles(sent))
	}
	over, near := sent[0], sent[1]
	if over.Body != "€60.00 of €50.00 spent in October, €10.00 over." || over.Importance != notify.Low || over.Category != AlertCategory ||
		over.Link != "/finance?tab=budget" || over.DedupeKey != "finance.budget:2026-10:"+uuidText(dining.ID.Bytes)+":100" {
		t.Fatalf("over = %+v", over)
	}
	if near.Body != "€85.00 of €100.00 spent in October, €15.00 left." || near.DedupeKey != "finance.budget:2026-10:"+uuidText(groceries.ID.Bytes)+":80" {
		t.Fatalf("near = %+v", near)
	}
	// Nothing is sent twice; past 100% the 80% notice is never sent.
	if result, sent := w.run("2026-10-15 12:05"); len(sent) != 0 || result.Handled != 2 {
		t.Fatalf("second check: %+v %s", result, titles(sent))
	}
	if keys := strings.Join(w.claimed("2026-10"), " "); !strings.Contains(keys, uuidText(dining.ID.Bytes)+":80") {
		t.Fatalf("claimed = %s", keys)
	}
	// Pending spending counts.
	w.spend(groceries, -2000, "2026-10-16", "pending")
	if _, sent := w.run("2026-10-16 09:00"); titles(sent) != "Groceries is over budget" || sent[0].Body != "€105.00 of €100.00 spent in October, €5.00 over. Includes €20.00 pending." {
		t.Fatalf("groceries over: %+v", sent)
	}

	// A send that fails for now is retried; a refused one is not.
	w.spend(transport, -900, "2026-10-17", "booked")
	w.notifier.fail = &notify.StatusError{Status: 503}
	if result, _ := w.run("2026-10-17 10:00"); result.Retry != 1 || result.Sent != 0 {
		t.Fatalf("failed send: %+v", result)
	}
	if keys := strings.Join(w.claimed("2026-10"), " "); strings.Contains(keys, uuidText(transport.ID.Bytes)) {
		t.Fatalf("the failed claim was kept: %s", keys)
	}
	w.notifier.fail = nil
	if _, sent := w.run("2026-10-17 10:05"); titles(sent) != "Transport budget at 90%" {
		t.Fatalf("retried: %s", titles(sent))
	}
	w.spend(fun, -1000, "2026-10-18", "booked")
	w.notifier.fail = &notify.StatusError{Status: 400, Code: "invalid_request"}
	if result, _ := w.run("2026-10-18 10:00"); result.Refused != 1 {
		t.Fatalf("refused: %+v", result)
	}
	w.notifier.fail = nil
	if result, sent := w.run("2026-10-18 10:05"); len(sent) != 0 || result.Handled != 4 {
		t.Fatalf("after a refusal: %+v %s", result, titles(sent))
	}

	// The month-end summary: on the 1st at 09:00, once, within a week.
	if _, sent := w.run("2026-11-01 08:59"); len(sent) != 0 {
		t.Fatalf("before 09:00: %s", titles(sent))
	}
	_, sent = w.run("2026-11-01 09:00")
	if len(sent) != 1 {
		t.Fatalf("summary: %s", titles(sent))
	}
	summary := sent[0]
	if summary.Title != "October: spent €184.00 of €1,170.00, saved €2,816.00" ||
		summary.Body != "Savings goal: €2,816.00 of €200.00. Over budget: Eating out (€60.00 of €50.00), Groceries (€105.00 of €100.00)." ||
		summary.Category != SummaryCategory || summary.Importance != notify.Low || summary.Link != "/finance?tab=budget&month=2026-10" ||
		summary.DedupeKey != "finance.budget:2026-10:summary" || !summary.OccurredAt.Equal(time.Date(2026, time.November, 1, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("summary = %+v", summary)
	}
	if _, sent := w.run("2026-11-01 09:05"); len(sent) != 0 {
		t.Fatalf("summary repeated: %s", titles(sent))
	}
}

func TestBudgetAlertsNeedAPlanAndNotifications(t *testing.T) {
	w := newAlertWorld(t)
	groceries := w.category("Groceries", KindExpense)
	w.spend(groceries, -100000, "2026-10-03", "booked")
	if result, sent := w.run("2026-10-15 12:00"); len(sent) != 0 || result != (AlertResult{}) {
		t.Fatalf("no plan: %+v %s", result, titles(sent))
	}
	// The summary waits a week at most for a Finance that was down.
	w.plan("2026-10", 0, 0, map[store.FinancialCategory]int64{groceries: 1000})
	w.notifier.enabled = false
	if result, sent := w.run("2026-10-15 12:05"); len(sent) != 0 || result != (AlertResult{}) {
		t.Fatalf("disabled: %+v", result)
	}
	w.notifier.enabled = true
	if _, sent := w.run("2026-11-08 09:00"); len(sent) != 0 {
		t.Fatalf("a week late: %s", titles(sent))
	}
	if _, sent := w.run("2026-11-07 23:00"); titles(sent) != "October: spent €1,000.00 of €10.00, €1,000.00 more than came in" {
		t.Fatalf("late summary: %s", titles(sent))
	}
}
