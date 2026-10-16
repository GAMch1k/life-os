package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GAMch1k/life-os/pkg/activity"
)

// draftSession is MCP with recipe_save_draft against a Food stand-in that
// answers creates in turn (the last repeated; 201 with the recipe sent by
// default) and recipe searches with recipes; it records every request.
func draftSession(t *testing.T, creates []fakeSave, recipes []map[string]any) (*mcp.ClientSession, *[]calendarRequest) {
	t.Helper()
	requests := &[]calendarRequest{}
	food := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		request := calendarRequest{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Actor: r.Header.Get(activity.ActorHeader), Idempotency: r.Header.Get("Idempotency-Key"), RawBody: string(raw)}
		_ = json.Unmarshal(raw, &request.Body)
		*requests = append(*requests, request)
		if r.Method == http.MethodGet {
			list, _ := json.Marshal(map[string]any{"items": recipes})
			_, _ = w.Write(list)
			return
		}
		if len(creates) > 0 {
			answer := creates[0]
			if len(creates) > 1 {
				creates = creates[1:]
			}
			w.WriteHeader(answer.status)
			_, _ = w.Write([]byte(answer.body))
			return
		}
		recipe := map[string]any{"id": recipeUUID}
		for field, value := range request.Body {
			recipe[field] = value
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(recipe)
	}))
	t.Cleanup(food.Close)
	server := mcp.NewServer(&mcp.Implementation{Name: "life-os-test", Version: "0"}, nil)
	addRecipeDraftTools(server, food.Client(), food.URL, activity.NewEmitter("", "mcp", nil))
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

// crispyChicken is the draft Codex builds from a dictated recipe.
func crispyChicken() map[string]any {
	return map[string]any{
		"idempotencyKey": "food-20",
		"title":          " crispy  chicken ",
		"servings":       2,
		"ingredients":    []any{map[string]any{"name": "Chicken thighs", "amount": "4"}, map[string]any{"name": " Flour ", "amount": "100 g"}, map[string]any{"name": "Salt"}, map[string]any{"name": " "}},
		"steps":          []any{"Coat the chicken in the flour\nwith the salt.", " ", "Air fry at 200 °C for 20 minutes."},
		"tags":           []any{"chicken", "To-Review", "air fryer", "!!!", strings.Repeat("t", 41)},
	}
}

func draftData(t *testing.T, result *mcp.CallToolResult) map[string]any {
	t.Helper()
	if result.IsError {
		t.Fatalf("recipe_save_draft failed: %s", resultText(result))
	}
	return result.StructuredContent.(map[string]any)["data"].(map[string]any)
}

func TestADraftIsSavedInFoodToReview(t *testing.T) {
	session, requests := draftSession(t, nil, nil)
	data := draftData(t, callTool(t, session, "recipe_save_draft", crispyChicken()))
	if data["saved"] != true || data["recipeId"] != recipeUUID || data["renamedFrom"] != nil {
		t.Fatalf("data = %#v", data)
	}
	if len(*requests) != 1 {
		t.Fatalf("Food saw %d requests", len(*requests))
	}
	got := (*requests)[0]
	if got.Method != http.MethodPost || got.Path != "/v1/recipes" || got.Idempotency != "food-20" || got.Actor != "agent:mcp" {
		t.Fatalf("Food saw %+v", got)
	}
	// No link: none is sent. The title capitalised, empty entries left out,
	// a step per line, to-review first, and tags Food would refuse left out.
	want := map[string]any{
		"title":       "Crispy chicken",
		"servings":    2.0,
		"ingredients": []any{map[string]any{"name": "Chicken thighs", "amount": "4"}, map[string]any{"name": "Flour", "amount": "100 g"}, map[string]any{"name": "Salt"}},
		"steps":       "Coat the chicken in the flour with the salt.\nAir fry at 200 °C for 20 minutes.",
		"tags":        []any{"to-review", "chicken", "air fryer"},
	}
	if !reflect.DeepEqual(got.Body, want) {
		t.Fatalf("Food got %s", got.RawBody)
	}

	// The link it came from and a note go along.
	draft := crispyChicken()
	draft["sourceUrl"], draft["note"] = instagram, "From Anna's reel."
	session, requests = draftSession(t, nil, nil)
	draftData(t, callTool(t, session, "recipe_save_draft", draft))
	if body := (*requests)[0].Body; body["sourceUrl"] != instagram || body["note"] != "From Anna's reel." {
		t.Fatalf("Food got %s", (*requests)[0].RawBody)
	}
}

