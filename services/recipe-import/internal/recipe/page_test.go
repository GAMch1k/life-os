package recipe

import (
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func parseFixture(t *testing.T, name, link string) Result {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	return Parse(parsed, body)
}

func ingredients(items []Ingredient) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.Amount+"|"+item.Name)
	}
	return out
}

func TestAJSONLDRecipeInAGraph(t *testing.T) {
	result := parseFixture(t, "chefkoch.html", "https://www.chefkoch.de/rezepte/123/spaghetti-bolognese.html")
	draft := result.Draft
	if result.Method != Structured || result.Reason != "" {
		t.Fatalf("result = %+v", result)
	}
	if draft.Title != "Spaghetti Bolognese" || draft.Servings != 4 || draft.SiteName != "Chefkoch.de" ||
		draft.SourceURL != "https://www.chefkoch.de/rezepte/123/spaghetti-bolognese.html" ||
		draft.ImageURL != "https://img.chefkoch-cdn.de/rezepte/123/bilder/1.jpg" || draft.Description != "Klassisch und einfach & schnell." {
		t.Fatalf("draft = %+v", draft)
	}
	if want := []string{"500 g|Spaghetti", "400 g|Hackfleisch, gemischt", "1 Dose|Tomaten, gehackt", "2 EL|Olivenöl", "|Salz und Pfeffer"}; !reflect.DeepEqual(ingredients(draft.Ingredients), want) {
		t.Fatalf("ingredients = %q", ingredients(draft.Ingredients))
	}
	if len(draft.Steps) != 4 || draft.Steps[0] != "Die Zwiebeln würfeln und in Olivenöl anbraten." || draft.Steps[3] != "Mit den Spaghetti servieren." {
		t.Fatalf("steps = %q", draft.Steps)
	}
	// The category first, then the keywords, normalised.
	if want := []string{"hauptgericht", "pasta", "hauptspeise", "italien"}; !reflect.DeepEqual(draft.Tags, want) {
		t.Fatalf("tags = %q", draft.Tags)
	}
}

func TestHowToSectionsFlattenIntoSteps(t *testing.T) {
	result := parseFixture(t, "bbc.html", "https://www.bbc.co.uk/food/recipes/shepherds_pie_123")
	draft := result.Draft
	if result.Method != Structured || draft.Title != "Shepherd's pie" || draft.Servings != 4 || draft.SiteName != "BBC Food" {
		t.Fatalf("result = %+v", result)
	}
	// A relative image becomes absolute.
	if draft.ImageURL != "https://www.bbc.co.uk/food/images/recipes/shepherds_pie_16x9.jpg" {
		t.Fatalf("image = %q", draft.ImageURL)
	}
	want := []string{"Heat the oil in a large pan & fry the onion.", "Add the mince and brown it.", "Mash the potatoes with the butter.", "Spread over the filling and bake for 25 minutes."}
	if !reflect.DeepEqual(draft.Steps, want) {
		t.Fatalf("steps = %q", draft.Steps)
	}
	// "500g/1lb 2oz" is no simple amount: the whole line is the name.
	if got := ingredients(draft.Ingredients); !reflect.DeepEqual(got, []string{"1 tbsp|olive oil", "|500g/1lb 2oz lamb mince", "1|onion, finely chopped"}) {
		t.Fatalf("ingredients = %q", got)
	}
	if !reflect.DeepEqual(draft.Tags, []string{"british", "comfort food", "winter"}) {
		t.Fatalf("tags = %q", draft.Tags)
	}
}

func TestARecipeAmongNoise(t *testing.T) {
	result := parseFixture(t, "blog.html", "https://annas-kitchen.example/2026/greek-salad")
	draft := result.Draft
	// Invalid JSON, other types, and plain scripts are passed over.
	if result.Method != Structured || draft.Title != "Greek salad" || draft.Servings != 2 || draft.SiteName != "annas-kitchen.example" {
		t.Fatalf("result = %+v", result)
	}
	// Group headings ("For the salad:") are no ingredients.
	if want := []string{"4|tomatoes", "1|cucumber", "200 g|feta", "3 tbsp|olive oil", "1 pinch|salt"}; !reflect.DeepEqual(ingredients(draft.Ingredients), want) {
		t.Fatalf("ingredients = %q", ingredients(draft.Ingredients))
	}
	if want := []string{"Cut the tomatoes and the cucumber.", "Crumble the feta on top.", "Whisk the dressing and pour it over."}; !reflect.DeepEqual(draft.Steps, want) {
		t.Fatalf("steps = %q", draft.Steps)
	}
	if draft.Description != "The salad I make every summer." || !reflect.DeepEqual(draft.Tags, []string{"salad", "side dish"}) {
		t.Fatalf("draft = %+v", draft)
	}
}

