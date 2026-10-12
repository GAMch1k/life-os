package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GAMch1k/life-os/pkg/activity"
)

// ownerZone is the owner's timezone in these tests.
var ownerZone, _ = time.LoadLocation("Europe/Berlin")

// kitchenTools is a fake MCP for the ideas job: Food's preferences, pantry,
// suggestions, ideas, and meal plan, and food_create_idea, which refuses the
// titles in exists with 409.
type kitchenTools struct {
	pantry    string
	ideas     map[string]string
	exists    map[string]bool
	fail      string
	calls     []string
	arguments map[string][]map[string]any
	actors    []activity.Actor
}

func newKitchen() *kitchenTools {
	return &kitchenTools{
		pantry:    `{"items":[{"name":"Hähnchenbrust","amount":"400 g","expiresOn":"2026-10-13"},{"name":"Paprika","expiresOn":null},{"name":"Reis","amount":"1 kg","expiresOn":"2027-03-01"}]}`,
		ideas:     map[string]string{},
		exists:    map[string]bool{},
		arguments: map[string][]map[string]any{},
	}
}

func (k *kitchenTools) Call(ctx context.Context, name string, args map[string]any) (json.RawMessage, error) {
	k.calls = append(k.calls, name)
	k.arguments[name] = append(k.arguments[name], args)
	k.actors = append(k.actors, activity.TraceFrom(ctx).Actor)
	if name == k.fail {
		return nil, errors.New("MCP tool " + name + " error: unavailable")
	}
	switch name {
	case "food_get_preferences":
		return json.RawMessage(`{"methods":["air_fryer","oven"],"dislikes":["Pilze","Koriander"],"dietaryNotes":"little red meat"}`), nil
	case "food_list_pantry_items":
		return json.RawMessage(k.pantry), nil
	case "food_list_suggestions":
		return json.RawMessage(`{"today":"2026-10-12","items":[{"recipe":{"title":"Pasta Bolognese"}}],"excluded":[]}`), nil
	case "food_list_ideas":
		if ideas, ok := k.ideas[args["state"].(string)]; ok {
			return json.RawMessage(ideas), nil
		}
		return json.RawMessage(`{"items":[]}`), nil
	case "food_list_meal_plan":
		return json.RawMessage(`{"items":[{"date":"2026-10-10","slot":"dinner","title":"Linsensuppe"}]}`), nil
	case "food_create_idea":
		if k.exists[args["title"].(string)] {
			return nil, errors.New(`MCP tool food_create_idea rejected: service returned 409: {"error":{"code":"idea_exists","message":"An idea is called that"}}`)
		}
		return json.RawMessage(`{"id":"idea-1"}`), nil
	}
	return json.RawMessage(`{}`), nil
}

// fakeIdeas answers with the given JSON and records what it was asked.
type fakeIdeas struct {
	answer    string
	err       error
	prompts   []string
	schemas   []string
	webSearch []string
}

func (f *fakeIdeas) Ideas(_ context.Context, prompt, schema, webSearch string) (string, error) {
	f.prompts, f.schemas, f.webSearch = append(f.prompts, prompt), append(f.schemas, schema), append(f.webSearch, webSearch)
	return f.answer, f.err
}

const threeIdeas = `{"ideas":[
 {"title":"Knuspriges Hähnchen aus dem Airfryer","ingredients":[{"name":"Hähnchenbrust","amount":"400 g"},{"name":"Paprika","amount":"1"},{"name":"Paprikapulver","amount":null}],"steps":["Hähnchenbrust würzen.","Im Airfryer bei 200 °C 18 Minuten garen.","Mit Paprika servieren."],"servings":2,"tags":["airfryer","schnell"],"methods":["air_fryer"],"reason":"Nutzt die Hähnchenbrust, die morgen abläuft, im Airfryer.","source":"ai","sourceUrl":null},
 {"title":"Paprika-Reis-Pfanne","ingredients":[{"name":"Reis","amount":"200 g"},{"name":"Paprika","amount":"2"}],"steps":["Reis kochen.","Paprika anbraten.","Alles mischen."],"servings":2,"tags":["pfanne"],"methods":["stovetop"],"reason":"Nutzt Reis und Paprika aus dem Vorrat.","source":"ai","sourceUrl":null},
 {"title":"Gefüllte Paprika aus dem Airfryer","ingredients":[{"name":"Paprika","amount":"2"},{"name":"Reis","amount":"100 g"},{"name":"Hähnchenbrust","amount":"200 g"}],"steps":["Paprika aushöhlen.","Mit Reis und Hähnchen füllen.","Im Airfryer bei 180 °C 15 Minuten garen."],"servings":2,"tags":["airfryer"],"methods":["air_fryer"],"reason":"Verbraucht die Hähnchenbrust vor morgen, im Airfryer.","source":"ai","sourceUrl":null}
]}`

