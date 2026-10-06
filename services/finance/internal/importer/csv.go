// Package importer normalizes bank-agnostic CSV rows into finance transactions.
package importer

import (
	"context"
	"encoding/csv"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	store "github.com/GAMch1k/life-os/services/finance/internal/repository/generated"
)

const Source = "csv"

type Error struct {
	Row     int
	Message string
}
type Result struct {
	Created, Skipped, Failed int
	Errors                   []Error
}
type transactionCreator interface {
	CreateTransaction(context.Context, store.CreateTransactionParams) (store.Transaction, error)
}

// Import accepts a UTF-8 CSV with this header order-independent:
// account_id,occurred_on,amount_minor,currency,external_id,category_id,note.
func Import(ctx context.Context, queries transactionCreator, input io.Reader) Result {
	reader := csv.NewReader(input)
	headers, err := reader.Read()
	if err != nil {
		return Result{Failed: 1, Errors: []Error{{Row: 1, Message: "read CSV header: " + err.Error()}}}
	}
	indexes := map[string]int{}
	for index, header := range headers {
		indexes[strings.TrimSpace(header)] = index
	}
	for _, name := range []string{"account_id", "occurred_on", "amount_minor", "currency", "external_id"} {
		if _, ok := indexes[name]; !ok {
			return Result{Failed: 1, Errors: []Error{{Row: 1, Message: "missing required column " + name}}}
		}
	}
	result := Result{}
	for rowNumber := 2; ; rowNumber++ {
		record, readErr := reader.Read()
		if readErr == io.EOF {
			return result
		}
		if readErr != nil {
			result.Failed++
			result.Errors = append(result.Errors, Error{Row: rowNumber, Message: "read CSV row: " + readErr.Error()})
			continue
		}
		value := func(name string) string {
			index, ok := indexes[name]
			if !ok || index >= len(record) {
				return ""
			}
			return strings.TrimSpace(record[index])
		}
		accountID, err := parseUUID(value("account_id"))
		if err != nil {
			result.Failed++
			result.Errors = append(result.Errors, Error{Row: rowNumber, Message: "account_id must be a UUID"})
			continue
		}
		date, err := time.Parse("2006-01-02", value("occurred_on"))
		if err != nil {
			result.Failed++
			result.Errors = append(result.Errors, Error{Row: rowNumber, Message: "occurred_on must use YYYY-MM-DD"})
			continue
		}
		amount, err := strconv.ParseInt(value("amount_minor"), 10, 64)
		if err != nil || amount == 0 {
			result.Failed++
			result.Errors = append(result.Errors, Error{Row: rowNumber, Message: "amount_minor must be a non-zero integer"})
			continue
		}
		currency := strings.ToUpper(value("currency"))
		if len(currency) != 3 {
			result.Failed++
			result.Errors = append(result.Errors, Error{Row: rowNumber, Message: "currency must be a three-letter code"})
			continue
		}
		externalID := value("external_id")
		if externalID == "" {
			result.Failed++
			result.Errors = append(result.Errors, Error{Row: rowNumber, Message: "external_id is required for duplicate protection"})
			continue
		}
		var categoryID pgtype.UUID
		if rawCategory := value("category_id"); rawCategory != "" {
			categoryID, err = parseUUID(rawCategory)
			if err != nil {
				result.Failed++
				result.Errors = append(result.Errors, Error{Row: rowNumber, Message: "category_id must be a UUID"})
				continue
			}
		}
		note := value("note")
		if note == "" {
			note = ""
		}
		_, err = queries.CreateTransaction(ctx, store.CreateTransactionParams{AccountID: accountID, CategoryID: categoryID, AmountMinor: amount, Currency: currency, OccurredOn: pgtype.Date{Time: date, Valid: true}, Note: &note, ImportSource: pointer(Source), ImportExternalID: &externalID, SourceMetadata: []byte(`{"format":"life-os-csv-v1"}`)})
		if err != nil {
			if isDuplicate(err) {
				result.Skipped++
				continue
			}
			result.Failed++
			result.Errors = append(result.Errors, Error{Row: rowNumber, Message: "create transaction: " + err.Error()})
			continue
		}
		result.Created++
	}
}

func parseUUID(raw string) (pgtype.UUID, error) {
	value, err := uuid.Parse(raw)
	if err != nil {
		return pgtype.UUID{}, err
	}
	return pgtype.UUID{Bytes: [16]byte(value), Valid: true}, nil
}
func pointer(value string) *string { return &value }
func isDuplicate(err error) bool {
	var pgErr *pgconn.PgError
	return strings.Contains(err.Error(), "transactions_import_identity_key") || (errors.As(err, &pgErr) && pgErr.Code == "23505")
}
