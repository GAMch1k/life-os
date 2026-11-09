package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/finance/internal/api/generated"
)

// acme is a made-up employer: the salary comes from it at the end of each
// month with "Lohn - Gehalt Abrechnung MM/YYYY".
const acme = "ACME Robotics GmbH"

// paydaySync is a sync of the Girokonto with transactions booked from
// `from` to `to`.
func paydaySync(from, to string, transactions ...map[string]any) map[string]any {
	return map[string]any{"connectionId": connectionID, "provider": "enablebanking", "institution": "Deutsche Bank",
		"window": map[string]any{"from": from, "to": to}, "accounts": []map[string]any{giroInput(100000, 100000)}, "transactions": transactions}
}

func salaryTx(id, status, date string, amount int64) map[string]any {
	return bankTx("acc-1", id, status, date, amount, acme, "description", "Lohn - Gehalt Abrechnung "+date[5:7]+"/"+date[:4])
}

func (c apiClient) setPayday(body map[string]any) generated.PaydaySettings {
	c.t.Helper()
	return expect[generated.PaydaySettings](c, http.MethodPut, "/v1/settings/payday", body, http.StatusOK, activity.ActorHeader, "user")
}

func (c apiClient) plansFor(month string) []generated.AiBudgetPlan {
	c.t.Helper()
	return expect[generated.AiBudgetPlanList](c, http.MethodGet, "/v1/ai-budget-plans?month="+month, nil, http.StatusOK).Items
}

// at sets the clock to an hour of a day in the owner's Berlin.
func (c apiClient) at(day string, hour int) {
	c.t.Helper()
	date, err := time.Parse(time.DateOnly, day)
	if err != nil {
		c.t.Fatal(err)
	}
	instant := time.Date(date.Year(), date.Month(), date.Day(), hour, 0, 0, 0, c.handler.location)
	c.handler.now = func() time.Time { return instant }
}

func TestASalaryOnThe28thAsksOnceForNextMonthsPlan(t *testing.T) {
	c := newAPIClient(t)
	c.setPayday(map[string]any{"enabled": true, "payerMatch": "acme robotics", "minAmountMinor": 100000})
	c.at("2026-10-28", 18)
	seen := len(c.recorder.all())
	sync := paydaySync("2026-10-20", "2026-10-28", salaryTx("salary-10", "booked", "2026-10-28", 310000))
	c.syncBank(sync)
	salary := c.byExternalID("salary-10")

	plans := c.plansFor("2026-11")
	if len(plans) != 1 {
		t.Fatalf("plans = %+v", plans)
	}
	plan := plans[0]
	if plan.Trigger != generated.AiBudgetPlanTriggerSalary || plan.State != generated.AiBudgetPlanStateRequested || plan.Salary == nil ||
		plan.Salary.BookingDate.String() != "2026-10-28" || *plan.Salary.TransactionId != salary.Id || plan.RequestedBy.Kind != generated.System ||
		*plan.RequestedBy.Name != "finance" || plan.SavingsGoal != nil {
		t.Fatalf("plan = %+v", plan)
	}
	var requested []activity.Event
	for _, event := range c.recorder.since(&seen) {
		if event.Type == "finance.ai_budget_plan.requested" {
			requested = append(requested, event)
		}
	}
	if len(requested) != 1 || requested[0].Summary != "AI budget plan for November 2026 requested when your salary arrived" ||
		requested[0].Actor != activity.System("finance") || requested[0].Data["trigger"] != "salary" || requested[0].Data["salaryBookedOn"] != "2026-10-28" {
		t.Fatalf("requested = %+v", requested)
	}
	// The job claims it like any other.
	if claimed := c.claimPlan(); claimed == nil || claimed.Id != plan.Id {
		t.Fatalf("claimed = %+v", claimed)
	}

	// The same sync again, and a second payment from the payer that
	// month, ask for nothing more.
	c.syncBank(sync)
	c.at("2026-10-29", 18)
	c.syncBank(paydaySync("2026-10-20", "2026-10-29", salaryTx("salary-10", "booked", "2026-10-28", 310000),
		salaryTx("salary-10-extra", "booked", "2026-10-29", 150000)))
	if plans := c.plansFor("2026-11"); len(plans) != 1 {
		t.Fatalf("plans after the repeats = %+v", plans)
	}

	// The job's own plan on the 1st answers with the salary's.
	c.at("2026-11-01", 6)
	if scheduled := c.requestPlan(map[string]any{"month": "2026-11", "trigger": "schedule"}, http.StatusOK, planActor); scheduled.Id != plan.Id {
		t.Fatalf("the schedule made its own plan: %+v", scheduled)
	}
	// Only Finance asks for salary plans.
	c.planError(http.MethodPost, "/v1/ai-budget-plans", map[string]any{"month": "2026-12", "trigger": "salary"}, "user", http.StatusBadRequest, "invalid_request")
}

