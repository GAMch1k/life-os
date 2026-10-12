package banking

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/GAMch1k/life-os/services/banking/internal/enablebanking"
	"github.com/GAMch1k/life-os/services/banking/internal/finance"
)

// ProviderEnableBanking is the only provider.
const ProviderEnableBanking = "enablebanking"

// StoredAccount is one consented account as Banking keeps it
// (bank_connections.accounts). It never holds the IBAN.
type StoredAccount struct {
	// UID is the provider's id for the account in the current session.
	UID string `json:"uid"`
	// Key is the account's stable id, the externalId Finance knows it by:
	// the same across consents, so a reconnect keeps the Finance account.
	Key             string `json:"key"`
	Name            string `json:"name"`
	Product         string `json:"product,omitempty"`
	Currency        string `json:"currency"`
	CashAccountType string `json:"cashAccountType,omitempty"`
	IBANMasked      string `json:"ibanMasked,omitempty"`
}

// Kind is the account's Finance kind: bank, card, or savings.
func (a StoredAccount) Kind() string {
	switch strings.ToUpper(a.CashAccountType) {
	case "CARD":
		return "card"
	case "SVGS", "LLSV", "ONDP", "MOMA":
		return "savings"
	}
	return "bank"
}

// storedAccounts keeps what Banking needs of the consented accounts.
func storedAccounts(institution string, accounts []enablebanking.Account) []StoredAccount {
	stored := make([]StoredAccount, 0, len(accounts))
	for _, account := range accounts {
		if account.UID == "" {
			continue
		}
		stored = append(stored, StoredAccount{
			UID: account.UID, Key: AccountKey(account), Name: accountName(institution, account),
			Product: clip(account.Product, 100), Currency: strings.ToUpper(strings.TrimSpace(account.Currency)),
			CashAccountType: strings.ToUpper(strings.TrimSpace(account.CashAccountType)), IBANMasked: MaskIBAN(account.IBAN()),
		})
	}
	return stored
}

// AccountKey is an account's stable id: a hash of its IBAN (never the IBAN
// itself), else the provider's identification hash (stable across
// sessions), else the hash of another account number. Only an account with
// none of these falls back to its per-session uid.
func AccountKey(account enablebanking.Account) string {
	if iban := normalizeIBAN(account.IBAN()); iban != "" {
		return "iban-" + digest("account/iban", iban)[:32]
	}
	if hash := strings.TrimSpace(account.IdentificationHash); hash != "" {
		return "eb-" + digest("account/identification-hash", hash)[:32]
	}
	if account.AccountID != nil && account.AccountID.Other != nil && strings.TrimSpace(account.AccountID.Other.Identification) != "" {
		return "acct-" + digest("account/other", strings.TrimSpace(account.AccountID.Other.Identification))[:32]
	}
	return "uid-" + account.UID
}

func normalizeIBAN(iban string) string {
	return strings.ToUpper(strings.Join(strings.Fields(iban), ""))
}

// MaskIBAN shows an IBAN as its first and last four characters, such as
// DE89…3000; "" for none.
func MaskIBAN(iban string) string {
	iban = normalizeIBAN(iban)
	if len(iban) < 8 {
		return ""
	}
	return iban[:4] + "…" + iban[len(iban)-4:]
}

// accountName is what the account is called: the description the owner or
// bank gave it, else the product (such as "Das Junge Konto"), else the bank
// and kind. The provider's name field is the account holder, never used.
func accountName(institution string, account enablebanking.Account) string {
	for _, candidate := range []string{account.Details, account.Product} {
		if name := clip(candidate, 100); name != "" {
			return name
		}
	}
	kind := map[string]string{"card": "card", "savings": "savings account"}[StoredAccount{CashAccountType: account.CashAccountType}.Kind()]
	if kind == "" {
		kind = "account"
	}
	return clip(institution+" "+kind, 100)
}

// currencyExponents are the ISO 4217 minor-unit digits that are not 2.
var currencyExponents = map[string]int{
	"BIF": 0, "CLP": 0, "DJF": 0, "GNF": 0, "ISK": 0, "JPY": 0, "KMF": 0, "KRW": 0, "PYG": 0,
	"RWF": 0, "UGX": 0, "UYI": 0, "VND": 0, "VUV": 0, "XAF": 0, "XOF": 0, "XPF": 0,
	"BHD": 3, "IQD": 3, "JOD": 3, "KWD": 3, "LYD": 3, "OMR": 3, "TND": 3,
}

// ErrAmount means an amount is not a decimal number in its currency's
// minor units.
var ErrAmount = errors.New("unreadable amount")

