package domain

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"Молоко":                 "молоко",
		"  МОЛОКО ":              "молоко",
		"Ёжик":                   "ежик",
		"ёлка":                   "елка",
		"ёжик":                  "ежик", // ё written as е and a combining diaeresis
		"Йогурт":                "йогурт",
		"Milk!!":                 "milk",
		"«Молоко»":               "молоко",
		"(eggs)":                 "eggs",
		"- Brot -":               "brot",
		"oat \t  milk\n":         "oat milk",
		"ｍｉｌｋ":                   "milk", // full-width letters
		"ﬁsh":                    "fish", // the fi ligature
		"Straße":                 "straße",
		"Coca-Cola":              "coca-cola",
		"3,5% Milch":             "3,5% milch",
		"Crème fraîche":          "crème fraîche",
		"  !!! ":                 "",
		"Vollmilch, 1 l.":        "vollmilch, 1 l",
		"Äpfel":                  "äpfel",
		"Äpfel":                 "äpfel", // decomposed umlaut composes
		"Туалетная   бумага ...": "туалетная бумага",
	}
	for input, want := range cases {
		if got := Normalize(input); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", input, got, want)
		}
	}
	if Normalize("Молоко") != Normalize("молоко") {
		t.Error("Молоко and молоко differ")
	}
}

// seededRules reads the built-in aisle rules from the migration that seeds
// them, so these tests check the rules the service starts with.
func seededRules(t *testing.T) []Rule {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "db", "migrations", "000002_food.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	pattern := regexp.MustCompile(`\('([^']+)', '([a-z_]+)', '(word|substring)'\)`)
	var rules []Rule
	seen := map[string]bool{}
	for _, match := range pattern.FindAllStringSubmatch(string(raw), -1) {
		rule := Rule{Keyword: match[1], Aisle: match[2], Match: match[3]}
		if seen[rule.Keyword] {
			t.Errorf("keyword %q is seeded twice", rule.Keyword)
		}
		seen[rule.Keyword] = true
		if Normalize(rule.Keyword) != rule.Keyword {
			t.Errorf("seeded keyword %q is not normalised (%q)", rule.Keyword, Normalize(rule.Keyword))
		}
		if !ValidAisle(rule.Aisle) || rule.Aisle == OtherAisle {
			t.Errorf("seeded keyword %q has aisle %q", rule.Keyword, rule.Aisle)
		}
		rules = append(rules, rule)
	}
	if len(rules) < 150 {
		t.Fatalf("only %d seeded rules", len(rules))
	}
	return rules
}

func TestSeededRulesPlaceCommonItems(t *testing.T) {
	rules := seededRules(t)
	cases := map[string]string{
		// English: whole words.
		"Milk":              "dairy",
		"oat milk":          "dairy",
		"buttermilk":        "other", // "milk" is not a word of it
		"steak":             "other", // nor is "tea"
		"ice cream":         "frozen",
		"sour cream":        "dairy",
		"2 l water":         "drinks",
		"toilet paper":      "household",
		"dish soap":         "household",
		"soap":              "drugstore",
		"free-range eggs":   "dairy",
		"Ribeye (beef)":     "meat_fish",
		"Whole wheat bread": "bakery",
		// Russian: whole words; "Молоко" and "молоко" are the same.
		"Молоко":              "dairy",
		"молоко 3,2%":         "dairy",
		"Сливочное масло":     "dairy",
		"Масло подсолнечное":  "pantry",
		"Оливковое масло":     "pantry",
		"Туалетная бумага":    "household",
		"Гель для душа":       "drugstore",
		"Средство для посуды": "household",
		"Ёжики шоколадные":    "other",
		"Чёрный шоколад":      "snacks",
		"Пельмени":            "frozen",
		"Гречка":              "pantry",
		// German: substrings, so compounds work; the longest keyword wins.
		"Vollmilch":       "dairy",
		"Hafermilch":      "dairy",
		"Milchschokolade": "snacks",
		"Orangensaft":     "drinks",
		"Apfelsaft":       "drinks",
		"Apfelschorle":    "drinks",
		"Äpfel":           "produce",
		"Rotwein":         "drinks",
		"Schweinebauch":   "meat_fish",
		"Hackfleisch":     "meat_fish",
		"Olivenöl":        "pantry",
		"Basmatireis":     "pantry",
		"Speiseeis":       "frozen",
		"Eis":             "frozen",
		"Eistee":          "drinks",
		"Kräutertee":      "pantry",
		"Wassermelone":    "produce",
		"Mineralwasser":   "drinks",
		"Kartoffelchips":  "snacks",
		"Käsekuchen":      "bakery",
		"Bio-Eier":        "dairy",
		"Fischstäbchen":   "frozen",
		"Toilettenpapier": "household",
		"Zahnpasta":       "drugstore",
		"Tiefkühlpizza":   "frozen",
		// Nothing matches.
		"Birthday candles": "other",
	}
	for name, want := range cases {
		got, _, _ := ChooseAisle(Normalize(name), rules)
		if got != want {
			t.Errorf("%q goes to %s, want %s", name, got, want)
		}
	}
}

func TestChooseAisleTieBreaks(t *testing.T) {
	rules := []Rule{
		{Keyword: "bohnen", Aisle: "pantry", Match: MatchSubstring},
		{Keyword: "kaffee", Aisle: "drinks", Match: MatchSubstring},
		{Keyword: "milch", Aisle: "dairy", Match: MatchSubstring},
		{Keyword: "milch", Aisle: "dairy", Match: MatchWord},
	}
	// Equally long: the keyword that sorts first wins, whatever the order.
	for _, ordered := range [][]Rule{rules, {rules[1], rules[0]}} {
		if aisle, rule, ok := ChooseAisle("kaffeebohnen", ordered); !ok || aisle != "pantry" || rule.Keyword != "bohnen" {
			t.Errorf("kaffeebohnen: %s %+v %v", aisle, rule, ok)
		}
	}
	// A whole-word match beats an equally long substring match.
	if _, rule, _ := ChooseAisle("frische milch", rules); rule.Match != MatchWord {
		t.Errorf("frische milch matched %+v", rule)
	}
	if aisle, _, ok := ChooseAisle("brot", rules); ok || aisle != OtherAisle {
		t.Errorf("brot: %s %v", aisle, ok)
	}
}

func TestAisles(t *testing.T) {
	if AisleRank("produce") != 1 || AisleRank("other") != len(Aisles) || AisleRank("nope") != 0 {
		t.Error("aisle ranks")
	}
	for aisle, want := range map[string]string{"frozen": "freezer", "dairy": "fridge", "meat_fish": "fridge", "pantry": "pantry", "drinks": "pantry", "produce": "other", "household": "other"} {
		if got := PantryLocation(aisle); got != want {
			t.Errorf("PantryLocation(%s) = %s, want %s", aisle, got, want)
		}
	}
}
