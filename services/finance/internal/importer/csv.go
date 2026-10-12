// Package importer reads bank-agnostic CSV files into Finance transactions.
//
// Parse turns a file into rows, reporting each unreadable row; Import stores
// the rows of one file in one account (or, for older-format files, in each
// row's account), skipping rows the account already has and letting the
// category rules choose a category for rows that come without one.
package importer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/GAMch1k/life-os/services/finance/internal/money"
	store "github.com/GAMch1k/life-os/services/finance/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/finance/internal/rules"
)

// Source is the transactions' source kind.
const Source = "csv"

// Error is one row that was not imported. Row is the file's line number of
// the record (the header is row 1).
type Error struct {
	Row     int
	Message string
}

// Row is one readable record.
type Row struct {
	Line         int
	AccountID    *uuid.UUID // the older format's account_id column
	Date         time.Time
	ValueDate    *time.Time
	Amount       money.Decimal // amount: in major units
	AmountMinor  *int64        // amount_minor: already in minor units
	Currency     string        // "" means the account's
	Counterparty string
	Description  string
	Notes        string
	CategoryName string
	CategoryID   *uuid.UUID
	ExternalID   string // given, or derived from the content
}

// Parsed is a read file.
type Parsed struct {
	Rows             []Row
	Errors           []Error
	HasAccountColumn bool
}

// HeaderError means the file cannot be imported at all.
type HeaderError struct{ Message string }

func (e HeaderError) Error() string { return e.Message }

// columns maps accepted header names to the column they mean.
var columns = map[string]string{
	"account_id": "account_id",
	"date":       "date", "booking_date": "date", "occurred_on": "date",
	"value_date":   "value_date",
	"amount":       "amount",
	"amount_minor": "amount_minor",
	"currency":     "currency",
	"counterparty": "counterparty",
	"description":  "description",
	"notes":        "notes", "note": "notes",
	"category":    "category",
	"category_id": "category_id",
	"external_id": "external_id",
}

func headerName(raw string) string {
	name := strings.ToLower(strings.TrimSpace(raw))
	return strings.NewReplacer(" ", "_", "-", "_").Replace(name)
}

// Parse reads a UTF-8 CSV file (a byte-order mark is ignored), separated by
// commas or semicolons as its header line shows.
func Parse(input io.Reader) (Parsed, error) {
	raw, err := io.ReadAll(input)
	if err != nil {
		return Parsed{}, HeaderError{"read CSV: " + err.Error()}
	}
	raw = bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))
	reader := csv.NewReader(bytes.NewReader(raw))
	firstLine, _, _ := bytes.Cut(raw, []byte("\n"))
	if bytes.Count(firstLine, []byte(";")) > bytes.Count(firstLine, []byte(",")) {
		reader.Comma = ';'
	}
	reader.FieldsPerRecord = -1
	headers, err := reader.Read()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return Parsed{}, HeaderError{"the file is empty"}
		}
		return Parsed{}, HeaderError{"read CSV header: " + err.Error()}
	}
	indexes := map[string]int{}
	for index, header := range headers {
		if column, ok := columns[headerName(header)]; ok {
			if _, seen := indexes[column]; !seen {
				indexes[column] = index
			}
		}
	}
	if _, ok := indexes["date"]; !ok {
		return Parsed{}, HeaderError{"missing the date column"}
	}
	_, hasAmount := indexes["amount"]
	_, hasMinor := indexes["amount_minor"]
	if !hasAmount && !hasMinor {
		return Parsed{}, HeaderError{"missing the amount (or amount_minor) column"}
	}
	_, hasAccount := indexes["account_id"]
	parsed := Parsed{HasAccountColumn: hasAccount}
	seen := map[string]int{}
	for {
		record, readErr := reader.Read()
		if readErr == io.EOF {
			return parsed, nil
		}
		if readErr != nil {
			line := 0
			var parseErr *csv.ParseError
			if errors.As(readErr, &parseErr) {
				line = parseErr.StartLine
			}
			parsed.Errors = append(parsed.Errors, Error{Row: max(line, 1), Message: "unreadable CSV row: " + readErr.Error()})
			continue
		}
		line, _ := reader.FieldPos(0)
		if blank(record) {
			continue
		}
		value := func(column string) string {
			index, ok := indexes[column]
			if !ok || index >= len(record) {
				return ""
			}
			return strings.TrimSpace(record[index])
		}
		row, message := parseRow(line, value)
		if message != "" {
			parsed.Errors = append(parsed.Errors, Error{Row: line, Message: message})
			continue
		}
		if row.ExternalID == "" {
			row.ExternalID = contentID(row, seen)
		}
		parsed.Rows = append(parsed.Rows, row)
	}
}

func blank(record []string) bool {
	for _, field := range record {
		if strings.TrimSpace(field) != "" {
			return false
		}
	}
	return true
}