// ParseMinor converts a decimal amount such as "12.34" or "-0.5" to minor
// units of currency (cents for EUR) without floating point. Extra
// fractional digits are accepted only when they are zeros.
func ParseMinor(amount, currency string) (int64, error) {
	text := strings.TrimSpace(amount)
	negative := false
	switch {
	case strings.HasPrefix(text, "-"):
		negative, text = true, text[1:]
	case strings.HasPrefix(text, "+"):
		text = text[1:]
	}
	whole, fraction, _ := strings.Cut(text, ".")
	if whole == "" && fraction == "" || !digits(whole) || !digits(fraction) {
		return 0, fmt.Errorf("%w: %q", ErrAmount, amount)
	}
	exponent, ok := currencyExponents[strings.ToUpper(currency)]
	if !ok {
		exponent = 2
	}
	if len(fraction) > exponent {
		if strings.Trim(fraction[exponent:], "0") != "" {
			return 0, fmt.Errorf("%w: %q has more decimals than %s", ErrAmount, amount, currency)
		}
		fraction = fraction[:exponent]
	}
	fraction += strings.Repeat("0", exponent-len(fraction))
	var value int64
	for _, r := range whole + fraction {
		digit := int64(r - '0')
		if value > (math.MaxInt64-digit)/10 {
			return 0, fmt.Errorf("%w: %q is too large", ErrAmount, amount)
		}
		value = value*10 + digit
	}
	if negative {
		value = -value
	}
	return value, nil
}

