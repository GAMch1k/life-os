package api

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	googleuuid "github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/oapi-codegen/runtime/types"

	"github.com/GAMch1k/life-os/services/finance/internal/api/generated"
	financemoney "github.com/GAMch1k/life-os/services/finance/internal/money"
	store "github.com/GAMch1k/life-os/services/finance/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/finance/internal/rules"
)

const (
	counterpartyLimit = 200
	descriptionLimit  = 1000
	notesLimit        = 2000
)

var (
	transactionStatuses = map[string]bool{"booked": true, "pending": true}
	sourceKinds         = map[string]bool{"manual": true, "csv": true, "bank": true}
	categorizers        = map[string]bool{rules.ByManual: true, rules.ByRule: true, rules.ByMCC: true, rules.ByAI: true}
)

func sourceKind(item store.Transaction) string {
	if item.ImportSource == nil {
		return "manual"
	}
	return *item.ImportSource
}

func transactionView(item store.Transaction) generated.Transaction {
	view := generated.Transaction{
		Id: uuid(item.ID), AccountId: uuid(item.AccountID), Amount: money(item.AmountMinor, item.Currency),
		BookingDate: types.Date{Time: item.OccurredOn.Time}, ValueDate: nullableDate(item.ValueDate),
		Status: generated.TransactionStatus(item.Status), Counterparty: item.Counterparty, Description: item.Description,
		Notes: item.Note, CategoryId: nullableUUID(item.CategoryID), CategoryRuleId: nullableUUID(item.CategoryRuleID),
		MerchantCategoryCode: item.MerchantCategoryCode, BankReference: item.BankReference,
		Source: generated.TransactionSource{Kind: generated.TransactionSourceKind(sourceKind(item)), ConnectionId: nullableUUID(item.ConnectionID),
			ExternalId: item.ImportExternalID},
		ReadOnly: item.ImportSource != nil, CreatedAt: item.CreatedAt.Time.UTC(), UpdatedAt: item.UpdatedAt.Time.UTC(),
	}
	if item.CategorizedBy != nil {
		by := generated.TransactionCategorizedBy(*item.CategorizedBy)
		view.CategorizedBy = &by
	}
	return view
}

// Cursors are opaque to clients: base64url of "tx|bookingDate|createdAt|id",
// the last row of the previous page.
type cursorPosition struct {
	Date      pgtype.Date
	CreatedAt pgtype.Timestamptz
	ID        pgtype.UUID
}

func encodeCursor(item store.Transaction) string {
	raw := strings.Join([]string{"tx", item.OccurredOn.Time.Format(time.DateOnly), item.CreatedAt.Time.UTC().Format(time.RFC3339Nano), uuid(item.ID).String()}, "|")
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(cursor string) (cursorPosition, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return cursorPosition{}, err
	}
	fields := strings.Split(string(raw), "|")
	if len(fields) != 4 || fields[0] != "tx" {
		return cursorPosition{}, errors.New("malformed cursor")
	}
	date, err := time.Parse(time.DateOnly, fields[1])
	if err != nil {
		return cursorPosition{}, err
	}
	createdAt, err := time.Parse(time.RFC3339Nano, fields[2])
	if err != nil {
		return cursorPosition{}, err
	}
	id, err := googleuuid.Parse(fields[3])
	if err != nil {
		return cursorPosition{}, err
	}
	return cursorPosition{Date: pgtype.Date{Time: date, Valid: true}, CreatedAt: pgtype.Timestamptz{Time: createdAt, Valid: true}, ID: pgtype.UUID{Bytes: id, Valid: true}}, nil
}

// likeEscaper makes a search word match literally inside ILIKE.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