func TestASalaryBeforeThe15thAsksForItsOwnMonth(t *testing.T) {
	c := newAPIClient(t)
	c.setPayday(map[string]any{"enabled": true, "payerMatch": acme})
	c.at("2026-11-03", 9)
	c.syncBank(paydaySync("2026-11-01", "2026-11-03", salaryTx("salary-11", "booked", "2026-11-03", 310000)))
	if plans := c.plansFor("2026-11"); len(plans) != 1 || plans[0].Trigger != generated.AiBudgetPlanTriggerSalary || plans[0].Salary.BookingDate.String() != "2026-11-03" {
		t.Fatalf("November = %+v", plans)
	}
	if plans := c.plansFor("2026-12"); len(plans) != 0 {
		t.Fatalf("December = %+v", plans)
	}
}

func TestOnlyABookedRecentPaymentFromThePayerAsks(t *testing.T) {
	c := newAPIClient(t)
	c.setPayday(map[string]any{"enabled": true, "payerMatch": "ACME Robotics", "minAmountMinor": 100000})
	c.at("2026-10-28", 18)
	asked := func() int {
		t.Helper()
		return len(expect[generated.AiBudgetPlanList](c, http.MethodGet, "/v1/ai-budget-plans", nil, http.StatusOK).Items)
	}

	// Pending, the salary asks for nothing yet.
	pending := salaryTx("salary-10", "pending", "2026-10-28", 310000)
	c.syncBank(paydaySync("2026-10-01", "2026-10-28", pending))
	// Neither do a refund below the minimum, another company, nor history
	// older than 40 days.
	c.syncBank(paydaySync("2026-07-01", "2026-10-28", pending,
		salaryTx("refund", "booked", "2026-10-27", 4500),
		bankTx("acc-1", "other", "booked", "2026-10-27", 310000, "ACME Logistics GmbH"),
		salaryTx("salary-09", "booked", "2026-09-17", 310000)))
	if n := asked(); n != 0 {
		t.Fatalf("%d plans asked for", n)
	}
	// Switched off, a booked salary asks for nothing either.
	c.setPayday(map[string]any{"enabled": false, "payerMatch": "ACME Robotics", "minAmountMinor": 100000})
	c.syncBank(paydaySync("2026-10-01", "2026-10-28", salaryTx("salary-10-early", "booked", "2026-10-26", 120000)))
	if n := asked(); n != 0 {
		t.Fatalf("%d plans asked for while off", n)
	}

	// Booked under the pending one's id, it asks.
	c.setPayday(map[string]any{"enabled": true, "payerMatch": "ACME Robotics", "minAmountMinor": 100000})
	c.syncBank(paydaySync("2026-10-01", "2026-10-28", salaryTx("salary-10", "booked", "2026-10-28", 310000)))
	plans := c.plansFor("2026-11")
	if len(plans) != 1 || *plans[0].Salary.TransactionId != c.byExternalID("salary-10").Id {
		t.Fatalf("plans = %+v", plans)
	}

	// A salary for a month already past asks for nothing.
	c2 := newAPIClient(t)
	c2.setPayday(map[string]any{"enabled": true, "payerMatch": acme})
	c2.at("2026-10-10", 9)
	c2.syncBank(paydaySync("2026-09-01", "2026-10-10", salaryTx("salary-09", "booked", "2026-09-05", 310000)))
	if list := expect[generated.AiBudgetPlanList](c2, http.MethodGet, "/v1/ai-budget-plans", nil, http.StatusOK); len(list.Items) != 0 {
		t.Fatalf("a past month was planned: %+v", list.Items)
	}
}

