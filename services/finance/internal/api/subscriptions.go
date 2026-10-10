package api

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/oapi-codegen/runtime/types"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/finance/internal/alerts"
	"github.com/GAMch1k/life-os/services/finance/internal/api/generated"
	financemoney "github.com/GAMch1k/life-os/services/finance/internal/money"
	store "github.com/GAMch1k/life-os/services/finance/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/finance/internal/subscriptions"
)

const subscriptionNameLimit = 100

var subscriptionStates = map[string]bool{subscriptions.StateSuggested: true, subscriptions.StateConfirmed: true, subscriptions.StateIgnored: true}

// subscriptionView is a subscription as the API shows it on today (the
// owner's), with the renewal reminders' lead days.
func subscriptionView(row store.Subscription, links []types.UUID, today time.Time, leadDays int) generated.Subscription {
	view := generated.Subscription{
		Id: uuid(row.ID), Name: row.Name, State: generated.SubscriptionState(row.State), Cadence: generated.SubscriptionCadence(row.Cadence),
		Amount: money(row.AmountMinor, row.Currency), MonthlyAmount: money(subscriptions.MonthlyEquivalent(row.AmountMinor, row.Cadence), row.Currency),
		CategoryId: nullableUUID(row.CategoryID), AccountId: nullableUUID(row.AccountID), LastDate: nullableDate(row.LastDate),
		NextDate: nullableDate(row.NextDate), Confidence: generated.SubscriptionConfidence(row.Confidence),
		Source: generated.SubscriptionSource(row.Source), TransactionIds: links, CreatedAt: row.CreatedAt.Time.UTC(), UpdatedAt: row.UpdatedAt.Time.UTC(),
	}
	if view.TransactionIds == nil {
		view.TransactionIds = []types.UUID{}
	}
	if row.State == subscriptions.StateConfirmed && row.NextDate.Valid {
		renewal := generated.SubscriptionRenewal(subscriptions.Renewal(row.NextDate.Time, today, leadDays))
		view.Renewal = &renewal
	}
	return view
}

// subscriptionLinks are the matched transactions per subscription (or of
// one, with id), latest first.
func subscriptionLinks(ctx context.Context, q *store.Queries, id pgtype.UUID) (map[[16]byte][]types.UUID, error) {
	rows, err := q.ListSubscriptionLinks(ctx, id)
	if err != nil {
		return nil, err
	}
	links := map[[16]byte][]types.UUID{}
	for _, row := range rows {
		links[row.SubscriptionID.Bytes] = append(links[row.SubscriptionID.Bytes], uuid(row.TransactionID))
	}
	return links, nil
}

// oneSubscriptionView reads what a single subscription's view needs.
func (h *Handler) oneSubscriptionView(ctx context.Context, q *store.Queries, row store.Subscription) (generated.Subscription, error) {
	links, err := subscriptionLinks(ctx, q, row.ID)
	if err != nil {
		return generated.Subscription{}, err
	}
	settings, err := alerts.LoadSettings(ctx, q)
	if err != nil {
		return generated.Subscription{}, err
	}
	return subscriptionView(row, links[row.ID.Bytes], h.today().Time, settings.LeadDays), nil
}

func (h *Handler) ListSubscriptions(c fiber.Ctx, params generated.ListSubscriptionsParams) error {
	if params.State != nil && !subscriptionStates[string(*params.State)] {
		return invalid("state must be suggested, confirmed, or ignored")
	}
	ctx := c.Context()
	rows, err := h.queries.ListSubscriptions(ctx, nil)
	if err != nil {
		return internal(err)
	}
	links, err := subscriptionLinks(ctx, h.queries, pgtype.UUID{})
	if err != nil {
		return internal(err)
	}
	settings, err := alerts.LoadSettings(ctx, h.queries)
	if err != nil {
		return internal(err)
	}
	today := h.today().Time
	result := generated.SubscriptionList{Items: make([]generated.Subscription, 0, len(rows)),
		Totals: generated.SubscriptionTotals{Currency: h.defaultCurrency, Monthly: money(0, h.defaultCurrency)}}
	for _, row := range rows {
		if row.State == subscriptions.StateConfirmed {
			if row.Currency == h.defaultCurrency {
				result.Totals.Monthly.Amount += subscriptions.MonthlyEquivalent(row.AmountMinor, row.Cadence)
				result.Totals.Confirmed++
			} else {
				result.Totals.Excluded++
			}
		}
		if params.State == nil || string(*params.State) == row.State {
			result.Items = append(result.Items, subscriptionView(row, links[row.ID.Bytes], today, settings.LeadDays))
		}
	}
	return c.JSON(result)
}

