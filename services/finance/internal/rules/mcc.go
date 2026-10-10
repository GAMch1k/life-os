package rules

import (
	"strconv"
	"strings"
)

// The default categories' system keys that merchant category codes map to.
const (
	KeyGroceries     = "groceries"
	KeyEatingOut     = "eating_out"
	KeyTransport     = "transport"
	KeySubscriptions = "subscriptions"
	KeyUtilities     = "utilities"
	KeyHealth        = "health"
	KeyFun           = "fun"
	KeyShopping      = "shopping"
)

type codeRange struct{ from, to int }

// merchantGroups maps standard merchant category code (ISO 18245) ranges to
// the default categories, tried in this order: the specific groups first,
// then shopping's wide retail ranges.
var merchantGroups = []struct {
	key    string
	ranges []codeRange
}{
	{KeyGroceries, []codeRange{{5411, 5411}, {5422, 5422}, {5441, 5441}, {5451, 5451}, {5462, 5462}, {5499, 5499}}},
	{KeyEatingOut, []codeRange{{5812, 5814}}},
	{KeyTransport, []codeRange{{4111, 4112}, {4121, 4121}, {4131, 4131}, {4784, 4784}, {4789, 4789}, {5541, 5542}, {7523, 7523}}},
	{KeySubscriptions, []codeRange{{4899, 4899}, {5815, 5818}}},
	{KeyUtilities, []codeRange{{4812, 4812}, {4814, 4814}, {4900, 4900}}},
	{KeyHealth, []codeRange{{5122, 5122}, {5912, 5912}, {8011, 8099}}},
	{KeyFun, []codeRange{{7832, 7832}, {7922, 7922}, {7991, 7999}}},
	{KeyShopping, []codeRange{{5200, 5399}, {5600, 5699}, {5732, 5732}, {5734, 5734}, {5940, 5999}}},
}

// MerchantGroup returns the system key of the default category for a
// merchant category code (four digits, such as "5411"); false for an empty,
// malformed, or unmapped code.
func MerchantGroup(code string) (string, bool) {
	code = strings.TrimSpace(code)
	if len(code) != 4 {
		return "", false
	}
	value, err := strconv.Atoi(code)
	if err != nil || value < 0 {
		return "", false
	}
	for _, group := range merchantGroups {
		for _, r := range group.ranges {
			if value >= r.from && value <= r.to {
				return group.key, true
			}
		}
	}
	return "", false
}
