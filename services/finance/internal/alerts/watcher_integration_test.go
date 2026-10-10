package alerts

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/GAMch1k/life-os/pkg/notify"
	store "github.com/GAMch1k/life-os/services/finance/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/finance/internal/subscriptions"
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

type world struct {
	t        *testing.T
	q        *store.Queries
	giro     store.Account
	notifier *fakeNotifier
	watcher  *Watcher
	now      time.Time
}

func newWorld(t *testing.T) *world {
	t.Helper()
	pool := testdb.Open(t, "")
	q := store.New(pool)
	giro, err := q.CreateAccount(t.Context(), store.CreateAccountParams{Name: "Giro", Currency: "EUR", Kind: "bank", OpeningBalanceMinor: 500000})
	if err != nil {
		t.Fatal(err)
	}
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	w := &world{t: t, q: q, giro: giro, notifier: &fakeNotifier{enabled: true}}
	w.watcher = New(pool, w.notifier, Config{Owner: berlin, Currency: "EUR"}, slog.New(slog.NewTextHandler(io.Discard, nil))).
		WithClock(func() time.Time { return w.now })
	return w
}

func date(text string) pgtype.Date {
	parsed, err := time.Parse(time.DateOnly, text)
	if err != nil {
		panic(err)
	}
	return subscriptions.Date(parsed)
}

func ptr(text string) *string { return &text }

func (w *world) pay(account store.Account, amount int64, day, status, counterparty, description string) store.Transaction {
	w.t.Helper()
	params := store.CreateTransactionParams{AccountID: account.ID, AmountMinor: amount, Currency: account.Currency, OccurredOn: date(day),
		Status: status, SourceMetadata: []byte(`{}`)}
	if counterparty != "" {
		params.Counterparty = ptr(counterparty)
	}
	if description != "" {
		params.Description = ptr(description)
	}
	item, err := w.q.CreateTransaction(w.t.Context(), params)
	if err != nil {
		w.t.Fatal(err)
	}
	return item
}

// run checks at an owner-local time ("2026-10-10 09:00").
func (w *world) run(at string) (Result, []notify.Notification) {
	w.t.Helper()
	parsed, err := time.ParseInLocation("2006-01-02 15:04", at, w.watcher.owner)
	if err != nil {
		w.t.Fatal(err)
	}
	w.now = parsed
	result, runErr := w.watcher.RunOnce(w.t.Context())
	if runErr != nil && w.notifier.fail == nil {
		w.t.Fatalf("run at %s: %v", at, runErr)
	}
	return result, w.notifier.take()
}

func (w *world) subscriptions(state string) []store.Subscription {
	w.t.Helper()
	rows, err := w.q.ListSubscriptions(w.t.Context(), &state)
	if err != nil {
		w.t.Fatal(err)
	}
	return rows
}

func (w *world) setState(sub store.Subscription, state string) {
	w.t.Helper()
	sub.State = state
	if _, err := w.q.UpdateSubscription(w.t.Context(), subscriptions.UpdateParams(sub)); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) settings(change func(*store.SaveFinanceSettingsParams)) {
	w.t.Helper()
	params := store.SaveFinanceSettingsParams{LargeDebitEnabled: true, LargeDebitMinor: DefaultLargeDebitMinor, LowBalanceEnabled: true,
		LowBalanceMinor: DefaultLowBalanceMinor, RenewalsEnabled: true, RenewalLeadDays: DefaultLeadDays}
	change(&params)
	if _, err := w.q.SaveFinanceSettings(w.t.Context(), params); err != nil {
		w.t.Fatal(err)
	}
}

func names(rows []store.Subscription) string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Name+" "+row.Cadence+" "+row.LastDate.Time.Format(time.DateOnly)+"→"+row.NextDate.Time.Format(time.DateOnly))
	}
	return strings.Join(out, " | ")
}

func titles(sent []notify.Notification) string {
	out := make([]string, 0, len(sent))
	for _, n := range sent {
		out = append(out, n.Title)
	}
	return strings.Join(out, " | ")
}

