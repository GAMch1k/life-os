package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/services/recipe-import/internal/api/generated"
	"github.com/GAMch1k/life-os/services/recipe-import/internal/fetch"
	"github.com/GAMch1k/life-os/services/recipe-import/internal/food"
)

// These tests serve the parser's fixture pages from a local server the
// fetcher is allowed to reach (as the verification stack's fixture host
// is), and stand in for Food and Activity. Nothing reaches the internet.

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// foodServer stands in for Food's POST and GET /v1/recipes.
type foodServer struct {
	mu       sync.Mutex
	requests []*http.Request
	bodies   []map[string]any
	// answer is the status and body of the next POST.
	status int
	answer string
	// existing is what GET /v1/recipes lists.
	existing []map[string]any
}

func (f *foodServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r)
	f.bodies = append(f.bodies, body)
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodGet {
		_ = json.NewEncoder(w).Encode(map[string]any{"items": f.existing})
		return
	}
	w.WriteHeader(f.status)
	_, _ = w.Write([]byte(f.answer))
}

func recipeJSON(id, title string) string {
	return `{"id":"` + id + `","title":"` + title + `","normalizedTitle":"` + strings.ToLower(title) + `","servings":4,"ingredients":[{"name":"Spaghetti","normalizedName":"spaghetti","amount":"500 g","note":null}],"steps":"Kochen.","tags":["pasta"],"sourceUrl":"https://example.org/x","note":null,"createdAt":"2026-10-12T10:00:00Z","updatedAt":"2026-10-12T10:00:00Z","createdBy":{"kind":"system","name":"recipe-import"},"updatedBy":{"kind":"system","name":"recipe-import"}}`
}

type activityRecorder struct {
	mu     sync.Mutex
	events []activity.Event
}

func (a *activityRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var event activity.Event
	_ = json.NewDecoder(r.Body).Decode(&event)
	a.mu.Lock()
	a.events = append(a.events, event)
	a.mu.Unlock()
	w.WriteHeader(http.StatusCreated)
}

type harness struct {
	t        *testing.T
	app      *httpserver.App
	pages    *httptest.Server
	pageHits int
	food     *foodServer
	foodURL  string
	activity *activityRecorder
}

const recipeID = "7f8d2b1e-1c1a-4b7e-9a51-2f0c6d1e9a10"

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, food: &foodServer{status: http.StatusCreated, answer: recipeJSON(recipeID, "Spaghetti Bolognese")}, activity: &activityRecorder{}}
	h.pages = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.pageHits++
		name := strings.TrimPrefix(r.URL.Path, "/")
		page, err := os.ReadFile(filepath.Join("..", "recipe", "testdata", name+".html"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(page)
	}))
	t.Cleanup(h.pages.Close)
	foodServer := httptest.NewServer(h.food)
	t.Cleanup(foodServer.Close)
	h.foodURL = foodServer.URL
	activityServer := httptest.NewServer(h.activity)
	t.Cleanup(activityServer.Close)
	// The page server listens on 127.0.0.1, allowed like a fixture host.
	fetcher := fetch.New(1<<20, 2*time.Second, []string{"127.0.0.1"})
	h.app = httpserver.New("recipe-import", httpserver.WithLogger(httpserver.NewJSONLogger(io.Discard, nil)))
	Register(h.app, NewHandler(fetcher, food.New(h.foodURL), activity.NewEmitter(activityServer.URL, "recipe-import", quiet), quiet))
	return h
}

func (h *harness) page(name string) string { return h.pages.URL + "/" + name }

func (h *harness) do(target string, body any, headers ...string) (int, []byte) {
	h.t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, target, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(activity.ActorHeader, "user")
	for index := 0; index+1 < len(headers); index += 2 {
		req.Header.Set(headers[index], headers[index+1])
	}
	resp, err := h.app.Test(req, fiber.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true})
	if err != nil {
		h.t.Fatal(err)
	}
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func expect[T any](h *harness, target string, body any, want int, headers ...string) T {
	h.t.Helper()
	status, raw := h.do(target, body, headers...)
	if status != want {
		h.t.Fatalf("POST %s: %d %s, want %d", target, status, raw, want)
	}
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		h.t.Fatalf("POST %s: %v in %s", target, err, raw)
	}
	return out
}