func newJob(kitchen *kitchenTools, generator *fakeIdeas, now *time.Time) *IdeasJob {
	return &IdeasJob{Generator: generator, Tools: kitchen, TimeZone: ownerZone, DailyAt: 16 * 60, WebSearch: WebSearchDisabled, Now: func() time.Time { return *now }}
}

func TestOwnerRequestsAreThrottled(t *testing.T) {
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, ownerZone)
	generator := &fakeIdeas{answer: `{"ideas":[]}`}
	job := newJob(newKitchen(), generator, &now)

	if answer := job.Request(); !answer.Queued || answer.Reason != "" {
		t.Fatalf("first request = %+v", answer)
	}
	// While it waits, another request joins it and counts for nothing.
	if answer := job.Request(); !answer.Queued {
		t.Fatalf("a request while one waits = %+v", answer)
	}
	job.Tick(context.Background())
	if len(generator.prompts) != 1 || job.Status().LastRun.Trigger != "request" {
		t.Fatalf("runs = %d, status = %+v", len(generator.prompts), job.Status())
	}
	now = now.Add(5 * time.Minute)
	answer := job.Request()
	if answer.Queued || answer.Reason != "too_soon" || !answer.RetryAt.Equal(time.Date(2026, 10, 12, 9, 10, 0, 0, ownerZone)) {
		t.Fatalf("a request after 5 minutes = %+v", answer)
	}
	for i := 2; i <= requestsPerDay; i++ {
		now = now.Add(requestGap)
		if answer := job.Request(); !answer.Queued {
			t.Fatalf("request %d = %+v", i, answer)
		}
		job.Tick(context.Background())
	}
	now = now.Add(requestGap)
	answer = job.Request()
	if answer.Reason != "daily_limit" || !answer.RetryAt.Equal(time.Date(2026, 10, 13, 0, 0, 0, 0, ownerZone)) {
		t.Fatalf("the seventh request = %+v", answer)
	}
	// A new day in Berlin, though not in UTC yet... 00:30 Berlin is 22:30 UTC.
	now = time.Date(2026, 10, 13, 0, 30, 0, 0, ownerZone)
	if answer := job.Request(); !answer.Queued {
		t.Fatalf("a request the next day = %+v", answer)
	}
}

func TestTheDailyRunComesOnceAtItsTime(t *testing.T) {
	now := time.Date(2026, 10, 12, 15, 59, 0, 0, ownerZone)
	kitchen, generator := newKitchen(), &fakeIdeas{answer: threeIdeas}
	job := newJob(kitchen, generator, &now)
	job.Tick(context.Background())
	if len(kitchen.calls) != 0 {
		t.Fatalf("ran before 16:00: %v", kitchen.calls)
	}
	if next := job.Status().NextRunAt; next == nil || !next.Equal(time.Date(2026, 10, 12, 16, 0, 0, 0, ownerZone)) {
		t.Fatalf("next run = %v", next)
	}
	now = now.Add(time.Minute)
	job.Tick(context.Background())
	run := job.Status().LastRun
	if run == nil || run.Trigger != "daily" || run.Outcome != "created" || run.Created != 3 || len(generator.prompts) != 1 {
		t.Fatalf("daily run = %+v", run)
	}
	now = now.Add(2 * time.Hour)
	job.Tick(context.Background())
	if len(generator.prompts) != 1 {
		t.Fatal("the daily run came twice in a day")
	}
	if next := job.Status().NextRunAt; !next.Equal(time.Date(2026, 10, 13, 16, 0, 0, 0, ownerZone)) {
		t.Fatalf("next run = %v", next)
	}
	now = time.Date(2026, 10, 13, 16, 5, 0, 0, ownerZone)
	job.Tick(context.Background())
	if len(generator.prompts) != 2 {
		t.Fatal("no daily run the next day")
	}
	// Off: never by itself.
	off := newJob(newKitchen(), &fakeIdeas{answer: threeIdeas}, &now)
	off.DailyAt = -1
	off.Tick(context.Background())
	if off.Status().LastRun != nil || off.Status().DailyAt != "" || off.Status().NextRunAt != nil {
		t.Fatalf("an off schedule ran or is due: %+v", off.Status())
	}
}