func text(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (h *Handler) CreateSubscriptionFromTransaction(c fiber.Ctx) error {
	var body generated.CreateSubscriptionRequest
	properties, err := decodeObject(c.Body(), &body)
	if err != nil {
		return invalid(err.Error())
	}
	if _, ok := properties["transactionId"]; !ok || isNull(properties, "transactionId") {
		return invalid("transactionId is required")
	}
	cadence := subscriptions.Monthly
	if body.Cadence != nil {
		if cadence = string(*body.Cadence); !subscriptions.ValidCadence(cadence) {
			return invalid("cadence must be weekly, monthly, quarterly, or yearly")
		}
	}
	name, err := cleanText("name", body.Name, subscriptionNameLimit)
	if err != nil {
		return err
	}
	ctx := c.Context()
	today := h.today().Time
	status, eventType := fiber.StatusOK, ""
	var saved store.Subscription
	var view generated.Subscription
	err = h.inTx(ctx, func(q *store.Queries) error {
		item, err := q.GetTransaction(ctx, pgUUID(body.TransactionId))
		if errors.Is(err, pgx.ErrNoRows) {
			return apiError(fiber.StatusBadRequest, "unknown_transaction", "transactionId is not a Finance transaction")
		}
		if err != nil {
			return err
		}
		if item.AmountMinor >= 0 {
			return invalid("only money out can be a subscription")
		}
		merchant := subscriptions.MerchantOf(text(item.Counterparty), text(item.Description))
		if merchant.Key == "" {
			return invalid("the transaction needs a counterparty or a description to be a subscription")
		}
		category := item.CategoryID
		if body.CategoryId != nil {
			chosen, err := usableCategory(ctx, q, pgUUID(*body.CategoryId))
			if err != nil {
				return err
			}
			category = chosen.ID
		}
		if err := q.LockSubscriptions(ctx); err != nil {
			return err
		}
		existing, err := q.GetSubscriptionByIdentity(ctx, store.GetSubscriptionByIdentityParams{MerchantKey: merchant.Key, MatchCadence: cadence})
		switch {
		case err == nil:
			// The counterparty already has one with this cadence: it is
			// confirmed and takes the transaction.
			next := existing
			next.State = subscriptions.StateConfirmed
			if name != nil {
				next.Name, next.NameEdited = *name, true
			}
			if body.CategoryId != nil {
				next.CategoryID, next.CategoryEdited = category, true
			}
			if !existing.LastDate.Valid || item.OccurredOn.Time.After(existing.LastDate.Time) {
				next.LastDate, next.NextDate = item.OccurredOn, subscriptions.Date(subscriptions.Next(item.OccurredOn.Time, next.Cadence))
				next.AccountID = item.AccountID
			}
			if !subscriptions.SameStored(existing, next) {
				if next, err = q.UpdateSubscription(ctx, subscriptions.UpdateParams(next)); err != nil {
					return err
				}
			}
			links, err := q.ListSubscriptionLinks(ctx, existing.ID)
			if err != nil {
				return err
			}
			ids := []pgtype.UUID{item.ID}
			for _, link := range links {
				if link.TransactionID != item.ID {
					ids = append(ids, link.TransactionID)
				}
			}
			if err := q.ReplaceSubscriptionLinks(ctx, store.ReplaceSubscriptionLinksParams{SubscriptionID: existing.ID, TransactionIds: ids}); err != nil {
				return err
			}
			if existing.State != subscriptions.StateConfirmed {
				eventType = subscriptionConfirmed
			}
			saved = next
		case errors.Is(err, pgx.ErrNoRows):
			params := store.InsertSubscriptionParams{MerchantKey: merchant.Key, MatchCadence: cadence, State: subscriptions.StateConfirmed,
				Source: subscriptions.SourceManual, Name: merchant.Name, Cadence: cadence, AmountMinor: -item.AmountMinor, Currency: item.Currency,
				CategoryID: category, CategoryEdited: body.CategoryId != nil, AccountID: item.AccountID, Confidence: subscriptions.ConfidenceHigh,
				LastDate: item.OccurredOn, NextDate: subscriptions.Date(subscriptions.Next(item.OccurredOn.Time, cadence))}
			if name != nil {
				params.Name, params.NameEdited = *name, true
			}
			if saved, err = q.InsertSubscription(ctx, params); err != nil {
				return err
			}
			if err := q.ReplaceSubscriptionLinks(ctx, store.ReplaceSubscriptionLinksParams{SubscriptionID: saved.ID, TransactionIds: []pgtype.UUID{item.ID}}); err != nil {
				return err
			}
			status, eventType = fiber.StatusCreated, subscriptionCreated
		default:
			return err
		}
		// Follow the counterparty's other payments at once.
		if _, err := subscriptions.Sync(ctx, q, today, financemoney.Exponent); err != nil {
			return err
		}
		if saved, err = q.GetSubscription(ctx, saved.ID); err != nil {
			return err
		}
		view, err = h.oneSubscriptionView(ctx, q, saved)
		return err
	})
	if err != nil {
		return passThrough(err)
	}
	if eventType != "" {
		h.recordSubscription(c, eventType, saved, nil)
		h.moneyChanged()
	}
	return c.Status(status).JSON(view)
}

// changeSubscription runs change on the subscription under the
// subscriptions lock and stores what it returns; it reports the changed
// fields (none: nothing was stored).
func (h *Handler) changeSubscription(c fiber.Ctx, id types.UUID, change func(q *store.Queries, before store.Subscription) (store.Subscription, []string, error)) (store.Subscription, generated.Subscription, []string, error) {
	ctx := c.Context()
	var after store.Subscription
	var view generated.Subscription
	var changes []string
	err := h.inTx(ctx, func(q *store.Queries) error {
		if err := q.LockSubscriptions(ctx); err != nil {
			return err
		}
		before, err := q.GetSubscriptionForUpdate(ctx, pgUUID(id))
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound("Subscription")
		}
		if err != nil {
			return err
		}
		next, found, err := change(q, before)
		if err != nil {
			return err
		}
		after, changes = before, found
		if len(changes) > 0 {
			if after, err = q.UpdateSubscription(ctx, subscriptions.UpdateParams(next)); err != nil {
				return err
			}
		}
		view, err = h.oneSubscriptionView(ctx, q, after)
		return err
	})
	return after, view, changes, passThroughOrNil(err)
}

