package banking

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/GAMch1k/life-os/services/banking/internal/enablebanking"
	"github.com/GAMch1k/life-os/services/banking/internal/finance"
)

func TestParseMinorUsesTheCurrencysMinorUnitsWithoutFloats(t *testing.T) {
	for _, tc := range []struct {
		amount, currency string
		want             int64
	}{
		{"12.34", "EUR", 1234}, {"0.1", "EUR", 10}, {"5", "EUR", 500}, {"5.", "EUR", 500}, {".5", "EUR", 50},
		{"-3.20", "EUR", -320}, {"+7.00", "EUR", 700}, {"1234.500", "EUR", 123450}, {"0.07", "EUR", 7},
		{"1500", "JPY", 1500}, {"1.234", "KWD", 1234}, {"92233720368547758.07", "EUR", 9223372036854775807},
		// 0.1 + 0.2 in floating point is not 0.3.
		{"0.30", "EUR", 30},
	} {
		got, err := ParseMinor(tc.amount, tc.currency)
		if err != nil || got != tc.want {
			t.Errorf("ParseMinor(%q, %s) = %d, %v; want %d", tc.amount, tc.currency, got, err, tc.want)
		}
	}
	for _, amount := range []string{"", ".", "1,50", "1.234", "abc", "1e3", "--1", "92233720368547758.08", "1.2.3"} {
		if _, err := ParseMinor(amount, "EUR"); !errors.Is(err, ErrAmount) {
			t.Errorf("ParseMinor(%q) accepted: %v", amount, err)
		}
	}
}

func TestSignedMinorTakesTheSignFromTheIndicator(t *testing.T) {
	for _, tc := range []struct {
		amount, indicator string
		want              int64
	}{
		{"25.00", "DBIT", -2500}, {"25.00", "CRDT", 2500}, {"-25.00", "DBIT", -2500}, {"-25.00", "CRDT", 2500},
		{"-25.00", "", -2500}, {"25.00", "", 2500},
	} {
		got, err := SignedMinor(enablebanking.Amount{Amount: tc.amount, Currency: "EUR"}, tc.indicator)
		if err != nil || got != tc.want {
			t.Errorf("SignedMinor(%s %s) = %d, %v; want %d", tc.amount, tc.indicator, got, err, tc.want)
		}
	}
}

func TestAccountsAreKeyedByAnIBANHashAndShownMasked(t *testing.T) {
	iban := "DE89 3704 0044 0532 0130 00"
	account := enablebanking.Account{UID: "uid-1", AccountID: &enablebanking.AccountIdentification{IBAN: iban}, Name: "Max Mustermann",
		Product: "Das Junge Konto", Currency: "eur", CashAccountType: "CACC", IdentificationHash: "h1"}
	again := account
	again.UID, again.AccountID = "uid-2", &enablebanking.AccountIdentification{IBAN: "DE89370400440532013000"}
	if AccountKey(account) != AccountKey(again) || !strings.HasPrefix(AccountKey(account), "iban-") {
		t.Fatalf("keys differ across sessions: %s %s", AccountKey(account), AccountKey(again))
	}
	if strings.Contains(AccountKey(account), "0532013000") {
		t.Fatal("the key contains the IBAN")
	}
	stored := storedAccounts("Deutsche Bank", []enablebanking.Account{account, {Currency: "EUR"}})
	if len(stored) != 1 {
		t.Fatalf("an account without uid is dropped: %+v", stored)
	}
	want := StoredAccount{UID: "uid-1", Key: AccountKey(account), Name: "Das Junge Konto", Product: "Das Junge Konto", Currency: "EUR", CashAccountType: "CACC", IBANMasked: "DE89…3000"}
	if stored[0] != want {
		t.Fatalf("stored = %+v, want %+v", stored[0], want)
	}
	raw, _ := json.Marshal(stored)
	if strings.Contains(string(raw), "0532013000") || strings.Contains(string(raw), "Mustermann") {
		t.Fatalf("stored accounts keep the IBAN or the holder's name: %s", raw)
	}
	noIBAN := enablebanking.Account{UID: "u", IdentificationHash: "abc", CashAccountType: "CARD", Currency: "EUR"}
	if key := AccountKey(noIBAN); !strings.HasPrefix(key, "eb-") {
		t.Errorf("without an IBAN the provider's hash keys it: %s", key)
	}
	if name := accountName("Deutsche Bank", noIBAN); name != "Deutsche Bank card" {
		t.Errorf("name = %q", name)
	}
	if kind := (StoredAccount{CashAccountType: "SVGS"}).Kind(); kind != "savings" {
		t.Errorf("SVGS kind = %s", kind)
	}
	if MaskIBAN("") != "" || MaskIBAN("DE12") != "" {
		t.Error("short or missing IBANs mask to nothing")
	}
}

