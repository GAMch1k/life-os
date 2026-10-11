package mealdb

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GAMch1k/life-os/services/recipe-import/internal/fetch"
	"github.com/GAMch1k/life-os/services/recipe-import/internal/recipe"
)

// The fixtures in testdata were recorded once from TheMealDB's API
// (filter.php?i=chicken_breast, three lookups, and a search with no meals).
// Nothing here reaches the internet.

// fixtures answers TheMealDB's requests from testdata and records them.
type fixtures struct {
	mu       sync.Mutex
	requests []string
	fail     error
}

func fixtureName(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	endpoint := strings.TrimSuffix(filepath.Base(parsed.Path), ".php")
	return endpoint + "_" + parsed.Query().Get("i") + ".json"
}

func (f *fixtures) FetchJSON(_ context.Context, raw string) ([]byte, error) {
	f.mu.Lock()
	f.requests = append(f.requests, raw)
	f.mu.Unlock()
	if f.fail != nil {
		return nil, f.fail
	}
	body, err := os.ReadFile(filepath.Join("testdata", fixtureName(raw)))
	if err != nil {
		return os.ReadFile(filepath.Join("testdata", "filter_none.json"))
	}
	return body, nil
}

func (f *fixtures) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func newClient(t *testing.T, get Getter) *Client {
	t.Helper()
	client, err := New("", get, time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestDiscoverReadsMealsByIngredient(t *testing.T) {
	get := &fixtures{}
	found, err := newClient(t, get).Discover(context.Background(), "Hähnchenbrust", 2)
	if err != nil {
		t.Fatal(err)
	}
	if found.Query != "chicken breast" || len(found.Drafts) != 2 {
		t.Fatalf("found = %q, %d drafts", found.Query, len(found.Drafts))
	}
	first := found.Drafts[0]
	if first.Title != "Belgian Waterzooi Chicken" || first.SourceURL != "https://www.themealdb.com/meal/53466" || first.SiteName != "TheMealDB" ||
		!strings.HasPrefix(first.ImageURL, "https://www.themealdb.com/images/") || !reflect.DeepEqual(first.Tags, []string{"Chicken"}) {
		t.Fatalf("first = %+v", first)
	}
	if first.Ingredients[0] != (recipe.Ingredient{Name: "Chicken Legs", Amount: "1"}) || first.Ingredients[2] != (recipe.Ingredient{Name: "Water", Amount: "3 Cups"}) {
		t.Fatalf("ingredients = %+v", first.Ingredients)
	}
	if first.Description != Attribution+". Original: https://junedarville.com/waterzooi.html" {
		t.Fatalf("description = %q", first.Description)
	}
	if len(first.Steps) < 2 || !strings.HasPrefix(first.Steps[0], "Put the chicken, water and one garlic clove") {
		t.Fatalf("steps = %q", first.Steps)
	}
	// Only TheMealDB's host was asked: the filter, then each meal.
	want := []string{
		"https://www.themealdb.com/api/json/v1/1/filter.php?i=chicken_breast",
		"https://www.themealdb.com/api/json/v1/1/lookup.php?i=53466",
		"https://www.themealdb.com/api/json/v1/1/lookup.php?i=53453",
	}
	if !reflect.DeepEqual(get.requests, want) {
		t.Fatalf("requests = %q", get.requests)
	}
}

func TestDiscoverCachesAnswers(t *testing.T) {
	get := &fixtures{}
	client := newClient(t, get)
	ctx := context.Background()
	if _, err := client.Discover(ctx, "chicken breast", 2); err != nil {
		t.Fatal(err)
	}
	// The same search, also spelled another way, asks nothing again.
	if _, err := client.Discover(ctx, "Chicken Breast", 2); err != nil || get.count() != 3 {
		t.Fatalf("after the second search: %d requests, %v", get.count(), err)
	}
	// One more result asks for one more meal only.
	if found, err := client.Discover(ctx, "chicken_breast", 3); err != nil || get.count() != 4 || found.Drafts[2].Title != "Chick-Fil-A Sandwich" {
		t.Fatalf("a third result: %d requests, %v", get.count(), err)
	}
	// Once the answers are old they are asked for again.
	client.cache.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if _, err := client.Discover(ctx, "chicken breast", 1); err != nil || get.count() != 6 {
		t.Fatalf("after expiry: %d requests, %v", get.count(), err)
	}
}

func TestDiscoverWithoutMealsOrWithoutTheProvider(t *testing.T) {
	found, err := newClient(t, &fixtures{}).Discover(context.Background(), "nonexistentingredientxyz", 0)
	if err != nil || len(found.Drafts) != 0 || found.Query != "nonexistentingredientxyz" {
		t.Fatalf("none = %+v, %v", found, err)
	}
	_, err = newClient(t, &fixtures{fail: errors.New("The site did not answer in time.")}).Discover(context.Background(), "chicken", 0)
	var plain *Error
	if !errors.As(err, &plain) || plain.Message != "TheMealDB could not be reached: The site did not answer in time." {
		t.Fatalf("down = %v", err)
	}
	if _, err := newClient(t, &fixtures{}).Discover(context.Background(), " !! ", 0); !errors.As(err, &plain) {
		t.Fatalf("no ingredient = %v", err)
	}
}

func TestOnlyTheMealDBIsAllowed(t *testing.T) {
	for _, base := range []string{"https://example.com/api/json/v1/1/", "http://127.0.0.1:8080/", "ftp://www.themealdb.com/", "not a url"} {
		if _, err := New(base, &fixtures{}, 0, nil); err == nil {
			t.Errorf("New(%q) accepted", base)
		}
	}
	if _, err := New("https://themealdb.com/api/json/v1/1", &fixtures{}, 0, nil); err != nil {
		t.Errorf("themealdb.com refused: %v", err)
	}
	// The verification stack's fixture host, when it is allowlisted.
	if _, err := New("http://ics-fixture/mealdb/", &fixtures{}, 0, []string{"ics-fixture"}); err != nil {
		t.Errorf("allowlisted fixture host refused: %v", err)
	}
}

func TestDiscoverThroughTheGuardedFetcher(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := os.ReadFile(filepath.Join("testdata", fixtureName(r.URL.String())))
		if err != nil {
			body, _ = os.ReadFile(filepath.Join("testdata", "filter_none.json"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer server.Close()
	parsed, _ := url.Parse(server.URL)
	// The test server stands in for TheMealDB on an allowlisted host, as
	// the verification stack's fixture server would.
	client, err := New(server.URL+"/api/json/v1/1/", fetch.New(1<<20, 2*time.Second, []string{parsed.Hostname()}), 0, []string{parsed.Hostname()})
	if err != nil {
		t.Fatal(err)
	}
	found, err := client.Discover(context.Background(), "Куриная грудка", 1)
	if err != nil || len(found.Drafts) != 1 {
		t.Fatalf("found = %+v, %v", found, err)
	}
	// Without the allowlist the guard refuses the loopback address.
	guarded, err := New(server.URL+"/api/json/v1/1/", fetch.New(1<<20, 2*time.Second, nil), 0, []string{parsed.Hostname()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guarded.Discover(context.Background(), "chicken", 1); err == nil || !strings.Contains(err.Error(), "private, loopback, or link-local") {
		t.Fatalf("guarded = %v", err)
	}
}

func TestQueryNamesTheIngredientInEnglish(t *testing.T) {
	for given, want := range map[string]string{
		"Hähnchenbrust":     "chicken_breast",
		"Hähnchen":          "chicken",
		"курица":            "chicken",
		"Куриное филе":      "chicken_breast",
		"Rinderhackfleisch": "minced_beef",
		"фарш":              "minced_beef",
		"Rindfleisch":       "beef",
		"Schweinefilet":     "pork",
		"Lachs":             "salmon",
		"Eier":              "eggs",
		"Ei":                "eggs",
		"Eisbergsalat":      "eisbergsalat", // "ei" only as a whole word
		"Reis":              "rice",
		"рис":               "rice",
		"Kartoffeln":        "potatoes",
		"картошка":          "potatoes",
		"Nudeln":            "spaghetti",
		"Thunfisch":         "tuna",
		"Chicken Breast":    "chicken_breast",
		"Halloumi":          "halloumi",
		"  ":                "",
	} {
		if got := Query(given); got != want {
			t.Errorf("Query(%q) = %q, want %q", given, got, want)
		}
	}
}

func TestStepsDropHeadings(t *testing.T) {
	got := steps("STEP 1\r\nPound the chicken.\r\n\r\nstep 2:\r\nFry it.\n3\nServe.")
	if !reflect.DeepEqual(got, []string{"Pound the chicken.", "Fry it.", "Serve."}) {
		t.Fatalf("steps = %q", got)
	}
}