func (h *Handler) ListTransactions(c fiber.Ctx, params generated.ListTransactionsParams) error {
	limit := 50
	if params.Limit != nil {
		if *params.Limit < 1 || *params.Limit > 100 {
			return invalid("limit must be between 1 and 100")
		}
		limit = *params.Limit
	}
	query := store.ListTransactionsParams{AccountID: optionalUUID(params.AccountId), FromDate: optionalDate(params.From), ToDate: optionalDate(params.To),
		AmountMin: params.AmountMin, AmountMax: params.AmountMax, Words: []string{}, PageLimit: int32(limit + 1)}
	if params.Q != nil {
		words := strings.Fields(*params.Q)
		if len(words) == 0 || len(*params.Q) > 200 || len(words) > 10 {
			return invalid("q must have 1 to 10 words and at most 200 characters")
		}
		for _, word := range words {
			query.Words = append(query.Words, likeEscaper.Replace(word))
		}
	}
	if params.CategoryId != nil {
		if *params.CategoryId == "uncategorized" {
			query.Uncategorized = true
		} else {
			id, err := googleuuid.Parse(*params.CategoryId)
			if err != nil {
				return invalid("categoryId must be a category id or uncategorized")
			}
			query.CategoryID = pgtype.UUID{Bytes: id, Valid: true}
		}
	}
	if params.Direction != nil {
		direction := string(*params.Direction)
		if direction != "in" && direction != "out" {
			return invalid("direction must be in or out")
		}
		query.Direction = &direction
	}
	if (params.AmountMin != nil && *params.AmountMin < 0) || (params.AmountMax != nil && *params.AmountMax < 0) {
		return invalid("amountMin and amountMax must not be negative")
	}
	if params.AmountMin != nil && params.AmountMax != nil && *params.AmountMin > *params.AmountMax {
		return invalid("amountMin must not be above amountMax")
	}
	if params.Status != nil {
		status := string(*params.Status)
		if !transactionStatuses[status] {
			return invalid("status must be booked or pending")
		}
		query.Status = &status
	}
	if params.Source != nil {
		source := string(*params.Source)
		if !sourceKinds[source] {
			return invalid("source must be manual, csv, or bank")
		}
		query.SourceKind = &source
	}
	if params.CategorizedBy != nil {
		by := string(*params.CategorizedBy)
		if !categorizers[by] {
			return invalid("categorizedBy must be manual, rule, mcc, or ai")
		}
		query.CategorizedBy = &by
	}
	if params.Cursor != nil {
		position, err := decodeCursor(*params.Cursor)
		if err != nil {
			return apiError(fiber.StatusBadRequest, "invalid_cursor", "The cursor is not one this list returned")
		}
		query.AfterDate, query.AfterCreatedAt, query.AfterID = position.Date, position.CreatedAt, position.ID
	}
	items, err := h.queries.ListTransactions(c.Context(), query)
	if err != nil {
		return internal(err)
	}
	result := generated.TransactionList{Items: make([]generated.Transaction, 0, min(len(items), limit))}
	for index, item := range items {
		if index == limit {
			cursor := encodeCursor(items[index-1])
			result.NextCursor = &cursor
			break
		}
		result.Items = append(result.Items, transactionView(item))
	}
	return c.JSON(result)
}

