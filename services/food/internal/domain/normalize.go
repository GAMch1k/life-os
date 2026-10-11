// Package domain holds Food's rules that do not depend on storage or
// transport: how names are normalised, the aisles, and how aisle rules choose
// an aisle for an item.
package domain

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Normalize returns the form of a name that Food compares: Unicode NFKC,
// lower case, "ё" as "е", runs of whitespace as one space, and no
// surrounding whitespace or punctuation. "  Молоко!" and "молоко" are the
// same item; "Ёжик" is "ежик". It does no stemming: "молоко" and "молока"
// stay different. An empty result means the name has nothing to compare.
func Normalize(name string) string {
	text := strings.ToLower(norm.NFKC.String(name))
	text = strings.ReplaceAll(text, "ё", "е")
	text = strings.Join(strings.Fields(text), " ")
	return strings.TrimFunc(text, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsPunct(r) })
}

// words splits a normalised text into its words: runs of letters, digits,
// and combining marks.
func words(text string) []string {
	return strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && !unicode.IsMark(r)
	})
}
