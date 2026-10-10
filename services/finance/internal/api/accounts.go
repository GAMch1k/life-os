package api

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/oapi-codegen/runtime/types"

	"github.com/GAMch1k/life-os/services/finance/internal/api/generated"
	financemoney "github.com/GAMch1k/life-os/services/finance/internal/money"
	store "github.com/GAMch1k/life-os/services/finance/internal/repository/generated"
)

// cashKey is the system key of the built-in Cash account.
const cashKey = "cash"

var accountKinds = map[string]bool{"bank": true, "cash": true, "card": true, "savings": true, "other": true}

// EnsureCashAccount returns the built-in Cash account, creating it when it
// is missing: an account the owner already named Cash becomes the built-in
// one, otherwise a new Cash account in the default currency is created. It
// is idempotent and safe to run concurrently.
func (h *Handler) EnsureCashAccount(ctx context.Context) (store.Account, error) {
	if account, err := h.queries.GetBuiltInAccount(ctx, cashKey); err == nil {
		return account, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return store.Account{}, err
	}
	var account store.Account
	created := false
	err := pgx.BeginFunc(ctx, h.pool, func(tx pgx.Tx) error {
		q := h.queries.WithTx(tx)
		if err := q.LockBuiltInAccounts(ctx); err != nil {
			return err
		}
		var err error
		if account, err = q.GetBuiltInAccount(ctx, cashKey); !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if account, err = q.AdoptAccountAsBuiltIn(ctx, store.AdoptAccountAsBuiltInParams{SystemKey: cashKey, Name: "Cash"}); !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		for _, name := range []string{"Cash", "Cash (Life OS)"} {
			// A savepoint keeps the transaction usable after a name clash
			// with an account Finance may not adopt (a bank account).
			err = pgx.BeginFunc(ctx, tx, func(savepoint pgx.Tx) error {
				var createErr error
				account, createErr = h.queries.WithTx(savepoint).CreateBuiltInAccount(ctx, store.CreateBuiltInAccountParams{Name: name, Currency: h.defaultCurrency, SystemKey: cashKey})
				return createErr
			})
			if !uniqueViolation(err, "accounts_name_key") {
				break
			}
		}
		created = err == nil
		return err
	})
	if err != nil {
		return store.Account{}, err
	}
	if created {
		h.recordAccount(ctx, nil, accountCreated, account, nil)
	}
	return account, nil
}

func (h *Handler) ListAccounts(c fiber.Ctx, params generated.ListAccountsParams) error {
	includeArchived := params.IncludeArchived != nil && *params.IncludeArchived
	rows, err := h.queries.ListAccountsWithBalance(c.Context(), store.ListAccountsWithBalanceParams{IncludeArchived: includeArchived})
	if err != nil {
		return internal(err)
	}
	result := generated.AccountList{Items: make([]generated.Account, 0, len(rows))}
	for _, row := range rows {
		result.Items = append(result.Items, accountView(row))
	}
	return c.JSON(result)
}

func (h *Handler) GetAccount(c fiber.Ctx, accountID types.UUID) error {
	view, err := h.accountWithBalance(c.Context(), h.queries, pgUUID(accountID))
	if err != nil {
		return passThrough(err)
	}
	return c.JSON(view)
}

func (h *Handler) CreateAccount(c fiber.Ctx) error {
	var body generated.CreateAccountRequest
	if _, err := decodeObject(c.Body(), &body); err != nil {
		return invalid(err.Error())
	}
	name, err := accountName(body.Name)
	if err != nil {
		return err
	}
	currency := strings.ToUpper(strings.TrimSpace(body.Currency))
	if !financemoney.ValidCurrency(currency) {
		return invalid("currency must be a three-letter code such as EUR")
	}
	kind := "other"
	if body.Kind != nil {
		kind = string(*body.Kind)
	}
	if !accountKinds[kind] {
		return invalid("kind must be bank, cash, card, savings, or other")
	}
	var opening int64
	if body.OpeningBalanceMinor != nil {
		opening = *body.OpeningBalanceMinor
	}
	created, err := h.queries.CreateAccount(c.Context(), store.CreateAccountParams{Name: name, Currency: currency, OpeningBalanceMinor: opening, Kind: kind})
	if uniqueViolation(err, "accounts_name_key") {
		return apiError(fiber.StatusConflict, "account_name_taken", "Another account is already called "+name)
	}
	if err != nil {
		return internal(err)
	}
	h.recordAccount(c.Context(), c, accountCreated, created, nil)
	view, err := h.accountWithBalance(c.Context(), h.queries, created.ID)
	if err != nil {
		return passThrough(err)
	}
	return c.Status(fiber.StatusCreated).JSON(view)
}

