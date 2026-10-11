package recipe

import (
	"regexp"
	"sort"
	"strings"
)

// Units an ingredient's amount may carry, in English, German, and Russian.
// Anything else after the number is part of the name ("2 große Zwiebeln" is
// 2 of "große Zwiebeln").
var units = []string{
	// Weight and volume.
	"mg", "g", "gr", "gramm", "kg", "ml", "cl", "dl", "l", "liter", "litre", "ltr", "oz", "lb", "lbs",
	// English kitchen units.
	"tbsp", "tbs", "tsp", "cup", "cups", "pinch", "pinches", "clove", "cloves", "can", "cans", "tin", "tins",
	"slice", "slices", "bunch", "bunches", "piece", "pieces", "handful", "pack", "packs", "packet", "packets",
	"stick", "sticks", "jar", "jars", "bottle", "bottles", "sprig", "sprigs",
	// German kitchen units.
	"el", "tl", "essl", "teel", "esslöffel", "teelöffel", "msp", "prise", "prisen", "pck", "pkt", "päckchen",
	"packung", "packungen", "dose", "dosen", "glas", "gläser", "becher", "bund", "zehe", "zehen", "knolle",
	"scheibe", "scheiben", "stück", "stk", "st", "tasse", "tassen", "handvoll", "flasche", "flaschen", "würfel",
	"zweig", "zweige", "blatt", "blätter", "beutel", "tüte",
	// Russian kitchen units.
	"ст. л", "ч. л", "ст.л", "ч.л", "ст. ложки", "ч. ложки", "ст. ложка", "ч. ложка", "стакан", "стакана",
	"стаканов", "шт", "г", "гр", "кг", "мл", "л", "щепотка", "щепотки", "зубчик", "зубчика", "зубчиков",
	"пучок", "пучка", "банка", "банки", "пачка", "пачки", "ложка", "ложки", "кусок", "куска",
}

// A count or a measure: "2", "1,5", "0.5", "2-3", "1/2", "½", "1 ½".
const number = `(?:\d+(?:[.,]\d+)?(?:\s*[½¼¾⅓⅔⅛])?(?:\s*[-–]\s*\d+(?:[.,]\d+)?)?|\d+\s*/\s*\d+|[½¼¾⅓⅔⅛])`

var leadingAmount = func() *regexp.Regexp {
	sorted := append([]string(nil), units...)
	// Longest first, so "kg" is not read as "g" and "ст. ложки" not as "ст. л".
	sort.Slice(sorted, func(a, b int) bool { return len(sorted[a]) > len(sorted[b]) })
	quoted := make([]string, len(sorted))
	for index, unit := range sorted {
		quoted[index] = strings.ReplaceAll(regexp.QuoteMeta(unit), `\ `, `\s*`)
	}
	unit := `(?:` + strings.Join(quoted, "|") + `)\.?`
	// The amount must be followed by a space and a name.
	return regexp.MustCompile(`(?i)^(` + number + `(?:\s*` + unit + `)?)\s+(\S.*)$`)
}()

// unitOnly matches a name that is only a unit: the amount was not one.
var unitOnly = regexp.MustCompile(`(?i)^(?:` + strings.Join(func() []string {
	quoted := make([]string, len(units))
	for index, unit := range units {
		quoted[index] = regexp.QuoteMeta(unit)
	}
	return quoted
}(), "|") + `)\.?$`)

// SplitIngredient splits an ingredient line into an amount written before
// the name and the name: "200 g Mehl" is "200 g" of "Mehl", "2 EL Olivenöl"
// is "2 EL" of "Olivenöl", "1 ст. л. сахара" is "1 ст. л." of "сахара". A
// line without a leading amount ("Salz und Pfeffer") is all name.
func SplitIngredient(line string) Ingredient {
	line = clean(line)
	match := leadingAmount.FindStringSubmatch(line)
	if match == nil || unitOnly.MatchString(match[2]) {
		return Ingredient{Name: line}
	}
	return Ingredient{Amount: strings.TrimSpace(match[1]), Name: strings.TrimSpace(match[2])}
}
