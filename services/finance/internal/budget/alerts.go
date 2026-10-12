package budget

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	googleuuid "github.com/google/uuid"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/GAMch1k/life-os/pkg/notify"
	financemoney "github.com/GAMch1k/life-os/services/finance/internal/money"
	store "github.com/GAMch1k/life-os/services/finance/internal/repository/generated"
)

// Notification categories, the Dashboard link, and the alert defaults.
const (
	AlertCategory   = "finance.budget"
	SummaryCategory = "finance.budget.summary"
	BudgetLink      = "/finance?tab=budget"
	// DefaultAlertInterval is how often the budget is checked, besides
	// right after a change (Wake).
	DefaultAlertInterval = 5 * time.Minute
	// SummaryHour is when, owner-local on the 1st, the previous month's
	// summary is due; SummaryDays is how long after that it is still sent
	// when Finance was down.
	SummaryHour = 9
	SummaryDays = 7
)

// Notifier sends notifications (*notify.Client).
type Notifier interface {
	Enabled() bool
	Send(ctx context.Context, n notify.Notification) error
}

// AlertStore is what the alerts need from the repository.
type AlertStore interface {
	Store
	ClaimBudgetNotification(ctx context.Context, arg store.ClaimBudgetNotificationParams) (int64, error)
	ReleaseBudgetNotification(ctx context.Context, dedupeKey string) error
	ListAIBudgetPlansToNotify(ctx context.Context, since pgtype.Timestamptz) ([]store.AiBudgetPlan, error)
}

// AlertConfig configures Alerts.
type AlertConfig struct {
	// Interval between checks (DefaultAlertInterval when zero).
	Interval time.Duration
	// Owner is the owner's timezone (LIFE_OS_TIMEZONE); nil is UTC.
	Owner *time.Location
	// Currency is the budget currency when no plan names one.
	Currency string
}

// Alerts notify the owner about the budget, at low importance: once per
// category and month when its spending reaches 80% and 100% of the planned
// amount, and on the 1st at 09:00 owner-local with the previous month's
// summary; and at medium importance when an AI budget plan the owner did
// not ask for is ready. Each notification is claimed in
// budget_notifications before it is sent, so a restart repeats none; a
// send that fails for now gives the claim back and is retried at the next
// check (the Notifications dedupe key makes a repeat harmless).
type Alerts struct {
	queries  AlertStore
	notifier Notifier
	owner    *time.Location
	currency string
	interval time.Duration
	now      func() time.Time
	logger   *slog.Logger
	wake     chan struct{}
}

