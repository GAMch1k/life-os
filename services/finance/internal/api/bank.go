package api

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/gofiber/fiber/v3"
	googleuuid "github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/oapi-codegen/runtime/types"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/finance/internal/alerts"
	"github.com/GAMch1k/life-os/services/finance/internal/api/generated"
	financemoney "github.com/GAMch1k/life-os/services/finance/internal/money"
	store "github.com/GAMch1k/life-os/services/finance/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/finance/internal/rules"
)

// bankSource is the transaction and account source kind of bank imports.
const bankSource = "bank"

var bankAccountKinds = map[string]bool{"bank": true, "card": true, "savings": true}

// bankCounts are a sync's outcome.
type bankCounts struct {
	AccountsCreated, AccountsUpdated                     int
	Created, Updated, Unchanged, RemovedPending, Skipped int
}

// validateBankImport checks a sync before anything is stored and returns
// it with trimmed text.
func validateBankImport(body *generated.BankImportRequest) error {
	body.Provider, body.Institution = strings.TrimSpace(body.Provider), strings.TrimSpace(body.Institution)
	if googleuuid.UUID(body.ConnectionId) == googleuuid.Nil {
		return invalid("connectionId is required")
	}
	if body.Provider == "" || utf8.RuneCountInString(body.Provider) > 64 {
		return invalid("provider must be 1 to 64 characters")
	}
	if body.Institution == "" || utf8.RuneCountInString(body.Institution) > 200 {
		return invalid("institution must be 1 to 200 characters")
	}
	if body.Window.From.IsZero() || body.Window.To.IsZero() || body.Window.From.After(body.Window.To.Time) {
		return invalid("window needs from and to, with from not after to")
	}
	accounts := map[string]string{}
	for index := range body.Accounts {
		account := &body.Accounts[index]
		account.ExternalId, account.Name = strings.TrimSpace(account.ExternalId), strings.TrimSpace(account.Name)
		account.Currency = strings.ToUpper(strings.TrimSpace(account.Currency))
		if account.ExternalId == "" || len(account.ExternalId) > 200 {
			return invalid("every account needs an externalId of at most 200 characters")
		}
		if _, seen := accounts[account.ExternalId]; seen {
			return invalid("account externalId " + account.ExternalId + " appears twice")
		}
		if account.Name == "" || utf8.RuneCountInString(account.Name) > 100 {
			return invalid("account " + account.ExternalId + " needs a name of 1 to 100 characters")
		}
		if !bankAccountKinds[string(account.Kind)] {
			return invalid("account kind must be bank, card, or savings")
		}
		if !financemoney.ValidCurrency(account.Currency) {
			return invalid("account " + account.ExternalId + " needs a three-letter currency")
		}
		for _, balance := range []*generated.BankBalance{account.BookedBalance, account.AvailableBalance} {
			if balance == nil {
				continue
			}
			if strings.ToUpper(balance.Currency) != account.Currency {
				return apiError(fiber.StatusBadRequest, "currency_mismatch", "A balance of account "+account.ExternalId+" is not in its currency "+account.Currency)
			}
			if balance.Date.IsZero() {
				return invalid("a balance needs its date")
			}
		}
		if account.IbanMasked != nil {
			masked := strings.TrimSpace(*account.IbanMasked)
			if utf8.RuneCountInString(masked) > 64 {
				return invalid("ibanMasked must be at most 64 characters")
			}
			account.IbanMasked = &masked
			if masked == "" {
				account.IbanMasked = nil
			}
		}
		accounts[account.ExternalId] = account.Currency
	}
	transactions := map[string]bool{}
	for index := range body.Transactions {
		item := &body.Transactions[index]
		item.ExternalId, item.AccountExternalId = strings.TrimSpace(item.ExternalId), strings.TrimSpace(item.AccountExternalId)
		if item.ExternalId == "" || len(item.ExternalId) > 200 || item.AccountExternalId == "" {
			return invalid("every transaction needs an externalId (at most 200 characters) and an accountExternalId")
		}
		if transactions[item.ExternalId] {
			return invalid("transaction externalId " + item.ExternalId + " appears twice")
		}
		transactions[item.ExternalId] = true
		if !transactionStatuses[string(item.Status)] {
			return invalid("transaction status must be booked or pending")
		}
		if item.BookingDate.IsZero() {
			return invalid("every transaction needs a bookingDate")
		}
		item.Amount.Currency = strings.ToUpper(strings.TrimSpace(item.Amount.Currency))
		if !financemoney.ValidCurrency(item.Amount.Currency) {
			return invalid("transaction " + item.ExternalId + " needs a three-letter currency")
		}
		for name, field := range map[string]struct {
			value **string
			limit int
		}{"counterparty": {&item.Counterparty, counterpartyLimit}, "description": {&item.Description, descriptionLimit}, "merchantCategoryCode": {&item.MerchantCategoryCode, 8}, "bankReference": {&item.BankReference, 200}} {
			cleaned, err := cleanText(name, *field.value, field.limit)
			if err != nil {
				return err
			}
			*field.value = cleaned
		}
	}
	return nil
}