func TestDetectionSuggestsTracksAndRespectsIgnored(t *testing.T) {
	w := newWorld(t)
	w.notifier.enabled = false
	for _, day := range []string{"2026-07-15", "2026-08-14", "2026-09-15"} {
		w.pay(w.giro, -1399, day, "booked", "NETFLIX.COM", "")
	}
	for _, day := range []string{"2026-07-03", "2026-08-03", "2026-09-03"} {
		w.pay(w.giro, -899, day, "booked", "PayPal Europe S.a.r.l. et Cie S.C.A", "PP.5323.PP . Patreon, Ihr Einkauf bei Patreon")
	}
	for _, day := range []string{"2026-07-20", "2026-08-20", "2026-09-21"} {
		w.pay(w.giro, -4900, day, "booked", "FitX Deutschland GmbH", "")
	}
	// Pending and money in never count.
	w.pay(w.giro, -1000, "2026-09-25", "pending", "Pending Abo", "")
	w.pay(w.giro, 250000, "2026-09-30", "booked", "ACME GmbH", "Gehalt")

	result, _ := w.run("2026-10-10 09:00")
	if result.Detection != (subscriptions.SyncResult{Suggested: 3}) {
		t.Fatalf("first run = %+v", result.Detection)
	}
	suggested := w.subscriptions(subscriptions.StateSuggested)
	if got := names(suggested); got != "FitX Deutschland GmbH monthly 2026-09-21→2026-10-21 | Netflix monthly 2026-09-15→2026-10-15 | "+
		"Patreon via PayPal monthly 2026-09-03→2026-10-03" {
		t.Fatalf("suggested = %s", got)
	}
	fitx, netflix, patreon := suggested[0], suggested[1], suggested[2]
	links, err := w.q.ListSubscriptionLinks(t.Context(), netflix.ID)
	if err != nil || len(links) != 3 || netflix.AmountMinor != 1399 || netflix.Confidence != "high" || netflix.Source != "detected" {
		t.Fatalf("netflix = %+v, %d links, %v", netflix, len(links), err)
	}
	// Nothing new: nothing changes, also on the same day without a Wake.
	if result, _ := w.run("2026-10-10 09:05"); result.Detection.Changed() {
		t.Fatalf("second run = %+v", result.Detection)
	}

	// The owner confirms Netflix and ignores FitX.
	w.setState(netflix, subscriptions.StateConfirmed)
	w.setState(fitx, subscriptions.StateIgnored)
	// A new Netflix payment (a little dearer) moves the confirmed one on;
	// FitX keeps paying but stays ignored.
	w.pay(w.giro, -1499, "2026-10-14", "booked", "Netflix International B.V.", "")
	w.pay(w.giro, -4900, "2026-10-20", "booked", "FitX Deutschland GmbH", "")
	w.watcher.Wake()
	if result, _ := w.run("2026-10-21 09:00"); result.Detection != (subscriptions.SyncResult{Updated: 1, Removed: 1}) {
		// Patreon stopped: its suggestion goes once its payment is more
		// than a week overdue.
		t.Fatalf("after new payments = %+v", result.Detection)
	}
	confirmed := w.subscriptions(subscriptions.StateConfirmed)
	if got := names(confirmed); got != "Netflix monthly 2026-10-14→2026-11-14" || confirmed[0].AmountMinor != 1399 {
		t.Fatalf("confirmed = %s, %d", got, confirmed[0].AmountMinor)
	}
	if ignored := w.subscriptions(subscriptions.StateIgnored); len(ignored) != 1 || ignored[0].LastDate != fitx.LastDate {
		t.Fatalf("ignored = %s", names(ignored))
	}
	if left := w.subscriptions(subscriptions.StateSuggested); len(left) != 0 {
		t.Fatalf("suggested = %s (patreon %s should be gone)", names(left), patreon.Name)
	}
	// The median of the last three follows a price rise.
	w.pay(w.giro, -1499, "2026-11-14", "booked", "NETFLIX.COM", "")
	w.watcher.Wake()
	w.run("2026-11-15 09:00")
	if confirmed := w.subscriptions(subscriptions.StateConfirmed); confirmed[0].AmountMinor != 1499 || names(confirmed) != "Netflix monthly 2026-11-14→2026-12-14" {
		t.Fatalf("confirmed = %s, %d", names(confirmed), confirmed[0].AmountMinor)
	}
}