func passThroughOrNil(err error) error {
	if err == nil {
		return nil
	}
	return passThrough(err)
}

func (h *Handler) setSubscriptionState(c fiber.Ctx, id types.UUID, state, eventType string) error {
	after, view, changes, err := h.changeSubscription(c, id, func(_ *store.Queries, before store.Subscription) (store.Subscription, []string, error) {
		if before.State == state {
			return before, nil, nil
		}
		next := before
		next.State = state
		return next, []string{"state"}, nil
	})
	if err != nil {
		return err
	}
	if len(changes) > 0 {
		h.recordSubscription(c, eventType, after, nil)
		h.moneyChanged()
	}
	return c.JSON(view)
}

func (h *Handler) ConfirmSubscription(c fiber.Ctx, subscriptionID types.UUID) error {
	return h.setSubscriptionState(c, subscriptionID, subscriptions.StateConfirmed, subscriptionConfirmed)
}

func (h *Handler) IgnoreSubscription(c fiber.Ctx, subscriptionID types.UUID) error {
	return h.setSubscriptionState(c, subscriptionID, subscriptions.StateIgnored, subscriptionIgnored)
}

func (h *Handler) UpdateSubscription(c fiber.Ctx, subscriptionID types.UUID) error {
	var body generated.UpdateSubscriptionRequest
	properties, err := decodeObject(c.Body(), &body)
	if err != nil {
		return invalid(err.Error())
	}
	present := map[string]bool{}
	for _, name := range []string{"name", "cadence", "amountMinor", "categoryId"} {
		if _, ok := properties[name]; ok {
			if name != "categoryId" && isNull(properties, name) {
				return invalid(name + " cannot be null")
			}
			present[name] = true
		}
	}
	if len(present) == 0 {
		return invalid("the update names no subscription field")
	}
	var name *string
	if present["name"] {
		if name, err = cleanText("name", body.Name, subscriptionNameLimit); err != nil {
			return err
		}
		if name == nil {
			return invalid("name must be 1 to 100 characters")
		}
	}
	if present["cadence"] && !subscriptions.ValidCadence(string(*body.Cadence)) {
		return invalid("cadence must be weekly, monthly, quarterly, or yearly")
	}
	if present["amountMinor"] && *body.AmountMinor < 1 {
		return invalid("amountMinor must be at least 1")
	}
	ctx := c.Context()
	after, view, changes, err := h.changeSubscription(c, subscriptionID, func(q *store.Queries, before store.Subscription) (store.Subscription, []string, error) {
		next := before
		var changes []string
		if name != nil && *name != before.Name {
			next.Name, next.NameEdited = *name, true
			changes = append(changes, "name")
		}
		if present["cadence"] && string(*body.Cadence) != before.Cadence {
			next.Cadence = string(*body.Cadence)
			if before.LastDate.Valid {
				next.NextDate = subscriptions.Date(subscriptions.Next(before.LastDate.Time, next.Cadence))
			}
			changes = append(changes, "cadence")
		}
		if present["amountMinor"] && *body.AmountMinor != before.AmountMinor {
			next.AmountMinor, next.AmountEdited = *body.AmountMinor, true
			changes = append(changes, "amount")
		}
		if present["categoryId"] {
			category := optionalUUID(body.CategoryId)
			if category != before.CategoryID {
				if category.Valid {
					if _, err := usableCategory(ctx, q, category); err != nil {
						return next, nil, err
					}
				}
				next.CategoryID, next.CategoryEdited = category, true
				changes = append(changes, "categoryId")
			}
		}
		return next, changes, nil
	})
	if err != nil {
		return err
	}
	if len(changes) > 0 {
		h.recordSubscription(c, subscriptionUpdated, after, changes)
		h.moneyChanged()
	}
	return c.JSON(view)
}