// bankAccountName is a free name for a new bank account: the bank's, else
// with the institution, the masked IBAN, or the account's external id.
func bankAccountName(c fiber.Ctx, q *store.Queries, account generated.BankAccountInput, institution string) (string, error) {
	candidates := []string{account.Name, account.Name + " (" + institution + ")"}
	if account.IbanMasked != nil {
		candidates = append(candidates, account.Name+" ("+*account.IbanMasked+")")
	}
	candidates = append(candidates, account.Name+" ("+institution+" "+account.ExternalId+")")
	for _, name := range candidates {
		taken, err := q.AccountNameExists(c.Context(), name)
		if err != nil || !taken {
			return name, err
		}
	}
	return "", apiError(fiber.StatusConflict, "account_name_taken", "No free name for account "+account.ExternalId)
}

func balanceParams(balance *generated.BankBalance) (*int64, pgtype.Date) {
	if balance == nil {
		return nil, pgtype.Date{}
	}
	return pointer(balance.Amount), pgDate(balance.Date)
}

func (h *Handler) ImportBankSync(c fiber.Ctx) error {
	var body generated.BankImportRequest
	properties, err := decodeObject(c.Body(), &body)
	if err != nil {
		return invalid(err.Error())
	}
	for _, name := range []string{"connectionId", "window", "accounts", "transactions"} {
		if _, ok := properties[name]; !ok {
			return invalid(name + " is required")
		}
	}
	if err := validateBankImport(&body); err != nil {
		return err
	}
	ctx := c.Context()
	connection := pgUUID(body.ConnectionId)
	var counts bankCounts
	accountIDs := make([]generated.BankAccountId, 0, len(body.Accounts))
	err = h.inTx(ctx, func(q *store.Queries) error {
		if err := q.LockBankConnection(ctx, connection); err != nil {
			return err
		}
		known, err := q.ListConnectionAccounts(ctx, connection)
		if err != nil {
			return err
		}
		accounts := map[string]store.Account{}
		for _, account := range known {
			accounts[*account.ExternalID] = account
		}
		for _, input := range body.Accounts {
			bookedMinor, bookedDate := balanceParams(input.BookedBalance)
			availableMinor, availableDate := balanceParams(input.AvailableBalance)
			account, exists := accounts[input.ExternalId]
			if exists {
				if account.Currency != input.Currency {
					return apiError(fiber.StatusBadRequest, "currency_mismatch", "Account "+input.ExternalId+" is in "+account.Currency+", not "+input.Currency)
				}
				account, err = q.UpdateBankAccount(ctx, store.UpdateBankAccountParams{ID: account.ID, Provider: &body.Provider, Institution: &body.Institution,
					IbanMasked: input.IbanMasked, BookedBalanceMinor: bookedMinor, BookedBalanceDate: bookedDate, AvailableBalanceMinor: availableMinor, AvailableBalanceDate: availableDate})
				if err != nil {
					return err
				}
				counts.AccountsUpdated++
			} else {
				name, err := bankAccountName(c, q, input, body.Institution)
				if err != nil {
					return err
				}
				account, err = q.CreateBankAccount(ctx, store.CreateBankAccountParams{Name: name, Kind: string(input.Kind), Currency: input.Currency,
					ConnectionID: connection, ExternalID: input.ExternalId, Provider: &body.Provider, Institution: &body.Institution, IbanMasked: input.IbanMasked,
					BookedBalanceMinor: bookedMinor, BookedBalanceDate: bookedDate, AvailableBalanceMinor: availableMinor, AvailableBalanceDate: availableDate})
				if err != nil {
					return err
				}
				counts.AccountsCreated++
			}
			accounts[input.ExternalId] = account
			accountIDs = append(accountIDs, generated.BankAccountId{ExternalId: input.ExternalId, Id: uuid(account.ID)})
		}

		keep := make([]string, 0, len(body.Transactions))
		for _, item := range body.Transactions {
			keep = append(keep, item.ExternalId)
		}
		stored, err := q.ListBankTransactions(ctx, store.ListBankTransactionsParams{ConnectionID: connection, ExternalIds: keep})
		if err != nil {
			return err
		}
		existing := map[string]store.Transaction{}
		for _, item := range stored {
			existing[*item.ImportExternalID] = item
		}
		chooser, err := categorizer(ctx, q)
		if err != nil {
			return err
		}
		for _, item := range body.Transactions {
			account, ok := accounts[item.AccountExternalId]
			if !ok {
				return apiError(fiber.StatusBadRequest, "unknown_account", "Transaction "+item.ExternalId+" names account "+item.AccountExternalId+", which this connection does not have")
			}
			if item.Amount.Currency != account.Currency {
				return apiError(fiber.StatusBadRequest, "currency_mismatch", fmt.Sprintf("Transaction %s is in %s but its account is in %s", item.ExternalId, item.Amount.Currency, account.Currency))
			}
			if item.Amount.Amount == 0 {
				counts.Skipped++
				continue
			}
			if before, ok := existing[item.ExternalId]; ok {
				changed, err := h.updateBankTransaction(c, q, chooser, before, account, item)
				if err != nil {
					return err
				}
				if changed {
					counts.Updated++
				} else {
					counts.Unchanged++
				}
				continue
			}
			params := store.CreateImportedTransactionParams{AccountID: account.ID, AmountMinor: item.Amount.Amount, Currency: item.Amount.Currency,
				OccurredOn: pgDate(item.BookingDate), ValueDate: optionalDate(item.ValueDate), Status: string(item.Status),
				Counterparty: item.Counterparty, Description: item.Description, ImportSource: bankSource, ImportExternalID: item.ExternalId,
				ConnectionID: connection, MerchantCategoryCode: item.MerchantCategoryCode, BankReference: item.BankReference, SourceMetadata: []byte(`{}`)}
			var replacedID pgtype.UUID
			if item.Status == generated.Booked {
				replaced, err := q.FindReplacedPending(ctx, store.FindReplacedPendingParams{AccountID: account.ID, AmountMinor: item.Amount.Amount,
					Counterparty: item.Counterparty, BookingDate: params.OccurredOn, Keep: keep})
				switch {
				case err == nil:
					// The owner's notes and category chosen by hand move to
					// the booked transaction.
					params.Note = replaced.Note
					if replaced.CategorizedBy != nil && *replaced.CategorizedBy == "manual" {
						params.CategoryID, params.CategorizedBy = replaced.CategoryID, replaced.CategorizedBy
					}
					if err := q.DeleteTransaction(ctx, replaced.ID); err != nil {
						return err
					}
					replacedID = replaced.ID
					counts.RemovedPending++
				case !errors.Is(err, pgx.ErrNoRows):
					return err
				}
			}
			if params.CategorizedBy == nil {
				// The rules choose, else the card's merchant category code.
				if assignment, ok := chooser.Choose(textWithCode(item.Counterparty, item.Description, item.MerchantCategoryCode)); ok {
					params.CategoryID, params.CategoryRuleID, params.CategorizedBy = assignmentFields(assignment)
				}
			}
			created, err := q.CreateImportedTransaction(ctx, params)
			if err != nil {
				return err
			}
			if replacedID.Valid {
				// The booked transaction is the debit the pending one was:
				// a large-debit alert sent for it is not sent again.
				if err := q.CarryOverAlertClaim(ctx, store.CarryOverAlertClaimParams{FromKey: alerts.LargeDebitKey(replacedID.Bytes),
					ToKey: alerts.LargeDebitKey(created.ID.Bytes)}); err != nil {
					return err
				}
			}
			counts.Created++
		}
		removed, err := q.DeleteStalePending(ctx, store.DeleteStalePendingParams{ConnectionID: connection, WindowFrom: pgDate(body.Window.From), WindowTo: pgDate(body.Window.To), Keep: keep})
		counts.RemovedPending += int(removed)
		return err
	})
	if err != nil {
		return passThrough(err)
	}
	h.recordBankImport(c, body, counts)
	h.budgetChanged()
	result := generated.BankImportResult{AccountIds: accountIDs}
	result.Accounts.Created, result.Accounts.Updated = counts.AccountsCreated, counts.AccountsUpdated
	result.Transactions.Created, result.Transactions.Updated, result.Transactions.Unchanged = counts.Created, counts.Updated, counts.Unchanged
	result.Transactions.RemovedPending, result.Transactions.Skipped = counts.RemovedPending, counts.Skipped
	return c.JSON(result)
}