func TestRenewalRemindersComeLeadDaysAheadOnce(t *testing.T) {
	w := newWorld(t)
	for _, day := range []string{"2026-07-13", "2026-08-13", "2026-09-13"} {
		w.pay(w.giro, -1399, day, "booked", "Spotify AB", "")
	}
	for _, day := range []string{"2025-10-01", "2026-09-30"} {
		w.pay(w.giro, -9400, day, "booked", "ADAC e.V.", "Mitgliedsbeitrag")
	}
	w.run("2026-10-01 09:00")
	suggested := w.subscriptions(subscriptions.StateSuggested)
	if names(suggested) != "ADAC e.V. yearly 2026-09-30→2027-09-30 | Spotify monthly 2026-09-13→2026-10-13" || suggested[0].Confidence != "low" {
		t.Fatalf("suggested = %s", names(suggested))
	}
	// Suggestions get no reminders.
	if _, sent := w.run("2026-10-10 09:00"); len(sent) != 0 {
		t.Fatalf("suggested reminders: %s", titles(sent))
	}
	w.setState(suggested[1], subscriptions.StateConfirmed)
	if _, sent := w.run("2026-10-09 09:00"); len(sent) != 0 {
		t.Fatalf("four days ahead: %s", titles(sent))
	}
	result, sent := w.run("2026-10-10 09:00")
	if result.Sent != 1 || len(sent) != 1 {
		t.Fatalf("reminder: %+v %s", result, titles(sent))
	}
	reminder := sent[0]
	if reminder.Title != "Spotify renews in 3 days: €13.99" || reminder.Body != "Expected on Tue, 13 Oct 2026 from Giro. Renews monthly." ||
		reminder.Importance != notify.Low || reminder.Category != "finance.subscription" || reminder.Link != "/finance?tab=subscriptions" ||
		reminder.DedupeKey != "finance.subscription:"+uuidText(suggested[1].ID.Bytes)+":2026-10-13" {
		t.Fatalf("reminder = %+v", reminder)
	}
	if result, sent := w.run("2026-10-12 09:00"); len(sent) != 0 || result.Handled != 1 {
		t.Fatalf("repeated: %+v %s", result, titles(sent))
	}
	// Late or missed payments are shown, not alerted.
	if _, sent := w.run("2026-10-25 09:00"); len(sent) != 0 {
		t.Fatalf("missed: %s", titles(sent))
	}
	// The next payment moves the date, and the next reminder (on the day
	// with lead days 0) has its own key; a yearly one names its monthly
	// cost.
	w.pay(w.giro, -1399, "2026-10-26", "booked", "Spotify AB", "")
	w.watcher.Wake()
	w.settings(func(p *store.SaveFinanceSettingsParams) { p.RenewalLeadDays = 0 })
	if _, sent := w.run("2026-11-25 09:00"); len(sent) != 0 {
		t.Fatalf("a day early with lead days 0: %s", titles(sent))
	}
	if _, sent := w.run("2026-11-26 08:00"); titles(sent) != "Spotify renews today: €13.99" {
		t.Fatalf("on the day: %s", titles(sent))
	}
	w.setState(w.subscriptions(subscriptions.StateSuggested)[0], subscriptions.StateConfirmed)
	w.settings(func(p *store.SaveFinanceSettingsParams) { p.RenewalLeadDays = 1 })
	if _, sent := w.run("2027-09-29 09:00"); len(sent) != 1 || sent[0].Title != "ADAC e.V. renews tomorrow: €94.00" ||
		sent[0].Body != "Expected on Thu, 30 Sep 2027 from Giro. Renews yearly, €7.83 a month." {
		t.Fatalf("yearly: %+v", sent)
	}
	// Reminders switched off send nothing.
	w.settings(func(p *store.SaveFinanceSettingsParams) { p.RenewalsEnabled = false; p.RenewalLeadDays = 30 })
	if _, sent := w.run("2026-12-20 09:00"); len(sent) != 0 {
		t.Fatalf("switched off: %s", titles(sent))
	}
}