func transaction(fields map[string]any) enablebanking.Transaction {
	base := map[string]any{
		"transaction_amount": map[string]any{"amount": "10.00", "currency": "EUR"}, "credit_debit_indicator": "DBIT",
		"status": "BOOK", "booking_date": "2026-10-01",
	}
	for key, value := range fields {
		base[key] = value
	}
	raw, _ := json.Marshal(base)
	var parsed enablebanking.Transaction
	if err := json.Unmarshal(raw, &parsed); err != nil {
		panic(err)
	}
	return parsed
}

func TestBuildImportMapsTransactionsForFinance(t *testing.T) {
	account := StoredAccount{UID: "uid-1", Key: "iban-k1", Name: "Das Junge Konto", Currency: "EUR", CashAccountType: "CACC", IBANMasked: "DE89…3000"}
	data := []accountData{{
		account: account,
		balances: []enablebanking.Balance{
			{Type: "XPCD", Amount: enablebanking.Amount{Amount: "90.00", Currency: "EUR"}},
			{Type: "ITAV", Amount: enablebanking.Amount{Amount: "100.50", Currency: "EUR"}, LastChangeDateTime: "2026-10-09T10:00:00Z"},
			{Type: "CLBD", Amount: enablebanking.Amount{Amount: "-20.00", Currency: "EUR"}, ReferenceDate: "2026-10-08"},
		},
		transactions: []enablebanking.Transaction{
			transaction(map[string]any{"entry_reference": "E1", "transaction_id": "T1", "creditor": map[string]any{"name": "REWE  Markt"},
				"debtor": map[string]any{"name": "Owner"}, "remittance_information": []string{"Einkauf", " Karte 1234 "},
				"value_date": "2026-10-02", "merchant_category_code": "5411", "reference_number": "R-1"}),
			transaction(map[string]any{"entry_reference": "E2", "credit_debit_indicator": "CRDT", "transaction_amount": map[string]any{"amount": "2500.00", "currency": "EUR"},
				"debtor": map[string]any{"name": "Employer GmbH"}, "creditor": map[string]any{"name": "Owner"}, "remittance_information": "Gehalt", "booking_date": "2026-10-05"}),
			transaction(map[string]any{"transaction_id": "T3", "status": "PDNG", "booking_date": nil, "transaction_date": "2026-10-09"}),
			transaction(map[string]any{"entry_reference": "E4", "status": "CNCL"}),
			transaction(map[string]any{"entry_reference": "E5", "transaction_amount": map[string]any{"amount": "5.00", "currency": "USD"}}),
			transaction(map[string]any{"entry_reference": "E6", "transaction_amount": map[string]any{"amount": "5,00", "currency": "EUR"}}),
			transaction(map[string]any{"status": "PDNG", "booking_date": nil, "transaction_amount": map[string]any{"amount": "1.99", "currency": "EUR"}}),
		},
	}}
	window := finance.Window{From: "2026-07-12", To: "2026-10-10"}
	read := buildImport("c0ffee00-0000-4000-8000-000000000001", "Deutsche Bank", window, data)
	got := read.Import
	if got.ConnectionID != "c0ffee00-0000-4000-8000-000000000001" || got.Provider != "enablebanking" || got.Institution != "Deutsche Bank" || got.Window != window {
		t.Fatalf("import header = %+v", got)
	}
	wantAccount := finance.Account{ExternalID: "iban-k1", Name: "Das Junge Konto", Kind: "bank", Currency: "EUR", IBANMasked: "DE89…3000",
		BookedBalance:    &finance.Balance{Amount: -2000, Currency: "EUR", Date: "2026-10-08"},
		AvailableBalance: &finance.Balance{Amount: 10050, Currency: "EUR", Date: "2026-10-09"}}
	if len(got.Accounts) != 1 || !equalJSON(got.Accounts[0], wantAccount) {
		t.Fatalf("account = %+v", got.Accounts)
	}
	if read.Fetched != 7 || read.Ignored != 3 || read.Cursor != "2026-10-05" {
		t.Fatalf("fetched %d ignored %d cursor %s", read.Fetched, read.Ignored, read.Cursor)
	}
	want := []finance.Transaction{
		{AccountExternalID: "iban-k1", ExternalID: "E1", Status: "booked", BookingDate: "2026-10-01", ValueDate: "2026-10-02",
			Amount: finance.Money{Amount: -1000, Currency: "EUR"}, Counterparty: "REWE Markt", Description: "Einkauf Karte 1234",
			MerchantCategoryCode: "5411", BankReference: "R-1"},
		{AccountExternalID: "iban-k1", ExternalID: "E2", Status: "booked", BookingDate: "2026-10-05",
			Amount: finance.Money{Amount: 250000, Currency: "EUR"}, Counterparty: "Employer GmbH", Description: "Gehalt"},
		{AccountExternalID: "iban-k1", ExternalID: "T3", Status: "pending", BookingDate: "2026-10-09", Amount: finance.Money{Amount: -1000, Currency: "EUR"}},
	}
	if len(got.Transactions) != 4 || !equalJSON(got.Transactions[:3], want) {
		t.Fatalf("transactions = %+v", got.Transactions)
	}
	last := got.Transactions[3]
	if !strings.HasPrefix(last.ExternalID, "h-") || last.BookingDate != "2026-10-10" || last.Status != "pending" {
		t.Fatalf("a transaction without ids gets a content id and today's date: %+v", last)
	}
	// The same content gives the same id in the next sync.
	if again := buildImport("c", "Deutsche Bank", window, data); again.Import.Transactions[3].ExternalID != last.ExternalID {
		t.Fatal("content ids are not stable")
	}
}

