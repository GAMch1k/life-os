package domain

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// pluralEndings are endings that make a word's plural (or another
// inflection) in English and German: "egg"/"eggs", "Tomate"/"Tomaten",
// "Ei"/"Eier", "Zwiebel"/"Zwiebeln".
var pluralEndings = []string{"s", "es", "e", "n", "en", "er", "r"}

// Matching thresholds, in runes. A word that starts another must be this
// long to match it ("hähnchen" starts "hähnchenbrust", but "egg" does not
// match "eggplant"); a word another one ends with must be this long to be
// covered by it or to cover it (see Covers).
const (
	prefixMin        = 5
	itemSuffixMin    = 3
	genericSuffixMin = 4
	stemMin          = 3
	stemEndingMax    = 2
)

// Covers reports whether an item named have (in the pantry, or bought)
// covers a recipe's ingredient named need, both normalised names. The names
// match word by word: every word of the name with fewer words must match a
// word of the other. Two words match when they are the same; when one is the
// other's plural ("eier" and "ei"); when one starts the other and is at least
// five letters ("hähnchen" and "hähnchenbrust"); when the item's word is a
// compound ending in the ingredient's of at least three letters
// ("vollmilch" covers "milch"); when the item is the generic word of at
// least four letters a compound ingredient ends with ("milch" covers
// "vollmilch"); or, in Cyrillic, when they share a stem of three letters and
// differ in endings of at most two ("молоко" and "молока", "яйцо" and
// "яйца"). It is a heuristic: names are free text in three languages.
func Covers(have, need string) bool {
	if have == "" || need == "" {
		return false
	}
	if have == need {
		return true
	}
	haveWords, needWords := words(have), words(need)
	if len(haveWords) == 0 || len(needWords) == 0 {
		return false
	}
	short, long, shortIsHave := needWords, haveWords, false
	if len(haveWords) < len(needWords) {
		short, long, shortIsHave = haveWords, needWords, true
	}
	for _, word := range short {
		found := false
		for _, other := range long {
			haveWord, needWord := other, word
			if shortIsHave {
				haveWord, needWord = word, other
			}
			if wordCovers(haveWord, needWord) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// NamesMatch reports whether two normalised names name the same thing in
// either direction, as Covers decides (a disliked ingredient and a recipe's
// ingredient, for example).
func NamesMatch(a, b string) bool { return Covers(a, b) || Covers(b, a) }

func wordCovers(have, need string) bool {
	if have == need {
		return true
	}
	if plural(have, need) || plural(need, have) {
		return true
	}
	haveLength, needLength := utf8.RuneCountInString(have), utf8.RuneCountInString(need)
	if min(haveLength, needLength) >= prefixMin && (strings.HasPrefix(have, need) || strings.HasPrefix(need, have)) {
		return true
	}
	if needLength >= itemSuffixMin && haveLength > needLength && strings.HasSuffix(have, need) {
		return true
	}
	if haveLength >= genericSuffixMin && needLength > haveLength && strings.HasSuffix(need, have) {
		return true
	}
	if cyrillic(have) && cyrillic(need) {
		stem := commonPrefix(have, need)
		if stem >= stemMin && haveLength-stem <= stemEndingMax && needLength-stem <= stemEndingMax {
			return true
		}
	}
	return false
}

// plural reports whether long is short with a plural ending.
func plural(long, short string) bool {
	if !strings.HasPrefix(long, short) {
		return false
	}
	ending := long[len(short):]
	for _, candidate := range pluralEndings {
		if ending == candidate {
			return true
		}
	}
	return false
}

func cyrillic(word string) bool {
	for _, r := range word {
		if unicode.Is(unicode.Cyrillic, r) {
			return true
		}
	}
	return false
}

// commonPrefix is how many runes two words share at their start.
func commonPrefix(a, b string) int {
	ar, br := []rune(a), []rune(b)
	n := 0
	for n < len(ar) && n < len(br) && ar[n] == br[n] {
		n++
	}
	return n
}

// basics are ingredients a kitchen is assumed to have, which no pantry
// lists: water, salt, pepper, and cooking oil. Coverage leaves them out.
var basics = map[string]bool{
	"water": true, "wasser": true, "вода": true,
	"salt": true, "salz": true, "соль": true,
	"pepper": true, "black pepper": true, "pfeffer": true, "schwarzer pfeffer": true, "черный перец": true, "молотый перец": true,
	"salt and pepper": true, "salz und pfeffer": true, "соль и перец": true,
	"oil": true, "cooking oil": true, "vegetable oil": true, "öl": true, "speiseöl": true, "pflanzenöl": true, "rapsöl": true,
	"растительное масло": true, "подсолнечное масло": true,
}

// Basic reports whether a normalised ingredient name is a kitchen basic
// (water, salt, pepper, cooking oil) that coverage leaves out.
func Basic(normalized string) bool { return basics[strings.Join(words(normalized), " ")] }