func (h *Handler) UpdateAccount(c fiber.Ctx, accountID types.UUID) error {
	var body generated.UpdateAccountRequest
	properties, err := decodeObject(c.Body(), &body)
	if err != nil {
		return invalid(err.Error())
	}
	known := 0
	for _, name := range []string{"name", "kind", "currency", "openingBalanceMinor", "archived"} {
		if _, ok := properties[name]; ok {
			if isNull(properties, name) {
				return invalid(name + " cannot be null")
			}
			known++
		}
	}
	if known == 0 {
		return invalid("the update names no account field")
	}
	params := store.UpdateAccountParams{ID: pgUUID(accountID), OpeningBalanceMinor: body.OpeningBalanceMinor, Archived: body.Archived}
	if body.Name != nil {
		name, err := accountName(*body.Name)
		if err != nil {
			return err
		}
		params.Name = &name
	}
	if body.Kind != nil {
		if !accountKinds[string(*body.Kind)] {
			return invalid("kind must be bank, cash, card, savings, or other")
		}
		params.Kind = pointer(string(*body.Kind))
	}
	if body.Currency != nil {
		currency := strings.ToUpper(strings.TrimSpace(*body.Currency))
		if !financemoney.ValidCurrency(currency) {
			return invalid("currency must be a three-letter code such as EUR")
		}
		params.Currency = &currency
	}
	var before, after store.Account
	err = h.inTx(c.Context(), func(q *store.Queries) error {
		var err error
		before, err = q.GetAccount(c.Context(), params.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound("Account")
		}
		if err != nil {
			return err
		}
		if before.SystemKey != nil {
			if params.Kind != nil && *params.Kind != "cash" {
				return apiError(fiber.StatusConflict, "built_in_account", "The built-in Cash account stays a cash account")
			}
			if params.Archived != nil && *params.Archived {
				return apiError(fiber.StatusConflict, "built_in_account", "The built-in Cash account cannot be archived")
			}
		}
		if params.Currency != nil && *params.Currency != before.Currency {
			if before.SourceKind == "bank" {
				return apiError(fiber.StatusConflict, "managed_by_source", "A bank account keeps the currency its bank reports")
			}
			count, err := q.CountAccountTransactions(c.Context(), params.ID)
			if err != nil {
				return err
			}
			if count > 0 {
				return apiError(fiber.StatusConflict, "account_has_transactions", "The currency of an account with transactions cannot change")
			}
		}
		after, err = q.UpdateAccount(c.Context(), params)
		if uniqueViolation(err, "accounts_name_key") {
			return apiError(fiber.StatusConflict, "account_name_taken", "Another account is already called "+*params.Name)
		}
		return err
	})
	if err != nil {
		return passThrough(err)
	}
	if eventType, changes := classifyAccountUpdate(before, after); eventType != "" {
		h.recordAccount(c.Context(), c, eventType, after, changes)
	}
	view, err := h.accountWithBalance(c.Context(), h.queries, after.ID)
	if err != nil {
		return passThrough(err)
	}
	return c.JSON(view)
}

func accountName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" || utf8.RuneCountInString(name) > 100 {
		return "", invalid("name must be 1 to 100 characters")
	}
	return name, nil
}

// accountWithBalance is one account as the API shows it, archived or not.
func (h *Handler) accountWithBalance(ctx context.Context, q *store.Queries, id pgtype.UUID) (generated.Account, error) {
	rows, err := q.ListAccountsWithBalance(ctx, store.ListAccountsWithBalanceParams{IncludeArchived: true, AccountID: id})
	if err != nil {
		return generated.Account{}, err
	}
	if len(rows) == 0 {
		return generated.Account{}, notFound("Account")
	}
	return accountView(rows[0]), nil
}

func accountView(row store.ListAccountsWithBalanceRow) generated.Account {
	view := generated.Account{
		Id: uuid(row.ID), Name: row.Name, Kind: generated.AccountKind(row.Kind), Currency: row.Currency,
		OpeningBalance: money(row.OpeningBalanceMinor, row.Currency), Balance: money(row.BalanceMinor, row.Currency),
		Pending: money(row.PendingMinor, row.Currency), TransactionCount: int(row.TransactionCount),
		BuiltIn: row.SystemKey != nil, Archived: row.ArchivedAt.Valid, ArchivedAt: nullableTime(row.ArchivedAt),
		Source: generated.AccountSource{Kind: generated.AccountSourceKind(row.SourceKind), ConnectionId: nullableUUID(row.ConnectionID), ExternalId: row.ExternalID,
			Provider: row.Provider, Institution: row.Institution, IbanMasked: row.IbanMasked, SyncedAt: nullableTime(row.SyncedAt)},
		CreatedAt: row.CreatedAt.Time.UTC(), UpdatedAt: row.UpdatedAt.Time.UTC(),
	}
	if row.SourceKind == "bank" {
		// The balance query shows the bank's booked balance when it has one.
		view.BalanceAsOf = nullableDate(row.BookedBalanceDate)
		if row.AvailableBalanceMinor != nil {
			view.Available = pointer(money(*row.AvailableBalanceMinor, row.Currency))
		}
	}
	return view
}

// classifyAccountUpdate names what an update did: archived, restored, or
// updated, with the changed fields; no type when nothing changed.
func classifyAccountUpdate(before, after store.Account) (string, []string) {
	var changes []string
	add := func(name string, changed bool) {
		if changed {
			changes = append(changes, name)
		}
	}
	add("name", before.Name != after.Name)
	add("kind", before.Kind != after.Kind)
	add("currency", before.Currency != after.Currency)
	add("openingBalance", before.OpeningBalanceMinor != after.OpeningBalanceMinor)
	add("archived", before.ArchivedAt.Valid != after.ArchivedAt.Valid)
	switch {
	case len(changes) == 0:
		return "", nil
	case !before.ArchivedAt.Valid && after.ArchivedAt.Valid:
		return accountArchived, changes
	case before.ArchivedAt.Valid && !after.ArchivedAt.Valid:
		return accountRestored, changes
	}
	return accountUpdated, changes
}