// updateBankTransaction applies a re-synced transaction's bank fields; the
// owner's category and notes stay, and a category not chosen by hand
// follows the rules (and the merchant category code) when the text or the
// code changed: an AI choice they do not replace is dropped, so the AI
// looks at the new text. It reports whether anything changed.
func (h *Handler) updateBankTransaction(c fiber.Ctx, q *store.Queries, chooser rules.Categorizer, before store.Transaction, account store.Account, item generated.BankTransactionInput) (bool, error) {
	next := before
	next.AccountID, next.AmountMinor, next.Currency = account.ID, item.Amount.Amount, item.Amount.Currency
	next.OccurredOn, next.ValueDate, next.Status = pgDate(item.BookingDate), optionalDate(item.ValueDate), string(item.Status)
	next.Counterparty, next.Description = item.Counterparty, item.Description
	next.MerchantCategoryCode, next.BankReference = item.MerchantCategoryCode, item.BankReference
	if (next.CategorizedBy == nil || *next.CategorizedBy != "manual") &&
		(!sameText(before.Counterparty, next.Counterparty) || !sameText(before.Description, next.Description) || !sameText(before.MerchantCategoryCode, next.MerchantCategoryCode)) {
		assignment, _ := chooser.Rechoose(assignmentOf(before), textWithCode(next.Counterparty, next.Description, next.MerchantCategoryCode))
		applyAssignment(&next, assignment)
	}
	if len(transactionChanges(before, next)) == 0 && sameText(before.MerchantCategoryCode, next.MerchantCategoryCode) && sameText(before.BankReference, next.BankReference) {
		return false, nil
	}
	_, err := q.UpdateBankTransaction(c.Context(), store.UpdateBankTransactionParams{ID: before.ID, AccountID: next.AccountID, AmountMinor: next.AmountMinor,
		Currency: next.Currency, OccurredOn: next.OccurredOn, ValueDate: next.ValueDate, Status: next.Status, Counterparty: next.Counterparty,
		Description: next.Description, MerchantCategoryCode: next.MerchantCategoryCode, BankReference: next.BankReference,
		CategoryID: next.CategoryID, CategorizedBy: next.CategorizedBy, CategoryRuleID: next.CategoryRuleID})
	return err == nil, err
}