func TestLargeDebitsNotifyOnceForRecentMoneyOut(t *testing.T) {
	w := newWorld(t)
	w.pay(w.giro, -30000, "2026-10-01", "booked", "Old Laptop Shop", "")     // more than 3 days ago
	w.pay(w.giro, -25000, "2026-10-07", "booked", "MediaMarkt", "")          // exactly the threshold, 3 days ago
	w.pay(w.giro, -48000, "2026-10-10", "pending", "", "Werkstatt Rechnung") // pending counts
	w.pay(w.giro, -24999, "2026-10-10", "booked", "Just Below", "")
	w.pay(w.giro, 90000, "2026-10-10", "booked", "Refund", "")
	usd, err := w.q.CreateAccount(t.Context(), store.CreateAccountParams{Name: "Dollar", Currency: "USD", Kind: "bank", OpeningBalanceMinor: 900000})
	if err != nil {
		t.Fatal(err)
	}
	w.pay(usd, -90000, "2026-10-10", "booked", "Other Currency", "")
	result, sent := w.run("2026-10-10 12:00")
	if result.Sent != 2 || titles(sent) != "Large debit: €250.00 at MediaMarkt | Large debit: €480.00 for “Werkstatt Rechnung”" {
		t.Fatalf("large debits: %+v %s", result, titles(sent))
	}
	if sent[1].Body != "Giro, Sat, 10 Oct 2026, pending. Your large-debit alert is €250.00." || sent[1].Importance != notify.Medium ||
		sent[1].Category != "finance.large_debit" || sent[1].Link != "/finance" || !strings.HasPrefix(sent[1].DedupeKey, "finance.large_debit:") {
		t.Fatalf("pending debit = %+v", sent[1])
	}
	if _, sent := w.run("2026-10-10 12:05"); len(sent) != 0 {
		t.Fatalf("repeated: %s", titles(sent))
	}
	// A higher threshold, or the alert off, sends nothing new.
	w.settings(func(p *store.SaveFinanceSettingsParams) { p.LargeDebitMinor = 100000 })
	w.pay(w.giro, -60000, "2026-10-11", "booked", "Below The New Threshold", "")
	if _, sent := w.run("2026-10-11 09:00"); len(sent) != 0 {
		t.Fatalf("higher threshold: %s", titles(sent))
	}
	w.settings(func(p *store.SaveFinanceSettingsParams) { p.LargeDebitEnabled = false; p.LargeDebitMinor = 100 })
	if _, sent := w.run("2026-10-11 09:05"); len(sent) != 0 {
		t.Fatalf("switched off: %s", titles(sent))
	}
	// A send that fails for now is retried; one refused is not.
	w.settings(func(p *store.SaveFinanceSettingsParams) {})
	w.pay(w.giro, -26000, "2026-10-11", "booked", "Retry Shop", "")
	w.notifier.fail = &notify.StatusError{Status: 503}
	if result, _ := w.run("2026-10-11 10:00"); result.Retry != 2 || result.Sent != 0 {
		t.Fatalf("failing: %+v", result)
	}
	w.notifier.fail = nil
	if _, sent := w.run("2026-10-11 10:05"); titles(sent) != "Large debit: €600.00 at Below The New Threshold | Large debit: €260.00 at Retry Shop" {
		t.Fatalf("retried: %s", titles(sent))
	}
}

