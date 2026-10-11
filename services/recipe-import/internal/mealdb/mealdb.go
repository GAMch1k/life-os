// Package mealdb discovers recipes by main ingredient in TheMealDB
// (https://www.themealdb.com/), a free recipe database: filter by
// ingredient, then look each meal up. It returns drafts in recipe-import's
// draft shape and saves nothing; the owner saves one through the usual
// import. Only TheMealDB's host is requested (an allowlist), through the
// same guarded fetcher as pages, and answers are cached so a search is not
// repeated.
//
// TheMealDB's terms: the test key "1" is for development and educational
// use; results credit TheMealDB as the data and image source and link each
// meal to https://www.themealdb.com/meal/{idMeal}. Its data is English only.
package mealdb

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/GAMch1k/life-os/services/recipe-import/internal/recipe"
)

// DefaultBase is TheMealDB's API with the public test key.
const DefaultBase = "https://www.themealdb.com/api/json/v1/1/"

// Attribution is how results credit TheMealDB.
const Attribution = "Recipe data and imagery: TheMealDB (https://www.themealdb.com/)"

// SiteName names the provider on drafts.
const SiteName = "TheMealDB"

// Hosts are TheMealDB's hosts, the only ones the provider requests.
var Hosts = []string{"www.themealdb.com", "themealdb.com"}

// Limits.
const (
	DefaultResults = 6
	MaxResults     = 12
	DefaultTTL     = 24 * time.Hour
	cacheEntries   = 500
	ingredientMax  = 20
)

// Getter downloads a JSON document (*fetch.Fetcher).
type Getter interface {
	FetchJSON(ctx context.Context, raw string) ([]byte, error)
}

// Client discovers recipes in TheMealDB.
type Client struct {
	base  *url.URL
	get   Getter
	cache *cache
}

// New returns a client for the API at base (DefaultBase when empty). The
// base's host must be one of Hosts, or one of extraHosts: the verification
// stack's fixture host only (RECIPE_IMPORT_ALLOW_HOSTS).
func New(base string, get Getter, ttl time.Duration, extraHosts []string) (*Client, error) {
	if base == "" {
		base = DefaultBase
	}
	parsed, err := url.Parse(base)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
		return nil, fmt.Errorf("mealdb: %q is not an http or https URL", base)
	}
	host := strings.ToLower(parsed.Hostname())
	allowed := false
	for _, known := range append(append([]string{}, Hosts...), extraHosts...) {
		if host == strings.ToLower(strings.TrimSpace(known)) {
			allowed = true
		}
	}
	if !allowed {
		return nil, fmt.Errorf("mealdb: %s is not TheMealDB's host", host)
	}
	if !strings.HasSuffix(parsed.Path, "/") {
		parsed.Path += "/"
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Client{base: parsed, get: get, cache: newCache(ttl)}, nil
}

// Error is a provider failure in plain words, safe to show the owner.
type Error struct{ Message string }

func (e *Error) Error() string { return e.Message }

// Discovery is what a search found.
type Discovery struct {
	// Query is the English ingredient TheMealDB was asked for.
	Query  string
	Drafts []recipe.Draft
}

// meal is one of TheMealDB's meals; strIngredientN and strMeasureN are read
// from the raw map.
type meal map[string]any

func (m meal) text(key string) string {
	value, _ := m[key].(string)
	return strings.TrimSpace(value)
}

type meals struct {
	Meals []meal `json:"meals"`
}

// Discover finds up to limit recipes whose main ingredient is ingredient
// (in English, German, or Russian; see Query), with their details.
func (c *Client) Discover(ctx context.Context, ingredient string, limit int) (Discovery, error) {
	if limit <= 0 {
		limit = DefaultResults
	}
	limit = min(limit, MaxResults)
	query := Query(ingredient)
	if query == "" {
		return Discovery{}, &Error{Message: "Name an ingredient to search for."}
	}
	found := Discovery{Query: strings.ReplaceAll(query, "_", " "), Drafts: []recipe.Draft{}}
	list, err := c.meals(ctx, "filter.php", query)
	if err != nil {
		return found, err
	}
	for _, summary := range list {
		if len(found.Drafts) == limit {
			break
		}
		id := summary.text("idMeal")
		if _, err := strconv.Atoi(id); err != nil {
			continue
		}
		details, err := c.meals(ctx, "lookup.php", id)
		if err != nil {
			return found, err
		}
		if len(details) == 0 {
			continue
		}
		found.Drafts = append(found.Drafts, Draft(details[0]))
	}
	return found, nil
}