func (h *Handler) RemoveBankConnection(c fiber.Ctx, connectionID types.UUID) error {
	archived, err := h.queries.ArchiveConnectionAccounts(c.Context(), pgUUID(connectionID))
	if err != nil {
		return internal(err)
	}
	if len(archived) > 0 {
		institution := "the bank"
		if archived[0].Institution != nil {
			institution = *archived[0].Institution
		}
		h.emit(c.Context(), c, activity.Event{ID: googleuuid.New(), Type: bankConnectionRemoved,
			Subject: activity.Subject{Type: "finance_bank_connection", ID: connectionID.String()},
			Summary: fmt.Sprintf("Bank connection to %s removed: %d %s archived", activity.Preview(institution, 80), len(archived), plural(len(archived), "account")),
			Data:    map[string]any{"connectionId": connectionID.String(), "institution": activity.Preview(institution, 200), "archivedAccounts": len(archived)}})
	}
	return c.SendStatus(fiber.StatusNoContent)
}

func (h *Handler) recordBankImport(c fiber.Ctx, body generated.BankImportRequest, counts bankCounts) {
	summary := fmt.Sprintf("%s sync: %d new, %d updated", activity.Preview(body.Institution, 80), counts.Created, counts.Updated)
	if counts.RemovedPending > 0 {
		summary += fmt.Sprintf(", %d pending removed", counts.RemovedPending)
	}
	accounts := counts.AccountsCreated + counts.AccountsUpdated
	summary += fmt.Sprintf(" (%d %s)", accounts, plural(accounts, "account"))
	h.emit(c.Context(), c, activity.Event{ID: googleuuid.New(), Type: bankImportCompleted,
		Subject: activity.Subject{Type: "finance_bank_connection", ID: googleuuid.UUID(body.ConnectionId).String()}, Summary: summary,
		Data: map[string]any{"connectionId": googleuuid.UUID(body.ConnectionId).String(), "provider": activity.Preview(body.Provider, 64), "institution": activity.Preview(body.Institution, 200),
			"windowFrom": body.Window.From.String(), "windowTo": body.Window.To.String(),
			"accountsCreated": counts.AccountsCreated, "accountsUpdated": counts.AccountsUpdated, "created": counts.Created, "updated": counts.Updated,
			"unchanged": counts.Unchanged, "removedPending": counts.RemovedPending, "skipped": counts.Skipped}})
}