func TestTheDailyRunSkipsAnEmptyPantryAndADayWithIdeas(t *testing.T) {
	now := time.Date(2026, 10, 12, 16, 0, 0, 0, ownerZone)
	empty, generator := newKitchen(), &fakeIdeas{answer: threeIdeas}
	empty.pantry = `{"items":[]}`
	job := newJob(empty, generator, &now)
	job.Tick(context.Background())
	if run := job.Status().LastRun; run.Outcome != "empty_pantry" || len(generator.prompts) != 0 || contains(empty.calls, "food_create_idea") {
		t.Fatalf("run = %+v, calls = %v", run, empty.calls)
	}
	// Restarted after today's run: the job's own idea of today is in Food.
	done := newKitchen()
	done.ideas["dismissed"] = `{"items":[{"title":"Ofengemüse","createdAt":"2026-10-12T14:05:00Z","createdBy":{"kind":"agent","name":"codex-recipe-ideas"}}]}`
	again := newJob(done, generator, &now)
	again.Tick(context.Background())
	if run := again.Status().LastRun; run.Outcome != "done_today" || len(generator.prompts) != 0 {
		t.Fatalf("run = %+v", run)
	}
	// The owner's request still runs.
	again.Request()
	again.Tick(context.Background())
	if len(generator.prompts) != 1 {
		t.Fatal("the owner's request did not run")
	}
}

func TestAFailedDailyRunIsTriedAgainAnHourLaterThreeTimes(t *testing.T) {
	now := time.Date(2026, 10, 12, 16, 0, 0, 0, ownerZone)
	generator := &fakeIdeas{err: errors.New("Codex usage limit")}
	job := newJob(newKitchen(), generator, &now)
	for try := 1; try <= dailyTries; try++ {
		job.Tick(context.Background())
		if run := job.Status().LastRun; run.Outcome != "failed" || len(generator.prompts) != try {
			t.Fatalf("try %d: run = %+v, prompts = %d", try, run, len(generator.prompts))
		}
		job.Tick(context.Background()) // not before the hour
		if len(generator.prompts) != try {
			t.Fatalf("retried at once after try %d", try)
		}
		now = now.Add(dailyRetry)
	}
	job.Tick(context.Background())
	if len(generator.prompts) != dailyTries {
		t.Fatal("tried more than three times in a day")
	}
}