func TestLowBalanceNotifiesEachEpisodeOnce(t *testing.T) {
	w := newWorld(t)
	q := w.q
	cash, err := q.CreateAccount(t.Context(), store.CreateAccountParams{Name: "Wallet", Currency: "EUR", Kind: "cash", OpeningBalanceMinor: 12000})
	if err != nil {
		t.Fatal(err)
	}
	w.pay(cash, 1, "2026-10-09", "booked", "Found a cent", "")
	// Cards, savings, archived accounts, other currencies, and an unused
	// account (no transactions, no bank balance) are not checked.
	for _, params := range []store.CreateAccountParams{
		{Name: "Card", Currency: "EUR", Kind: "card"}, {Name: "Tagesgeld", Currency: "EUR", Kind: "savings"}, {Name: "Spare", Currency: "EUR", Kind: "cash"},
	} {
		account, err := q.CreateAccount(t.Context(), params)
		if err != nil {
			t.Fatal(err)
		}
		if params.Name != "Spare" {
			w.pay(account, -100, "2026-10-09", "booked", "Something", "")
		}
	}
	usd, err := q.CreateAccount(t.Context(), store.CreateAccountParams{Name: "USD", Currency: "USD", Kind: "bank"})
	if err != nil {
		t.Fatal(err)
	}
	w.pay(usd, -100, "2026-10-09", "booked", "Something", "")
	old, err := q.CreateAccount(t.Context(), store.CreateAccountParams{Name: "Old", Currency: "EUR", Kind: "bank"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.UpdateAccount(t.Context(), store.UpdateAccountParams{ID: old.ID, Archived: pointerTo(true)}); err != nil {
		t.Fatal(err)
	}
	if _, sent := w.run("2026-10-10 09:00"); len(sent) != 0 {
		t.Fatalf("all fine: %s", titles(sent))
	}
	w.pay(cash, -4000, "2026-10-10", "booked", "Bäckerei", "")
	result, sent := w.run("2026-10-10 10:00")
	if result.Sent != 1 || len(sent) != 1 || sent[0].Title != "Low balance: Wallet at €80.01" || sent[0].Body != "Below your €100.00 alert." ||
		sent[0].Importance != notify.Medium || sent[0].Category != "finance.low_balance" || sent[0].Link != "/finance" ||
		sent[0].DedupeKey != "finance.low_balance:"+uuidText(cash.ID.Bytes)+":1" {
		t.Fatalf("low: %+v %+v", result, sent)
	}
	w.pay(cash, -1000, "2026-10-10", "booked", "Kiosk", "")
	if result, sent := w.run("2026-10-10 11:00"); len(sent) != 0 || result.Handled != 1 {
		t.Fatalf("still low: %+v %s", result, titles(sent))
	}
	// Back above re-arms it; the next fall is a new episode.
	w.pay(cash, 5000, "2026-10-11", "booked", "ATM", "")
	if _, sent := w.run("2026-10-11 09:00"); len(sent) != 0 {
		t.Fatalf("back above: %s", titles(sent))
	}
	w.pay(cash, -3000, "2026-10-12", "booked", "Markt", "")
	if _, sent := w.run("2026-10-12 09:00"); len(sent) != 1 || !strings.HasSuffix(sent[0].DedupeKey, ":2") || sent[0].Title != "Low balance: Wallet at €90.01" {
		t.Fatalf("second episode: %+v", sent)
	}
	// A bank account shows the bank's balance; a fall while the alert is
	// off is notified once it is switched on.
	w.settings(func(p *store.SaveFinanceSettingsParams) { p.LowBalanceEnabled = false })
	provider, institution := "enablebanking", "Deutsche Bank"
	if _, err := q.CreateBankAccount(t.Context(), store.CreateBankAccountParams{Name: "Girokonto", Kind: "bank", Currency: "EUR",
		ConnectionID: pgtype.UUID{Bytes: [16]byte{9}, Valid: true}, ExternalID: "acc-1", Provider: &provider, Institution: &institution,
		BookedBalanceMinor: pointerTo(int64(5000)), BookedBalanceDate: date("2026-10-12")}); err != nil {
		t.Fatal(err)
	}
	if _, sent := w.run("2026-10-12 10:00"); len(sent) != 0 {
		t.Fatalf("switched off: %s", titles(sent))
	}
	w.settings(func(p *store.SaveFinanceSettingsParams) {})
	if _, sent := w.run("2026-10-12 10:05"); len(sent) != 1 || sent[0].Title != "Low balance: Girokonto at €50.00" ||
		sent[0].Body != "Below your €100.00 alert. Balance reported by the bank on Mon, 12 Oct 2026." {
		t.Fatalf("bank balance: %+v", sent)
	}
}

func pointerTo[T any](value T) *T { return &value }
