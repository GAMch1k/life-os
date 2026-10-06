package importer

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	store "github.com/GAMch1k/life-os/services/finance/internal/repository/generated"
)

type fakeTransactions struct {
	calls []store.CreateTransactionParams
	err   error
}

func (f *fakeTransactions) CreateTransaction(_ context.Context, params store.CreateTransactionParams) (store.Transaction, error) {
	f.calls = append(f.calls, params)
	return store.Transaction{}, f.err
}

func TestImportCreatesNormalizedRowsAndSkipsDuplicates(t *testing.T) {
	transactions := &fakeTransactions{}
	csv := "account_id,occurred_on,amount_minor,currency,external_id,note\n11111111-1111-1111-1111-111111111111,2026-10-05,-500,usd,row-1,coffee\n"
	result := Import(context.Background(), transactions, strings.NewReader(csv))
	if result.Created != 1 || result.Failed != 0 || len(transactions.calls) != 1 {
		t.Fatalf("result = %#v, calls = %d", result, len(transactions.calls))
	}
	if transactions.calls[0].Currency != "USD" || transactions.calls[0].AmountMinor != -500 || transactions.calls[0].ImportSource == nil || *transactions.calls[0].ImportSource != Source {
		t.Fatalf("normalized transaction = %#v", transactions.calls[0])
	}
	transactions.err = errors.New("duplicate key value violates unique constraint transactions_import_identity_key")
	result = Import(context.Background(), transactions, strings.NewReader(csv))
	if result.Skipped != 1 || result.Failed != 0 {
		t.Fatalf("duplicate result = %#v", result)
	}
}

func TestImportReportsActionableInvalidRows(t *testing.T) {
	transactions := &fakeTransactions{}
	result := Import(context.Background(), transactions, strings.NewReader("account_id,occurred_on,amount_minor,currency,external_id\nnot-a-uuid,bad,0,US,\n"))
	if result.Failed != 1 || len(result.Errors) != 1 || result.Errors[0].Row != 2 || !strings.Contains(result.Errors[0].Message, "account_id") {
		t.Fatalf("result = %#v", result)
	}
	if len(transactions.calls) != 0 {
		t.Fatal("invalid row reached persistence")
	}
}

var _ = pgtype.UUID{}
