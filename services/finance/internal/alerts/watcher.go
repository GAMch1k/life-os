// Package alerts keeps Finance's subscriptions up to date and notifies the
// owner about money: renewal reminders for confirmed subscriptions (low),
// large debits and low balances (medium). A background loop runs it at
// startup, every few minutes, and right after money or a setting changed.
package alerts

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/GAMch1k/life-os/pkg/notify"
	financemoney "github.com/GAMch1k/life-os/services/finance/internal/money"
	store "github.com/GAMch1k/life-os/services/finance/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/finance/internal/subscriptions"
)

// Notification categories, links, and the loop's defaults.
const (
	RenewalCategory    = "finance.subscription"
	LargeDebitCategory = "finance.large_debit"
	LowBalanceCategory = "finance.low_balance"
	SubscriptionsLink  = "/finance?tab=subscriptions"
	FinanceLink        = "/finance"
	// DefaultInterval is how often the alerts are checked, besides right
	// after a change (Wake).
	DefaultInterval = 5 * time.Minute
	// LargeDebitDays is how many days before today a large debit may be
	// booked and still be notified, so a first 90-day bank import does not
	// notify old ones.
	LargeDebitDays = 3
)

// Default settings, as the migration stores them.
const (
	DefaultLargeDebitMinor = 25000
	DefaultLowBalanceMinor = 10000
	DefaultLeadDays        = 3
)

// Notifier sends notifications (*notify.Client).
type Notifier interface {
	Enabled() bool
	Send(ctx context.Context, n notify.Notification) error
}

// Config configures a Watcher.
type Config struct {
	// Interval between checks (DefaultInterval when zero).
	Interval time.Duration
	// Owner is the owner's timezone (LIFE_OS_TIMEZONE); nil is UTC.
	Owner *time.Location
	// Currency is the default currency: thresholds are in it, and only its
	// accounts and transactions are checked.
	Currency string
}

// Watcher runs subscription detection and the money alerts. Detection runs
// at the first check, after each Wake, and once a day; the alerts at every
// check. Each notification is claimed in alert_notifications before it is
// sent, so a restart repeats none; a send that fails for now gives the claim
// back and is retried at the next check (the Notifications dedupe key makes
// a repeat harmless).
type Watcher struct {
	pool     *pgxpool.Pool
	notifier Notifier
	owner    *time.Location
	currency string
	interval time.Duration
	now      func() time.Time
	logger   *slog.Logger
	wake     chan struct{}
	// dirty asks for detection at the next check; detectedOn is the owner
	// day detection last ran.
	dirty      atomic.Bool
	detectedOn time.Time
}

// New returns a watcher over pool. A notifier that is not Enabled sends
// nothing; detection runs anyway.
func New(pool *pgxpool.Pool, notifier Notifier, config Config, logger *slog.Logger) *Watcher {
	if config.Interval <= 0 {
		config.Interval = DefaultInterval
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
	w := &Watcher{pool: pool, notifier: notifier, owner: config.Owner, currency: config.Currency, interval: config.Interval,
		now: time.Now, logger: logger, wake: make(chan struct{}, 1)}
	w.dirty.Store(true)
	return w
}

// WithClock replaces the clock (for tests).
func (w *Watcher) WithClock(now func() time.Time) *Watcher {
	w.now = now
	return w
}

// Wake asks for detection and a check soon, after money or a setting
// changed. It never blocks.
func (w *Watcher) Wake() {
	if w == nil {
		return
	}
	w.dirty.Store(true)
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *Watcher) sending() bool { return w.notifier != nil && w.notifier.Enabled() }

// Run checks at once, then every Interval and after each Wake, until ctx
// ends.
func (w *Watcher) Run(ctx context.Context) {
	if w.sending() {
		w.logger.Info("finance alerts started", "interval", w.interval.String(), "timeZone", w.owner.String())
	} else {
		w.logger.Warn("finance alert notifications are off: NOTIFICATIONS_SERVICE_URL is not set; subscriptions are still detected")
	}
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		result, err := w.RunOnce(ctx)
		switch {
		case err != nil && ctx.Err() == nil:
			w.logger.Warn("finance alerts check failed", "error", err)
		case result.Detection.Changed() || result.Sent+result.Refused > 0:
			w.logger.Info("finance alerts checked", "suggested", result.Detection.Suggested, "updated", result.Detection.Updated,
				"removed", result.Detection.Removed, "sent", result.Sent, "refused", result.Refused)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-w.wake:
		}
	}
}

