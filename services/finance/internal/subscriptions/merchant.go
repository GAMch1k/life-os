package subscriptions

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/GAMch1k/life-os/services/finance/internal/rules"
)

// Merchant is who a payment went to: Key groups payments (lower case,
// without references, dates, or legal forms) and Name is how people read
// it.
type Merchant struct {
	Key  string
	Name string
}

// service is a well-known subscription. Its patterns are matched as whole
// words anywhere in the counterparty or description, as the default
// Subscriptions rules do, so a payment through PayPal or Google Play that
// names it groups with the direct ones.
type service struct {
	key, name string
	patterns  []string
}

// knownServices are the default Subscriptions rules' services (Netflix,
// Spotify, Disney+, Amazon Prime, YouTube Premium) and a few more that are
// often paid through PayPal, Google, or Apple.
var knownServices = []service{
	{"netflix", "Netflix", []string{"netflix"}},
	{"spotify", "Spotify", []string{"spotify"}},
	{"disney-plus", "Disney+", []string{"disney+", "disney plus", "disneyplus"}},
	{"amazon-prime", "Amazon Prime", []string{"amazon prime", "prime video", "amzn prime"}},
	{"youtube-premium", "YouTube Premium", []string{"youtube premium", "youtubepremium"}},
	{"icloud", "iCloud", []string{"icloud"}},
	{"apple-music", "Apple Music", []string{"apple music"}},
	{"google-one", "Google One", []string{"google one"}},
	{"audible", "Audible", []string{"audible"}},
	{"dazn", "DAZN", []string{"dazn"}},
	{"microsoft-365", "Microsoft 365", []string{"microsoft 365", "office 365"}},
}

// intermediaries collect payments for many merchants: their counterparty
// says little, so the merchant comes from the card descriptor after "*" or
// from the description ("Ihr Einkauf bei Patreon").
var intermediaries = []service{
	{"paypal", "PayPal", []string{"paypal"}},
	{"google", "Google", []string{"google payment", "google play", "google ireland", "google *", "google*"}},
	{"apple", "Apple", []string{"apple.com/bill", "itunes", "apple services", "apple distribution"}},
	{"klarna", "Klarna", []string{"klarna"}},
}

var purchaseAt = regexp.MustCompile(`(?i)(?:einkauf bei|purchase at|purchase from|zahlung an|payment to)\s+([^,;/]+)`)

// legalForms and webWords are left out of keys: "Netflix International
// B.V." and "NETFLIX INTERNATIONAL BV" are the same merchant.
var legalForms = wordSet("gmbh mbh ag kg kgaa se ug ev e v co inc ltd limited llc plc sa sas sarl sca bv nv ab oy srl spa et cie und the www com de net org eu ie")

// descriptionNoise are words a bank's purpose text carries for every
// payment (SEPA terms, months), left out of a description's key.
var descriptionNoise = wordSet("sepa lastschrift basislastschrift dauerauftrag kartenzahlung kartenumsatz gutschrift überweisung ueberweisung " +
	"debit card payment zahlung ref referenz mandat mandatsreferenz glaeubiger gläubiger id end to nr kundennr kundennummer rechnung rg " +
	"vom für fuer for beitrag monat monatlich monthly abo pp paypal google play apple " +
	"january february march april may june july august september october november december " +
	"januar februar märz maerz mai juni juli oktober dezember jan feb mar mär apr jun jul aug sep sept oct okt nov dec dez")

func wordSet(words string) map[string]bool {
	set := map[string]bool{}
	for _, word := range strings.Fields(words) {
		set[word] = true
	}
	return set
}

