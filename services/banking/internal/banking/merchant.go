package banking

import (
	"regexp"
	"strings"
	"unicode"
)

// MappingVersion is the version of how Banking maps the bank's transactions
// for Finance. A connection remembers the version of its last full read
// (bank_connections.mapping_version); while it is older, the next sync reads
// the whole InitialDays window instead of only the days since the cursor, so
// the transactions Finance already has are sent again with the new mapping
// (Finance updates them by external id and lets the rules choose again).
//
//	1: the first mapping (P13-03).
//	2: card settlements and PayPal purchases name the merchant (BUG-38).
const MappingVersion = 2

// Deutsche Bank books every card payment with the counterparty "ABRECHNUNG
// KARTE" (card settlement) and puts the merchant into the remittance text:
//
//	REWE Markt GmbH//Darmstadt/DE 08-10-2026T20:51:57 Kartennr. 1234XXXXXXXX5678
//	FIGMA//SAN FRANCISCO/US 28-09-2026T08:27:54 Kartennr. … Entgelt 0,24 EUR
//
// The merchant comes before "//", then the place and country, the card's
// local time, the masked card number, and sometimes a foreign currency fee.
// PayPal payments name PayPal Europe as the counterparty and the merchant
// in the text ("…, Ihr Einkauf bei Crunchyroll, LLC").
//
// normalizeMerchant makes the merchant the counterparty of both, so
// Finance's merchant rules (counterparty contains "REWE") match, and keeps
// the rest readable in the description. Text in any other shape keeps its
// counterparty and description as the bank sent them. The card number is
// never kept, in any shape.
func normalizeMerchant(counterparty, description string) (string, string) {
	description = withoutCardNumbers(description)
	switch {
	case isCardSettlement(counterparty):
		if merchant, detail, ok := cardSettlement(description); ok {
			return merchant, detail
		}
	case isPayPal(counterparty):
		if merchant, ok := payPalMerchant(description); ok {
			return merchant, "via PayPal · " + description
		}
	}
	return counterparty, description
}

// words are text's runs of letters, lowercased.
func words(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) })
}

// isCardSettlement reports whether the counterparty is the bank's card
// settlement marker rather than a merchant, ignoring case, punctuation, and
// digits ("ABRECHNUNG KARTE", "Abrechnung Karte.").
func isCardSettlement(counterparty string) bool {
	found := words(counterparty)
	return len(found) == 2 && found[0] == "abrechnung" && found[1] == "karte"
}

// isPayPal reports whether the counterparty is PayPal ("PayPal Europe
// S.a.r.l. et Cie S.C.A", "PayPal (Europe) S.a r.l. et Cie, S. C.A.").
func isPayPal(counterparty string) bool {
	for _, word := range words(counterparty) {
		if word == "paypal" {
			return true
		}
	}
	return false
}

var (
	// cardTime is the card's local time in a card settlement, such as
	// 08-10-2026T20:51:57.
	cardTime = regexp.MustCompile(`\b(\d{2})-(\d{2})-(\d{4})T(\d{2}:\d{2}(?::\d{2})?)\b`)
	// cardLabel introduces the card number; maskedCard is a masked card
	// number on its own, such as 1234XXXXXXXX5678.
	cardLabel  = regexp.MustCompile(`(?i)\bKarten?(?:-?nr|-?nummer)\b\.?:?`)
	maskedCard = regexp.MustCompile(`\b\d{4,6}[Xx*•]{4,10}\d{2,4}\b`)
	// cardDigits is one piece of a card number after its label: digits and
	// mask characters (a bank may split the number across text lines).
	cardDigits  = regexp.MustCompile(`^[0-9Xx*•.…]+$`)
	countryCode = regexp.MustCompile(`^[A-Z]{2}$`)
	// purchaseAt names the merchant in PayPal's text.
	purchaseAt = regexp.MustCompile(`(?i)\bIhr Einkauf bei\s+(.+)$`)
)

// withoutCardNumbers removes card numbers and their label ("Kartennr.
// 1234XXXXXXXX5678") from text. Nothing else is removed.
func withoutCardNumbers(text string) string {
	text = clean(text)
	if text == "" {
		return text
	}
	for {
		match := cardLabel.FindStringIndex(text)
		if match == nil {
			break
		}
		// The label and the number's pieces after it, at most a card
		// number's 19 characters.
		rest := strings.Fields(text[match[1]:])
		taken, length := 0, 0
		for taken < len(rest) && cardDigits.MatchString(rest[taken]) && length+len(rest[taken]) <= 19 {
			length += len(rest[taken])
			taken++
		}
		text = clean(text[:match[0]] + " " + strings.Join(rest[taken:], " "))
	}
	return clean(maskedCard.ReplaceAllString(text, " "))
}

// cardSettlement reads a card settlement's text (its card number already
// removed): the merchant before "//", and a description of the rest, such
// as "Card payment · Darmstadt, DE · 2026-10-08 20:51:57 · Entgelt 0,24
// EUR". ok is false when the text has no merchant.
func cardSettlement(text string) (merchant, description string, ok bool) {
	before, after, found := strings.Cut(text, "//")
	merchant = clip(before, 200)
	if !found || !hasLetterOrDigit(merchant) {
		return "", "", false
	}
	place, when, extra := clean(after), "", ""
	if match := cardTime.FindStringSubmatchIndex(place); match != nil {
		day, month, year, clock := place[match[2]:match[3]], place[match[4]:match[5]], place[match[6]:match[7]], place[match[8]:match[9]]
		when = year + "-" + month + "-" + day + " " + clock
		place, extra = clean(place[:match[0]]), clean(place[match[1]:])
	}
	// "Darmstadt/DE" is the place and its country.
	if slash := strings.LastIndex(place, "/"); slash >= 0 && countryCode.MatchString(strings.TrimSpace(place[slash+1:])) {
		city := strings.Trim(clean(place[:slash]), "/ ")
		place = strings.TrimSpace(place[slash+1:])
		if city != "" {
			place = city + ", " + place
		}
	}
	parts := []string{"Card payment"}
	for _, part := range []string{place, when, extra} {
		if part = strings.Trim(part, "/ "); part != "" {
			parts = append(parts, part)
		}
	}
	return merchant, strings.Join(parts, " · "), true
}

// payPalMerchant is the merchant PayPal's text names after "Ihr Einkauf
// bei", as the bank spells it.
func payPalMerchant(text string) (string, bool) {
	found := purchaseAt.FindStringSubmatch(text)
	if found == nil {
		return "", false
	}
	merchant := clip(strings.TrimRight(found[1], " ,.;:"), 200)
	return merchant, hasLetterOrDigit(merchant)
}

func hasLetterOrDigit(text string) bool {
	return strings.IndexFunc(text, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) >= 0
}