func TestARunTellsCodexTheKitchenAndLeavesCheckedIdeas(t *testing.T) {
	now := time.Date(2026, 10, 12, 16, 0, 0, 0, ownerZone)
	kitchen, generator := newKitchen(), &fakeIdeas{answer: threeIdeas}
	kitchen.ideas["new"] = `{"items":[{"title":"Ofengemüse","createdAt":"2026-10-11T14:00:00Z","createdBy":{"kind":"agent","name":"codex-recipe-ideas"}}]}`
	kitchen.exists["Paprika-Reis-Pfanne"] = true
	job := newJob(kitchen, generator, &now)
	job.Tick(context.Background())

	for _, want := range []map[string]any{{"state": "in_stock", "limit": 200}} {
		if got := kitchen.arguments["food_list_pantry_items"][0]; got["state"] != want["state"] || got["limit"] != want["limit"] {
			t.Fatalf("pantry read = %#v", got)
		}
	}
	if got := kitchen.arguments["food_list_meal_plan"][0]; got["from"] != "2026-09-28" || got["to"] != "2026-10-19" {
		t.Fatalf("meal plan read = %#v", got)
	}
	for _, actor := range kitchen.actors {
		if actor != ideasActor {
			t.Fatalf("a call was made as %+v", actor)
		}
	}
	prompt := generator.prompts[0]
	kitchenJSON := prompt[strings.Index(prompt, "Kitchen: ")+len("Kitchen: "):]
	var told ideasContext
	if err := json.Unmarshal([]byte(kitchenJSON), &told); err != nil {
		t.Fatalf("kitchen = %s: %v", kitchenJSON, err)
	}
	if told.Today != "Monday 2026-10-12" || len(told.Pantry) != 3 || len(told.ExpiringSoon) != 1 || told.ExpiringSoon[0].Name != "Hähnchenbrust" || *told.ExpiringSoon[0].InDays != 1 {
		t.Fatalf("kitchen = %+v", told)
	}
	if told.Preferences.Methods[0] != "air_fryer" || told.Preferences.Dislikes[0] != "Pilze" || told.Preferences.DietaryNotes != "little red meat" || told.SavedRecipes[0] != "Pasta Bolognese" || told.Ideas[0] != "Ofengemüse" || told.RecentMeals[0].Title != "Linsensuppe" {
		t.Fatalf("kitchen = %+v", told)
	}
	if !strings.Contains(prompt, `source is "ai" and sourceUrl is null`) || generator.webSearch[0] != WebSearchDisabled || generator.schemas[0] != ideasSchema {
		t.Fatalf("web search off: prompt %q, mode %q", prompt[:200], generator.webSearch[0])
	}
	var schema map[string]any
	if err := json.Unmarshal([]byte(ideasSchema), &schema); err != nil {
		t.Fatalf("the schema is not JSON: %v", err)
	}

	run := job.Status().LastRun
	if run.Created != 2 || run.Skipped != 1 || run.Failed != 0 || run.Outcome != "created" {
		t.Fatalf("run = %+v", run)
	}
	first := kitchen.arguments["food_create_idea"][0]
	if first["title"] != "Knuspriges Hähnchen aus dem Airfryer" || first["source"] != "ai" || first["steps"] != "Hähnchenbrust würzen.\nIm Airfryer bei 200 °C 18 Minuten garen.\nMit Paprika servieren." || first["servings"] != 2 || first["sourceUrl"] != nil {
		t.Fatalf("idea = %#v", first)
	}
	ingredients := first["ingredients"].([]any)
	if len(ingredients) != 3 || ingredients[2].(map[string]any)["amount"] != nil || ingredients[0].(map[string]any)["amount"] != "400 g" {
		t.Fatalf("ingredients = %#v", ingredients)
	}
	if first["idempotencyKey"] != ideaKey("daily:2026-10-12", "Knuspriges Hähnchen aus dem Airfryer") {
		t.Fatalf("key = %v", first["idempotencyKey"])
	}
}