func TestAMonthsOtherPlansDecideWhetherTheSalaryAsks(t *testing.T) {
	c := newAPIClient(t)
	c.setPayday(map[string]any{"enabled": true, "payerMatch": acme})
	c.at("2026-10-28", 18)
	// The owner already asked for November: the salary asks for nothing.
	owners := c.requestPlan(map[string]any{"month": "2026-11"}, http.StatusAccepted, "user")
	c.syncBank(paydaySync("2026-10-20", "2026-10-28", salaryTx("salary-a", "booked", "2026-10-28", 310000)))
	if plans := c.plansFor("2026-11"); len(plans) != 1 || plans[0].Id != owners.Id {
		t.Fatalf("plans = %+v", plans)
	}
	// Once that plan is dismissed, the next payment from the payer asks.
	expect[generated.AiBudgetPlan](c, http.MethodPost, "/v1/ai-budget-plans/"+owners.Id.String()+"/dismiss", nil, http.StatusOK, activity.ActorHeader, "user")
	c.syncBank(paydaySync("2026-10-20", "2026-10-28", salaryTx("salary-b", "booked", "2026-10-28", 310000)))
	plans := c.plansFor("2026-11")
	if len(plans) != 2 || plans[0].Trigger != generated.AiBudgetPlanTriggerSalary {
		t.Fatalf("plans = %+v", plans)
	}
	salary := plans[0]

	// The salary's plan failed for good: the job's plan on the 1st is made
	// after all.
	c.claimPlan()
	expect[generated.AiBudgetPlan](c, http.MethodPost, "/v1/ai-budget-plans/"+salary.Id.String()+"/failure", map[string]any{"error": "unusable answers"},
		http.StatusOK, activity.ActorHeader, planActor)
	// Both plans were asked for on the 28th, before November began, but the
	// database stamps created_at with the real clock, not the test's.
	if _, err := c.handler.pool.Exec(t.Context(), "UPDATE ai_budget_plans SET created_at = $1", c.handler.now()); err != nil {
		t.Fatal(err)
	}
	c.at("2026-11-01", 6)
	scheduled := c.requestPlan(map[string]any{"month": "2026-11", "trigger": "schedule"}, http.StatusAccepted, planActor)
	if scheduled.Trigger != generated.AiBudgetPlanTriggerSchedule {
		t.Fatalf("scheduled = %+v", scheduled)
	}

	// A salary plan the owner dismissed still stands for its month.
	c.at("2026-11-28", 18)
	c.syncBank(paydaySync("2026-11-20", "2026-11-28", salaryTx("salary-c", "booked", "2026-11-28", 310000)))
	december := c.plansFor("2026-12")
	if len(december) != 1 {
		t.Fatalf("December = %+v", december)
	}
	expect[generated.AiBudgetPlan](c, http.MethodPost, "/v1/ai-budget-plans/"+december[0].Id.String()+"/dismiss", nil, http.StatusOK, activity.ActorHeader, "user")
	c.at("2026-12-01", 6)
	if again := c.requestPlan(map[string]any{"month": "2026-12", "trigger": "schedule"}, http.StatusOK, planActor); again.Id != december[0].Id {
		t.Fatalf("the schedule made its own December plan: %+v", again)
	}
}

func TestManualAndCSVSalariesAskToo(t *testing.T) {
	c := newAPIClient(t)
	c.setPayday(map[string]any{"enabled": true, "payerMatch": acme})
	c.at("2026-10-28", 18)
	cash := c.cash()
	// A pending manual entry asks once it is booked.
	pending := c.spend(map[string]any{"accountId": cash.Id, "amount": eur(310000), "bookingDate": "2026-10-28", "counterparty": acme, "status": "pending"})
	if plans := c.plansFor("2026-11"); len(plans) != 0 {
		t.Fatalf("a pending entry asked: %+v", plans)
	}
	expect[generated.Transaction](c, http.MethodPatch, "/v1/transactions/"+pending.Id.String(), map[string]any{"status": "booked"}, http.StatusOK)
	plans := c.plansFor("2026-11")
	if len(plans) != 1 || *plans[0].Salary.TransactionId != pending.Id {
		t.Fatalf("plans = %+v", plans)
	}
	// An entry dated months ahead asks for nothing.
	c.spend(map[string]any{"accountId": cash.Id, "amount": eur(310000), "bookingDate": "2027-03-29", "counterparty": acme})
	if april := c.plansFor("2027-04"); len(april) != 0 {
		t.Fatalf("April = %+v", april)
	}
	// An entry created booked asks too (here for December, a month later).
	c.at("2026-11-27", 18)
	booked := c.spend(map[string]any{"accountId": cash.Id, "amount": eur(310000), "bookingDate": "2026-11-27", "counterparty": acme})
	if december := c.plansFor("2026-12"); len(december) != 1 || *december[0].Salary.TransactionId != booked.Id {
		t.Fatalf("December = %+v", december)
	}
	// So does a CSV row.
	c.at("2027-01-04", 9)
	expect[generated.CsvImport](c, http.MethodPost, "/v1/imports/csv?accountId="+cash.Id.String(),
		csvBody("date,amount,currency,counterparty,description\n2027-01-04,3100.00,EUR,"+acme+",Lohn - Gehalt Abrechnung 01/2027\n"), http.StatusOK)
	if january := c.plansFor("2027-01"); len(january) != 1 || january[0].Salary.BookingDate.String() != "2027-01-04" {
		t.Fatalf("January = %+v", january)
	}
}