func TestExternalIDsAreUniqueEvenWhenTheBankRepeatsAReference(t *testing.T) {
	a := StoredAccount{UID: "a", Key: "iban-a", Currency: "EUR"}
	b := StoredAccount{UID: "b", Key: "iban-b", Currency: "EUR"}
	coffee := transaction(map[string]any{"entry_reference": "SAME", "transaction_amount": map[string]any{"amount": "3.00", "currency": "EUR"}})
	data := []accountData{
		{account: a, transactions: []enablebanking.Transaction{coffee, coffee, transaction(map[string]any{"entry_reference": "SAME", "transaction_amount": map[string]any{"amount": "4.00", "currency": "EUR"}})}},
		{account: b, transactions: []enablebanking.Transaction{coffee, transaction(map[string]any{"entry_reference": strings.Repeat("x", 300)})}},
	}
	read := buildImport("c", "Deutsche Bank", finance.Window{From: "2026-10-01", To: "2026-10-10"}, data)
	seen := map[string]bool{}
	for _, item := range read.Import.Transactions {
		if seen[item.ExternalID] || len(item.ExternalID) > 200 || item.ExternalID == "" {
			t.Fatalf("external id %q is repeated, empty, or too long: %+v", item.ExternalID, read.Import.Transactions)
		}
		seen[item.ExternalID] = true
	}
	if !strings.HasPrefix(read.Import.Transactions[0].ExternalID, "SAME~") || !strings.HasSuffix(read.Import.Transactions[1].ExternalID, "~2") {
		t.Fatalf("ids = %+v", read.Import.Transactions)
	}
}

func TestBalancesFallBackAndSkipOtherCurrencies(t *testing.T) {
	booked, available := pickBalances([]enablebanking.Balance{
		{Type: "CLBD", Amount: enablebanking.Amount{Amount: "1.00", Currency: "USD"}},
		{Type: "ITBD", Amount: enablebanking.Amount{Amount: "2.00", Currency: "EUR"}},
		{Type: "XPCD", Amount: enablebanking.Amount{Amount: "3.00", Currency: "EUR"}, ReferenceDate: "2026-10-09"},
	}, "EUR", "2026-10-10")
	if booked == nil || *booked != (finance.Balance{Amount: 200, Currency: "EUR", Date: "2026-10-10"}) {
		t.Errorf("booked = %+v", booked)
	}
	if available == nil || *available != (finance.Balance{Amount: 300, Currency: "EUR", Date: "2026-10-09"}) {
		t.Errorf("available = %+v", available)
	}
	if booked, available := pickBalances(nil, "EUR", "2026-10-10"); booked != nil || available != nil {
		t.Error("no balances, none reported")
	}
}

func equalJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