func parseRow(line int, value func(string) string) (Row, string) {
	row := Row{Line: line, Counterparty: value("counterparty"), Description: value("description"), Notes: value("notes"),
		CategoryName: value("category"), ExternalID: value("external_id"), Currency: strings.ToUpper(value("currency"))}
	if raw := value("account_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			return Row{}, "account_id must be a UUID"
		}
		row.AccountID = &id
	}
	date, ok := parseDate(value("date"))
	if !ok {
		return Row{}, "date must be YYYY-MM-DD or DD.MM.YYYY"
	}
	row.Date = date
	if raw := value("value_date"); raw != "" {
		valueDate, ok := parseDate(raw)
		if !ok {
			return Row{}, "value_date must be YYYY-MM-DD or DD.MM.YYYY"
		}
		row.ValueDate = &valueDate
	}
	if raw := value("amount_minor"); raw != "" {
		minor, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return Row{}, "amount_minor must be a whole number of minor units, such as -1250"
		}
		if minor == 0 {
			return Row{}, "the amount must not be zero"
		}
		row.AmountMinor = &minor
	} else {
		raw := value("amount")
		if raw == "" {
			return Row{}, "the amount is missing"
		}
		decimal, err := money.ParseDecimal(raw)
		if err != nil {
			return Row{}, "amount must be a signed decimal such as -12.50 or -12,50"
		}
		if decimal.Mantissa == 0 {
			return Row{}, "the amount must not be zero"
		}
		row.Amount = decimal
	}
	if row.Currency != "" && !money.ValidCurrency(row.Currency) {
		return Row{}, "currency must be a three-letter code such as EUR"
	}
	if raw := value("category_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			return Row{}, "category_id must be a UUID"
		}
		row.CategoryID = &id
	}
	if len(row.ExternalID) > 200 {
		return Row{}, "external_id must be at most 200 characters"
	}
	if utf8.RuneCountInString(row.Counterparty) > 200 || utf8.RuneCountInString(row.Description) > 1000 || utf8.RuneCountInString(row.Notes) > 2000 {
		return Row{}, "counterparty, description, or notes is too long (200, 1000, 2000 characters at most)"
	}
	return row, ""
}

func parseDate(raw string) (time.Time, bool) {
	for _, layout := range []string{"2006-01-02", "02.01.2006", "2.1.2006"} {
		if date, err := time.Parse(layout, raw); err == nil {
			return date, true
		}
	}
	return time.Time{}, false
}

// contentID identifies a row without an external_id by its content and its
// position among identical rows of the file, so two equal coffees on one day
// are two transactions and re-importing the file finds both.
func contentID(row Row, seen map[string]int) string {
	amount := row.Amount.String()
	if row.AmountMinor != nil {
		amount = "minor:" + strconv.FormatInt(*row.AmountMinor, 10)
	}
	key := strings.Join([]string{row.Date.Format(time.DateOnly), amount, row.Currency, rules.Normalize(row.Counterparty), rules.Normalize(row.Description)}, "\x1f")
	seen[key]++
	sum := sha256.Sum256([]byte(key + "\x1f" + strconv.Itoa(seen[key])))
	return "content:" + hex.EncodeToString(sum[:12])
}

// Store is what Import needs from the repository (a database transaction's
// queries).
type Store interface {
	GetAccount(context.Context, pgtype.UUID) (store.Account, error)
	CreateImportedTransaction(context.Context, store.CreateImportedTransactionParams) (store.Transaction, error)
}

// Request-level refusals.
var (
	ErrAccountRequired = errors.New("account required")
	ErrUnknownAccount  = errors.New("unknown account")
	ErrAccountArchived = errors.New("account archived")
	ErrAccountManaged  = errors.New("bank account")
)

// Input is what an import needs besides the file.
type Input struct {
	AccountID  *uuid.UUID
	Categories []store.FinancialCategory
	Rules      []rules.Rule
}

// Result is an import's outcome.
type Result struct {
	Account                                   *store.Account // the chosen account, if any
	Imported, Duplicates, Failed, Categorized int
	Errors                                    []Error
	// Created are the imported transactions, for the payday.
	Created []store.Transaction
}