// NewAlerts returns the budget alerts; a notifier that is not Enabled
// leaves them off.
func NewAlerts(queries AlertStore, notifier Notifier, config AlertConfig, logger *slog.Logger) *Alerts {
	if config.Interval <= 0 {
		config.Interval = DefaultAlertInterval
	}
	if config.Owner == nil {
		config.Owner = time.UTC
	}
	if config.Currency == "" {
		config.Currency = "EUR"
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Alerts{queries: queries, notifier: notifier, owner: config.Owner, currency: config.Currency, interval: config.Interval,
		now: time.Now, logger: logger, wake: make(chan struct{}, 1)}
}

// WithClock replaces the clock (for tests).
func (a *Alerts) WithClock(now func() time.Time) *Alerts {
	a.now = now
	return a
}

// Enabled reports whether budget notifications are sent.
func (a *Alerts) Enabled() bool { return a != nil && a.notifier != nil && a.notifier.Enabled() }

// Wake asks for a check soon, after money or the plan changed. It never
// blocks.
func (a *Alerts) Wake() {
	if !a.Enabled() {
		return
	}
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

// Run checks the budget at once, then every Interval and after each Wake,
// until ctx ends. It returns at once when budget notifications are off.
func (a *Alerts) Run(ctx context.Context) {
	if !a.Enabled() {
		a.logger.Warn("budget notifications are off: NOTIFICATIONS_SERVICE_URL is not set")
		return
	}
	a.logger.Info("budget notifications started", "interval", a.interval.String(), "timeZone", a.owner.String())
	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()
	for {
		if result, err := a.RunOnce(ctx); err != nil && ctx.Err() == nil {
			a.logger.Warn("budget check failed", "error", err)
		} else if result.Sent+result.Refused > 0 {
			a.logger.Info("budget notifications sent", "sent", result.Sent, "refused", result.Refused)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-a.wake:
		}
	}
}

// AlertResult counts what one check did: Sent notifications, Refused by
// Notifications (not retried), Retry (failed for now, retried next check),
// and Handled (claimed before).
type AlertResult struct {
	Sent, Refused, Retry, Handled int
}

// Key is a budget notification's dedupe key: finance.budget:<YYYY-MM>:
// <categoryId>:<percent>, or finance.budget:<YYYY-MM>:summary.
func Key(month time.Time, parts ...string) string {
	return strings.Join(append([]string{AlertCategory, MonthKey(month)}, parts...), ":")
}

// RunOnce checks the current month's categories, sends the previous
// month's summary when due, and notifies the AI budget plans that are
// ready.
func (a *Alerts) RunOnce(ctx context.Context) (AlertResult, error) {
	var result AlertResult
	if !a.Enabled() {
		return result, nil
	}
	now := a.now()
	current := CurrentMonth(now, a.owner)
	month, err := Load(ctx, a.queries, current, a.currency)
	if err != nil {
		return result, fmt.Errorf("load the budget: %w", err)
	}
	var errs []error
	if month.Plan != nil {
		for _, line := range month.Lines {
			id := uuidText(line.Category.ID)
			switch {
			case line.Reached(FullPercent):
				// Past 100% the 80% notice is moot: it is claimed unsent.
				errs = append(errs, a.send(ctx, &result, current, lineMessage(month, line, now), Key(current, id, "80")))
			case line.Reached(NearPercent):
				errs = append(errs, a.send(ctx, &result, current, lineMessage(month, line, now)))
			}
		}
	}
	due := time.Date(current.Year(), current.Month(), 1, SummaryHour, 0, 0, 0, a.owner)
	if !now.Before(due) && now.Before(due.AddDate(0, 0, SummaryDays)) {
		previous := AddMonths(current, -1)
		last, err := Load(ctx, a.queries, previous, a.currency)
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("load the previous month's budget: %w", err))
		case last.Plan != nil:
			errs = append(errs, a.send(ctx, &result, previous, summaryMessage(last, due)))
		}
	}
	errs = append(errs, a.readyPlans(ctx, &result, now))
	return result, errors.Join(errs...)
}

// send claims the notification (and the alsoClaim keys) and sends it; a
// failure that may pass gives the claims back.
func (a *Alerts) send(ctx context.Context, result *AlertResult, month time.Time, message notify.Notification, alsoClaim ...string) error {
	claimed, err := a.queries.ClaimBudgetNotification(ctx, store.ClaimBudgetNotificationParams{DedupeKey: message.DedupeKey, Month: Date(month)})
	if err != nil {
		return fmt.Errorf("claim %s: %w", message.DedupeKey, err)
	}
	if claimed == 0 {
		result.Handled++
		return nil
	}
	claims := []string{message.DedupeKey}
	for _, key := range alsoClaim {
		extra, err := a.queries.ClaimBudgetNotification(ctx, store.ClaimBudgetNotificationParams{DedupeKey: key, Month: Date(month)})
		if err != nil {
			return errors.Join(fmt.Errorf("claim %s: %w", key, err), a.release(ctx, claims))
		}
		if extra == 1 {
			claims = append(claims, key)
		}
	}
	err = a.notifier.Send(ctx, message)
	switch {
	case err == nil:
		result.Sent++
		a.logger.Info("budget notification sent", "key", message.DedupeKey)
		return nil
	case permanent(err):
		// Refused: retrying would be refused again.
		result.Refused++
		a.logger.Warn("budget notification refused by notifications", "key", message.DedupeKey, "error", err)
		return nil
	}
	result.Retry++
	return errors.Join(err, a.release(context.WithoutCancel(ctx), claims))
}