func TestIdeasAreCheckedStrictly(t *testing.T) {
	kitchen := ideasContext{Preferences: ideasPreferences{Dislikes: []string{"Pilze", "blue cheese"}}, SavedRecipes: []string{"Pasta Bolognese"}, Ideas: []string{"Ofengemüse"}}
	good := `{"title":"Paprika-Reis-Pfanne","ingredients":[{"name":"Reis","amount":"200 g"},{"name":"Paprika","amount":null}],"steps":["Reis kochen.","Paprika anbraten."],"servings":2,"tags":["pfanne"],"methods":["stovetop"],"reason":"Nutzt Reis und Paprika aus dem Vorrat.","source":"ai","sourceUrl":null}`
	variant := func(replace ...string) string {
		out := good
		for i := 0; i+1 < len(replace); i += 2 {
			out = strings.Replace(out, replace[i], replace[i+1], 1)
		}
		return out
	}
	for _, tc := range []struct {
		name string
		idea string
		web  string
		keep bool
	}{
		{"a good idea", good, WebSearchDisabled, true},
		{"a disliked ingredient", variant(`"name":"Paprika"`, `"name":"Champignons und Pilze"`), WebSearchDisabled, false},
		{"a dislike in another form", variant(`"name":"Paprika"`, `"name":"Steinpilz"`), WebSearchDisabled, false},
		{"a dislike in the title", variant("Paprika-Reis-Pfanne", "Pilzpfanne mit Reis"), WebSearchDisabled, false},
		{"a two-word dislike", variant(`"name":"Paprika"`, `"name":"Blue Cheese"`), WebSearchDisabled, false},
		{"only one word of a two-word dislike", variant(`"name":"Paprika"`, `"name":"Cheese"`), WebSearchDisabled, true},
		{"a made-up method", variant(`"stovetop"`, `"campfire"`), WebSearchDisabled, false},
		{"one ingredient", variant(`,{"name":"Paprika","amount":null}`, ``), WebSearchDisabled, false},
		{"one step", variant(`,"Paprika anbraten."`, ``), WebSearchDisabled, false},
		{"a long step", variant("Reis kochen.", strings.Repeat("Reis ", 70)), WebSearchDisabled, false},
		{"a long title", variant("Paprika-Reis-Pfanne", strings.Repeat("Reis", 31)), WebSearchDisabled, false},
		{"a short reason", variant("Nutzt Reis und Paprika aus dem Vorrat.", "Gut."), WebSearchDisabled, false},
		{"twenty servings", variant(`"servings":2`, `"servings":20`), WebSearchDisabled, false},
		{"another source", variant(`"source":"ai"`, `"source":"book"`), WebSearchDisabled, false},
		{"a web idea without search", variant(`"source":"ai","sourceUrl":null`, `"source":"web","sourceUrl":"https://example.com/pfanne"`), WebSearchDisabled, false},
		{"a web idea with search", variant(`"source":"ai","sourceUrl":null`, `"source":"web","sourceUrl":"https://example.com/pfanne"`), WebSearchLive, true},
		{"a web idea without its page", variant(`"source":"ai","sourceUrl":null`, `"source":"web","sourceUrl":"example.com"`), WebSearchLive, false},
		{"a web idea on localhost", variant(`"source":"ai","sourceUrl":null`, `"source":"web","sourceUrl":"http://localhost:8080/rezept"`), WebSearchLive, false},
		{"a web idea on an IP address", variant(`"source":"ai","sourceUrl":null`, `"source":"web","sourceUrl":"http://192.168.1.10/rezept"`), WebSearchLive, false},
		{"a web idea on an IPv6 address", variant(`"source":"ai","sourceUrl":null`, `"source":"web","sourceUrl":"https://[::1]/rezept"`), WebSearchLive, false},
		{"a web idea on a bare host name", variant(`"source":"ai","sourceUrl":null`, `"source":"web","sourceUrl":"http://food/rezept"`), WebSearchLive, false},
		{"a web idea on another scheme", variant(`"source":"ai","sourceUrl":null`, `"source":"web","sourceUrl":"ftp://www.chefkoch.de/rezept"`), WebSearchLive, false},
		{"a web idea on chefkoch", variant(`"source":"ai","sourceUrl":null`, `"source":"web","sourceUrl":"https://www.chefkoch.de/rezepte/123/paprika-reis-pfanne.html"`), WebSearchCached, true},
		{"a saved recipe", variant("Paprika-Reis-Pfanne", "pasta bolognese"), WebSearchDisabled, false},
		{"an idea already suggested", variant("Paprika-Reis-Pfanne", "Ofengemüse!"), WebSearchDisabled, false},
	} {
		kept, rejected, err := checkIdeas(`{"ideas":[`+tc.idea+`]}`, kitchen, tc.web)
		if err != nil || (len(kept) == 1) != tc.keep || rejected != map[bool]int{true: 0, false: 1}[tc.keep] {
			t.Errorf("%s: kept %d, rejected %d, err %v", tc.name, len(kept), rejected, err)
		}
	}
	// At most three, each title once, and the steps and texts trimmed.
	kept, rejected, err := checkIdeas("```json\n"+`{"ideas":[`+strings.Repeat(good+",", 1)+variant("Paprika-Reis-Pfanne", " Reis-Paprika ")+","+variant("Paprika-Reis-Pfanne", "Bunter Reis")+","+variant("Paprika-Reis-Pfanne", "Reispfanne")+","+variant("Paprika-Reis-Pfanne", "Paprika-Reis-Pfanne")+`]}`+"\n```", kitchen, WebSearchDisabled)
	if err != nil || len(kept) != 3 || rejected != 2 || kept[1].Title != "Reis-Paprika" {
		t.Fatalf("kept %+v, rejected %d, err %v", kept, rejected, err)
	}
	if _, _, err := checkIdeas("Here are some ideas!", kitchen, WebSearchDisabled); err == nil {
		t.Fatal("an answer that is not JSON was accepted")
	}
}

func TestIdeaKeysAreDeterministicPerRunAndTitle(t *testing.T) {
	key := ideaKey("daily:2026-10-12", "Paprika-Reis-Pfanne")
	if !strings.HasPrefix(key, "ideas-") || len(key) > 200 || ideaKey("daily:2026-10-12", " paprika-reis-pfanne! ") != key {
		t.Fatalf("key = %s", key)
	}
	if ideaKey("daily:2026-10-13", "Paprika-Reis-Pfanne") == key || ideaKey("daily:2026-10-12", "Reispfanne") == key {
		t.Fatal("another run or title reused the key")
	}
}