// Import stores the parsed rows. It returns an error for a request it cannot
// serve at all (ErrAccountRequired, ErrUnknownAccount, ErrAccountArchived,
// ErrAccountManaged) or a failing database; row problems are counted and reported instead.
func Import(ctx context.Context, queries Store, parsed Parsed, input Input) (Result, error) {
	result := Result{Failed: len(parsed.Errors), Errors: append([]Error(nil), parsed.Errors...)}
	accounts := map[uuid.UUID]*store.Account{}
	lookup := func(id uuid.UUID) (*store.Account, error) {
		if account, ok := accounts[id]; ok {
			return account, nil
		}
		account, err := queries.GetAccount(ctx, pgtype.UUID{Bytes: id, Valid: true})
		if errors.Is(err, pgx.ErrNoRows) {
			accounts[id] = nil
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		accounts[id] = &account
		return &account, nil
	}
	if input.AccountID != nil {
		account, err := lookup(*input.AccountID)
		if err != nil {
			return Result{}, err
		}
		if account == nil {
			return Result{}, ErrUnknownAccount
		}
		if account.ArchivedAt.Valid {
			return Result{}, ErrAccountArchived
		}
		if account.SourceKind == "bank" {
			return Result{}, ErrAccountManaged
		}
		result.Account = account
	} else if !parsed.HasAccountColumn {
		return Result{}, ErrAccountRequired
	}
	byName := map[string]store.FinancialCategory{}
	byID := map[uuid.UUID]store.FinancialCategory{}
	for _, category := range input.Categories {
		byID[category.ID.Bytes] = category
		name := rules.Normalize(category.Name)
		if _, taken := byName[name]; !taken && !category.ArchivedAt.Valid {
			byName[name] = category
		}
	}
	fail := func(row Row, message string) {
		result.Failed++
		result.Errors = append(result.Errors, Error{Row: row.Line, Message: message})
	}
	for _, row := range parsed.Rows {
		account := result.Account
		switch {
		case account != nil && row.AccountID != nil && *row.AccountID != uuid.UUID(account.ID.Bytes):
			fail(row, "account_id is not the account chosen for this import")
			continue
		case account == nil && row.AccountID == nil:
			fail(row, "account_id is missing")
			continue
		case account == nil:
			found, err := lookup(*row.AccountID)
			if err != nil {
				return Result{}, err
			}
			if found == nil {
				fail(row, "account_id is not a Finance account")
				continue
			}
			if found.ArchivedAt.Valid {
				fail(row, "the account is archived")
				continue
			}
			if found.SourceKind == "bank" {
				fail(row, "the account's transactions come from its bank")
				continue
			}
			account = found
		}
		currency := row.Currency
		if currency == "" {
			currency = account.Currency
		}
		if currency != account.Currency {
			fail(row, fmt.Sprintf("currency %s is not the account's currency (%s)", currency, account.Currency))
			continue
		}
		var amount int64
		if row.AmountMinor != nil {
			amount = *row.AmountMinor
		} else {
			minor, err := row.Amount.Minor(money.Exponent(currency))
			if err != nil {
				fail(row, "the amount "+err.Error()+" for "+currency)
				continue
			}
			amount = minor
		}
		params := store.CreateImportedTransactionParams{
			AccountID: account.ID, AmountMinor: amount, Currency: currency,
			OccurredOn: pgtype.Date{Time: row.Date, Valid: true}, Status: "booked",
			Counterparty: optional(row.Counterparty), Description: optional(row.Description), Note: optional(row.Notes),
			ImportSource: Source, ImportExternalID: row.ExternalID, SourceMetadata: metadata(row),
		}
		if row.ValueDate != nil {
			params.ValueDate = pgtype.Date{Time: *row.ValueDate, Valid: true}
		}
		chosen := false
		if row.CategoryID != nil {
			category, ok := byID[*row.CategoryID]
			if !ok {
				fail(row, "category_id is not a Finance category")
				continue
			}
			if !category.ArchivedAt.Valid {
				params.CategoryID, params.CategorizedBy, chosen = category.ID, pointer("manual"), true
			}
		} else if row.CategoryName != "" {
			if category, ok := byName[rules.Normalize(row.CategoryName)]; ok {
				params.CategoryID, params.CategorizedBy, chosen = category.ID, pointer("manual"), true
			}
		}
		byRule := false
		if !chosen {
			if rule, ok := rules.First(input.Rules, rules.Text{Counterparty: row.Counterparty, Description: row.Description}); ok {
				params.CategoryID = pgtype.UUID{Bytes: rule.CategoryID, Valid: true}
				params.CategoryRuleID = pgtype.UUID{Bytes: rule.ID, Valid: true}
				params.CategorizedBy, byRule = pointer("rule"), true
			}
		}
		created, err := queries.CreateImportedTransaction(ctx, params)
		if errors.Is(err, pgx.ErrNoRows) {
			result.Duplicates++
			continue
		}
		if err != nil {
			return Result{}, err
		}
		result.Imported++
		result.Created = append(result.Created, created)
		if byRule {
			result.Categorized++
		}
	}
	sort.SliceStable(result.Errors, func(i, j int) bool { return result.Errors[i].Row < result.Errors[j].Row })
	return result, nil
}

func metadata(row Row) []byte {
	raw, _ := json.Marshal(map[string]any{"format": "life-os-csv-v2", "row": row.Line})
	return raw
}

func optional(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func pointer(value string) *string { return &value }
