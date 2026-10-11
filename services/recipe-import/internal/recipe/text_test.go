package recipe

import (
	"reflect"
	"testing"
)

func TestACaptionWithHeadings(t *testing.T) {
	caption := `Creamy tomato pasta 🍝
Perfect for busy evenings! Für 2 Personen.

Zutaten:
• 250 g Pasta
• 1 Dose Tomaten
- 100 ml Sahne
- Salz & Pfeffer

Zubereitung:
1. Pasta kochen.
2. Tomaten und Sahne einkochen lassen.
3) Alles mischen und servieren.

#pasta #Vegetarisch #quick`
	result := ParseText(caption, "https://www.instagram.com/p/ABC123/")
	draft := result.Draft
	if result.Method != Text || result.Reason != "" || draft.Title != "Creamy tomato pasta" || draft.Servings != 2 ||
		draft.SourceURL != "https://www.instagram.com/p/ABC123/" {
		t.Fatalf("result = %+v", result)
	}
	if want := []string{"250 g|Pasta", "1 Dose|Tomaten", "100 ml|Sahne", "|Salz & Pfeffer"}; !reflect.DeepEqual(ingredients(draft.Ingredients), want) {
		t.Fatalf("ingredients = %q", ingredients(draft.Ingredients))
	}
	if want := []string{"Pasta kochen.", "Tomaten und Sahne einkochen lassen.", "Alles mischen und servieren."}; !reflect.DeepEqual(draft.Steps, want) {
		t.Fatalf("steps = %q", draft.Steps)
	}
	if !reflect.DeepEqual(draft.Tags, []string{"pasta", "vegetarisch", "quick"}) {
		t.Fatalf("tags = %q", draft.Tags)
	}
}

func TestRussianAndEnglishCaptions(t *testing.T) {
	russian := ParseText(`Сырники на 4 порции
Ингредиенты:
500 г творога
2 яйца
3 ст. л. муки
Приготовление:
Смешать творог с яйцами и мукой.
Обжарить на сковороде с двух сторон.`, "")
	if russian.Draft.Title != "Сырники на 4 порции" || russian.Draft.Servings != 4 ||
		!reflect.DeepEqual(ingredients(russian.Draft.Ingredients), []string{"500 г|творога", "2|яйца", "3 ст. л.|муки"}) ||
		len(russian.Draft.Steps) != 2 {
		t.Fatalf("russian = %+v", russian.Draft)
	}
	// Without headings: amount lines are ingredients, numbered lines steps.
	english := ParseText(`Quick pancakes (serves 3)
2 eggs
250 ml milk
1 cup flour
1. Whisk everything.
2. Fry in a hot pan.`, "")
	if english.Draft.Title != "Quick pancakes (serves 3)" || english.Draft.Servings != 3 ||
		!reflect.DeepEqual(ingredients(english.Draft.Ingredients), []string{"2|eggs", "250 ml|milk", "1 cup|flour"}) ||
		!reflect.DeepEqual(english.Draft.Steps, []string{"Whisk everything.", "Fry in a hot pan."}) {
		t.Fatalf("english = %+v", english.Draft)
	}
	// "Step 1:" is a step, not a heading.
	steps := ParseText("Ingredients\n2 eggs\nMethod\nStep 1: Beat the eggs.\nStep 2: Fry them.", "")
	if !reflect.DeepEqual(steps.Draft.Steps, []string{"Beat the eggs.", "Fry them."}) {
		t.Fatalf("steps = %q", steps.Draft.Steps)
	}
}

func TestTextWithoutARecipe(t *testing.T) {
	result := ParseText("What a lovely day at the beach!\n#summer", "")
	if result.Draft.Title != "What a lovely day at the beach!" || len(result.Draft.Ingredients) != 0 ||
		result.Reason != "No ingredients or steps were recognised: add them yourself." || !reflect.DeepEqual(result.Draft.Tags, []string{"summer"}) {
		t.Fatalf("result = %+v", result)
	}
}