func (h *Handler) DeleteSubscription(c fiber.Ctx, subscriptionID types.UUID) error {
	ctx := c.Context()
	var deleted store.Subscription
	err := h.inTx(ctx, func(q *store.Queries) error {
		if err := q.LockSubscriptions(ctx); err != nil {
			return err
		}
		var err error
		deleted, err = q.DeleteSubscription(ctx, pgUUID(subscriptionID))
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound("Subscription")
		}
		return err
	})
	if err != nil {
		return passThrough(err)
	}
	h.recordSubscription(c, subscriptionDeleted, deleted, nil)
	return c.SendStatus(fiber.StatusNoContent)
}

var subscriptionLabels = map[string]string{"categoryId": "category"}

// recordSubscription: "Subscription “Netflix” confirmed (€13.99
// monthly)".
func (h *Handler) recordSubscription(c fiber.Ctx, eventType string, sub store.Subscription, changes []string) {
	id := uuid(sub.ID).String()
	keys := []string{id}
	if eventType != subscriptionCreated && eventType != subscriptionDeleted {
		keys = append(keys, version(sub.UpdatedAt.Time))
	}
	name := activity.Preview(sub.Name, 120)
	price := fmt.Sprintf("%s %s", financemoney.Format(sub.AmountMinor, sub.Currency), sub.Cadence)
	summary := map[string]string{
		subscriptionCreated:   fmt.Sprintf("“%s” marked as a subscription (%s)", name, price),
		subscriptionConfirmed: fmt.Sprintf("Subscription “%s” confirmed (%s)", name, price),
		subscriptionIgnored:   fmt.Sprintf("Subscription “%s” ignored", name),
		subscriptionDeleted:   fmt.Sprintf("Subscription “%s” deleted", name),
		subscriptionUpdated:   fmt.Sprintf("Subscription “%s” updated: %s", name, labels(changes, subscriptionLabels)),
	}[eventType]
	data := map[string]any{"name": activity.Preview(sub.Name, 200), "state": sub.State, "cadence": sub.Cadence, "amount": sub.AmountMinor,
		"currency": sub.Currency, "source": sub.Source}
	if sub.CategoryID.Valid {
		data["categoryId"] = uuid(sub.CategoryID).String()
	}
	if changes != nil {
		data["changes"] = changes
	}
	h.emit(c.Context(), c, activity.Event{ID: activity.EventID(eventType, keys...), Type: eventType,
		Subject: activity.Subject{Type: "finance_subscription", ID: id}, Summary: summary, Data: data})
}
