package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GAMch1k/life-os/pkg/activity"
)

const (
	chefkoch  = "https://www.chefkoch.de/rezepte/123/gulasch.html"
	instagram = "https://www.instagram.com/reel/DGx7abc/"
)

// fakeImport is a recipe-import stand-in: previews by link, one answer to
// text reads, and one to saves; it records every request.
type fakeImport struct {
	previews   map[string]string
	text       string
	saveStatus int
	saveBody   string
}

var (
	structuredPreview = `{"method":"structured","reason":null,"draft":{"title":"Omas Gulasch","ingredients":[{"name":"Rindfleisch","amount":"1 kg"}],"steps":["Anbraten."],"tags":["eintopf"],"sourceUrl":"` + chefkoch + `","siteName":"Chefkoch"}}`
	instagramPreview  = `{"method":"preview","reason":"Instagram posts can't be read directly: paste the caption to fill in the ingredients and steps.","draft":{"title":"Anna on Instagram","ingredients":[],"steps":[],"tags":["to-review"],"sourceUrl":"` + instagram + `","imageUrl":"https://cdn.example/reel.jpg","description":"Shakshuka … the whole caption","siteName":"Instagram"}}`
	failedPreview     = `{"method":"failed","reason":"The site would not show the page to Life OS (HTTP 403).","draft":{"title":"","ingredients":[],"steps":[],"tags":["to-review"],"sourceUrl":"` + instagram + `"}}`
	captionRead       = `{"method":"text","reason":null,"draft":{"title":"Shakshuka in 20 Minuten","servings":2,"ingredients":[{"name":"Eier","amount":"4"},{"name":"Tomaten","amount":"1 Dose"}],"steps":["Anbraten.","Eier dazu."],"tags":["shakshuka"]}}`
)

func recipeSession(t *testing.T, fake fakeImport) (*mcp.ClientSession, *[]calendarRequest) {
	t.Helper()
	requests := &[]calendarRequest{}
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		request := calendarRequest{Method: r.Method, Path: r.URL.Path, Actor: r.Header.Get(activity.ActorHeader), Idempotency: r.Header.Get("Idempotency-Key"), RawBody: string(raw)}
		_ = json.Unmarshal(raw, &request.Body)
		*requests = append(*requests, request)
		switch r.URL.Path {
		case "/v1/imports/preview":
			if preview, ok := fake.previews[request.Body["url"].(string)]; ok {
				_, _ = w.Write([]byte(preview))
				return
			}
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":"invalid_request","message":"The link points to a private address."},"request_id":"r"}`))
		case "/v1/imports/text":
			_, _ = w.Write([]byte(fake.text))
		default:
			status := fake.saveStatus
			if status == 0 {
				status = http.StatusCreated
			}
			w.WriteHeader(status)
			body := fake.saveBody
			if body == "" {
				body = `{"method":"structured","recipe":{"id":"` + recipeUUID + `","title":"Omas Gulasch"}}`
			}
			_, _ = w.Write([]byte(body))
		}
	}))
	t.Cleanup(service.Close)
	server := mcp.NewServer(&mcp.Implementation{Name: "life-os-test", Version: "0"}, nil)
	addRecipeImportTools(server, service.Client(), service.URL, activity.NewEmitter("", "mcp", nil))
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := server.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session, requests
}

// importData is a recipe_import result's data.
func importData(t *testing.T, result *mcp.CallToolResult) map[string]any {
	t.Helper()
	if result.IsError {
		t.Fatalf("recipe_import failed: %s", resultText(result))
	}
	return result.StructuredContent.(map[string]any)["data"].(map[string]any)
}

func paths(requests []calendarRequest) []string {
	out := make([]string, len(requests))
	for i, request := range requests {
		out[i] = request.Path
	}
	return out
}