func (h *harness) expectError(target string, body any, status int, code string, headers ...string) {
	h.t.Helper()
	got, raw := h.do(target, body, headers...)
	var envelope generated.ErrorResponse
	_ = json.Unmarshal(raw, &envelope)
	if got != status || envelope.Error.Code != code {
		h.t.Fatalf("POST %s: %d %s, want %d %s", target, got, raw, status, code)
	}
}

func TestPreviewReadsAStructuredRecipe(t *testing.T) {
	h := newHarness(t)
	link := h.page("chefkoch")
	got := expect[generated.ImportPreview](h, "/v1/imports/preview", map[string]any{"url": link}, http.StatusOK)
	draft := got.Draft
	if got.Method != generated.Structured || got.Reason != nil || draft.Title != "Spaghetti Bolognese" || draft.Servings == nil || *draft.Servings != 4 ||
		len(draft.Ingredients) != 5 || *draft.Ingredients[0].Amount != "500 g" || draft.Ingredients[0].Name != "Spaghetti" ||
		len(draft.Steps) != 4 || *draft.SourceUrl != link || *draft.SiteName != "Chefkoch.de" {
		t.Fatalf("preview = %+v", got)
	}
	if len(h.food.requests) != 0 {
		t.Fatal("a preview called Food")
	}
}

func TestPreviewOfPagesWithoutARecipe(t *testing.T) {
	h := newHarness(t)
	social := expect[generated.ImportPreview](h, "/v1/imports/preview", map[string]any{"url": h.page("instagram")}, http.StatusOK)
	if social.Method != generated.Preview || social.Reason == nil || len(social.Draft.Tags) != 1 || social.Draft.Tags[0] != "to-review" || social.Draft.ImageUrl == nil {
		t.Fatalf("preview = %+v", social)
	}
	missing := expect[generated.ImportPreview](h, "/v1/imports/preview", map[string]any{"url": h.page("gone")}, http.StatusOK)
	if missing.Method != generated.Failed || missing.Reason == nil || *missing.Reason != "The page answered HTTP 404." || *missing.Draft.SourceUrl != h.page("gone") {
		t.Fatalf("missing = %+v", missing)
	}
	for _, link := range []string{"ftp://example.org/x", "http://10.0.0.1/recipe", "http://169.254.169.254/latest", "not a link", ""} {
		h.expectError("/v1/imports/preview", map[string]any{"url": link}, http.StatusBadRequest, "invalid_request")
	}
	h.expectError("/v1/imports/preview", map[string]any{"url": h.page("chefkoch")}, http.StatusBadRequest, "actor_required", activity.ActorHeader, "")
}

func TestPastedTextGivesADraft(t *testing.T) {
	h := newHarness(t)
	got := expect[generated.ImportPreview](h, "/v1/imports/text", map[string]any{
		"text": "Pasta\nZutaten:\n250 g Pasta\n1 Dose Tomaten\nZubereitung:\n1. Kochen.\n2. Mischen.\n#quick", "sourceUrl": "https://www.instagram.com/p/X/",
	}, http.StatusOK)
	if got.Method != generated.Text || got.Draft.Title != "Pasta" || len(got.Draft.Ingredients) != 2 || len(got.Draft.Steps) != 2 ||
		*got.Draft.SourceUrl != "https://www.instagram.com/p/X/" || got.Draft.Tags[0] != "quick" {
		t.Fatalf("text = %+v", got)
	}
	h.expectError("/v1/imports/text", map[string]any{"text": "  "}, http.StatusBadRequest, "invalid_request")
	h.expectError("/v1/imports/text", map[string]any{"text": strings.Repeat("x", 20001)}, http.StatusBadRequest, "invalid_request")
	h.expectError("/v1/imports/text", map[string]any{"text": "x", "sourceUrl": "file:///etc/passwd"}, http.StatusBadRequest, "invalid_request")
}