func TestADraftIsChecked(t *testing.T) {
	session, requests := draftSession(t, nil, nil)
	for name, change := range map[string]func(map[string]any){
		"no title":          func(d map[string]any) { d["title"] = " " },
		"a long title":      func(d map[string]any) { d["title"] = strings.Repeat("a", 201) },
		"nothing to cook":   func(d map[string]any) { d["ingredients"], d["steps"] = []any{map[string]any{"name": " "}}, []any{} },
		"too many servings": func(d map[string]any) { d["servings"] = 101 },
		"a long amount": func(d map[string]any) {
			d["ingredients"] = []any{map[string]any{"name": "Salt", "amount": strings.Repeat("1", 51)}}
		},
		"a link of no page": func(d map[string]any) { d["sourceUrl"] = "javascript:alert(1)" },
		"a long note":       func(d map[string]any) { d["note"] = strings.Repeat("n", 1001) },
		"too many ingredients": func(d map[string]any) {
			many := make([]any, 101)
			for i := range many {
				many[i] = map[string]any{"name": "Salt"}
			}
			d["ingredients"] = many
		},
	} {
		draft := crispyChicken()
		change(draft)
		if result := callTool(t, session, "recipe_save_draft", draft); !result.IsError {
			t.Errorf("%s: accepted", name)
		}
	}
	if len(*requests) != 0 {
		t.Fatalf("refused drafts reached Food: %d", len(*requests))
	}
	// Steps alone, or ingredients alone, are a recipe.
	draft := crispyChicken()
	delete(draft, "ingredients")
	draftData(t, callTool(t, session, "recipe_save_draft", draft))
	draft = crispyChicken()
	delete(draft, "steps")
	draftData(t, callTool(t, session, "recipe_save_draft", draft))
}

func TestADraftTitleFoodHasGetsANumber(t *testing.T) {
	exists := fakeSave{http.StatusConflict, `{"error":{"code":"recipe_exists","message":"A recipe called “Crispy chicken” exists"},"request_id":"r"}`}
	created := fakeSave{http.StatusCreated, `{"id":"11111111-1111-4111-8111-111111111111","title":"Crispy chicken (2)"}`}
	// Without a link: another recipe of that name, so "… (2)" with its own
	// key, and no search.
	session, requests := draftSession(t, []fakeSave{exists, created}, nil)
	data := draftData(t, callTool(t, session, "recipe_save_draft", crispyChicken()))
	if data["saved"] != true || data["recipeId"] != "11111111-1111-4111-8111-111111111111" || data["renamedFrom"] != "Crispy chicken" || len(*requests) != 2 {
		t.Fatalf("data = %#v, requests = %d", data, len(*requests))
	}
	if second := (*requests)[1]; second.Body["title"] != "Crispy chicken (2)" || second.Idempotency != "food-20#2" || !reflect.DeepEqual(second.Body["tags"], []any{"to-review", "chicken", "air fryer"}) {
		t.Fatalf("second save = %s (%s)", second.RawBody, second.Idempotency)
	}
	// With a link: the recipe of that name from the same link is this one,
	// saved before.
	draft := crispyChicken()
	draft["sourceUrl"] = instagram
	session, requests = draftSession(t, []fakeSave{exists}, []map[string]any{{"id": recipeUUID, "title": "Crispy Chicken", "normalizedTitle": "crispy chicken", "sourceUrl": instagram + "?igsh=abc"}})
	data = draftData(t, callTool(t, session, "recipe_save_draft", draft))
	if data["saved"] != true || data["alreadySaved"] != true || data["recipeId"] != recipeUUID || len(*requests) != 2 {
		t.Fatalf("data = %#v, requests = %d", data, len(*requests))
	}
	if search := (*requests)[1]; search.Method != http.MethodGet || !strings.Contains(search.Query, "q=Crispy+chicken") {
		t.Fatalf("search = %+v", search)
	}
	// From another link: numbered.
	session, requests = draftSession(t, []fakeSave{exists, created}, []map[string]any{{"id": recipeUUID, "normalizedTitle": "crispy chicken", "sourceUrl": "https://www.tiktok.com/@cook/video/1"}})
	if data := draftData(t, callTool(t, session, "recipe_save_draft", draft)); data["renamedFrom"] != "Crispy chicken" || (*requests)[2].Body["title"] != "Crispy chicken (2)" {
		t.Fatalf("data = %#v", data)
	}
	// Every number taken: nothing saved, with the reason.
	session, requests = draftSession(t, []fakeSave{exists}, nil)
	data = draftData(t, callTool(t, session, "recipe_save_draft", crispyChicken()))
	if data["saved"] != false || data["reason"] != takenReason("Crispy chicken") || len(*requests) != maxTitleNumber {
		t.Fatalf("data = %#v, requests = %d", data, len(*requests))
	}
	// The capture's key used for another draft of it (Codex wrote it
	// differently on a retry): saved before.
	session, _ = draftSession(t, []fakeSave{{http.StatusConflict, `{"error":{"code":"idempotency_conflict","message":"x"},"request_id":"r"}`}}, nil)
	if data := draftData(t, callTool(t, session, "recipe_save_draft", crispyChicken())); data["saved"] != true || data["alreadySaved"] != true {
		t.Fatalf("data = %#v", data)
	}
	// Food refusing the recipe: nothing saved, with Food's reason; Food
	// failing: an error, retried.
	session, _ = draftSession(t, []fakeSave{{http.StatusBadRequest, `{"error":{"code":"invalid_request","message":"title must contain a letter or a digit"},"request_id":"r"}`}}, nil)
	if data := draftData(t, callTool(t, session, "recipe_save_draft", crispyChicken())); data["saved"] != false || data["reason"] != "title must contain a letter or a digit" {
		t.Fatalf("data = %#v", data)
	}
	session, _ = draftSession(t, []fakeSave{{http.StatusServiceUnavailable, `{}`}}, nil)
	if result := callTool(t, session, "recipe_save_draft", crispyChicken()); !result.IsError {
		t.Fatalf("a failing Food was a result: %s", resultText(result))
	}
}