func TestARecipePageIsSavedAsItIs(t *testing.T) {
	session, requests := recipeSession(t, fakeImport{previews: map[string]string{chefkoch: structuredPreview}})
	data := importData(t, callTool(t, session, "recipe_import", map[string]any{"url": chefkoch, "idempotencyKey": "food-9"}))
	if data["saved"] != true || data["recipeId"] != recipeUUID || data["method"] != "structured" {
		t.Fatalf("data = %#v", data)
	}
	if got := paths(*requests); !reflect.DeepEqual(got, []string{"/v1/imports/preview", "/v1/imports"}) {
		t.Fatalf("recipe-import saw %q", got)
	}
	preview, save := (*requests)[0], (*requests)[1]
	if preview.Idempotency != "" || preview.Actor != "agent:mcp" || save.Actor != "agent:mcp" || save.Idempotency != "food-9" {
		t.Fatalf("headers: preview %+v, save %+v", preview, save)
	}
	// The page is read again by recipe-import: no overrides.
	if !reflect.DeepEqual(save.Body, map[string]any{"url": chefkoch}) {
		t.Fatalf("save body = %s", save.RawBody)
	}

	// A title the owner names is saved with the page's draft.
	session, requests = recipeSession(t, fakeImport{previews: map[string]string{chefkoch: structuredPreview}})
	importData(t, callTool(t, session, "recipe_import", map[string]any{"url": chefkoch, "title": "Gulasch wie bei Oma"}))
	overrides, _ := (*requests)[1].Body["overrides"].(map[string]any)
	if overrides["title"] != "Gulasch wie bei Oma" || overrides["siteName"] != "Chefkoch" {
		t.Fatalf("save body = %s", (*requests)[1].RawBody)
	}
}

func TestAPostIsSavedFromItsCaption(t *testing.T) {
	for _, preview := range []string{instagramPreview, failedPreview} {
		session, requests := recipeSession(t, fakeImport{previews: map[string]string{instagram: preview}, text: captionRead})
		data := importData(t, callTool(t, session, "recipe_import", map[string]any{"url": instagram, "caption": " Shakshuka in 20 Minuten\nZutaten: 4 Eier … ", "idempotencyKey": "food-10"}))
		if data["saved"] != true || data["recipeId"] != recipeUUID {
			t.Fatalf("data = %#v", data)
		}
		if got := paths(*requests); !reflect.DeepEqual(got, []string{"/v1/imports/preview", "/v1/imports/text", "/v1/imports"}) {
			t.Fatalf("recipe-import saw %q", got)
		}
		read, save := (*requests)[1], (*requests)[2]
		if read.Body["text"] != "Shakshuka in 20 Minuten\nZutaten: 4 Eier …" || read.Body["sourceUrl"] != instagram || read.Idempotency != "" {
			t.Fatalf("text read = %s", read.RawBody)
		}
		overrides, _ := save.Body["overrides"].(map[string]any)
		if overrides["title"] != "Shakshuka in 20 Minuten" || overrides["sourceUrl"] != instagram || len(overrides["ingredients"].([]any)) != 2 || overrides["description"] != nil || save.Idempotency != "food-10" {
			t.Fatalf("save body = %s", save.RawBody)
		}
		if preview == instagramPreview && (overrides["imageUrl"] != "https://cdn.example/reel.jpg" || overrides["siteName"] != "Instagram") {
			t.Fatalf("the page's image and site were lost: %s", save.RawBody)
		}
	}
}