// meals asks endpoint for the meals with i=value, from the cache when it
// was asked lately.
func (c *Client) meals(ctx context.Context, endpoint, value string) ([]meal, error) {
	target := c.base.ResolveReference(&url.URL{Path: endpoint, RawQuery: url.Values{"i": {value}}.Encode()})
	if target.Host != c.base.Host {
		return nil, &Error{Message: "TheMealDB could not be asked."}
	}
	key := target.String()
	body, ok := c.cache.get(key)
	if !ok {
		var err error
		if body, err = c.get.FetchJSON(ctx, key); err != nil {
			// The fetcher's errors are plain words.
			return nil, &Error{Message: "TheMealDB could not be reached: " + strings.TrimSuffix(err.Error(), ".") + "."}
		}
		c.cache.put(key, body)
	}
	var answer meals
	if err := json.Unmarshal(body, &answer); err != nil {
		return nil, &Error{Message: "TheMealDB answered something that is not a list of meals."}
	}
	return answer.Meals, nil
}

// Draft is a TheMealDB meal as a recipe draft: its name, ingredients with
// their measures, steps, category, cuisine, and tags, its picture, and its
// TheMealDB page as the link. The description credits TheMealDB and names
// the original source when the meal has one.
func Draft(m meal) recipe.Draft {
	draft := recipe.Draft{Title: m.text("strMeal"), ImageURL: m.text("strMealThumb"), SiteName: SiteName}
	if id := m.text("idMeal"); id != "" {
		draft.SourceURL = "https://www.themealdb.com/meal/" + url.PathEscape(id)
	}
	for index := 1; index <= ingredientMax; index++ {
		name := m.text("strIngredient" + strconv.Itoa(index))
		if name == "" {
			continue
		}
		draft.Ingredients = append(draft.Ingredients, recipe.Ingredient{Name: name, Amount: m.text("strMeasure" + strconv.Itoa(index))})
	}
	draft.Steps = steps(m.text("strInstructions"))
	seen := map[string]bool{}
	for _, tag := range append([]string{m.text("strCategory"), m.text("strArea")}, strings.Split(m.text("strTags"), ",")...) {
		tag = strings.TrimSpace(tag)
		if key := strings.ToLower(tag); tag != "" && !seen[key] {
			seen[key] = true
			draft.Tags = append(draft.Tags, tag)
		}
	}
	description := Attribution + "."
	if source := m.text("strSource"); strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		description += " Original: " + source
	}
	draft.Description = description
	return draft
}

// steps splits TheMealDB's instructions into steps: one per line, without
// empty lines or bare "STEP 1" headings.
func steps(text string) []string {
	out := []string{}
	for _, line := range strings.FieldsFunc(text, func(r rune) bool { return r == '\n' || r == '\r' }) {
		line = strings.TrimSpace(line)
		if line == "" || stepHeading(line) {
			continue
		}
		out = append(out, line)
	}
	return out
}

func stepHeading(line string) bool {
	fields := strings.Fields(strings.ToLower(strings.TrimRight(line, ".:")))
	if len(fields) == 1 {
		_, err := strconv.Atoi(fields[0])
		return err == nil
	}
	if len(fields) != 2 || fields[0] != "step" {
		return false
	}
	_, err := strconv.Atoi(fields[1])
	return err == nil
}