func TestAFailingReadOrFoodRefusalIsReported(t *testing.T) {
	now := time.Date(2026, 10, 12, 10, 0, 0, 0, ownerZone)
	kitchen := newKitchen()
	kitchen.fail = "food_get_preferences"
	generator := &fakeIdeas{answer: threeIdeas}
	job := newJob(kitchen, generator, &now)
	job.Request()
	job.Tick(context.Background())
	if run := job.Status().LastRun; run.Outcome != "failed" || !strings.Contains(run.Error, "food_get_preferences") || len(generator.prompts) != 0 {
		t.Fatalf("run = %+v", run)
	}
	refused := newKitchen()
	refused.fail = "food_create_idea"
	job = newJob(refused, &fakeIdeas{answer: threeIdeas}, &now)
	job.Request()
	job.Tick(context.Background())
	if run := job.Status().LastRun; run.Outcome != "failed" || run.Failed != 3 || run.Created != 0 {
		t.Fatalf("run = %+v", run)
	}
	bad := newJob(newKitchen(), &fakeIdeas{answer: "no JSON"}, &now)
	bad.Request()
	bad.Tick(context.Background())
	if run := bad.Status().LastRun; run.Outcome != "failed" || !strings.Contains(run.Error, "decode ideas") {
		t.Fatalf("run = %+v", run)
	}
}

func TestWebSearchIsOnlyForTheIdeasJob(t *testing.T) {
	now := time.Date(2026, 10, 12, 10, 0, 0, 0, ownerZone)
	generator := &fakeIdeas{answer: `{"ideas":[]}`}
	job := newJob(newKitchen(), generator, &now)
	job.WebSearch = WebSearchLive
	job.Request()
	job.Tick(context.Background())
	if generator.webSearch[0] != WebSearchLive || !strings.Contains(generator.prompts[0], "Search the web for recipes that fit these rules: at least one idea must be a recipe found on a real recipe page, preferably a site in the owner's language (such as chefkoch.de for German)") || !strings.Contains(generator.prompts[0], "never copied from the page") || !strings.Contains(generator.prompts[0], `The other ideas may be your own, with source "ai" and sourceUrl null.`) || strings.Contains(generator.prompts[0], "do not search the web") {
		t.Fatalf("web search on: %q", generator.webSearch)
	}
	if run := job.Status().LastRun; run.Outcome != "none" || job.Status().WebSearch != WebSearchLive {
		t.Fatalf("status = %+v", job.Status())
	}
}

func TestTheIdeasRoutes(t *testing.T) {
	now := time.Date(2026, 10, 12, 10, 0, 0, 0, ownerZone)
	job := newJob(newKitchen(), &fakeIdeas{answer: `{"ideas":[]}`}, &now)
	service := &Service{Run: func(context.Context) error { return nil }, PollInterval: time.Second, Ideas: job}
	handler := service.Handler()
	call := func(method, path, actor string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, path, nil)
		if actor != "" {
			request.Header.Set(activity.ActorHeader, actor)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}
	if response := call(http.MethodPost, "/ideas/requests", ""); response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "actor_required") {
		t.Fatalf("no actor = %d %s", response.Code, response.Body)
	}
	if response := call(http.MethodPost, "/ideas/requests", "user"); response.Code != http.StatusAccepted || !strings.Contains(response.Body.String(), `"queued":true`) {
		t.Fatalf("request = %d %s", response.Code, response.Body)
	}
	job.Tick(context.Background())
	response := call(http.MethodPost, "/ideas/requests", "user")
	if response.Code != http.StatusTooManyRequests || !strings.Contains(response.Body.String(), `"too_soon"`) || !strings.Contains(response.Body.String(), `"retryAt"`) {
		t.Fatalf("too soon = %d %s", response.Code, response.Body)
	}
	status := call(http.MethodGet, "/ideas", "")
	var got IdeasStatus
	if status.Code != http.StatusOK || json.Unmarshal(status.Body.Bytes(), &got) != nil || got.LastRun == nil || got.DailyAt != "16:00" || got.WebSearch != WebSearchDisabled {
		t.Fatalf("status = %d %s", status.Code, status.Body)
	}
	if response := call(http.MethodGet, "/ideas/requests", "user"); response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET requests = %d", response.Code)
	}
}