// Result counts what one check did: what detection changed, notifications
// Sent, Refused by Notifications (not retried), Retry (failed for now,
// retried next check), and Handled (claimed before).
type Result struct {
	Detection                     subscriptions.SyncResult
	Sent, Refused, Retry, Handled int
}

// Settings are the alert settings.
type Settings struct {
	LargeDebitEnabled bool
	LargeDebitMinor   int64
	LowBalanceEnabled bool
	LowBalanceMinor   int64
	RenewalsEnabled   bool
	LeadDays          int
	UpdatedAt         time.Time
}

// DefaultSettings are the settings before the owner changed any.
func DefaultSettings() Settings {
	return Settings{LargeDebitEnabled: true, LargeDebitMinor: DefaultLargeDebitMinor, LowBalanceEnabled: true, LowBalanceMinor: DefaultLowBalanceMinor,
		RenewalsEnabled: true, LeadDays: DefaultLeadDays}
}

// SettingsStore reads the settings.
type SettingsStore interface {
	GetFinanceSettings(ctx context.Context) (store.FinanceSetting, error)
}

// LoadSettings reads the settings; the defaults when none are stored.
func LoadSettings(ctx context.Context, q SettingsStore) (Settings, error) {
	row, err := q.GetFinanceSettings(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return DefaultSettings(), nil
	}
	if err != nil {
		return Settings{}, err
	}
	return FromRow(row), nil
}

// FromRow converts stored settings.
func FromRow(row store.FinanceSetting) Settings {
	return Settings{LargeDebitEnabled: row.LargeDebitEnabled, LargeDebitMinor: row.LargeDebitMinor, LowBalanceEnabled: row.LowBalanceEnabled,
		LowBalanceMinor: row.LowBalanceMinor, RenewalsEnabled: row.RenewalsEnabled, LeadDays: int(row.RenewalLeadDays), UpdatedAt: row.UpdatedAt.Time.UTC()}
}

// Today is the owner's current day as a UTC midnight.
func Today(now time.Time, owner *time.Location) time.Time {
	local := now.In(owner)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
}

// Detect runs subscription detection as of the owner's today, in one
// database transaction.
func Detect(ctx context.Context, pool *pgxpool.Pool, today time.Time) (subscriptions.SyncResult, error) {
	var result subscriptions.SyncResult
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		var err error
		result, err = subscriptions.Sync(ctx, store.New(tx), today, financemoney.Exponent)
		return err
	})
	return result, err
}

// RunOnce runs detection when it is due, then checks low balances, large
// debits, and renewals.
func (w *Watcher) RunOnce(ctx context.Context) (Result, error) {
	var result Result
	now := w.now()
	today := Today(now, w.owner)
	var errs []error
	if w.dirty.Swap(false) || !w.detectedOn.Equal(today) {
		detection, err := Detect(ctx, w.pool, today)
		if err != nil {
			w.dirty.Store(true)
			errs = append(errs, fmt.Errorf("detect subscriptions: %w", err))
		} else {
			result.Detection, w.detectedOn = detection, today
		}
	}
	q := store.New(w.pool)
	settings, err := LoadSettings(ctx, q)
	if err != nil {
		return result, errors.Join(append(errs, fmt.Errorf("load the alert settings: %w", err))...)
	}
	errs = append(errs, w.lowBalances(ctx, q, &result, settings, now))
	if w.sending() && settings.LargeDebitEnabled {
		errs = append(errs, w.largeDebits(ctx, q, &result, settings, today, now))
	}
	if w.sending() && settings.RenewalsEnabled {
		errs = append(errs, w.renewals(ctx, q, &result, settings, today, now))
	}
	return result, errors.Join(errs...)
}

// LargeDebitKey, LowBalanceKey, and RenewalKey are the dedupe keys.
func LargeDebitKey(transactionID [16]byte) string {
	return LargeDebitCategory + ":" + uuidText(transactionID)
}

func LowBalanceKey(accountID [16]byte, episode int32) string {
	return fmt.Sprintf("%s:%s:%d", LowBalanceCategory, uuidText(accountID), episode)
}

func RenewalKey(subscriptionID [16]byte, expected time.Time) string {
	return RenewalCategory + ":" + uuidText(subscriptionID) + ":" + expected.Format(time.DateOnly)
}

