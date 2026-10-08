// Package api contains the finance HTTP transport adapters.
package api

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/gofiber/fiber/v3"
	googleuuid "github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/oapi-codegen/runtime/types"

	"github.com/GAMch1k/life-os/services/finance/internal/api/generated"
	"github.com/GAMch1k/life-os/services/finance/internal/importer"
	store "github.com/GAMch1k/life-os/services/finance/internal/repository/generated"
)

type Handler struct{ queries *store.Queries }

var _ generated.ServerInterface = (*Handler)(nil)

func NewHandler(queries *store.Queries) *Handler { return &Handler{queries: queries} }

func (h *Handler) ListAccounts(c fiber.Ctx) error {
	items, err := h.queries.ListAccountsWithBalance(c.Context())
	if err != nil {
		return internal(err)
	}
	result := make([]generated.Account, 0, len(items))
	for _, item := range items {
		result = append(result, generated.Account{Id: uuid(item.ID), Name: item.Name, Balance: generated.Money{Amount: item.BalanceMinor, Currency: item.Currency}})
	}
	return c.JSON(generated.AccountList{Items: result})
}

func (h *Handler) ListCategories(c fiber.Ctx) error {
	items, err := h.queries.ListFinancialCategories(c.Context())
	if err != nil {
		return internal(err)
	}
	result := make([]generated.Category, 0, len(items))
	for _, item := range items {
		result = append(result, generated.Category{Id: uuid(item.ID), Name: item.Name, Kind: generated.CategoryKind(item.Kind)})
	}
	return c.JSON(generated.CategoryList{Items: result})
}

func (h *Handler) UpdateCategory(c fiber.Ctx, categoryID types.UUID) error {
	var body generated.UpdateCategoryJSONRequestBody
	if err := c.Bind().Body(&body); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid category request")
	}
	var kind *string
	if body.Kind != nil {
		value := string(*body.Kind)
		kind = &value
	}
	item, err := h.queries.UpdateFinancialCategory(c.Context(), store.UpdateFinancialCategoryParams{ID: pgUUID(categoryID), Name: body.Name, Kind: kind})
	if err != nil {
		return databaseError(err)
	}
	return c.JSON(generated.Category{Id: uuid(item.ID), Name: item.Name, Kind: generated.CategoryKind(item.Kind)})
}

func (h *Handler) CreateCsvImport(c fiber.Ctx) error {
	result := importer.Import(c.Context(), h.queries, bytes.NewReader(c.Body()))
	errors := make([]generated.CsvImportError, 0, len(result.Errors))
	for _, item := range result.Errors {
		errors = append(errors, generated.CsvImportError{Row: item.Row, Message: item.Message})
	}
	status := generated.Completed
	if result.Failed > 0 && result.Created == 0 {
		status = generated.Failed
	}
	return c.Status(fiber.StatusAccepted).JSON(generated.CsvImport{Id: googleuuid.New(), Status: status, Created: result.Created, Skipped: result.Skipped, Failed: result.Failed, Errors: errors})
}

func (h *Handler) GetMonthlySummary(c fiber.Ctx, params generated.GetMonthlySummaryParams) error {
	month, err := time.Parse("2006-01", params.Month)
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "month must use YYYY-MM")
	}
	cashflow, err := h.queries.GetMonthlyCashflow(c.Context(), pgtype.Date{Time: month, Valid: true})
	if err != nil {
		return internal(err)
	}
	accounts, err := h.queries.ListAccountsWithBalance(c.Context())
	if err != nil {
		return internal(err)
	}
	var balance int64
	for _, account := range accounts {
		balance += account.BalanceMinor
	}
	currency := "USD"
	if len(accounts) > 0 {
		currency = accounts[0].Currency
	}
	spendingRows, err := h.queries.GetMonthlySpendingByCategory(c.Context(), pgtype.Date{Time: month, Valid: true})
	if err != nil {
		return internal(err)
	}
	spendingByCategory := make([]generated.SpendingByCategory, 0, len(spendingRows))
	for _, row := range spendingRows {
		spendingByCategory = append(spendingByCategory, generated.SpendingByCategory{CategoryId: uuid(row.CategoryID), CategoryName: row.CategoryName, Amount: generated.Money{Amount: row.AmountMinor, Currency: currency}})
	}
	return c.JSON(generated.MonthlySummary{Month: params.Month, TotalBalance: generated.Money{Amount: balance, Currency: currency}, Income: generated.Money{Amount: cashflow.IncomeMinor, Currency: currency}, Spending: generated.Money{Amount: cashflow.SpendingMinor, Currency: currency}, Net: generated.Money{Amount: cashflow.NetMinor, Currency: currency}, SpendingByCategory: spendingByCategory})
}