func TestIdeasConfiguration(t *testing.T) {
	for value, want := range map[string]int{"16:00": 960, "07:30": 450, "off": -1} {
		if got, err := ideasTime(value); err != nil || got != want {
			t.Errorf("ideasTime(%q) = %d, %v", value, got, err)
		}
	}
	if _, err := ideasTime("4pm"); err == nil {
		t.Error("4pm was accepted")
	}
	t.Setenv("LIFE_OS_IDEAS_WEB_SEARCH", "always")
	if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "LIFE_OS_IDEAS_WEB_SEARCH") {
		t.Fatalf("an unknown web search mode: %v", err)
	}
	t.Setenv("LIFE_OS_IDEAS_WEB_SEARCH", "live")
	t.Setenv("LIFE_OS_IDEAS_TIME", "")
	cfg, err := LoadConfig()
	if err != nil || cfg.IdeasWebSearch != WebSearchLive || cfg.IdeasAt != 960 {
		t.Fatalf("config = %+v, %v", cfg, err)
	}
}

func TestDislikesAreFoundInOtherForms(t *testing.T) {
	for _, tc := range []struct {
		text, dislike string
		want          bool
	}{
		{"Steinpilz", "Pilze", true},
		{"Pilzpfanne mit Reis", "Pilze", true},
		{"Champignons", "Pilze", false},
		{"mushroom sauce", "mushrooms", true},
		{"грибной соус", "грибы", true},
		{"Reisnudeln", "Reis", true},
		{"Preiselbeeren", "Reis", false},
		{"Steak", "tea", false},
		{"Blue cheese dressing", "blue cheese", true},
		{"Cheese", "blue cheese", false},
		{"Koriandergrün", "Koriander", true},
	} {
		if got := containsDislike(tc.text, tc.dislike); got != tc.want {
			t.Errorf("containsDislike(%q, %q) = %v, want %v", tc.text, tc.dislike, got, tc.want)
		}
	}
}

