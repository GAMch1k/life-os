package api

import (
	"bytes"
	"errors"

	"github.com/gofiber/fiber/v3"
	googleuuid "github.com/google/uuid"

	"github.com/GAMch1k/life-os/services/finance/internal/api/generated"
	"github.com/GAMch1k/life-os/services/finance/internal/importer"
	store "github.com/GAMch1k/life-os/services/finance/internal/repository/generated"
)

// maxReportedErrors bounds the row errors in a response; the failed count
// stays complete.
const maxReportedErrors = 200

func (h *Handler) CreateCsvImport(c fiber.Ctx, params generated.CreateCsvImportParams) error {
	parsed, err := importer.Parse(bytes.NewReader(c.Body()))
	var headerErr importer.HeaderError
	if errors.As(err, &headerErr) {
		return apiError(fiber.StatusBadRequest, "invalid_csv", "The CSV file cannot be imported: "+headerErr.Message)
	}
	if err != nil {
		return internal(err)
	}
	input := importer.Input{}
	if params.AccountId != nil {
		id := googleuuid.UUID(*params.AccountId)
		input.AccountID = &id
	}
	ctx := c.Context()
	var result importer.Result
	err = h.inTx(ctx, func(q *store.Queries) error {
		categories, err := q.ListFinancialCategories(ctx, true)
		if err != nil {
			return err
		}
		active, err := q.ListActiveCategoryRules(ctx)
		if err != nil {
			return err
		}
		input.Categories, input.Rules = categories, ruleSet(active)
		result, err = importer.Import(ctx, q, parsed, input)
		return err
	})
	switch {
	case errors.Is(err, importer.ErrAccountRequired):
		return apiError(fiber.StatusBadRequest, "account_required", "Choose the account to import into (accountId), or give every row an account_id column")
	case errors.Is(err, importer.ErrUnknownAccount):
		return apiError(fiber.StatusBadRequest, "unknown_account", "accountId is not a Finance account")
	case errors.Is(err, importer.ErrAccountArchived):
		return apiError(fiber.StatusConflict, "account_archived", "The account is archived")
	case errors.Is(err, importer.ErrAccountManaged):
		return apiError(fiber.StatusConflict, "managed_by_source", "The transactions of a bank account come from its bank")
	case err != nil:
		return internal(err)
	}
	id := googleuuid.New()
	failed := result.Imported == 0 && result.Failed > 0
	status := generated.Completed
	if failed {
		status = generated.Failed
	}
	response := generated.CsvImport{Id: id, Status: status, Imported: result.Imported, Duplicates: result.Duplicates, Failed: result.Failed,
		Categorized: result.Categorized, Errors: make([]generated.CsvImportError, 0, min(len(result.Errors), maxReportedErrors))}
	if result.Account != nil {
		response.AccountId = nullableUUID(result.Account.ID)
	}
	for index, item := range result.Errors {
		if index == maxReportedErrors {
			break
		}
		response.Errors = append(response.Errors, generated.CsvImportError{Row: item.Row, Message: item.Message})
	}
	h.recordImport(c, id, result.Account, failed, result.Imported, result.Duplicates, result.Failed, result.Categorized)
	return c.JSON(response)
}
