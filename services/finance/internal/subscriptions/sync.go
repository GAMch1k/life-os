package subscriptions

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	store "github.com/GAMch1k/life-os/services/finance/internal/repository/generated"
)

// SyncStore is what Sync needs from the repository.
type SyncStore interface {
	LockSubscriptions(ctx context.Context) error
	ListRecurringCandidates(ctx context.Context, since pgtype.Date) ([]store.ListRecurringCandidatesRow, error)
	ListSubscriptions(ctx context.Context, state *string) ([]store.Subscription, error)
	ListSubscriptionLinks(ctx context.Context, subscriptionID pgtype.UUID) ([]store.SubscriptionTransaction, error)
	InsertSubscription(ctx context.Context, arg store.InsertSubscriptionParams) (store.Subscription, error)
	UpdateSubscription(ctx context.Context, arg store.UpdateSubscriptionParams) (store.Subscription, error)
	DeleteSubscription(ctx context.Context, id pgtype.UUID) (store.Subscription, error)
	ReplaceSubscriptionLinks(ctx context.Context, arg store.ReplaceSubscriptionLinksParams) error
}

// SyncResult counts what a run changed: new suggestions, subscriptions
// whose dates, amount, or payments changed, and suggestions removed
// because their payments are no longer regular.
type SyncResult struct {
	Suggested, Updated, Removed int
}

// Changed reports whether the run changed anything.
func (r SyncResult) Changed() bool { return r.Suggested+r.Updated+r.Removed > 0 }

// Date is a day as a database date.
func Date(day time.Time) pgtype.Date {
	return pgtype.Date{Time: time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC), Valid: true}
}

// Payments converts the stored candidates.
func Payments(rows []store.ListRecurringCandidatesRow) []Payment {
	payments := make([]Payment, 0, len(rows))
	for _, row := range rows {
		payment := Payment{ID: row.ID.Bytes, AccountID: row.AccountID.Bytes, Amount: -row.AmountMinor, Currency: row.Currency, Date: row.OccurredOn.Time}
		if row.Counterparty != nil {
			payment.Counterparty = *row.Counterparty
		}
		if row.Description != nil {
			payment.Description = *row.Description
		}
		if row.CategoryID.Valid {
			id := row.CategoryID.Bytes
			payment.CategoryID = &id
		}
		payments = append(payments, payment)
	}
	return payments
}

// Sync runs detection on the booked money out of the last LookbackDays as
// of today and stores the outcome (run it in a transaction):
//
//   - a candidate without a subscription of its merchant and cadence
//     becomes a suggestion;
//   - a suggestion takes the candidate's latest dates, payments, amount,
//     category, and name (but what the owner set stays);
//   - a confirmed subscription follows its merchant's payments of a
//     similar amount: the latest one gives its last and next date, and the
//     median of the last three its amount, unless the owner set one;
//   - an ignored one is never touched, so its merchant and cadence are not
//     suggested again;
//   - a suggestion no candidate supports any more is removed.
func Sync(ctx context.Context, q SyncStore, today time.Time, exponent Exponent) (SyncResult, error) {
	var result SyncResult
	if err := q.LockSubscriptions(ctx); err != nil {
		return result, err
	}
	rows, err := q.ListRecurringCandidates(ctx, Date(today.AddDate(0, 0, -LookbackDays)))
	if err != nil {
		return result, err
	}
	payments := withMerchants(Payments(rows))
	existing, err := q.ListSubscriptions(ctx, nil)
	if err != nil {
		return result, err
	}
	linkRows, err := q.ListSubscriptionLinks(ctx, pgtype.UUID{})
	if err != nil {
		return result, err
	}
	links := map[[16]byte][][16]byte{}
	for _, row := range linkRows {
		links[row.SubscriptionID.Bytes] = append(links[row.SubscriptionID.Bytes], row.TransactionID.Bytes)
	}
	byIdentity := map[string]store.Subscription{}
	for _, sub := range existing {
		byIdentity[identity(sub.MerchantKey, sub.MatchCadence)] = sub
	}

	for _, sub := range existing {
		if sub.State != StateConfirmed {
			continue
		}
		matched := Track(sub.MerchantKey, sub.Currency, sub.AmountMinor, payments, exponent)
		if len(matched) == 0 {
			continue
		}
		next := sub
		latest := matched[0]
		next.LastDate, next.NextDate = Date(latest.Date), Date(Next(latest.Date, sub.Cadence))
		next.AccountID = pgtype.UUID{Bytes: latest.AccountID, Valid: true}
		if !sub.AmountEdited {
			next.AmountMinor = Typical(matched)
		}
		changed, err := save(ctx, q, sub, next, links[sub.ID.Bytes], ids(matched))
		if err != nil {
			return result, err
		}
		if changed {
			result.Updated++
		}
	}

	supported := map[[16]byte]bool{}
	for _, candidate := range Detect(payments, today, exponent) {
		sub, ok := byIdentity[identity(candidate.Merchant.Key, candidate.Cadence)]
		if !ok {
			created, err := q.InsertSubscription(ctx, store.InsertSubscriptionParams{MerchantKey: candidate.Merchant.Key, MatchCadence: candidate.Cadence,
				State: StateSuggested, Source: SourceDetected, Name: candidate.Merchant.Name, Cadence: candidate.Cadence, AmountMinor: candidate.Amount,
				Currency: candidate.Currency, CategoryID: optionalID(candidate.CategoryID), AccountID: pgtype.UUID{Bytes: candidate.AccountID, Valid: true},
				Confidence: candidate.Confidence, LastDate: Date(candidate.Last), NextDate: Date(candidate.Next)})
			if err != nil {
				return result, fmt.Errorf("suggest %s: %w", candidate.Merchant.Key, err)
			}
			if err := q.ReplaceSubscriptionLinks(ctx, store.ReplaceSubscriptionLinksParams{SubscriptionID: created.ID, TransactionIds: uuids(ids(candidate.Payments))}); err != nil {
				return result, err
			}
			result.Suggested++
			continue
		}
		if sub.State != StateSuggested {
			continue
		}
		supported[sub.ID.Bytes] = true
		next := sub
		if !sub.NameEdited {
			next.Name = candidate.Merchant.Name
		}
		if !sub.AmountEdited {
			next.AmountMinor = candidate.Amount
		}
		if !sub.CategoryEdited {
			next.CategoryID = optionalID(candidate.CategoryID)
		}
		next.Currency = candidate.Currency
		next.AccountID = pgtype.UUID{Bytes: candidate.AccountID, Valid: true}
		next.Confidence = candidate.Confidence
		next.LastDate, next.NextDate = Date(candidate.Last), Date(Next(candidate.Last, sub.Cadence))
		changed, err := save(ctx, q, sub, next, links[sub.ID.Bytes], ids(candidate.Payments))
		if err != nil {
			return result, err
		}
		if changed {
			result.Updated++
		}
	}
	for _, sub := range existing {
		if sub.State == StateSuggested && !supported[sub.ID.Bytes] {
			if _, err := q.DeleteSubscription(ctx, sub.ID); err != nil {
				return result, err
			}
			result.Removed++
		}
	}
	return result, nil
}

