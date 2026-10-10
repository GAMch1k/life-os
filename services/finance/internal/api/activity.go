package api

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	googleuuid "github.com/google/uuid"

	"github.com/GAMch1k/life-os/pkg/activity"
	financemoney "github.com/GAMch1k/life-os/services/finance/internal/money"
	store "github.com/GAMch1k/life-os/services/finance/internal/repository/generated"
)

// Activity event types Finance records. Each change made through the API is
// one event, attributed to the request's actor; an import or a re-apply of
// the rules is one summary event, not one per transaction.
const (
	accountCreated     = "finance.account.created"
	accountUpdated     = "finance.account.updated"
	accountArchived    = "finance.account.archived"
	accountRestored    = "finance.account.restored"
	transactionCreated = "finance.transaction.created"
	transactionUpdated = "finance.transaction.updated"
	transactionDeleted = "finance.transaction.deleted"
	categoryCreated    = "finance.category.created"
	categoryUpdated    = "finance.category.updated"
	categoryArchived   = "finance.category.archived"
	categoryRestored   = "finance.category.restored"
	ruleCreated        = "finance.category_rule.created"
	ruleUpdated        = "finance.category_rule.updated"
	ruleDeleted        = "finance.category_rule.deleted"
	rulesApplied       = "finance.category_rules.applied"
	importCompleted    = "finance.import.completed"
	importFailed       = "finance.import.failed"
	// A bank sync is one summary event with its counts, never transaction
	// details.
	bankImportCompleted   = "finance.bank_import.completed"
	bankConnectionRemoved = "finance.bank_connection.removed"
)

// emit records one event. With a request, the event carries its actor,
// correlation, and causation; without one (a startup task) it is the
// service's own (system:finance).
func (h *Handler) emit(ctx context.Context, c fiber.Ctx, event activity.Event) {
	if !h.recording() {
		return
	}
	if c != nil {
		trace := activity.FromRequest(c)
		event.CorrelationID, event.CausationID, event.Actor = trace.CorrelationID, trace.CausationID, trace.Actor
	}
	if event.Outcome == "" {
		event.Outcome = activity.Succeeded
	}
	event.Summary = activity.Preview(event.Summary, 500)
	h.activity.Emit(ctx, event)
}

func (h *Handler) recording() bool { return h.activity != nil && h.activity.URL != "" }

// version tells repeated changes of one entity apart in deterministic ids.
func version(updatedAt time.Time) string { return updatedAt.UTC().Format(time.RFC3339Nano) }

func (h *Handler) recordAccount(ctx context.Context, c fiber.Ctx, eventType string, account store.Account, changes []string) {
	id := uuid(account.ID).String()
	keys := []string{id}
	if eventType != accountCreated {
		keys = append(keys, version(account.UpdatedAt.Time))
	}
	data := map[string]any{"name": activity.Preview(account.Name, 200), "kind": account.Kind, "currency": account.Currency, "builtIn": account.SystemKey != nil}
	if changes != nil {
		data["changes"] = changes
	}
	name := activity.Preview(account.Name, 120)
	summary := map[string]string{
		accountCreated:  fmt.Sprintf("Account “%s” created (%s, %s)", name, account.Kind, account.Currency),
		accountArchived: fmt.Sprintf("Account “%s” archived", name),
		accountRestored: fmt.Sprintf("Account “%s” restored", name),
	}[eventType]
	if eventType == accountUpdated {
		summary = fmt.Sprintf("Account “%s” updated: %s", name, labels(changes, accountLabels))
	}
	h.emit(ctx, c, activity.Event{ID: activity.EventID(eventType, keys...), Type: eventType,
		Subject: activity.Subject{Type: "finance_account", ID: id}, Summary: summary, Data: data})
}

var accountLabels = map[string]string{"openingBalance": "opening balance", "archived": "archived"}

var transactionLabels = map[string]string{"accountId": "account", "bookingDate": "date", "valueDate": "value date", "categoryId": "category"}