func TestCodexSearchesTheWebOnlyForIdeasAndShapesTheirAnswer(t *testing.T) {
	dir := t.TempDir()
	argsFile, schemaCopy := filepath.Join(dir, "args"), filepath.Join(dir, "schema")
	// The fake CLI records its arguments and the schema file it was given.
	codex := fakeCodex(t, `printf '{"ideas":[]}' > "$out"`)
	wrapper := filepath.Join(dir, "codex-args")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + "\nprev=''\nfor arg in \"$@\"; do\n  if [ \"$prev\" = '--output-schema' ]; then cp \"$arg\" " + schemaCopy + "; fi\n  prev=$arg\ndone\nexec " + codex + " \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// The run's own temporary files (output, schema) must all be removed.
	temp := t.TempDir()
	t.Setenv("TMPDIR", temp)
	cli := CodexCLI{Command: wrapper, WorkDir: dir, Model: "gpt-5.6-luna"}
	args := func() string {
		raw, _ := os.ReadFile(argsFile)
		return string(raw)
	}
	if _, err := cli.Plan(context.Background(), "buy milk"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(args(), "web_search=\"disabled\"\n") || strings.Contains(args(), "--output-schema") {
		t.Fatalf("capture planning args = %q", args())
	}
	if out, err := cli.Ideas(context.Background(), "suggest", ideasSchema, WebSearchLive); err != nil || out != `{"ideas":[]}` {
		t.Fatalf("ideas = %q, %v", out, err)
	}
	if !strings.Contains(args(), "web_search=\"live\"\n") || !strings.Contains(args(), "--output-schema\n") || !strings.HasSuffix(strings.TrimSpace(args()), "\nsuggest") {
		t.Fatalf("ideas args = %q", args())
	}
	if schema, _ := os.ReadFile(schemaCopy); string(schema) != ideasSchema {
		t.Fatalf("schema given = %q", schema)
	}
	// An unknown mode searches nothing.
	if _, err := cli.Ideas(context.Background(), "suggest", ideasSchema, "everything"); err != nil || !strings.Contains(args(), "web_search=\"disabled\"\n") {
		t.Fatalf("unknown mode args = %q, %v", args(), err)
	}
	if entries, _ := os.ReadDir(temp); len(entries) > 0 {
		t.Fatalf("files left behind: %v", entries)
	}
}

func TestPlanPromptAsksMostIdeasWithTheFirstPreferredMethod(t *testing.T) {
	rule := "Prefer the preferred cooking methods, the first most: at least 2 of 3 ideas (most of them, whatever their number) cook with the first preferred method wherever the dish suits it, and only a dish that truly needs another method uses one; when that method is the air fryer, cook in the air fryer and give its temperature and time in the steps. "
	airFryer := ideasPrompt(ideasContext{Preferences: ideasPreferences{Methods: []string{"air_fryer", "oven"}}}, WebSearchDisabled)
	if !strings.Contains(airFryer, rule) || !strings.Contains(airFryer, "Here the first preferred method is the air fryer (air_fryer): at least 2 of 3 ideas cook with it. ") {
		t.Fatalf("prompt = %q", airFryer)
	}
	oven := ideasPrompt(ideasContext{Preferences: ideasPreferences{Methods: []string{"oven"}}}, WebSearchDisabled)
	if !strings.Contains(oven, "Here the first preferred method is the oven (oven)") {
		t.Fatalf("prompt = %q", oven)
	}
	if none := ideasPrompt(ideasContext{}, WebSearchDisabled); strings.Contains(none, "Here the first preferred method") || !strings.Contains(none, rule) {
		t.Fatalf("prompt without preferred methods = %q", none)
	}
}

func TestARunCountsIdeasWithThePreferredMethods(t *testing.T) {
	now := time.Date(2026, 10, 12, 10, 0, 0, 0, ownerZone)
	// Two air fryer ideas and one in the oven, the second preferred method.
	answer := strings.Replace(threeIdeas, `"methods":["stovetop"]`, `"methods":["oven"]`, 1)
	kitchen := newKitchen()
	var logged []string
	job := newJob(kitchen, &fakeIdeas{answer: answer}, &now)
	job.Logf = func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }
	job.Request()
	job.Tick(context.Background())
	run := job.Status().LastRun
	if run.Created != 3 || run.FirstMethod != "air_fryer" || run.WithFirstMethod != 2 || run.WithPreferredMethod != 3 {
		t.Fatalf("run = %+v", run)
	}
	if len(logged) != 1 || logged[0] != "recipe ideas: request run created: 3 created (2 with air_fryer, 3 with a preferred method, 0 from the web), 0 skipped, 0 rejected, 0 failed" {
		t.Fatalf("logged %q", logged)
	}
	// Only created ideas count: one Food already has is skipped.
	kitchen = newKitchen()
	kitchen.exists["Knuspriges Hähnchen aus dem Airfryer"] = true
	job = newJob(kitchen, &fakeIdeas{answer: threeIdeas}, &now)
	job.Request()
	job.Tick(context.Background())
	if run := job.Status().LastRun; run.Created != 2 || run.WithFirstMethod != 1 || run.WithPreferredMethod != 1 {
		t.Fatalf("run = %+v", run)
	}
	var status map[string]any
	raw, _ := json.Marshal(job.Status())
	if json.Unmarshal(raw, &status) != nil || status["lastRun"].(map[string]any)["withFirstMethod"] != 1.0 || status["lastRun"].(map[string]any)["firstMethod"] != "air_fryer" {
		t.Fatalf("status = %s", raw)
	}
}

func TestARunCountsIdeasFromTheWeb(t *testing.T) {
	now := time.Date(2026, 10, 12, 10, 0, 0, 0, ownerZone)
	answer := strings.Replace(threeIdeas, `"source":"ai","sourceUrl":null}`, `"source":"web","sourceUrl":"https://www.chefkoch.de/rezepte/42/haehnchen-airfryer.html"}`, 1)
	kitchen := newKitchen()
	var logged []string
	job := newJob(kitchen, &fakeIdeas{answer: answer}, &now)
	job.WebSearch = WebSearchLive
	job.Logf = func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }
	job.Request()
	job.Tick(context.Background())
	if run := job.Status().LastRun; run.Created != 3 || run.FromWeb != 1 {
		t.Fatalf("run = %+v", run)
	}
	if first := kitchen.arguments["food_create_idea"][0]; first["source"] != "web" || first["sourceUrl"] != "https://www.chefkoch.de/rezepte/42/haehnchen-airfryer.html" {
		t.Fatalf("idea = %#v", first)
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "1 from the web") {
		t.Fatalf("logged %q", logged)
	}
	// With search off the same answer's web idea is rejected, not counted.
	off := newJob(newKitchen(), &fakeIdeas{answer: answer}, &now)
	off.Request()
	off.Tick(context.Background())
	if run := off.Status().LastRun; run.Created != 2 || run.Rejected != 1 || run.FromWeb != 0 {
		t.Fatalf("run = %+v", run)
	}
}