func identity(merchantKey, cadence string) string { return merchantKey + "\x00" + cadence }

// save writes next and its payments when they differ from what is stored.
func save(ctx context.Context, q SyncStore, before, next store.Subscription, storedLinks, matched [][16]byte) (bool, error) {
	fieldsChanged := !SameStored(before, next)
	linksChanged := !sameIDs(storedLinks, matched)
	if fieldsChanged {
		if _, err := q.UpdateSubscription(ctx, UpdateParams(next)); err != nil {
			return false, err
		}
	}
	if linksChanged {
		if err := q.ReplaceSubscriptionLinks(ctx, store.ReplaceSubscriptionLinksParams{SubscriptionID: before.ID, TransactionIds: uuids(matched)}); err != nil {
			return false, err
		}
	}
	return fieldsChanged || linksChanged, nil
}

// SameStored reports whether two rows hold the same subscription fields.
func SameStored(a, b store.Subscription) bool {
	return a.State == b.State && a.Name == b.Name && a.NameEdited == b.NameEdited && a.Cadence == b.Cadence && a.AmountMinor == b.AmountMinor &&
		a.AmountEdited == b.AmountEdited && a.Currency == b.Currency && a.CategoryID == b.CategoryID && a.CategoryEdited == b.CategoryEdited &&
		a.AccountID == b.AccountID && a.Confidence == b.Confidence && sameDate(a.LastDate, b.LastDate) && sameDate(a.NextDate, b.NextDate)
}

// sameIDs reports whether two lists hold the same ids, in any order.
func sameIDs(a, b [][16]byte) bool {
	if len(a) != len(b) {
		return false
	}
	sorted := func(values [][16]byte) [][16]byte {
		out := slices.Clone(values)
		slices.SortFunc(out, func(x, y [16]byte) int { return slices.Compare(x[:], y[:]) })
		return out
	}
	return slices.EqualFunc(sorted(a), sorted(b), func(x, y [16]byte) bool { return x == y })
}

func sameDate(a, b pgtype.Date) bool {
	return a.Valid == b.Valid && (!a.Valid || a.Time.Equal(b.Time))
}

// UpdateParams writes every field of a row.
func UpdateParams(sub store.Subscription) store.UpdateSubscriptionParams {
	return store.UpdateSubscriptionParams{ID: sub.ID, State: sub.State, Name: sub.Name, NameEdited: sub.NameEdited, Cadence: sub.Cadence,
		AmountMinor: sub.AmountMinor, AmountEdited: sub.AmountEdited, Currency: sub.Currency, CategoryID: sub.CategoryID, CategoryEdited: sub.CategoryEdited,
		AccountID: sub.AccountID, Confidence: sub.Confidence, LastDate: sub.LastDate, NextDate: sub.NextDate}
}

func ids(payments []Payment) [][16]byte {
	out := make([][16]byte, 0, len(payments))
	for _, payment := range payments {
		out = append(out, payment.ID)
	}
	return out
}

func uuids(values [][16]byte) []pgtype.UUID {
	out := make([]pgtype.UUID, 0, len(values))
	for _, value := range values {
		out = append(out, pgtype.UUID{Bytes: value, Valid: true})
	}
	return out
}

func optionalID(id *[16]byte) pgtype.UUID {
	if id == nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: *id, Valid: true}
}