func (h *Handler) GetTransaction(c fiber.Ctx, transactionID types.UUID) error {
	item, err := h.queries.GetTransaction(c.Context(), pgUUID(transactionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound("Transaction")
	}
	if err != nil {
		return internal(err)
	}
	return c.JSON(transactionView(item))
}

// usableAccount loads an account a transaction may be booked to.
func usableAccount(ctx context.Context, q *store.Queries, id pgtype.UUID) (store.Account, error) {
	account, err := q.GetAccount(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Account{}, apiError(fiber.StatusBadRequest, "unknown_account", "accountId is not a Finance account")
	}
	if err != nil {
		return store.Account{}, err
	}
	if account.ArchivedAt.Valid {
		return store.Account{}, apiError(fiber.StatusConflict, "account_archived", "The account "+account.Name+" is archived")
	}
	if account.SourceKind == "bank" {
		return store.Account{}, apiError(fiber.StatusConflict, "managed_by_source", "The transactions of "+account.Name+" come from its bank")
	}
	return account, nil
}

// usableCategory loads a category a transaction or rule may be given.
func usableCategory(ctx context.Context, q *store.Queries, id pgtype.UUID) (store.FinancialCategory, error) {
	category, err := q.GetFinancialCategory(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.FinancialCategory{}, apiError(fiber.StatusBadRequest, "unknown_category", "categoryId is not a Finance category")
	}
	if err != nil {
		return store.FinancialCategory{}, err
	}
	if category.ArchivedAt.Valid {
		return store.FinancialCategory{}, apiError(fiber.StatusConflict, "category_archived", "The category "+category.Name+" is archived")
	}
	return category, nil
}

func validMoney(amount generated.Money) (int64, string, error) {
	currency := strings.ToUpper(strings.TrimSpace(amount.Currency))
	if amount.Amount == 0 || !financemoney.ValidCurrency(currency) {
		return 0, "", invalid("amount must be non-zero with a three-letter currency")
	}
	return amount.Amount, currency, nil
}

func currencyMismatch(currency string, account store.Account) error {
	return apiError(fiber.StatusBadRequest, "currency_mismatch", "The amount is in "+currency+" but the account "+account.Name+" is in "+account.Currency)
}

func (h *Handler) CreateManualTransaction(c fiber.Ctx) error {
	var body generated.CreateManualTransactionRequest
	properties, err := decodeObject(c.Body(), &body)
	if err != nil {
		return invalid(err.Error())
	}
	if _, ok := properties["amount"]; !ok {
		return invalid("amount is required")
	}
	amount, currency, err := validMoney(body.Amount)
	if err != nil {
		return err
	}
	params := store.CreateTransactionParams{AmountMinor: amount, Currency: currency, Status: "booked", SourceMetadata: []byte(`{}`), OccurredOn: h.today(), ValueDate: optionalDate(body.ValueDate)}
	if body.BookingDate != nil {
		params.OccurredOn = pgDate(*body.BookingDate)
	}
	if body.Status != nil {
		if !transactionStatuses[string(*body.Status)] {
			return invalid("status must be booked or pending")
		}
		params.Status = string(*body.Status)
	}
	if params.Counterparty, err = cleanText("counterparty", body.Counterparty, counterpartyLimit); err != nil {
		return err
	}
	if params.Description, err = cleanText("description", body.Description, descriptionLimit); err != nil {
		return err
	}
	if params.Note, err = cleanText("notes", body.Notes, notesLimit); err != nil {
		return err
	}
	ctx := c.Context()
	var account store.Account
	if body.AccountId == nil {
		if account, err = h.EnsureCashAccount(ctx); err != nil {
			return internal(err)
		}
	} else if account, err = usableAccount(ctx, h.queries, pgUUID(*body.AccountId)); err != nil {
		return passThrough(err)
	}
	if currency != account.Currency {
		return currencyMismatch(currency, account)
	}
	params.AccountID = account.ID
	if body.CategoryId != nil {
		category, err := usableCategory(ctx, h.queries, pgUUID(*body.CategoryId))
		if err != nil {
			return passThrough(err)
		}
		params.CategoryID, params.CategorizedBy = category.ID, pointer("manual")
	} else {
		chooser, err := categorizer(ctx, h.queries)
		if err != nil {
			return internal(err)
		}
		if assignment, ok := chooser.Choose(textOf(params.Counterparty, params.Description)); ok {
			params.CategoryID, params.CategoryRuleID, params.CategorizedBy = assignmentFields(assignment)
		}
	}
	key, hash := idempotency(c)
	params.AutomationIdempotencyKey, params.AutomationPayloadHash = key, hash
	item, err := h.queries.CreateTransaction(ctx, params)
	if err != nil {
		return internal(err)
	}
	if key != nil && (item.AutomationPayloadHash == nil || *item.AutomationPayloadHash != *hash) {
		return apiError(fiber.StatusConflict, "idempotency_conflict", "The Idempotency-Key was already used with a different body")
	}
	// A deterministic event id makes an idempotent replay record nothing new.
	h.recordTransaction(c, transactionCreated, item, nil)
	h.budgetChanged()
	return c.Status(fiber.StatusCreated).JSON(transactionView(item))
}

func textOf(counterparty, description *string) rules.Text {
	return textWithCode(counterparty, description, nil)
}

// textWithCode is what the rules and the merchant category code fallback
// look at.
func textWithCode(counterparty, description, merchantCategoryCode *string) rules.Text {
	var text rules.Text
	if counterparty != nil {
		text.Counterparty = *counterparty
	}
	if description != nil {
		text.Description = *description
	}
	if merchantCategoryCode != nil {
		text.MerchantCategoryCode = *merchantCategoryCode
	}
	return text
}

// assignmentFields are a chosen category's stored columns.
func assignmentFields(assignment rules.Assignment) (categoryID, ruleID pgtype.UUID, categorizedBy *string) {
	var item store.Transaction
	applyAssignment(&item, assignment)
	return item.CategoryID, item.CategoryRuleID, item.CategorizedBy
}

// transactionFields are the UpdateTransactionRequest properties;
// importedEditable are the ones an imported transaction takes.
var (
	transactionFields = []string{"accountId", "amount", "bookingDate", "valueDate", "status", "counterparty", "description", "notes", "categoryId"}
	importedEditable  = map[string]bool{"categoryId": true, "notes": true}
	requiredFields    = map[string]bool{"accountId": true, "amount": true, "bookingDate": true, "status": true}
)

func (h *Handler) UpdateTransaction(c fiber.Ctx, transactionID types.UUID) error {
	var body generated.UpdateTransactionRequest
	properties, err := decodeObject(c.Body(), &body)
	if err != nil {
		return invalid(err.Error())
	}
	present := map[string]bool{}
	for _, name := range transactionFields {
		if _, ok := properties[name]; ok {
			if requiredFields[name] && isNull(properties, name) {
				return invalid(name + " cannot be null")
			}
			present[name] = true
		}
	}
	if len(present) == 0 {
		return invalid("the update names no transaction field")
	}
	ctx := c.Context()
	var before, after store.Transaction
	var changes []string
	err = h.inTx(ctx, func(q *store.Queries) error {
		var err error
		before, err = q.GetTransactionForUpdate(ctx, pgUUID(transactionID))
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound("Transaction")
		}
		if err != nil {
			return err
		}
		if before.ImportSource != nil {
			for name := range present {
				if !importedEditable[name] {
					return apiError(fiber.StatusConflict, "managed_by_source", "This transaction was imported: only its category and notes can change")
				}
			}
		}
		next := before
		accountChanged := false
		if present["accountId"] && pgUUID(*body.AccountId) != before.AccountID {
			if _, err := usableAccount(ctx, q, pgUUID(*body.AccountId)); err != nil {
				return err
			}
			next.AccountID, accountChanged = pgUUID(*body.AccountId), true
		}
		if present["amount"] {
			if next.AmountMinor, next.Currency, err = validMoney(*body.Amount); err != nil {
				return err
			}
		}
		if accountChanged || next.Currency != before.Currency {
			account, err := q.GetAccount(ctx, next.AccountID)
			if err != nil {
				return err
			}
			if next.Currency != account.Currency {
				return currencyMismatch(next.Currency, account)
			}
		}
		if present["bookingDate"] {
			next.OccurredOn = pgDate(*body.BookingDate)
		}
		if present["valueDate"] {
			next.ValueDate = optionalDate(body.ValueDate)
		}
		if present["status"] {
			if !transactionStatuses[string(*body.Status)] {
				return invalid("status must be booked or pending")
			}
			next.Status = string(*body.Status)
		}
		for name, target := range map[string]**string{"counterparty": &next.Counterparty, "description": &next.Description, "notes": &next.Note} {
			if !present[name] {
				continue
			}
			value := map[string]*string{"counterparty": body.Counterparty, "description": body.Description, "notes": body.Notes}[name]
			limit := map[string]int{"counterparty": counterpartyLimit, "description": descriptionLimit, "notes": notesLimit}[name]
			if *target, err = cleanText(name, value, limit); err != nil {
				return err
			}
		}
		if present["categoryId"] {
			// A choice by hand, also when it clears the category or takes
			// over the AI's.
			next.CategoryID, next.CategoryRuleID, next.CategorizedBy = pgtype.UUID{}, pgtype.UUID{}, pointer("manual")
			if body.CategoryId != nil {
				id := pgUUID(*body.CategoryId)
				if id != before.CategoryID {
					if _, err := usableCategory(ctx, q, id); err != nil {
						return err
					}
				}
				next.CategoryID = id
			}
		} else if (next.CategorizedBy == nil || *next.CategorizedBy != "manual") && (!sameText(before.Counterparty, next.Counterparty) || !sameText(before.Description, next.Description)) {
			chooser, err := categorizer(ctx, q)
			if err != nil {
				return err
			}
			assignment, _ := chooser.Rechoose(assignmentOf(before), textWithCode(next.Counterparty, next.Description, next.MerchantCategoryCode))
			applyAssignment(&next, assignment)
		}
		if changes = transactionChanges(before, next); len(changes) == 0 {
			after = before
			return nil
		}
		after, err = q.UpdateTransaction(ctx, store.UpdateTransactionParams{
			ID: before.ID, AccountID: next.AccountID, AmountMinor: next.AmountMinor, Currency: next.Currency, OccurredOn: next.OccurredOn,
			ValueDate: next.ValueDate, Status: next.Status, Counterparty: next.Counterparty, Description: next.Description, Note: next.Note,
			CategoryID: next.CategoryID, CategorizedBy: next.CategorizedBy, CategoryRuleID: next.CategoryRuleID,
		})
		return err
	})
	if err != nil {
		return passThrough(err)
	}
	if len(changes) > 0 {
		h.recordTransaction(c, transactionUpdated, after, changes)
		h.budgetChanged()
	}
	return c.JSON(transactionView(after))
}