func (h *Handler) ListTransactions(c fiber.Ctx, params generated.ListTransactionsParams) error {
	limit := 50
	if params.Limit != nil {
		limit = *params.Limit
	}
	items, err := h.queries.ListTransactions(c.Context(), store.ListTransactionsParams{AccountID: optionalUUID(params.AccountId), CategoryID: optionalUUID(params.CategoryId), FromDate: optionalDate(params.From), ToDate: optionalDate(params.To), PageLimit: int32(limit)})
	if err != nil {
		return internal(err)
	}
	result := make([]generated.Transaction, 0, len(items))
	for _, item := range items {
		result = append(result, transaction(item))
	}
	return c.JSON(generated.TransactionList{Items: result})
}

func (h *Handler) CreateManualTransaction(c fiber.Ctx) error {
	var body generated.CreateManualTransactionJSONRequestBody
	if err := c.Bind().Body(&body); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid transaction request")
	}
	if body.Amount.Amount == 0 || len(body.Amount.Currency) != 3 {
		return fiber.NewError(fiber.StatusBadRequest, "amount must be non-zero with a three-letter currency")
	}
	key, hash := idempotency(c)
	item, err := h.queries.CreateTransaction(c.Context(), store.CreateTransactionParams{AccountID: pgUUID(body.AccountId), CategoryID: optionalUUID(body.CategoryId), AmountMinor: body.Amount.Amount, Currency: body.Amount.Currency, OccurredOn: pgtype.Date{Time: body.OccurredOn.Time, Valid: true}, Note: body.Note, SourceMetadata: []byte(`{}`), AutomationIdempotencyKey: key, AutomationPayloadHash: hash})
	if err != nil {
		return databaseError(err)
	}
	if key != nil && (item.AutomationPayloadHash == nil || *item.AutomationPayloadHash != *hash) {
		return fiber.NewError(fiber.StatusConflict, "idempotency key was already used with a different payload")
	}
	return c.Status(fiber.StatusCreated).JSON(transaction(item))
}
func idempotency(c fiber.Ctx) (*string, *string) {
	key := c.Get("Idempotency-Key")
	if key == "" {
		return nil, nil
	}
	sum := sha256.Sum256(c.Body())
	hash := fmt.Sprintf("%x", sum[:])
	return &key, &hash
}

func transaction(item store.Transaction) generated.Transaction {
	return generated.Transaction{Id: uuid(item.ID), AccountId: uuid(item.AccountID), CategoryId: nullableUUID(item.CategoryID), Amount: generated.Money{Amount: item.AmountMinor, Currency: item.Currency}, OccurredOn: types.Date{Time: item.OccurredOn.Time}, Note: item.Note}
}
func uuid(value pgtype.UUID) types.UUID   { return types.UUID(value.Bytes) }
func pgUUID(value types.UUID) pgtype.UUID { return pgtype.UUID{Bytes: [16]byte(value), Valid: true} }
func nullableUUID(value pgtype.UUID) *types.UUID {
	if !value.Valid {
		return nil
	}
	result := uuid(value)
	return &result
}
func optionalUUID(value *types.UUID) pgtype.UUID {
	if value == nil {
		return pgtype.UUID{}
	}
	return pgUUID(*value)
}
func optionalDate(value *types.Date) pgtype.Date {
	if value == nil {
		return pgtype.Date{}
	}
	return pgtype.Date{Time: value.Time, Valid: true}
}
func internal(err error) error {
	return fiber.NewError(fiber.StatusInternalServerError, fmt.Sprintf("finance query failed: %v", err))
}
func databaseError(err error) error {
	if err != nil {
		return internal(err)
	}
	return nil
}
