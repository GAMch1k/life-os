package domain

import (
	"strings"
	"unicode/utf8"
)

// Aisles are the sections of the shopping list, in the order the list shows
// them. The order is also the aisle_rank column of shopping_items (migration
// 000002), which must list them the same way.
var Aisles = []string{"produce", "bakery", "dairy", "meat_fish", "frozen", "pantry", "drinks", "snacks", "household", "drugstore", "other"}

// OtherAisle is the aisle of an item no rule places.
const OtherAisle = "other"

// ValidAisle reports whether aisle is one of Aisles.
func ValidAisle(aisle string) bool {
	for _, known := range Aisles {
		if aisle == known {
			return true
		}
	}
	return false
}

// AisleRank is the 1-based position of aisle in Aisles, 0 for an unknown one.
func AisleRank(aisle string) int {
	for index, known := range Aisles {
		if aisle == known {
			return index + 1
		}
	}
	return 0
}

// Rule match modes. A word rule matches its keyword as whole words of the
// name ("milk" in "oat milk", not in "buttermilk"); a substring rule matches
// it anywhere, also inside a word, so German compounds work ("milch" in
// "vollmilch").
const (
	MatchWord      = "word"
	MatchSubstring = "substring"
)

// Rule places items whose name contains Keyword (normalised) in Aisle.
type Rule struct {
	Keyword string
	Aisle   string
	Match   string
}

// ChooseAisle returns the aisle of the rule whose keyword is the longest one
// found in name (a normalised name), and that rule. When two found keywords
// are equally long, a whole-word match beats a substring match, then the
// keyword that sorts first wins, so the choice never depends on the order of
// rules. ok is false when no rule matches; the item then goes to OtherAisle.
func ChooseAisle(name string, rules []Rule) (aisle string, rule Rule, ok bool) {
	text := " " + strings.Join(words(name), " ") + " "
	bestLength := 0
	for _, candidate := range rules {
		keyword := strings.Join(words(candidate.Keyword), " ")
		if keyword == "" {
			continue
		}
		matched := strings.Contains(text, " "+keyword+" ")
		if !matched && candidate.Match == MatchSubstring {
			matched = strings.Contains(text, keyword)
		}
		if !matched {
			continue
		}
		length := utf8.RuneCountInString(keyword)
		if !ok || length > bestLength || length == bestLength && better(candidate, rule) {
			aisle, rule, bestLength, ok = candidate.Aisle, candidate, length, true
		}
	}
	if !ok {
		return OtherAisle, Rule{}, false
	}
	return aisle, rule, true
}

// better breaks a tie between two equally long matching keywords.
func better(candidate, current Rule) bool {
	if (candidate.Match == MatchWord) != (current.Match == MatchWord) {
		return candidate.Match == MatchWord
	}
	return candidate.Keyword < current.Keyword
}

// PantryLocation is where an item of aisle is usually kept when a pantry item
// is added without a location: frozen food in the freezer, dairy, meat, and
// fish in the fridge, shelf-stable food in the pantry, anything else other.
func PantryLocation(aisle string) string {
	switch aisle {
	case "frozen":
		return "freezer"
	case "dairy", "meat_fish":
		return "fridge"
	case "bakery", "pantry", "drinks", "snacks":
		return "pantry"
	}
	return "other"
}