func TestAPostWithoutACaptionIsSavedOnlyWhenAsked(t *testing.T) {
	// A bare shared link: nothing is saved, and the reason says what to do.
	session, requests := recipeSession(t, fakeImport{previews: map[string]string{instagram: instagramPreview}})
	data := importData(t, callTool(t, session, "recipe_import", map[string]any{"url": instagram}))
	if data["saved"] != false || data["reason"] != "Instagram posts can't be read directly: paste the caption to fill in the ingredients and steps." || len(*requests) != 1 {
		t.Fatalf("data = %#v, requests = %q", data, paths(*requests))
	}
	// "Save this": the stub to review.
	session, requests = recipeSession(t, fakeImport{previews: map[string]string{instagram: instagramPreview}})
	data = importData(t, callTool(t, session, "recipe_import", map[string]any{"url": instagram, "saveLink": true}))
	overrides, _ := (*requests)[1].Body["overrides"].(map[string]any)
	if data["saved"] != true || overrides["title"] != "Anna on Instagram" || !reflect.DeepEqual(overrides["tags"], []any{"to-review"}) {
		t.Fatalf("data = %#v, save = %s", data, (*requests)[1].RawBody)
	}
	// A caption without ingredients or steps is no recipe: as without one.
	session, requests = recipeSession(t, fakeImport{previews: map[string]string{instagram: instagramPreview}, text: `{"method":"text","reason":"No ingredients or steps found.","draft":{"title":"So lecker 😍","ingredients":[],"steps":[],"tags":[]}}`})
	if data := importData(t, callTool(t, session, "recipe_import", map[string]any{"url": instagram, "caption": "So lecker 😍"})); data["saved"] != false || len(*requests) != 2 {
		t.Fatalf("data = %#v, requests = %q", data, paths(*requests))
	}
	// A page that cannot be read has no title to save a stub with.
	session, requests = recipeSession(t, fakeImport{previews: map[string]string{instagram: failedPreview}})
	data = importData(t, callTool(t, session, "recipe_import", map[string]any{"url": instagram, "saveLink": true}))
	if data["saved"] != false || data["reason"] != "The site would not show the page to Life OS (HTTP 403)." || len(*requests) != 1 {
		t.Fatalf("data = %#v, requests = %q", data, paths(*requests))
	}
}

func TestSaveOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name         string
		status       int
		body         string
		saved, error bool
		want         map[string]any
	}{
		{"already saved", http.StatusConflict, `{"error":{"code":"recipe_exists","message":"A recipe called “Omas Gulasch” exists."},"request_id":"r","recipeId":"` + recipeUUID + `"}`, true, false, map[string]any{"saved": true, "alreadySaved": true, "method": "structured", "recipeId": recipeUUID}},
		{"nothing to save", http.StatusUnprocessableEntity, `{"error":{"code":"import_failed","message":"The page has no recipe and no title."},"request_id":"r"}`, false, false, map[string]any{"saved": false, "method": "structured", "reason": "The page has no recipe and no title."}},
		{"refused by Food", http.StatusBadRequest, `{"error":{"code":"invalid_request","message":"title is too long"},"request_id":"r"}`, false, false, map[string]any{"saved": false, "method": "structured", "reason": "title is too long"}},
		{"Food unavailable", http.StatusBadGateway, `{"error":{"code":"food_unavailable","message":"Food is not answering."},"request_id":"r"}`, false, true, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session, _ := recipeSession(t, fakeImport{previews: map[string]string{chefkoch: structuredPreview}, saveStatus: tc.status, saveBody: tc.body})
			result := callTool(t, session, "recipe_import", map[string]any{"url": chefkoch})
			if tc.error {
				if !result.IsError {
					t.Fatalf("a failing Food was a result: %s", resultText(result))
				}
				return
			}
			if data := importData(t, result); !reflect.DeepEqual(data, tc.want) {
				t.Fatalf("data = %#v, want %#v", data, tc.want)
			}
		})
	}
}

func TestRecipeLinksAreChecked(t *testing.T) {
	session, requests := recipeSession(t, fakeImport{previews: map[string]string{}})
	for _, link := range []string{"ftp://example.com/r", "www.chefkoch.de/rezepte/1", "https://", "javascript:alert(1)"} {
		if result := callTool(t, session, "recipe_import", map[string]any{"url": link}); !result.IsError {
			t.Errorf("%q was accepted", link)
		}
	}
	if len(*requests) != 0 {
		t.Fatalf("refused links reached recipe-import: %q", paths(*requests))
	}
	// A link recipe-import refuses is no recipe, not an error.
	data := importData(t, callTool(t, session, "recipe_import", map[string]any{"url": "http://10.0.0.5/recipe"}))
	if data["saved"] != false || data["reason"] != "The link points to a private address." {
		t.Fatalf("data = %#v", data)
	}
}