func TestImportSavesInFoodAsTheIntegration(t *testing.T) {
	h := newHarness(t)
	link := h.page("chefkoch")
	got := expect[generated.ImportResult](h, "/v1/imports", map[string]any{"url": link}, http.StatusCreated,
		"Idempotency-Key", "save-1", "X-Correlation-ID", "corr-1")
	if got.Method != generated.Structured || got.Recipe.Id.String() != recipeID || got.Recipe.Title != "Spaghetti Bolognese" {
		t.Fatalf("result = %+v", got)
	}
	if len(h.food.requests) != 1 {
		t.Fatalf("%d Food requests", len(h.food.requests))
	}
	request, body := h.food.requests[0], h.food.bodies[0]
	if request.Method != http.MethodPost || request.URL.Path != "/v1/recipes" || request.Header.Get(activity.ActorHeader) != "system:recipe-import" ||
		request.Header.Get("Idempotency-Key") != "recipe-import:save-1" || request.Header.Get(activity.CorrelationIDHeader) != "corr-1" {
		t.Fatalf("Food request = %s %s %v", request.Method, request.URL, request.Header)
	}
	if body["title"] != "Spaghetti Bolognese" || body["servings"] != float64(4) || body["sourceUrl"] != link ||
		body["steps"] != "Die Zwiebeln würfeln und in Olivenöl anbraten.\nDas Hackfleisch dazugeben und krümelig braten.\nTomaten zugeben und 30 Minuten köcheln lassen.\nMit den Spaghetti servieren." ||
		body["note"] != "Klassisch und einfach & schnell.\nImage: https://img.chefkoch-cdn.de/rezepte/123/bilder/1.jpg" {
		t.Fatalf("Food body = %v", body)
	}
	ingredients := body["ingredients"].([]any)
	if len(ingredients) != 5 || ingredients[0].(map[string]any)["amount"] != "500 g" || ingredients[4].(map[string]any)["amount"] != nil {
		t.Fatalf("ingredients = %v", ingredients)
	}
	// One import event, by the owner, which caused Food's.
	h.activity.mu.Lock()
	events := append([]activity.Event(nil), h.activity.events...)
	h.activity.mu.Unlock()
	if len(events) != 1 {
		t.Fatalf("events = %+v", events)
	}
	event := events[0]
	if event.Type != "food.recipe.imported" || event.Source != "recipe-import" || event.Actor != activity.User() ||
		event.Subject != (activity.Subject{Type: "food_recipe", ID: recipeID}) || event.CorrelationID != "corr-1" ||
		event.ID != activity.EventID("food.recipe.imported", link, "save-1") || request.Header.Get(activity.CausationIDHeader) != event.ID.String() ||
		event.Summary != "Imported the recipe “Spaghetti Bolognese” from 127.0.0.1" || event.Data["method"] != "structured" {
		t.Fatalf("event = %+v", event)
	}
}

func TestImportOfAnEditedDraftDoesNotFetchAgain(t *testing.T) {
	h := newHarness(t)
	got := expect[generated.ImportResult](h, "/v1/imports", map[string]any{"url": "https://www.instagram.com/p/ABC/", "overrides": map[string]any{
		"title": " Creamy tomato pasta ", "servings": 2,
		"ingredients": []any{map[string]any{"name": "Pasta", "amount": "250 g"}, map[string]any{"name": " "}},
		"steps":       []any{"Kochen.", "", "Mischen."}, "tags": []any{"to-review", "pasta", strings.Repeat("x", 41)},
		"imageUrl": "https://scontent.cdninstagram.com/1.jpg",
	}}, http.StatusCreated)
	if got.Method != generated.Edited || h.pageHits != 0 {
		t.Fatalf("result = %+v, page hits %d", got, h.pageHits)
	}
	body := h.food.bodies[0]
	if body["title"] != "Creamy tomato pasta" || body["steps"] != "Kochen.\nMischen." || body["sourceUrl"] != "https://www.instagram.com/p/ABC/" ||
		len(body["ingredients"].([]any)) != 1 || len(body["tags"].([]any)) != 2 || body["note"] != "Image: https://scontent.cdninstagram.com/1.jpg" {
		t.Fatalf("Food body = %v", body)
	}
	h.expectError("/v1/imports", map[string]any{"url": "https://example.org/x", "overrides": map[string]any{"title": " ", "ingredients": []any{}, "steps": []any{}, "tags": []any{}}}, http.StatusBadRequest, "invalid_request")
	h.expectError("/v1/imports", map[string]any{"url": "https://example.org/x", "overrides": map[string]any{"title": "x", "servings": 0, "ingredients": []any{}, "steps": []any{}, "tags": []any{}}}, http.StatusBadRequest, "invalid_request")
}