// lowBalances keeps each bank account's low-balance state (also while the
// alert is off) and notifies the episode it is in while its balance,
// pending money included, is below the threshold: an account that falls
// below starts a new episode, and one back at or above the threshold is
// re-armed. A sync that books a pending payment moves it from pending to
// booked in one database transaction, so the balance stays and so does the
// episode. Cash and other manual accounts are never watched.
func (w *Watcher) lowBalances(ctx context.Context, q *store.Queries, result *Result, settings Settings, now time.Time) error {
	rows, err := q.ListLowBalanceAccounts(ctx, w.currency)
	if err != nil {
		return fmt.Errorf("list balances: %w", err)
	}
	var errs []error
	for _, row := range rows {
		below := row.BookedMinor+row.PendingMinor < settings.LowBalanceMinor
		wasBelow := row.Below != nil && *row.Below
		episode := int32(0)
		if row.Episode != nil {
			episode = *row.Episode
		}
		switch {
		case below && !wasBelow:
			episode++
			if err := q.SetLowBalanceState(ctx, store.SetLowBalanceStateParams{AccountID: row.ID, Below: true, Episode: episode}); err != nil {
				errs = append(errs, err)
				continue
			}
		case !below && wasBelow:
			if err := q.SetLowBalanceState(ctx, store.SetLowBalanceStateParams{AccountID: row.ID, Below: false, Episode: episode}); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		if below && settings.LowBalanceEnabled && w.sending() {
			errs = append(errs, w.send(ctx, q, result, lowBalanceMessage(row, episode, settings.LowBalanceMinor, w.currency, now)))
		}
	}
	return errors.Join(errs...)
}

func (w *Watcher) largeDebits(ctx context.Context, q *store.Queries, result *Result, settings Settings, today, now time.Time) error {
	rows, err := q.ListLargeDebits(ctx, store.ListLargeDebitsParams{Currency: w.currency, ThresholdMinor: settings.LargeDebitMinor,
		Since: subscriptions.Date(today.AddDate(0, 0, -LargeDebitDays))})
	if err != nil {
		return fmt.Errorf("list large debits: %w", err)
	}
	var errs []error
	for _, row := range rows {
		errs = append(errs, w.send(ctx, q, result, largeDebitMessage(row, settings.LargeDebitMinor, now)))
	}
	return errors.Join(errs...)
}

func (w *Watcher) renewals(ctx context.Context, q *store.Queries, result *Result, settings Settings, today, now time.Time) error {
	rows, err := q.ListDueRenewals(ctx, store.ListDueRenewalsParams{FromDate: subscriptions.Date(today), ToDate: subscriptions.Date(today.AddDate(0, 0, settings.LeadDays))})
	if err != nil {
		return fmt.Errorf("list renewals: %w", err)
	}
	var errs []error
	for _, row := range rows {
		errs = append(errs, w.send(ctx, q, result, renewalMessage(row, today, now)))
	}
	return errors.Join(errs...)
}

// send claims the notification and sends it; a failure that may pass
// gives the claim back.
func (w *Watcher) send(ctx context.Context, q *store.Queries, result *Result, message notify.Notification) error {
	claimed, err := q.ClaimAlertNotification(ctx, message.DedupeKey)
	if err != nil {
		return fmt.Errorf("claim %s: %w", message.DedupeKey, err)
	}
	if claimed == 0 {
		result.Handled++
		return nil
	}
	err = w.notifier.Send(ctx, message)
	switch {
	case err == nil:
		result.Sent++
		w.logger.Info("finance alert sent", "key", message.DedupeKey)
		return nil
	case permanent(err):
		result.Refused++
		w.logger.Warn("finance alert refused by notifications", "key", message.DedupeKey, "error", err)
		return nil
	}
	result.Retry++
	if releaseErr := q.ReleaseAlertNotification(context.WithoutCancel(ctx), message.DedupeKey); releaseErr != nil {
		return errors.Join(err, fmt.Errorf("release %s: %w", message.DedupeKey, releaseErr))
	}
	return err
}

// permanent reports whether Notifications refused the notification (4xx)
// or it was invalid; anything else may succeed later.
func permanent(err error) bool {
	var status *notify.StatusError
	return errors.Is(err, notify.ErrInvalid) || (errors.As(err, &status) && !status.Temporary())
}

func dateText(date pgtype.Date) string { return date.Time.Format("Mon, 2 Jan 2006") }
