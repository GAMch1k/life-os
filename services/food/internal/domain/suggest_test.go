package domain

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCovers(t *testing.T) {
	for _, c := range []struct {
		have, need string
		want       bool
	}{
		{"milch", "milch", true},
		{"hähnchen", "hähnchenbrust", true},      // a word starting a compound
		{"hähnchenbrustfilet", "hähnchen", true}, // and the other way round
		{"vollmilch", "milch", true},             // a compound covers its generic word
		{"milch", "vollmilch", true},             // the generic word covers the compound
		{"eier", "ei", true},                     // plurals
		{"tomaten", "tomate", true},
		{"eggs", "egg", true},
		{"молоко", "молока", true}, // Russian endings
		{"яйца", "яйцо", true},
		{"chicken", "chicken breast", true}, // a word of a longer name
		{"red onions", "onion", true},
		{"egg", "eggplant", false}, // a short word does not start a compound
		{"tea", "steak", false},
		{"сок", "соль", false},
		{"butter", "zucker", false},
		{"reis", "mais", false},
		{"", "milch", false},
	} {
		if got := Covers(c.have, c.need); got != c.want {
			t.Errorf("Covers(%q, %q) = %v, want %v", c.have, c.need, got, c.want)
		}
	}
	if !NamesMatch("koriander", "frischer koriander") || NamesMatch("koriander", "petersilie") {
		t.Fatal("NamesMatch")
	}
	for _, name := range []string{"salz", "Salt", "wasser", "вода", "olive oil", "Öl", "salz und pfeffer"} {
		want := name != "olive oil"
		if got := Basic(Normalize(name)); got != want {
			t.Errorf("Basic(%q) = %v", name, got)
		}
	}
}

func TestInferMethods(t *testing.T) {
	for _, c := range []struct {
		title string
		tags  []string
		steps string
		want  []string
	}{
		{"Air Fryer Chicken Wings", nil, "Preheat the air fryer to 200 °C.", []string{"air_fryer"}},
		{"Crispy wings", []string{"airfryer"}, "", []string{"air_fryer"}},
		{"Hähnchen aus der Heißluftfritteuse", nil, "", []string{"air_fryer"}},
		{"Курица в аэрогриле", nil, "Запекать в аэрогриле 20 минут.", []string{"air_fryer"}},
		// The air fryer's words are not also frying or the grill.
		{"Air-fried potatoes", nil, "Air fry until golden.", []string{"air_fryer"}},
		{"Lasagne", nil, "Im Backofen bei 180 °C backen.", []string{"oven"}},
		{"Gemüsepfanne", nil, "Das Gemüse in der Pfanne anbraten.", []string{"stovetop"}},
		{"Борщ", nil, "Варить в кастрюле 40 минут.", []string{"stovetop"}},
		{"Pulled pork", []string{"slow cooker"}, "Cook on low for 8 hours.", []string{"slow_cooker"}},
		{"Шашлык", nil, "Жарить на мангале.", []string{"stovetop", "grill"}},
		{"Overnight oats", []string{"no cook"}, "Mix and refrigerate.", []string{"no_cook"}},
		{"Kartoffelsalat", nil, "Kartoffeln kochen, dann ohne Backen servieren.", []string{"stovetop", "no_cook"}},
		{"Roast chicken", nil, "Roast in the oven, then rest. Make gravy in a pan.", []string{"oven", "stovetop"}},
		{"Mug cake", nil, "Microwave for 90 seconds.", []string{"microwave"}},
		{"Toast", nil, "", []string{}},
	} {
		if got := InferMethods(c.title, c.tags, c.steps); !reflect.DeepEqual(got, c.want) {
			t.Errorf("InferMethods(%q) = %v, want %v", c.title, got, c.want)
		}
	}
}

func day(text string) time.Time {
	value, err := time.Parse(time.DateOnly, text)
	if err != nil {
		panic(err)
	}
	return value
}

func ingredients(names ...string) []RecipeIngredient {
	out := make([]RecipeIngredient, 0, len(names))
	for _, name := range names {
		out = append(out, RecipeIngredient{Name: name, Normalized: Normalize(name)})
	}
	return out
}

func stocked(name, expires string) StockedItem {
	item := StockedItem{ID: name, Name: name, Normalized: Normalize(name)}
	if expires != "" {
		item.ExpiresOn = day(expires)
	}
	return item
}

func reasons(ranked Ranked) string {
	out := make([]string, 0, len(ranked.Reasons))
	for _, reason := range ranked.Reasons {
		out = append(out, reason.Text)
	}
	return strings.Join(out, "; ")
}