func labels(changes []string, names map[string]string) string {
	out := make([]string, 0, len(changes))
	for _, change := range changes {
		if label, ok := names[change]; ok {
			out = append(out, label)
		} else {
			out = append(out, change)
		}
	}
	return strings.Join(out, ", ")
}

// recordTransaction records a transaction change. It names the account, so
// the summary reads like "Spent €12.00 at Bakery (Cash)".
func (h *Handler) recordTransaction(c fiber.Ctx, eventType string, item store.Transaction, changes []string) {
	if !h.recording() {
		return
	}
	accountName := "an account"
	if account, err := h.queries.GetAccount(c.Context(), item.AccountID); err == nil {
		accountName = account.Name
	}
	id := uuid(item.ID).String()
	keys := []string{id}
	if eventType == transactionUpdated {
		keys = append(keys, version(item.UpdatedAt.Time))
	}
	data := map[string]any{
		"accountId": uuid(item.AccountID).String(), "accountName": activity.Preview(accountName, 200),
		"amount": item.AmountMinor, "currency": item.Currency, "bookingDate": item.OccurredOn.Time.Format(time.DateOnly),
		"status": item.Status, "source": sourceKind(item),
	}
	if item.Counterparty != nil {
		data["counterparty"] = activity.Preview(*item.Counterparty, 200)
	}
	if item.Description != nil {
		data["description"] = activity.Preview(*item.Description, 200)
	}
	if item.CategoryID.Valid {
		data["categoryId"] = uuid(item.CategoryID).String()
	}
	if changes != nil {
		data["changes"] = changes
	}
	h.emit(c.Context(), c, activity.Event{ID: activity.EventID(eventType, keys...), Type: eventType,
		Subject: activity.Subject{Type: "finance_transaction", ID: id}, Summary: transactionSummary(eventType, item, accountName, changes), Data: data})
}

// transactionSummary is the change as a sentence, amounts in the
// transaction's currency.
func transactionSummary(eventType string, item store.Transaction, accountName string, changes []string) string {
	amount := financemoney.Format(item.AmountMinor, item.Currency)
	account := activity.Preview(accountName, 80)
	label := ""
	switch {
	case item.Counterparty != nil:
		label = activity.Preview(*item.Counterparty, 80)
	case item.Description != nil:
		label = activity.Preview(*item.Description, 80)
	}
	switch eventType {
	case transactionCreated:
		sentence := "Received " + financemoney.Format(item.AmountMinor, item.Currency)
		preposition := " from "
		if item.AmountMinor < 0 {
			sentence, preposition = "Spent "+financemoney.Format(-item.AmountMinor, item.Currency), " at "
		}
		switch {
		case item.Counterparty != nil:
			sentence += preposition + label
		case item.Description != nil:
			sentence += " for “" + label + "”"
		}
		sentence += " (" + account + ")"
		if item.Status == "pending" {
			sentence += ", pending"
		}
		return sentence
	}
	subject := fmt.Sprintf("Transaction of %s (%s)", amount, account)
	if label != "" {
		subject = fmt.Sprintf("Transaction “%s” (%s, %s)", label, amount, account)
	}
	if eventType == transactionDeleted {
		return subject + " deleted"
	}
	return subject + " updated: " + labels(changes, transactionLabels)
}

func (h *Handler) recordCategory(c fiber.Ctx, eventType string, category store.FinancialCategory, changes []string) {
	id := uuid(category.ID).String()
	keys := []string{id}
	if eventType != categoryCreated {
		keys = append(keys, version(category.UpdatedAt.Time))
	}
	data := map[string]any{"name": activity.Preview(category.Name, 200), "kind": category.Kind}
	if changes != nil {
		data["changes"] = changes
	}
	name := activity.Preview(category.Name, 120)
	summary := map[string]string{
		categoryCreated:  fmt.Sprintf("Category “%s” created (%s)", name, category.Kind),
		categoryArchived: fmt.Sprintf("Category “%s” archived", name),
		categoryRestored: fmt.Sprintf("Category “%s” restored", name),
	}[eventType]
	if eventType == categoryUpdated {
		summary = fmt.Sprintf("Category “%s” updated: %s", name, strings.Join(changes, ", "))
	}
	h.emit(c.Context(), c, activity.Event{ID: activity.EventID(eventType, keys...), Type: eventType,
		Subject: activity.Subject{Type: "finance_category", ID: id}, Summary: summary, Data: data})
}

