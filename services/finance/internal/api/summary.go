package api

import (
	"errors"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/GAMch1k/life-os/services/finance/internal/api/generated"
	financemoney "github.com/GAMch1k/life-os/services/finance/internal/money"
	store "github.com/GAMch1k/life-os/services/finance/internal/repository/generated"
)

func (h *Handler) GetMonthlySummary(c fiber.Ctx, params generated.GetMonthlySummaryParams) error {
	month, err := time.Parse("2006-01", params.Month)
	if err != nil {
		return invalid("month must use YYYY-MM")
	}
	ctx := c.Context()
	currency := ""
	if params.Currency != nil {
		currency = strings.ToUpper(strings.TrimSpace(*params.Currency))
		if !financemoney.ValidCurrency(currency) {
			return invalid("currency must be a three-letter code such as EUR")
		}
	} else {
		cash, err := h.queries.GetBuiltInAccount(ctx, cashKey)
		switch {
		case err == nil:
			currency = cash.Currency
		case errors.Is(err, pgx.ErrNoRows):
			currency = h.defaultCurrency
		default:
			return internal(err)
		}
	}
	start := pgtype.Date{Time: month, Valid: true}
	accountID := optionalUUID(params.AccountId)
	cashflow, err := h.queries.GetMonthlyCashflow(ctx, store.GetMonthlyCashflowParams{Currency: currency, AccountID: accountID, MonthStart: start})
	if err != nil {
		return internal(err)
	}
	accounts, err := h.queries.ListAccountsWithBalance(ctx, store.ListAccountsWithBalanceParams{IncludeArchived: true, AccountID: accountID})
	if err != nil {
		return internal(err)
	}
	var balance int64
	for _, account := range accounts {
		if account.Currency == currency {
			balance += account.BalanceMinor
		}
	}
	totals, err := h.queries.GetMonthlyCategoryTotals(ctx, store.GetMonthlyCategoryTotalsParams{Currency: currency, AccountID: accountID, MonthStart: start})
	if err != nil {
		return internal(err)
	}
	summary := generated.MonthlySummary{
		Month: params.Month, Currency: currency, TotalBalance: money(balance, currency),
		Income: money(cashflow.IncomeMinor, currency), Spending: money(cashflow.SpendingMinor, currency), Net: money(cashflow.NetMinor, currency),
		SpendingByCategory: []generated.SpendingByCategory{}, ByCategory: make([]generated.CategoryTotal, 0, len(totals)),
	}
	for _, row := range totals {
		total := generated.CategoryTotal{CategoryId: nullableUUID(row.CategoryID), CategoryName: row.CategoryName,
			Income: money(row.IncomeMinor, currency), Spending: money(row.SpendingMinor, currency),
			Net: money(row.IncomeMinor-row.SpendingMinor, currency), TransactionCount: int(row.TransactionCount)}
		if row.CategoryKind != nil {
			kind := generated.CategoryTotalCategoryKind(*row.CategoryKind)
			total.CategoryKind = &kind
		}
		summary.ByCategory = append(summary.ByCategory, total)
		// Rows come largest spending first, so this list keeps that order.
		if row.CategoryID.Valid && row.CategoryName != nil && row.SpendingMinor > 0 && (row.CategoryKind == nil || *row.CategoryKind != "transfer") {
			summary.SpendingByCategory = append(summary.SpendingByCategory, generated.SpendingByCategory{
				CategoryId: uuid(row.CategoryID), CategoryName: *row.CategoryName, Amount: money(row.SpendingMinor, currency)})
		}
	}
	return c.JSON(summary)
}
