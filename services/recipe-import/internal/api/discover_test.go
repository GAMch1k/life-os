package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/services/recipe-import/internal/api/generated"
	"github.com/GAMch1k/life-os/services/recipe-import/internal/fetch"
	"github.com/GAMch1k/life-os/services/recipe-import/internal/food"
	"github.com/GAMch1k/life-os/services/recipe-import/internal/mealdb"
)

// mealDBFixtures serves TheMealDB's recorded answers (../mealdb/testdata)
// on a local, allowlisted host, as the verification stack's fixture server
// would; hits counts the requests.
func mealDBFixtures(t *testing.T, hits *int) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*hits++
		name := strings.TrimSuffix(filepath.Base(r.URL.Path), ".php") + "_" + r.URL.Query().Get("i") + ".json"
		body, err := os.ReadFile(filepath.Join("..", "mealdb", "testdata", name))
		if err != nil {
			body, _ = os.ReadFile(filepath.Join("..", "mealdb", "testdata", "filter_none.json"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	return server
}

// withDiscovery is the harness with TheMealDB discovery at server.
func (h *harness) withDiscovery(server *httptest.Server) {
	h.t.Helper()
	parsed, _ := url.Parse(server.URL)
	fetcher := fetch.New(1<<20, 2*time.Second, []string{parsed.Hostname()})
	client, err := mealdb.New(server.URL+"/api/json/v1/1/", fetcher, time.Hour, []string{parsed.Hostname()})
	if err != nil {
		h.t.Fatal(err)
	}
	h.app = httpserver.New("recipe-import", httpserver.WithLogger(httpserver.NewJSONLogger(io.Discard, nil)))
	Register(h.app, NewHandler(fetcher, food.New(h.foodURL), nil, quiet).WithDiscovery(client))
}

func (h *harness) get(target string) (int, []byte) {
	h.t.Helper()
	resp, err := h.app.Test(httptest.NewRequest(http.MethodGet, target, nil), fiber.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true})
	if err != nil {
		h.t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

func TestDiscoverFindsDraftsInTheMealDB(t *testing.T) {
	h := newHarness(t)
	hits := 0
	server := mealDBFixtures(t, &hits)
	h.withDiscovery(server)
	// A read: no actor needed.
	status, raw := h.get("/v1/discover?ingredient=" + url.QueryEscape("Hähnchenbrust") + "&limit=2")
	var found generated.DiscoverResult
	if err := json.Unmarshal(raw, &found); status != http.StatusOK || err != nil {
		t.Fatalf("discover: %d %s", status, raw)
	}
	if found.Ingredient != "Hähnchenbrust" || found.Query != "chicken breast" || found.Provider != "TheMealDB" || found.Attribution != mealdb.Attribution || len(found.Items) != 2 {
		t.Fatalf("found = %+v", found)
	}
	draft := found.Items[0]
	if draft.Title != "Belgian Waterzooi Chicken" || *draft.SourceUrl != "https://www.themealdb.com/meal/53466" || *draft.SiteName != "TheMealDB" ||
		len(draft.Ingredients) == 0 || *draft.Ingredients[0].Amount != "1" || !strings.HasPrefix(*draft.Description, "Recipe data and imagery: TheMealDB") {
		t.Fatalf("draft = %+v", draft)
	}
	// Asked again: answered from the cache.
	h.get("/v1/discover?ingredient=chicken%20breast&limit=2")
	if hits != 3 {
		t.Fatalf("%d requests to TheMealDB", hits)
	}

	// Saving a draft goes through the import, with the TheMealDB page as the
	// link; nothing is fetched again, and the note credits TheMealDB.
	saved := expect[generated.ImportResult](h, "/v1/imports", map[string]any{"url": *draft.SourceUrl, "overrides": draft}, http.StatusCreated)
	body := h.food.bodies[0]
	if saved.Method != generated.Edited || h.pageHits != 0 || hits != 3 || body["sourceUrl"] != "https://www.themealdb.com/meal/53466" ||
		!strings.HasPrefix(body["note"].(string), "Recipe data and imagery: TheMealDB (https://www.themealdb.com/). Original: https://junedarville.com/waterzooi.html") {
		t.Fatalf("saved = %+v, Food body = %v", saved, body)
	}
}

func TestDiscoverRefusalsAndAnUnreachableProvider(t *testing.T) {
	h := newHarness(t)
	hits := 0
	server := mealDBFixtures(t, &hits)
	h.withDiscovery(server)
	if status, raw := h.get("/v1/discover?ingredient=nonexistentingredientxyz"); status != http.StatusOK || !strings.Contains(string(raw), `"items":[]`) {
		t.Fatalf("no meals: %d %s", status, raw)
	}
	for _, target := range []string{"/v1/discover?ingredient=chicken&limit=13", "/v1/discover?ingredient=%20%20", "/v1/discover?ingredient=" + url.QueryEscape("!!")} {
		if status, raw := h.get(target); status != http.StatusBadRequest {
			t.Errorf("%s: %d %s", target, status, raw)
		}
	}
	if status, _ := h.get("/v1/discover"); status != http.StatusBadRequest {
		t.Errorf("no ingredient: %d", status)
	}
	server.Close()
	status, raw := h.get("/v1/discover?ingredient=beef")
	var envelope generated.ErrorResponse
	_ = json.Unmarshal(raw, &envelope)
	if status != http.StatusBadGateway || envelope.Error.Code != "provider_unavailable" || !strings.HasPrefix(envelope.Error.Message, "TheMealDB could not be reached") {
		t.Fatalf("unreachable: %d %s", status, raw)
	}
	// Without discovery set up, it says so.
	plain := newHarness(t)
	plain.app = httpserver.New("recipe-import", httpserver.WithLogger(httpserver.NewJSONLogger(io.Discard, nil)))
	Register(plain.app, NewHandler(fetch.New(0, 0, nil), food.New(plain.foodURL), activity.NewEmitter("", "recipe-import", quiet), quiet))
	if status, _ := plain.get("/v1/discover?ingredient=beef"); status != http.StatusBadGateway {
		t.Fatalf("not set up: %d", status)
	}
}