func TestImportRefusals(t *testing.T) {
	h := newHarness(t)
	// Nothing to save.
	h.expectError("/v1/imports", map[string]any{"url": h.page("empty")}, http.StatusUnprocessableEntity, "import_failed")
	h.expectError("/v1/imports", map[string]any{"url": h.page("gone")}, http.StatusUnprocessableEntity, "import_failed")
	if len(h.food.requests) != 0 {
		t.Fatal("a failed import called Food")
	}

	// Food already has the title: its id comes back.
	h.food.status, h.food.answer = http.StatusConflict, `{"error":{"code":"recipe_exists","message":"A recipe called “Spaghetti Bolognese” already exists"},"request_id":"r"}`
	h.food.existing = []map[string]any{{"id": "11111111-1111-4111-8111-111111111111", "normalizedTitle": "spaghetti bolognese salad"}, {"id": recipeID, "normalizedTitle": "spaghetti bolognese"}}
	status, raw := h.do("/v1/imports", map[string]any{"url": h.page("chefkoch")})
	var exists generated.RecipeExistsError
	_ = json.Unmarshal(raw, &exists)
	if status != http.StatusConflict || exists.Error.Code != "recipe_exists" || exists.RecipeId == nil || exists.RecipeId.String() != recipeID || exists.SourceUrl != nil {
		t.Fatalf("exists: %d %s", status, raw)
	}
	// With the link it was saved from, so a caller tells this recipe saved
	// before from another of the same name.
	h.food.existing = []map[string]any{{"id": recipeID, "normalizedTitle": "spaghetti bolognese", "sourceUrl": "https://www.chefkoch.de/rezepte/1"}}
	status, raw = h.do("/v1/imports", map[string]any{"url": h.page("chefkoch")})
	exists = generated.RecipeExistsError{}
	_ = json.Unmarshal(raw, &exists)
	if status != http.StatusConflict || exists.SourceUrl == nil || *exists.SourceUrl != "https://www.chefkoch.de/rezepte/1" {
		t.Fatalf("exists with a link: %d %s", status, raw)
	}
	if lookup := h.food.requests[len(h.food.requests)-1]; lookup.Method != http.MethodGet || lookup.URL.Query().Get("q") != "Spaghetti Bolognese" {
		t.Fatalf("lookup = %s %s", lookup.Method, lookup.URL)
	}

	// Food refuses the recipe, or cannot be reached.
	h.food.status, h.food.answer = http.StatusBadRequest, `{"error":{"code":"invalid_request","message":"title must contain a letter or a digit"},"request_id":"r"}`
	h.expectError("/v1/imports", map[string]any{"url": h.page("chefkoch")}, http.StatusBadRequest, "invalid_request")
	h.food.status, h.food.answer = http.StatusConflict, `{"error":{"code":"idempotency_conflict","message":"x"},"request_id":"r"}`
	h.expectError("/v1/imports", map[string]any{"url": h.page("chefkoch")}, http.StatusConflict, "idempotency_conflict", "Idempotency-Key", "k")
	h.food.status, h.food.answer = http.StatusServiceUnavailable, `{}`
	h.expectError("/v1/imports", map[string]any{"url": h.page("chefkoch")}, http.StatusBadGateway, "food_unavailable")
	h.expectError("/v1/imports", map[string]any{"url": h.page("chefkoch")}, http.StatusBadRequest, "invalid_request", "Idempotency-Key", strings.Repeat("k", 151))
	h.activity.mu.Lock()
	defer h.activity.mu.Unlock()
	if len(h.activity.events) != 0 {
		t.Fatalf("refusals recorded %+v", h.activity.events)
	}
}