func TestRankExplainsTheScore(t *testing.T) {
	home := Household{
		Today: day("2026-10-11"),
		Stocked: []StockedItem{
			stocked("Hähnchenbrust", "2026-10-12"), stocked("Paprika", ""), stocked("Reis", ""), stocked("Sahne", "2026-10-13"),
			stocked("Joghurt", "2026-10-09"), // expired: covered, but no bonus
		},
		Bought:    []string{"hähnchenbrust", "paprika"},
		Preferred: []string{"air_fryer"},
	}
	recipe := RecipeFacts{Title: "Hähnchen mit Reis", NormalizedTitle: "hähnchen mit reis",
		Ingredients: ingredients("Hähnchen", "Paprika", "Reis", "Sahne", "Zwiebel", "Salz"),
		Methods:     []string{"air_fryer", "stovetop"},
		Planned:     []PlannedMeal{{Day: day("2026-09-20"), Slot: "dinner"}, {Day: day("2026-10-01"), Slot: "dinner"}, {Day: day("2026-10-04"), Slot: "lunch"}},
	}
	ranked := Rank(recipe, home)
	want := "You have 4 of its 5 ingredients; Hähnchenbrust expires tomorrow, Sahne expires in 2 days; Air fryer, your preferred way; " +
		"You had it 3 times in the last 60 days; You bought 2 of its ingredients lately"
	if got := reasons(ranked); got != want {
		t.Fatalf("reasons = %s\nwant      %s", got, want)
	}
	// 32 coverage + 24 expiring + 10 air fryer + 9 planned + 3 bought.
	if ranked.Score != 32+24+10+9+3 {
		t.Fatalf("score = %d (%+v)", ranked.Score, ranked.Reasons)
	}
	if len(ranked.Missing) != 1 || ranked.Missing[0].Name != "Zwiebel" || len(ranked.Basics) != 1 || ranked.Basics[0].Name != "Salz" {
		t.Fatalf("missing = %+v, basics = %+v", ranked.Missing, ranked.Basics)
	}
	if ranked.Have[0].Item.Name != "Hähnchenbrust" || !ranked.Have[0].Expiring || ranked.Have[0].Days != 1 {
		t.Fatalf("have = %+v", ranked.Have[0])
	}
}

func TestRankHabitsAreCappedAndRecentMealsLose(t *testing.T) {
	home := Household{Today: day("2026-10-11"), Stocked: []StockedItem{stocked("Nudeln", "")}}
	plain := RecipeFacts{Title: "Nudeln", NormalizedTitle: "nudeln", Ingredients: ingredients("Nudeln")}
	base := Rank(plain, home).Score
	many := plain
	for _, d := range []string{"2026-08-20", "2026-08-27", "2026-09-03", "2026-09-10", "2026-09-17", "2026-09-24"} {
		many.Planned = append(many.Planned, PlannedMeal{Day: day(d), Slot: "dinner"})
	}
	// Six times counts as three: exact repeats are not rewarded further.
	if got := Rank(many, home).Score - base; got != 9 {
		t.Fatalf("six meals add %d", got)
	}
	old := plain
	old.Planned = []PlannedMeal{{Day: day("2026-08-01")}} // before the 60 days
	if got := Rank(old, home).Score; got != base {
		t.Fatalf("an old meal changed the score: %d", got)
	}
	recent := plain
	recent.Planned = []PlannedMeal{{Day: day("2026-10-09"), Slot: "dinner"}}
	ranked := Rank(recent, home)
	if ranked.Score != base+4-10 || !strings.Contains(reasons(ranked), "Planned 2 days ago") {
		t.Fatalf("recent = %d %s", ranked.Score, reasons(ranked))
	}
	upcoming := plain
	upcoming.Planned = []PlannedMeal{{Day: day("2026-10-12"), Slot: "dinner"}}
	if got := reasons(Rank(upcoming, home)); !strings.Contains(got, "Already planned for tomorrow") {
		t.Fatalf("upcoming = %s", got)
	}
}

func TestRankRulesOutDislikesAndWeighsTheSlot(t *testing.T) {
	home := Household{Today: day("2026-10-11"), Dislikes: []string{"koriander"}, Slot: "breakfast"}
	curry := RecipeFacts{Title: "Curry", NormalizedTitle: "curry", Ingredients: ingredients("Reis", "Frischer Koriander")}
	if got := Rank(curry, home); got.Disliked != "Frischer Koriander" || got.Score != 0 || got.Reasons != nil {
		t.Fatalf("curry = %+v", got)
	}
	pesto := RecipeFacts{Title: "Koriander-Pesto", NormalizedTitle: Normalize("Koriander-Pesto")}
	if got := Rank(pesto, home); got.Disliked != "koriander" {
		t.Fatalf("pesto = %+v", got)
	}
	porridge := RecipeFacts{Title: "Porridge", NormalizedTitle: "porridge", Tags: []string{"frühstück"}}
	if got := Rank(porridge, home); got.Score != SlotPoints || reasons(got) != "Fits breakfast" {
		t.Fatalf("porridge = %+v", got)
	}
	stew := RecipeFacts{Title: "Stew", NormalizedTitle: "stew", Tags: []string{"ужин"}}
	if got := Rank(stew, home); got.Score != OtherSlotPenalty || reasons(got) != "Usually dinner" {
		t.Fatalf("stew = %+v", got)
	}
	// No preferred method, no method reason; without ingredients no coverage.
	if got := Rank(RecipeFacts{Title: "Toast", NormalizedTitle: "toast", Methods: []string{"oven"}}, Household{Today: home.Today}); got.Score != 0 || got.Reasons != nil {
		t.Fatalf("toast = %+v", got)
	}
}