func TestPaydaySettingsAndTheSuggestion(t *testing.T) {
	c := newAPIClient(t)
	// It is 11 October in Berlin.
	settings := expect[generated.PaydaySettings](c, http.MethodGet, "/v1/settings/payday", nil, http.StatusOK)
	if settings.Enabled || settings.PayerMatch != nil || settings.MinAmount != nil || settings.Suggestion != nil || settings.Currency != "EUR" {
		t.Fatalf("defaults = %+v", settings)
	}
	cash := c.cash()
	income := func(amount int64, date, counterparty string) {
		c.spend(map[string]any{"accountId": cash.Id, "amount": eur(amount), "bookingDate": date, "counterparty": counterparty})
	}
	income(310000, "2026-08-28", acme)
	income(312000, "2026-09-29", "ACME ROBOTICS GMBH")
	income(4500, "2026-10-02", acme)
	income(25500, "2026-08-15", "Familienkasse")
	income(25500, "2026-09-15", "Familienkasse")
	income(900000, "2026-09-01", "Erbengemeinschaft Weber")
	suggestion := expect[generated.PaydaySettings](c, http.MethodGet, "/v1/settings/payday", nil, http.StatusOK).Suggestion
	if suggestion == nil || suggestion.PayerMatch != "ACME ROBOTICS GMBH" || suggestion.TypicalAmount.Amount != 312000 || suggestion.Payments != 2 ||
		suggestion.LastDate.String() != "2026-09-29" {
		t.Fatalf("suggestion = %+v", suggestion)
	}

	for _, bad := range []map[string]any{
		{"payerMatch": acme}, {"enabled": true}, {"enabled": true, "payerMatch": "  "}, {"enabled": true, "payerMatch": strings.Repeat("x", 201)},
		{"enabled": true, "payerMatch": acme, "minAmountMinor": 0},
	} {
		c.expectError(http.MethodPut, "/v1/settings/payday", bad, http.StatusBadRequest, "invalid_request")
	}
	seen := len(c.recorder.all())
	saved := c.setPayday(map[string]any{"enabled": true, "payerMatch": " " + acme + " ", "minAmountMinor": 100000})
	if !saved.Enabled || *saved.PayerMatch != acme || saved.MinAmount.Amount != 100000 || saved.Suggestion == nil {
		t.Fatalf("saved = %+v", saved)
	}
	events := c.recorder.since(&seen)
	if len(events) != 1 || events[0].Type != "finance.settings.updated" || events[0].Subject.ID != "payday" || events[0].Actor != activity.User() ||
		events[0].Summary != "Payday updated: plan the budget when money from “ACME Robotics GmbH” of at least €1,000.00 arrives" {
		t.Fatalf("events = %+v", events)
	}
	// The same again records nothing; switched off it says so.
	c.setPayday(map[string]any{"enabled": true, "payerMatch": acme, "minAmountMinor": 100000})
	if events := c.recorder.since(&seen); len(events) != 0 {
		t.Fatalf("a repeat recorded %+v", events)
	}
	off := c.setPayday(map[string]any{"enabled": false, "payerMatch": acme})
	events = c.recorder.since(&seen)
	if off.Enabled || off.MinAmount != nil || len(events) != 1 || events[0].Summary != "Payday updated: plan the budget when money from “ACME Robotics GmbH” arrives (off)" {
		t.Fatalf("off = %+v events %+v", off, events)
	}
}