// MerchantOf identifies the merchant of a payment: a known service named in
// the counterparty or description first; for an intermediary (PayPal,
// Google, Apple, Klarna) the merchant it paid; else the counterparty; else
// the description's first words. The key is empty when there is nothing
// to go by.
func MerchantOf(counterparty, description string) Merchant {
	counterparty, description = tidy(counterparty), tidy(description)
	text := rules.Normalize(counterparty + " " + description)
	for _, known := range knownServices {
		if matchesAny(text, known.patterns) {
			return Merchant{Key: "service:" + known.key, Name: known.name}
		}
	}
	lowerCounterparty := rules.Normalize(counterparty)
	for _, via := range intermediaries {
		if !matchesAny(lowerCounterparty, via.patterns) {
			continue
		}
		merchant := ""
		if index := strings.Index(counterparty, "*"); index >= 0 {
			merchant = tidy(counterparty[index+1:])
		}
		if key(merchant, 4, nil) == "" {
			merchant = ""
			if found := purchaseAt.FindStringSubmatch(description); found != nil {
				merchant = tidy(found[1])
			}
		}
		if merchantKey := key(merchant, 4, nil); merchantKey != "" {
			return Merchant{Key: via.key + ":" + merchantKey, Name: limit(merchant) + " via " + via.name}
		}
		if descriptionKey := key(description, 3, descriptionNoise); descriptionKey != "" {
			return Merchant{Key: via.key + ":" + descriptionKey, Name: titleWords(descriptionKey) + " via " + via.name}
		}
		return Merchant{Key: via.key, Name: via.name}
	}
	if counterpartyKey := key(counterparty, 4, nil); counterpartyKey != "" {
		return Merchant{Key: "cp:" + counterpartyKey, Name: limit(counterparty)}
	}
	if descriptionKey := key(description, 3, descriptionNoise); descriptionKey != "" {
		return Merchant{Key: "text:" + descriptionKey, Name: titleWords(descriptionKey)}
	}
	return Merchant{}
}

// matchesAny reports whether text (normalized) contains one of the
// patterns as a whole word.
func matchesAny(text string, patterns []string) bool {
	for _, pattern := range patterns {
		if containsWord(text, pattern) {
			return true
		}
	}
	return false
}

func containsWord(text, pattern string) bool {
	for start := 0; start <= len(text)-len(pattern); {
		index := strings.Index(text[start:], pattern)
		if index < 0 {
			return false
		}
		index += start
		end := index + len(pattern)
		first, _ := utf8.DecodeRuneInString(pattern)
		last, _ := utf8.DecodeLastRuneInString(pattern)
		before, _ := utf8.DecodeLastRuneInString(text[:index])
		after, _ := utf8.DecodeRuneInString(text[end:])
		boundaryBefore := index == 0 || !wordRune(first) || !wordRune(before)
		boundaryAfter := end == len(text) || !wordRune(last) || !wordRune(after)
		if boundaryBefore && boundaryAfter {
			return true
		}
		start = index + 1
	}
	return false
}

func wordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// key is text's comparable form: lower case words of letters and digits,
// without words that contain a digit (references, dates) or are in drop,
// and without legal forms, the first max words; words are kept when
// dropping them would leave none.
func key(text string, max int, drop map[string]bool) string {
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !wordRune(r) })
	if len(words) == 0 {
		return ""
	}
	kept := words[:0:0]
	for _, word := range words {
		if strings.IndexFunc(word, unicode.IsDigit) >= 0 || legalForms[word] || drop[word] {
			continue
		}
		kept = append(kept, word)
	}
	if len(kept) == 0 {
		if drop != nil {
			// A description of noise alone says nothing.
			return ""
		}
		kept = words
	}
	if len(kept) > max {
		kept = kept[:max]
	}
	return strings.Join(kept, " ")
}

// tidy trims text and collapses its spaces.
func tidy(text string) string { return strings.Join(strings.Fields(text), " ") }

// limit shortens a name to 100 characters.
func limit(name string) string {
	if utf8.RuneCountInString(name) <= 100 {
		return name
	}
	return strings.TrimSpace(string([]rune(name)[:100]))
}

func titleWords(text string) string {
	words := strings.Fields(text)
	for index, word := range words {
		first, size := utf8.DecodeRuneInString(word)
		words[index] = string(unicode.ToUpper(first)) + word[size:]
	}
	return limit(strings.Join(words, " "))
}
