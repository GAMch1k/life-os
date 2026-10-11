package api

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	store "github.com/GAMch1k/life-os/services/food/internal/repository/generated"
)

func TestAmountsJoinInOrder(t *testing.T) {
	text := func(value *string) string {
		if value == nil {
			return "<none>"
		}
		return *value
	}
	for _, c := range []struct {
		amounts []string
		want    string
	}{
		{[]string{"200 g", "1 Pck."}, "200 g + 1 Pck."},
		{[]string{"200 g", "200 g"}, "200 g + 200 g"},
		{[]string{" 2 ", "", "  "}, "2"},
		{nil, "<none>"},
		{[]string{""}, "<none>"},
	} {
		if got := text(joinAmounts(c.amounts)); got != c.want {
			t.Errorf("joinAmounts(%q) = %q, want %q", c.amounts, got, c.want)
		}
	}
	// Shortened to the amount limit.
	long := joinAmounts([]string{strings.Repeat("a", 30), strings.Repeat("b", 30)})
	if runes := []rune(*long); len(runes) != amountLimit || runes[len(runes)-1] != '…' {
		t.Fatalf("long amount = %q", *long)
	}
}

func TestIngredientsCollectOneLinePerName(t *testing.T) {
	id := func(b byte) pgtype.UUID { return pgtype.UUID{Bytes: [16]byte{b}, Valid: true} }
	amount := func(text string) *string { return &text }
	pasta := store.Recipe{ID: id(1), Title: "Pasta"}
	chili := store.Recipe{ID: id(2), Title: "Chili"}
	ingredients := map[[16]byte][]store.RecipeIngredient{
		pasta.ID.Bytes: {
			{Name: "Hackfleisch", NormalizedName: "hackfleisch", Amount: amount("400 g")},
			{Name: "Spaghetti", NormalizedName: "spaghetti", Amount: amount("500 g")},
			{Name: "Salz", NormalizedName: "salz"},
		},
		chili.ID.Bytes: {
			{Name: "hackfleisch", NormalizedName: "hackfleisch", Amount: amount("200 g")},
			{Name: "Bohnen", NormalizedName: "bohnen", Amount: amount("1 Dose")},
			{Name: "Salz", NormalizedName: "salz", Amount: amount("1 Prise")},
		},
	}
	needs := collectIngredients([]store.Recipe{pasta, chili}, ingredients)
	var got []string
	for _, need := range needs {
		titles := make([]string, 0, len(need.recipes))
		for _, ref := range need.recipes {
			titles = append(titles, ref.Title)
		}
		got = append(got, need.name+"|"+strings.Join(need.amounts, ",")+"|"+strings.Join(titles, ","))
	}
	want := []string{"Hackfleisch|400 g,200 g|Pasta,Chili", "Spaghetti|500 g|Pasta", "Salz|1 Prise|Pasta,Chili", "Bohnen|1 Dose|Chili"}
	if strings.Join(got, " ; ") != strings.Join(want, " ; ") {
		t.Fatalf("collected %q\nwant %q", got, want)
	}
	if needs[0].firstRecipe != pasta.ID || needs[3].firstRecipe != chili.ID {
		t.Fatal("the first recipe is not kept")
	}
}