// translations name TheMealDB's English ingredient for a main ingredient
// written in German or Russian (or English). A key matches a word that
// starts with it; a key starting with "=" must be the whole word. The more
// specific keys come first.
var translations = []struct{ key, query string }{
	{"hähnchenbrust", "chicken_breast"}, {"hühnerbrust", "chicken_breast"}, {"chicken breast", "chicken_breast"}, {"куриная грудка", "chicken_breast"}, {"куриное филе", "chicken_breast"},
	{"hähnchenschenkel", "chicken_thighs"}, {"chicken thigh", "chicken_thighs"}, {"куриные бедр", "chicken_thighs"},
	{"hähnchen", "chicken"}, {"hühnchen", "chicken"}, {"huhn", "chicken"}, {"geflügel", "chicken"}, {"куриц", "chicken"}, {"курин", "chicken"}, {"цыпл", "chicken"},
	{"rinderhack", "minced_beef"}, {"hackfleisch", "minced_beef"}, {"gehacktes", "minced_beef"}, {"minced beef", "minced_beef"}, {"фарш", "minced_beef"},
	{"rind", "beef"}, {"говядин", "beef"}, {"говяж", "beef"},
	{"schwein", "pork"}, {"свинин", "pork"}, {"свин", "pork"},
	{"lamm", "lamb"}, {"баранин", "lamb"}, {"ягнят", "lamb"},
	{"ente", "duck"}, {"утк", "duck"}, {"утин", "duck"},
	{"schinken", "ham"}, {"ветчин", "ham"},
	{"speck", "bacon"}, {"бекон", "bacon"},
	{"würstchen", "sausages"}, {"wurst", "sausages"}, {"сосиск", "sausages"}, {"колбас", "sausages"},
	{"lachs", "salmon"}, {"лосос", "salmon"}, {"семг", "salmon"}, {"сёмг", "salmon"},
	{"thunfisch", "tuna"}, {"тунец", "tuna"}, {"тунц", "tuna"},
	{"garnele", "prawns"}, {"shrimp", "prawns"}, {"кревет", "prawns"},
	{"kabeljau", "cod"}, {"треск", "cod"},
	{"makrele", "mackerel"}, {"скумбри", "mackerel"},
	{"fisch", "white_fish"}, {"рыб", "white_fish"},
	{"=reis", "rice"}, {"=рис", "rice"},
	{"süßkartoffel", "sweet_potatoes"}, {"батат", "sweet_potatoes"},
	{"kartoffel", "potatoes"}, {"картоф", "potatoes"}, {"картош", "potatoes"},
	{"=ei", "eggs"}, {"=eier", "eggs"}, {"яйц", "eggs"}, {"яйко", "eggs"},
	{"nudel", "spaghetti"}, {"pasta", "spaghetti"}, {"spaghetti", "spaghetti"}, {"макарон", "spaghetti"}, {"спагетт", "spaghetti"},
	{"tomate", "tomatoes"}, {"помидор", "tomatoes"}, {"томат", "tomatoes"},
	{"zwiebel", "onion"}, {"=лук", "onion"},
	{"pilz", "mushrooms"}, {"champignon", "mushrooms"}, {"гриб", "mushrooms"}, {"шампиньон", "mushrooms"},
	{"mozzarella", "mozzarella"}, {"моцарелл", "mozzarella"},
	{"feta", "feta"}, {"фет", "feta"},
	{"käse", "cheese"}, {"сыр", "cheese"},
	{"kichererbse", "chickpeas"}, {"=нут", "chickpeas"},
	{"linse", "lentils"}, {"чечевиц", "lentils"},
	{"kidneybohne", "kidney_beans"}, {"фасол", "kidney_beans"},
	{"spinat", "spinach"}, {"шпинат", "spinach"},
	{"brokkoli", "broccoli"}, {"брокколи", "broccoli"},
	{"karotte", "carrots"}, {"möhre", "carrots"}, {"морков", "carrots"},
	{"aubergine", "aubergine"}, {"баклажан", "aubergine"},
	{"zucchini", "courgettes"}, {"кабач", "courgettes"},
	{"kohl", "cabbage"}, {"капуст", "cabbage"},
	{"paprika", "red_pepper"}, {"болгарский перец", "red_pepper"},
	{"knoblauch", "garlic"}, {"чеснок", "garlic"},
	{"avocado", "avocado"}, {"авокадо", "avocado"},
	{"zitrone", "lemon"}, {"лимон", "lemon"},
	{"apfel", "apples"}, {"äpfel", "apples"}, {"яблок", "apples"}, {"яблоч", "apples"},
	{"tofu", "tofu"}, {"тофу", "tofu"},
}

// Query is the ingredient as TheMealDB names it: German and Russian main
// ingredients become English ("Hähnchenbrust" is chicken_breast, "фарш"
// minced_beef); anything else is asked for as written, lower case, with
// underscores for spaces ("Chicken Breast" is chicken_breast). Empty when
// the text has no letters.
func Query(ingredient string) string {
	text := strings.ToLower(strings.Join(strings.FieldsFunc(ingredient, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}), " "))
	text = strings.ReplaceAll(text, "ё", "е")
	if text == "" {
		return ""
	}
	padded := " " + text
	for _, entry := range translations {
		key := strings.TrimPrefix(entry.key, "=")
		if strings.HasPrefix(entry.key, "=") {
			if strings.Contains(" "+text+" ", " "+key+" ") {
				return entry.query
			}
			continue
		}
		if strings.Contains(padded, " "+key) {
			return entry.query
		}
	}
	return strings.ReplaceAll(text, " ", "_")
}

// cache keeps TheMealDB's answers for a while, so the same search is not
// sent again; at most cacheEntries, the oldest dropped first.
type cache struct {
	mu      sync.Mutex
	ttl     time.Duration
	now     func() time.Time
	entries map[string]cached
}

type cached struct {
	body    []byte
	expires time.Time
}

func newCache(ttl time.Duration) *cache {
	return &cache{ttl: ttl, now: time.Now, entries: map[string]cached{}}
}

func (c *cache) get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok || !c.now().Before(entry.expires) {
		delete(c.entries, key)
		return nil, false
	}
	return entry.body, true
}

func (c *cache) put(key string, body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if len(c.entries) >= cacheEntries {
		oldest := ""
		for other, entry := range c.entries {
			if !now.Before(entry.expires) {
				delete(c.entries, other)
				continue
			}
			if oldest == "" || entry.expires.Before(c.entries[oldest].expires) {
				oldest = other
			}
		}
		if len(c.entries) >= cacheEntries && oldest != "" {
			delete(c.entries, oldest)
		}
	}
	c.entries[key] = cached{body: body, expires: now.Add(c.ttl)}
}