func (h *Handler) recordRule(c fiber.Ctx, eventType string, rule store.CategoryRule) {
	if !h.recording() {
		return
	}
	categoryName := "a category"
	if category, err := h.queries.GetFinancialCategory(c.Context(), rule.CategoryID); err == nil {
		categoryName = category.Name
	}
	id := uuid(rule.ID).String()
	keys := []string{id}
	if eventType == ruleUpdated {
		keys = append(keys, version(rule.UpdatedAt.Time))
	}
	verb := map[string]string{ruleCreated: "created", ruleUpdated: "updated", ruleDeleted: "deleted"}[eventType]
	summary := fmt.Sprintf("Category rule %s: %s %s “%s” → %s", verb, rule.MatchField, rule.MatchOperator,
		activity.Preview(rule.MatchText, 80), activity.Preview(categoryName, 80))
	h.emit(c.Context(), c, activity.Event{ID: activity.EventID(eventType, keys...), Type: eventType,
		Subject: activity.Subject{Type: "finance_category_rule", ID: id}, Summary: summary,
		Data: map[string]any{"categoryId": uuid(rule.CategoryID).String(), "categoryName": activity.Preview(categoryName, 200),
			"field": rule.MatchField, "operator": rule.MatchOperator, "text": activity.Preview(rule.MatchText, 200), "priority": rule.Priority}})
}

func (h *Handler) recordRulesApplied(c fiber.Ctx, scope string, counts ruleCounts) {
	target := "uncategorized transactions"
	if scope == "all" {
		target = "all transactions not categorized by hand"
	}
	summary := fmt.Sprintf("Category rules applied to %s: %d categorized", target, counts.Categorized)
	if scope == "all" {
		summary += fmt.Sprintf(", %d recategorized, %d cleared", counts.Recategorized, counts.Cleared)
	}
	h.emit(c.Context(), c, activity.Event{ID: googleuuid.New(), Type: rulesApplied,
		Subject: activity.Subject{Type: "finance_category_rules", ID: "all"}, Summary: summary,
		Data: map[string]any{"scope": scope, "examined": counts.Examined, "categorized": counts.Categorized,
			"recategorized": counts.Recategorized, "cleared": counts.Cleared, "unchanged": counts.Unchanged}})
}

func (h *Handler) recordImport(c fiber.Ctx, id googleuuid.UUID, account *store.Account, failed bool, imported, duplicates, failures, categorized int) {
	target := "each row's account"
	data := map[string]any{"imported": imported, "duplicates": duplicates, "failed": failures, "categorized": categorized}
	if account != nil {
		target = activity.Preview(account.Name, 80)
		data["accountId"], data["accountName"] = uuid(account.ID).String(), activity.Preview(account.Name, 200)
	}
	event := activity.Event{ID: id, Type: importCompleted, Subject: activity.Subject{Type: "finance_import", ID: id.String()}, Data: data,
		Summary: fmt.Sprintf("Imported %d %s into %s (%d duplicates, %d failed)", imported, plural(imported, "transaction"), target, duplicates, failures)}
	if failed {
		event.Type, event.Outcome = importFailed, activity.Failed
		event.Summary = fmt.Sprintf("CSV import into %s failed: %d %s could not be imported", target, failures, plural(failures, "row"))
	}
	h.emit(c.Context(), c, event)
}

func plural(count int, word string) string {
	if count == 1 {
		return word
	}
	return word + "s"
}