func (a *Alerts) release(ctx context.Context, keys []string) error {
	var errs []error
	for _, key := range keys {
		if err := a.queries.ReleaseBudgetNotification(ctx, key); err != nil {
			errs = append(errs, fmt.Errorf("release %s: %w", key, err))
		}
	}
	return errors.Join(errs...)
}

// permanent reports whether Notifications refused the notification (4xx)
// or it was invalid; anything else may succeed later.
func permanent(err error) bool {
	var status *notify.StatusError
	return errors.Is(err, notify.ErrInvalid) || (errors.As(err, &status) && !status.Temporary())
}

func uuidText(id [16]byte) string { return googleuuid.UUID(id).String() }

// lineMessage is a category's notice: at 80% or more, used up at exactly
// 100%, or over budget.
func lineMessage(month Month, line Line, now time.Time) notify.Notification {
	format := func(amount int64) string { return financemoney.Format(amount, month.Currency) }
	name := line.Category.Name
	monthName := month.Month.Month().String()
	spent := fmt.Sprintf("%s of %s spent in %s", format(line.Spent), format(line.Planned), monthName)
	var title, body, threshold string
	switch {
	case line.Spent > line.Planned:
		title, body, threshold = name+" is over budget", fmt.Sprintf("%s, %s over.", spent, format(line.Spent-line.Planned)), "100"
	case line.Reached(FullPercent):
		title, body, threshold = name+" budget used up", spent+".", "100"
	default:
		title, body, threshold = fmt.Sprintf("%s budget at %d%%", name, *line.Percent()), fmt.Sprintf("%s, %s left.", spent, format(line.Remaining())), "80"
	}
	if line.Pending > 0 {
		body += fmt.Sprintf(" Includes %s pending.", format(line.Pending))
	}
	return notify.Notification{Title: title, Body: body, Importance: notify.Low, Category: AlertCategory, Link: BudgetLink,
		DedupeKey: Key(month.Month, uuidText(line.Category.ID), threshold), OccurredAt: now}
}

// summaryMessage is a month's end: "October: spent €1,234.00 of
// €2,000.00, saved €500.00".
func summaryMessage(month Month, due time.Time) notify.Notification {
	format := func(amount int64) string { return financemoney.Format(amount, month.Currency) }
	title := fmt.Sprintf("%s: spent %s", month.Month.Month(), format(month.Spent))
	if month.Planned > 0 {
		title += " of " + format(month.Planned)
	}
	if saved := month.Saved(); saved >= 0 {
		title += ", saved " + format(saved)
	} else {
		title += fmt.Sprintf(", %s more than came in", format(-saved))
	}
	var body []string
	if goal := month.Plan.SavingsGoal; goal > 0 {
		body = append(body, fmt.Sprintf("Savings goal: %s of %s.", format(max(0, month.Saved())), format(goal)))
	}
	if month.MovedToSavings != 0 {
		body = append(body, fmt.Sprintf("%s moved to Savings, %s left over.", format(month.MovedToSavings), format(month.Leftover())))
	}
	if over := month.OverBudget(); len(over) > 0 {
		names := make([]string, 0, len(over))
		for index, line := range over {
			if index == 5 {
				names = append(names, fmt.Sprintf("%d more", len(over)-5))
				break
			}
			names = append(names, fmt.Sprintf("%s (%s of %s)", line.Category.Name, format(line.Spent), format(line.Planned)))
		}
		body = append(body, "Over budget: "+strings.Join(names, ", ")+".")
	}
	if month.Uncategorized.Spent > 0 {
		body = append(body, fmt.Sprintf("%s has no category yet.", format(month.Uncategorized.Spent)))
	}
	return notify.Notification{Title: title, Body: strings.Join(body, " "), Importance: notify.Low, Category: SummaryCategory,
		Link: BudgetLink + "&month=" + MonthKey(month.Month), DedupeKey: Key(month.Month, "summary"), OccurredAt: due}
}