func (h *Handler) DeleteTransaction(c fiber.Ctx, transactionID types.UUID) error {
	ctx := c.Context()
	var deleted store.Transaction
	err := h.inTx(ctx, func(q *store.Queries) error {
		var err error
		deleted, err = q.GetTransactionForUpdate(ctx, pgUUID(transactionID))
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound("Transaction")
		}
		if err != nil {
			return err
		}
		if deleted.ImportSource != nil {
			return apiError(fiber.StatusConflict, "managed_by_source", "This transaction was imported and cannot be deleted")
		}
		return q.DeleteTransaction(ctx, deleted.ID)
	})
	if err != nil {
		return passThrough(err)
	}
	h.recordTransaction(c, transactionDeleted, deleted, nil)
	h.budgetChanged()
	return c.SendStatus(fiber.StatusNoContent)
}

func sameText(a, b *string) bool { return (a == nil && b == nil) || (a != nil && b != nil && *a == *b) }

func assignmentOf(item store.Transaction) rules.Assignment {
	var assignment rules.Assignment
	if item.CategoryID.Valid {
		id := item.CategoryID.Bytes
		assignment.CategoryID = &id
	}
	if item.CategoryRuleID.Valid {
		id := item.CategoryRuleID.Bytes
		assignment.RuleID = &id
	}
	if item.CategorizedBy != nil {
		assignment.CategorizedBy = *item.CategorizedBy
	}
	return assignment
}