func TestMicrodata(t *testing.T) {
	result := parseFixture(t, "microdata.html", "https://pancakes.example/recipe")
	draft := result.Draft
	// The author's name is not the recipe's.
	if result.Method != Structured || draft.Title != "Pancakes" || draft.Servings != 8 || draft.ImageURL != "https://pancakes.example/img/pancakes.jpg" {
		t.Fatalf("result = %+v", result)
	}
	if want := []string{"250 ml|milk", "2|eggs", "150 g|flour"}; !reflect.DeepEqual(ingredients(draft.Ingredients), want) {
		t.Fatalf("ingredients = %q", ingredients(draft.Ingredients))
	}
	if !reflect.DeepEqual(draft.Steps, []string{"Whisk everything together.", "Fry in a hot pan."}) || !reflect.DeepEqual(draft.Tags, []string{"breakfast"}) {
		t.Fatalf("draft = %+v", draft)
	}
}

func TestASocialPostGivesAPreview(t *testing.T) {
	result := parseFixture(t, "instagram.html", "https://www.instagram.com/p/ABC123/")
	draft := result.Draft
	if result.Method != Preview || result.Reason != "Instagram posts can't be read directly: paste the caption to fill in the ingredients and steps." {
		t.Fatalf("result = %+v", result)
	}
	// The preview text is the caption, without the likes and comments.
	if !strings.HasPrefix(draft.Title, "Anna on Instagram: \"Creamy tomato pasta") || draft.SiteName != "Instagram" ||
		draft.ImageURL != "https://scontent.cdninstagram.com/v/t51/123.jpg" || draft.Description != "Creamy tomato pasta 🍝 Zutaten: 250 g Pasta…" ||
		!reflect.DeepEqual(draft.Tags, []string{ReviewTag}) || len(draft.Ingredients) != 0 || draft.SourceURL != "https://www.instagram.com/p/ABC123/" {
		t.Fatalf("draft = %+v", draft)
	}
}

func TestAPageWithNothingFails(t *testing.T) {
	result := parseFixture(t, "empty.html", "https://app.example/recipe/1")
	if result.Method != Failed || result.Reason != "The page has no recipe and no title that could be read." ||
		result.Draft.SourceURL != "https://app.example/recipe/1" || result.Draft.Title != "" {
		t.Fatalf("result = %+v", result)
	}
	// A plain page with a title is a preview.
	link, _ := url.Parse("https://news.example/a")
	if got := Parse(link, []byte(`<html><head><title> A story </title></head></html>`)); got.Method != Preview || got.Draft.Title != "A story" {
		t.Fatalf("plain page = %+v", got)
	}
}

func TestAFetchFailureStillOffersTheCaption(t *testing.T) {
	link, _ := url.Parse("https://www.tiktok.com/@cook/video/1")
	result := FromFetchError(link, "The site would not show the page to Life OS (HTTP 403).")
	if result.Method != Failed || result.Reason != "The site would not show the page to Life OS (HTTP 403). TikTok posts can't be read directly: paste the caption to fill in the ingredients and steps." ||
		result.Draft.SiteName != "TikTok" || !reflect.DeepEqual(result.Draft.Tags, []string{ReviewTag}) {
		t.Fatalf("result = %+v", result)
	}
	other, _ := url.Parse("https://www.example.org/x")
	if got := FromFetchError(other, "The page answered HTTP 404."); got.Reason != "The page answered HTTP 404." || got.Draft.SiteName != "example.org" {
		t.Fatalf("other = %+v", got)
	}
}

func TestLimits(t *testing.T) {
	long := strings.Repeat("x", 300)
	draft := finish(Draft{Title: long, Servings: 500, Ingredients: []Ingredient{{Name: ""}, {Name: long, Amount: long}}, Steps: []string{" ", "a"}})
	if len([]rune(draft.Title)) != titleLimit || draft.Servings != 0 || len(draft.Ingredients) != 1 ||
		len([]rune(draft.Ingredients[0].Amount)) != amountLimit || !reflect.DeepEqual(draft.Steps, []string{"a"}) || draft.Tags == nil {
		t.Fatalf("draft = %+v", draft)
	}
	tags := []string{}
	for _, tag := range []string{"#Vegan", "vegan", "!!", strings.Repeat("y", 41), "a", "b", "c", "d", "e", "f", "g", "h", "i", "j"} {
		tags = addTag(tags, tag)
	}
	if len(tags) != tagsLimit || tags[0] != "vegan" {
		t.Fatalf("tags = %q", tags)
	}
}
