package alerts

import (
	"fmt"
	"time"

	googleuuid "github.com/google/uuid"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/notify"
	financemoney "github.com/GAMch1k/life-os/services/finance/internal/money"
	store "github.com/GAMch1k/life-os/services/finance/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/finance/internal/subscriptions"
)

func uuidText(id [16]byte) string { return googleuuid.UUID(id).String() }

// lowBalanceMessage: "Low balance: Girokonto at €84.20", the balance with
// pending money; the body splits it when some is pending.
func lowBalanceMessage(row store.ListLowBalanceAccountsRow, episode int32, threshold int64, currency string, now time.Time) notify.Notification {
	format := func(amount int64) string { return financemoney.Format(amount, currency) }
	body := fmt.Sprintf("Below your %s alert.", format(threshold))
	if row.PendingMinor != 0 {
		body += fmt.Sprintf(" Booked %s, pending %s.", format(row.BookedMinor), format(row.PendingMinor))
	}
	return notify.Notification{Title: fmt.Sprintf("Low balance: %s at %s", activity.Preview(row.Name, 100), format(row.BookedMinor+row.PendingMinor)), Body: body,
		Importance: notify.Medium, Category: LowBalanceCategory, Link: FinanceLink, DedupeKey: LowBalanceKey(row.ID.Bytes, episode), OccurredAt: now}
}

// largeDebitMessage: "Large debit: €480.00 at MediaMarkt".
func largeDebitMessage(row store.ListLargeDebitsRow, threshold int64, now time.Time) notify.Notification {
	format := func(amount int64) string { return financemoney.Format(amount, row.Currency) }
	title := "Large debit: " + format(-row.AmountMinor)
	switch {
	case row.Counterparty != nil:
		title += " at " + activity.Preview(*row.Counterparty, 120)
	case row.Description != nil:
		title += " for “" + activity.Preview(*row.Description, 120) + "”"
	}
	body := activity.Preview(row.AccountName, 100) + ", " + dateText(row.OccurredOn)
	if row.Status == "pending" {
		body += ", pending"
	}
	body += fmt.Sprintf(". Your large-debit alert is %s.", format(threshold))
	return notify.Notification{Title: title, Body: body, Importance: notify.Medium, Category: LargeDebitCategory, Link: FinanceLink,
		DedupeKey: LargeDebitKey(row.ID.Bytes), OccurredAt: now}
}

// renewalMessage: "Netflix renews in 3 days: €13.99".
func renewalMessage(row store.ListDueRenewalsRow, today, now time.Time) notify.Notification {
	format := func(amount int64) string { return financemoney.Format(amount, row.Currency) }
	when := "today"
	switch days := int(row.NextDate.Time.Sub(today).Hours() / 24); {
	case days == 1:
		when = "tomorrow"
	case days > 1:
		when = fmt.Sprintf("in %d days", days)
	}
	body := "Expected on " + dateText(row.NextDate)
	if row.AccountName != nil {
		body += " from " + activity.Preview(*row.AccountName, 100)
	}
	body += ". Renews " + row.Cadence
	if row.Cadence != subscriptions.Monthly {
		body += ", " + format(subscriptions.MonthlyEquivalent(row.AmountMinor, row.Cadence)) + " a month"
	}
	body += "."
	return notify.Notification{Title: fmt.Sprintf("%s renews %s: %s", activity.Preview(row.Name, 100), when, format(row.AmountMinor)), Body: body,
		Importance: notify.Low, Category: RenewalCategory, Link: SubscriptionsLink, DedupeKey: RenewalKey(row.ID.Bytes, row.NextDate.Time), OccurredAt: now}
}