func applyAssignment(item *store.Transaction, assignment rules.Assignment) {
	item.CategoryID, item.CategoryRuleID, item.CategorizedBy = pgtype.UUID{}, pgtype.UUID{}, nil
	if assignment.CategoryID != nil {
		item.CategoryID = pgtype.UUID{Bytes: *assignment.CategoryID, Valid: true}
	}
	if assignment.RuleID != nil {
		item.CategoryRuleID = pgtype.UUID{Bytes: *assignment.RuleID, Valid: true}
	}
	if assignment.CategorizedBy != "" {
		item.CategorizedBy = pointer(assignment.CategorizedBy)
	}
}

// transactionChanges lists the API fields that differ. A category taken
// over by hand without another value is a change of who categorized it,
// recorded as a category change.
func transactionChanges(before, after store.Transaction) []string {
	var changes []string
	add := func(name string, changed bool) {
		if changed {
			changes = append(changes, name)
		}
	}
	add("accountId", before.AccountID != after.AccountID)
	add("amount", before.AmountMinor != after.AmountMinor || before.Currency != after.Currency)
	add("bookingDate", !before.OccurredOn.Time.Equal(after.OccurredOn.Time))
	add("valueDate", before.ValueDate.Valid != after.ValueDate.Valid || (before.ValueDate.Valid && !before.ValueDate.Time.Equal(after.ValueDate.Time)))
	add("status", before.Status != after.Status)
	add("counterparty", !sameText(before.Counterparty, after.Counterparty))
	add("description", !sameText(before.Description, after.Description))
	add("notes", !sameText(before.Note, after.Note))
	add("categoryId", before.CategoryID != after.CategoryID || !sameText(before.CategorizedBy, after.CategorizedBy) || before.CategoryRuleID != after.CategoryRuleID)
	return changes
}