func digits(text string) bool {
	for _, r := range text {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// SignedMinor is a transaction amount in minor units, negative for money
// out: the credit/debit indicator decides the sign (the API sends positive
// amounts); without one the amount's own sign stays.
func SignedMinor(amount enablebanking.Amount, indicator string) (int64, error) {
	value, err := ParseMinor(amount.Amount, amount.Currency)
	if err != nil {
		return 0, err
	}
	if value < 0 {
		value = -value
		if indicator == "" {
			return -value, nil
		}
	}
	switch strings.ToUpper(indicator) {
	case "DBIT":
		return -value, nil
	}
	return value, nil
}

// accountData is what one sync read of an account.
type accountData struct {
	account      StoredAccount
	balances     []enablebanking.Balance
	transactions []enablebanking.Transaction
}

// readResult is a sync read from the bank, ready for Finance: the import,
// how many transactions were read and left out, the latest booking date of
// a booked transaction (the next sync's cursor) and the earliest, the
// oldest booking date of a pending one (where the next sync starts at the
// latest), and the MappingVersion of a read of the whole InitialDays window
// (0 for a read since the cursor; an import kept for Finance from before a
// mapping change has none).
type readResult struct {
	Import        finance.Import `json:"import"`
	Fetched       int            `json:"fetched"`
	Ignored       int            `json:"ignored"`
	Cursor        string         `json:"cursor,omitempty"`
	Earliest      string         `json:"earliest,omitempty"`
	OldestPending string         `json:"oldestPending,omitempty"`
	Mapping       int            `json:"mapping,omitempty"`
}

// buildImport maps a sync read to Finance's bank import.
func buildImport(connectionID, institution string, window finance.Window, data []accountData) readResult {
	result := readResult{Import: finance.Import{
		ConnectionID: connectionID, Provider: ProviderEnableBanking, Institution: institution, Window: window,
		Accounts: []finance.Account{}, Transactions: []finance.Transaction{},
	}}
	type mapped struct {
		transaction finance.Transaction
		base, hash  string
	}
	var all []mapped
	for _, item := range data {
		account := item.account
		booked, available := pickBalances(item.balances, account.Currency, window.To)
		result.Import.Accounts = append(result.Import.Accounts, finance.Account{
			ExternalID: account.Key, Name: account.Name, Kind: account.Kind(), Currency: account.Currency,
			IBANMasked: account.IBANMasked, BookedBalance: booked, AvailableBalance: available,
		})
		for _, raw := range item.transactions {
			result.Fetched++
			transaction, ok := mapTransaction(account, raw, window.To)
			if !ok {
				result.Ignored++
				continue
			}
			if transaction.Status == "booked" && transaction.BookingDate > result.Cursor {
				result.Cursor = transaction.BookingDate
			}
			if transaction.Status == "booked" && (result.Earliest == "" || transaction.BookingDate < result.Earliest) {
				result.Earliest = transaction.BookingDate
			}
			if transaction.Status == "pending" && (result.OldestPending == "" || transaction.BookingDate < result.OldestPending) {
				result.OldestPending = transaction.BookingDate
			}
			hash := contentHash(account.Key, raw)
			base := transactionRef(raw)
			if base == "" {
				base = "h-" + hash
			} else if len(base) > 180 {
				base = "r-" + digest("transaction/reference", base)[:40]
			}
			all = append(all, mapped{transaction: transaction, base: base, hash: hash})
		}
	}
	// External ids must be unique in the import (Finance keys them by
	// connection). A reference the bank repeats, within or across accounts,
	// is told apart by the transaction's content; identical twins by order.
	counts := map[string]int{}
	for _, item := range all {
		counts[item.base]++
	}
	used := map[string]int{}
	for _, item := range all {
		id := item.base
		if counts[id] > 1 {
			id += "~" + item.hash[:12]
		}
		used[id]++
		if used[id] > 1 {
			id = fmt.Sprintf("%s~%d", id, used[id])
		}
		item.transaction.ExternalID = id
		result.Import.Transactions = append(result.Import.Transactions, item.transaction)
	}
	return result
}

// transactionRef is the bank's reference for a transaction: the entry
// reference, which the provider documents as the stable one, else the
// transaction id.
func transactionRef(transaction enablebanking.Transaction) string {
	if ref := strings.TrimSpace(transaction.EntryReference); ref != "" {
		return ref
	}
	return strings.TrimSpace(transaction.TransactionID)
}

func contentHash(accountKey string, t enablebanking.Transaction) string {
	return digest("transaction/content", accountKey, t.CreditDebitIndicator, t.Amount.Amount, t.Amount.Currency,
		t.BookingDate, t.ValueDate, t.TransactionDate, partyName(t.Creditor), partyName(t.Debtor),
		strings.Join(t.Remittance, "\n"), t.ReferenceNumber)[:24]
}

func partyName(party *enablebanking.Party) string {
	if party == nil {
		return ""
	}
	return party.Name
}

// mapTransaction maps one transaction; ok is false for one Finance must not
// get: cancelled, rejected, or scheduled, in another currency than its
// account, or with an unreadable amount.
func mapTransaction(account StoredAccount, raw enablebanking.Transaction, today string) (finance.Transaction, bool) {
	var status string
	switch strings.ToUpper(raw.Status) {
	case "BOOK":
		status = "booked"
	case "CNCL", "RJCT", "SCHD":
		return finance.Transaction{}, false
	default: // PDNG, HOLD, OTHR
		status = "pending"
	}
	currency := strings.ToUpper(strings.TrimSpace(raw.Amount.Currency))
	if currency != account.Currency {
		return finance.Transaction{}, false
	}
	amount, err := SignedMinor(enablebanking.Amount{Amount: raw.Amount.Amount, Currency: currency}, raw.CreditDebitIndicator)
	if err != nil {
		return finance.Transaction{}, false
	}
	booking := firstDate(raw.BookingDate, raw.TransactionDate, raw.ValueDate)
	if booking == "" {
		booking = today
	}
	counterparty := raw.Creditor
	if strings.EqualFold(raw.CreditDebitIndicator, "CRDT") {
		counterparty = raw.Debtor
	}
	description := clean(strings.Join(raw.Remittance, " "))
	if description == "" {
		description = clean(raw.Note)
	}
	// A card settlement or PayPal names the merchant in the text.
	name, description := normalizeMerchant(partyName(counterparty), description)
	mcc := strings.TrimSpace(raw.MerchantCategoryCode)
	if len(mcc) > 8 {
		mcc = ""
	}
	return finance.Transaction{
		AccountExternalID: account.Key, Status: status, BookingDate: booking, ValueDate: firstDate(raw.ValueDate),
		Amount: finance.Money{Amount: amount, Currency: currency}, Counterparty: clip(name, 200),
		Description: clip(description, 1000), MerchantCategoryCode: mcc, BankReference: clip(raw.ReferenceNumber, 200),
	}, true
}

// balanceOrder is which balance types stand for the booked and the
// available balance, best first.
var (
	bookedTypes    = []string{"CLBD", "ITBD", "OPBD"}
	availableTypes = []string{"ITAV", "CLAV", "XPCD", "OPAV"}
)

func pickBalances(balances []enablebanking.Balance, currency, today string) (booked, available *finance.Balance) {
	pick := func(types []string) *finance.Balance {
		for _, kind := range types {
			for _, balance := range balances {
				if !strings.EqualFold(balance.Type, kind) || !strings.EqualFold(balance.Amount.Currency, currency) {
					continue
				}
				amount, err := ParseMinor(balance.Amount.Amount, currency)
				if err != nil {
					continue
				}
				date := firstDate(balance.ReferenceDate, balance.LastChangeDateTime)
				if date == "" {
					date = today
				}
				return &finance.Balance{Amount: amount, Currency: currency, Date: date}
			}
		}
		return nil
	}
	return pick(bookedTypes), pick(availableTypes)
}

// firstDate is the first value that starts with an ISO date, as that date.
func firstDate(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if len(value) >= 10 && isDate(value[:10]) {
			return value[:10]
		}
	}
	return ""
}

func isDate(value string) bool {
	if len(value) != 10 || value[4] != '-' || value[7] != '-' {
		return false
	}
	return digits(value[:4]) && digits(value[5:7]) && digits(value[8:])
}

// clean collapses whitespace and drops control characters.
func clean(text string) string {
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, text)
	return strings.Join(strings.Fields(text), " ")
}

// clip cleans text and cuts it to at most limit characters.
func clip(text string, limit int) string {
	text = clean(text)
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	return strings.TrimSpace(string([]rune(text)[:limit]))
}

func digest(purpose string, parts ...string) string {
	sum := sha256.Sum256([]byte("life-os/banking/" + purpose + "\x00" + strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}
