package processor

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/GAMch1k/life-os/pkg/activity"
)

// The recipe ideas job (P15-09) runs next to capture processing: once a day
// at LIFE_OS_IDEAS_TIME, when the pantry has food in stock, and when the
// owner asks ("Get ideas" in "What can I cook?", through the Dashboard to
// POST /ideas/requests). Each run reads the owner's preferences, pantry,
// suggestions, ideas, and recent meals through MCP, asks Codex for three
// recipe ideas as structured output (CodexCLI.Ideas, with web search only
// when configured for this job), checks them strictly, and leaves each with
// food_create_idea for the owner to save or dismiss. It never saves or
// dismisses ideas.

// ideasActor is who the job's reads and ideas are attributed to.
var ideasActor = activity.Agent("codex-recipe-ideas")

const (
	// ideasPerRun is how many ideas a run asks for and leaves at most.
	ideasPerRun = 3
	// requestGap and requestsPerDay throttle the owner's requests.
	requestGap     = 10 * time.Minute
	requestsPerDay = 6
	// dailyTries is how often a failed daily run is tried again that day,
	// dailyRetry how long after.
	dailyTries = 3
	dailyRetry = time.Hour
	// expiringDays is how soon an item expires to be used first, as Food's
	// suggestions count it.
	expiringDays = 3
	// ideasTick is how often the job checks whether a run is due.
	ideasTick = 30 * time.Second
)

// ideaGenerator writes recipe ideas; CodexCLI is the real one.
type ideaGenerator interface {
	Ideas(ctx context.Context, prompt, schema, webSearch string) (string, error)
}

// IdeasJob schedules and runs recipe ideas. Its schedule and the owner's
// requests live in memory: a restart forgets a request not yet run and the
// throttle's counts, and a daily run is not repeated on a day the job
// already left ideas (Food has them, by this job's actor).
type IdeasJob struct {
	Generator ideaGenerator
	Tools     toolCaller
	// TimeZone is the owner's; nil means UTC.
	TimeZone *time.Location
	// DailyAt is the minute of the owner's day the daily run is due; -1
	// turns it off.
	DailyAt int
	// WebSearch is Codex's web_search mode for this job.
	WebSearch string
	// Now is the clock; tests replace it.
	Now func() time.Time
	// OnStatus, when set, receives the job's status after every change.
	OnStatus func(IdeasStatus)

	mu sync.Mutex
	// queued is the owner's request waiting for a run.
	queued *time.Time
	// requests are when the owner's accepted requests came, for the throttle.
	requests []time.Time
	running  bool
	// dailyDone is the owner-local day the daily run is done (or skipped)
	// for; dailyFailures counts its failed tries on dailyFailedDay, and
	// dailyRetryAt is when it may be tried again.
	dailyDone      string
	dailyFailures  int
	dailyFailedDay string
	dailyRetryAt   time.Time
	lastRun        *IdeasRun
}

// IdeasStatus is the job's part of the processor's /status.
type IdeasStatus struct {
	// DailyAt is when the daily run is due ("16:00"); empty when off.
	DailyAt   string     `json:"dailyAt,omitempty"`
	WebSearch string     `json:"webSearch"`
	NextRunAt *time.Time `json:"nextRunAt"`
	Running   bool       `json:"running"`
	Queued    bool       `json:"queued"`
	LastRun   *IdeasRun  `json:"lastRun"`
}

// IdeasRun is what a run did.
type IdeasRun struct {
	// Trigger is daily or request.
	Trigger    string    `json:"trigger"`
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt"`
	// Created ideas were left for the owner; Skipped ones Food already had
	// (409); Rejected ones failed the checks; Failed ones Food refused.
	Created  int `json:"created"`
	Skipped  int `json:"skipped"`
	Rejected int `json:"rejected"`
	Failed   int `json:"failed"`
	// Outcome: created, none (nothing new), empty_pantry or done_today (a
	// daily run with nothing to do), or failed.
	Outcome string `json:"outcome"`
	Error   string `json:"error,omitempty"`
}

// RequestAnswer is the answer to the owner's request for ideas.
type RequestAnswer struct {
	// Queued: a run will start shortly (or one is already running).
	Queued  bool `json:"queued"`
	Running bool `json:"running"`
	// Reason says why a request was refused: too_soon or daily_limit,
	// with when to ask again.
	Reason  string     `json:"reason,omitempty"`
	RetryAt *time.Time `json:"retryAt,omitempty"`
}

func (j *IdeasJob) now() time.Time {
	if j.Now != nil {
		return j.Now()
	}
	return time.Now()
}

func (j *IdeasJob) location() *time.Location {
	if j.TimeZone != nil {
		return j.TimeZone
	}
	return time.UTC
}

// day is the owner-local date of t.
func (j *IdeasJob) day(t time.Time) string { return t.In(j.location()).Format(time.DateOnly) }

// Request queues a run for the owner, throttled to one every requestGap and
// requestsPerDay a day (the owner's day). A request while a run waits or
// runs joins it and counts for nothing.
func (j *IdeasJob) Request() RequestAnswer {
	j.mu.Lock()
	defer j.mu.Unlock()
	now := j.now()
	if j.running || j.queued != nil {
		return RequestAnswer{Queued: true, Running: j.running}
	}
	today := j.day(now)
	kept := j.requests[:0]
	for _, at := range j.requests {
		if j.day(at) == today {
			kept = append(kept, at)
		}
	}
	j.requests = kept
	if n := len(j.requests); n > 0 && now.Sub(j.requests[n-1]) < requestGap {
		retry := j.requests[n-1].Add(requestGap)
		return RequestAnswer{Reason: "too_soon", RetryAt: &retry}
	}
	if len(j.requests) >= requestsPerDay {
		local := now.In(j.location())
		retry := time.Date(local.Year(), local.Month(), local.Day()+1, 0, 0, 0, 0, j.location())
		return RequestAnswer{Reason: "daily_limit", RetryAt: &retry}
	}
	j.requests = append(j.requests, now)
	j.queued = &now
	j.publish()
	return RequestAnswer{Queued: true}
}

// Loop checks every ideasTick whether a run is due and runs it, until ctx
// ends. Runs never overlap; Codex's slots keep them from crowding capture
// planning beyond LIFE_OS_CODEX_MAX_CONCURRENCY.
func (j *IdeasJob) Loop(ctx context.Context) {
	j.mu.Lock()
	j.publish()
	j.mu.Unlock()
	// The first check comes a tick after startup, when MCP is up.
	ticker := time.NewTicker(ideasTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			j.Tick(ctx)
		}
	}
}

// Tick runs the job once when a run is due: the owner's request first, else
// the daily run once its time has come.
func (j *IdeasJob) Tick(ctx context.Context) {
	trigger, at, ok := j.take()
	if !ok {
		return
	}
	run := j.run(ctx, trigger, at)
	j.mu.Lock()
	defer j.mu.Unlock()
	j.running = false
	j.lastRun = &run
	if trigger == "daily" {
		day := j.day(at)
		if run.Outcome == "failed" {
			if j.dailyFailedDay != day {
				j.dailyFailedDay, j.dailyFailures = day, 0
			}
			j.dailyFailures++
			j.dailyRetryAt = j.now().Add(dailyRetry)
		}
		if run.Outcome != "failed" || j.dailyFailures >= dailyTries {
			j.dailyDone = day
		}
	}
	j.publish()
}

// take claims a due run, if any.
func (j *IdeasJob) take() (trigger string, at time.Time, ok bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	now := j.now()
	switch {
	case j.running:
		return "", time.Time{}, false
	case j.queued != nil:
		at, j.queued = *j.queued, nil
		trigger = "request"
	case j.dailyDue(now):
		at, trigger = now, "daily"
	default:
		return "", time.Time{}, false
	}
	j.running = true
	j.publish()
	return trigger, at, true
}

// dailyDue reports whether the daily run is due at now.
func (j *IdeasJob) dailyDue(now time.Time) bool {
	if j.DailyAt < 0 {
		return false
	}
	local := now.In(j.location())
	return local.Hour()*60+local.Minute() >= j.DailyAt && j.dailyDone != local.Format(time.DateOnly) && !now.Before(j.dailyRetryAt)
}

// Status is the job's status now.
func (j *IdeasJob) Status() IdeasStatus {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.status()
}

func (j *IdeasJob) status() IdeasStatus {
	status := IdeasStatus{WebSearch: j.WebSearch, Running: j.running, Queued: j.queued != nil, LastRun: j.lastRun}
	if status.WebSearch == "" {
		status.WebSearch = WebSearchDisabled
	}
	now := j.now()
	if j.DailyAt >= 0 {
		status.DailyAt = fmt.Sprintf("%02d:%02d", j.DailyAt/60, j.DailyAt%60)
	}
	if j.queued != nil {
		status.NextRunAt = &now
	} else if j.DailyAt >= 0 {
		local := now.In(j.location())
		next := time.Date(local.Year(), local.Month(), local.Day(), j.DailyAt/60, j.DailyAt%60, 0, 0, j.location())
		if j.dailyDone == local.Format(time.DateOnly) {
			next = next.AddDate(0, 0, 1)
		} else if next.Before(j.dailyRetryAt) {
			next = j.dailyRetryAt
		}
		status.NextRunAt = &next
	}
	return status
}

func (j *IdeasJob) publish() {
	if j.OnStatus != nil {
		j.OnStatus(j.status())
	}
}

// Routes serves the job on the processor's private port: GET /ideas (its
// status) and POST /ideas/requests (the owner asks for ideas; 202, or 429
// with when to ask again). A request must name its actor, as every write in
// Life OS does.
func (j *IdeasJob) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /ideas", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, j.Status())
	})
	mux.HandleFunc("POST /ideas/requests", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := activity.ParseActor(r.Header.Get(activity.ActorHeader)); !ok {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "actor_required", "message": "Say who asks for ideas in X-Life-OS-Actor."}})
			return
		}
		answer := j.Request()
		switch answer.Reason {
		case "":
			writeJSON(w, http.StatusAccepted, answer)
		case "too_soon":
			writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": map[string]string{"code": "too_soon", "message": "Ideas were asked for a few minutes ago: ask again in a little while."}, "retryAt": answer.RetryAt})
		default:
			writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": map[string]string{"code": "daily_limit", "message": fmt.Sprintf("Ideas can be asked for %d times a day: ask again tomorrow.", requestsPerDay)}, "retryAt": answer.RetryAt})
		}
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// ideasContext is what a run tells Codex about the owner's kitchen.
type ideasContext struct {
	Today        string           `json:"today"`
	Preferences  ideasPreferences `json:"preferences"`
	Pantry       []pantryEntry    `json:"pantry"`
	ExpiringSoon []pantryEntry    `json:"expiringSoon"`
	SavedRecipes []string         `json:"savedRecipes"`
	Ideas        []string         `json:"existingIdeas"`
	RecentMeals  []mealEntry      `json:"recentMeals"`
	// ranToday is set when this job already left ideas today.
	ranToday bool
}

type ideasPreferences struct {
	Methods      []string `json:"methods"`
	Dislikes     []string `json:"dislikes"`
	DietaryNotes string   `json:"dietaryNotes,omitempty"`
}

type pantryEntry struct {
	Name      string `json:"name"`
	Amount    string `json:"amount,omitempty"`
	ExpiresOn string `json:"expiresOn,omitempty"`
	// InDays is how many days until it expires, for those expiring soon.
	InDays *int `json:"expiresInDays,omitempty"`
}

type mealEntry struct {
	Date  string `json:"date"`
	Slot  string `json:"slot"`
	Title string `json:"title"`
}

// run is one run of the job; its outcome is returned, never an error.
func (j *IdeasJob) run(ctx context.Context, trigger string, at time.Time) IdeasRun {
	run := IdeasRun{Trigger: trigger, StartedAt: j.now()}
	finish := func(outcome string, err error) IdeasRun {
		run.Outcome, run.FinishedAt = outcome, j.now()
		if err != nil {
			run.Error = activity.Preview(err.Error(), 500)
		}
		return run
	}
	runID := trigger + ":" + j.day(at)
	if trigger == "request" {
		runID = trigger + ":" + at.UTC().Format(time.RFC3339Nano)
	}
	ctx = activity.WithTrace(ctx, activity.Trace{CorrelationID: "recipe-ideas-" + runKey(runID), Actor: ideasActor})
	kitchen, err := j.gather(ctx, at)
	if err != nil {
		return finish("failed", err)
	}
	if trigger == "daily" && len(kitchen.Pantry) == 0 {
		return finish("empty_pantry", nil)
	}
	if trigger == "daily" && kitchen.ranToday {
		return finish("done_today", nil)
	}
	raw, err := j.Generator.Ideas(ctx, ideasPrompt(kitchen, j.WebSearch), ideasSchema, j.WebSearch)
	if err != nil {
		return finish("failed", err)
	}
	ideas, rejected, err := checkIdeas(raw, kitchen, j.WebSearch)
	run.Rejected = rejected
	if err != nil {
		return finish("failed", err)
	}
	for _, idea := range ideas {
		arguments := idea.arguments()
		arguments["idempotencyKey"] = ideaKey(runID, idea.Title)
		_, err := j.Tools.Call(ctx, "food_create_idea", arguments)
		switch {
		case err == nil:
			run.Created++
		case strings.Contains(err.Error(), "returned 409") && (strings.Contains(err.Error(), "idea_exists") || strings.Contains(err.Error(), "recipe_exists")):
			run.Skipped++
		default:
			run.Failed++
			run.Error = activity.Preview(err.Error(), 500)
		}
	}
	if run.Created > 0 {
		return finish("created", nil)
	}
	if run.Failed > 0 {
		return finish("failed", nil)
	}
	return finish("none", nil)
}

// gather reads what Codex is told, through MCP: the preferences, the
// in-stock pantry (and what expires within expiringDays), the top
// suggestions (saved recipes not to repeat), the ideas new and dismissed
// (not to repeat), saved ones too (to know whether the job already ran
// today), and the meals planned two weeks back and one ahead.
func (j *IdeasJob) gather(ctx context.Context, at time.Time) (ideasContext, error) {
	local := at.In(j.location())
	today := local.Format(time.DateOnly)
	kitchen := ideasContext{Today: local.Format("Monday 2006-01-02"), Pantry: []pantryEntry{}, ExpiringSoon: []pantryEntry{}, SavedRecipes: []string{}, Ideas: []string{}, RecentMeals: []mealEntry{}}
	var preferences struct {
		Methods      []string `json:"methods"`
		Dislikes     []string `json:"dislikes"`
		DietaryNotes *string  `json:"dietaryNotes"`
	}
	if err := j.read(ctx, "food_get_preferences", map[string]any{}, &preferences); err != nil {
		return kitchen, err
	}
	kitchen.Preferences = ideasPreferences{Methods: nonNil(preferences.Methods), Dislikes: nonNil(preferences.Dislikes)}
	if preferences.DietaryNotes != nil {
		kitchen.Preferences.DietaryNotes = *preferences.DietaryNotes
	}
	var pantry struct {
		Items []struct {
			Name      string  `json:"name"`
			Amount    *string `json:"amount"`
			ExpiresOn *string `json:"expiresOn"`
		} `json:"items"`
	}
	if err := j.read(ctx, "food_list_pantry_items", map[string]any{"state": "in_stock", "limit": 200}, &pantry); err != nil {
		return kitchen, err
	}
	midnight, _ := time.ParseInLocation(time.DateOnly, today, j.location())
	for _, item := range pantry.Items {
		entry := pantryEntry{Name: item.Name, Amount: deref(item.Amount), ExpiresOn: deref(item.ExpiresOn)}
		if expires, err := time.ParseInLocation(time.DateOnly, entry.ExpiresOn, j.location()); err == nil {
			if days := int(expires.Sub(midnight).Hours()/24 + 0.5); days <= expiringDays {
				entry.InDays = &days
				kitchen.ExpiringSoon = append(kitchen.ExpiringSoon, entry)
			}
		}
		kitchen.Pantry = append(kitchen.Pantry, entry)
	}
	var suggestions struct {
		Items []struct {
			Recipe struct {
				Title string `json:"title"`
			} `json:"recipe"`
		} `json:"items"`
	}
	if err := j.read(ctx, "food_list_suggestions", map[string]any{"limit": 10}, &suggestions); err != nil {
		return kitchen, err
	}
	for _, suggestion := range suggestions.Items {
		kitchen.SavedRecipes = append(kitchen.SavedRecipes, suggestion.Recipe.Title)
	}
	for _, state := range []string{"new", "dismissed", "saved"} {
		var ideas struct {
			Items []struct {
				Title     string         `json:"title"`
				CreatedAt time.Time      `json:"createdAt"`
				CreatedBy activity.Actor `json:"createdBy"`
			} `json:"items"`
		}
		if err := j.read(ctx, "food_list_ideas", map[string]any{"state": state, "limit": 100}, &ideas); err != nil {
			return kitchen, err
		}
		for _, idea := range ideas.Items {
			if state != "saved" {
				kitchen.Ideas = append(kitchen.Ideas, idea.Title)
			}
			if idea.CreatedBy == ideasActor && j.day(idea.CreatedAt) == today {
				kitchen.ranToday = true
			}
		}
	}
	var meals struct {
		Items []mealEntry `json:"items"`
	}
	from, to := midnight.AddDate(0, 0, -14).Format(time.DateOnly), midnight.AddDate(0, 0, 7).Format(time.DateOnly)
	if err := j.read(ctx, "food_list_meal_plan", map[string]any{"from": from, "to": to}, &meals); err != nil {
		return kitchen, err
	}
	kitchen.RecentMeals = append(kitchen.RecentMeals, meals.Items...)
	return kitchen, nil
}

// read calls an MCP read tool and decodes its data into out.
func (j *IdeasJob) read(ctx context.Context, tool string, arguments map[string]any, out any) error {
	raw, err := j.Tools.Call(ctx, tool, arguments)
	if err != nil {
		return fmt.Errorf("%s: %w", tool, err)
	}
	if err := json.Unmarshal(toolData(raw), out); err != nil {
		return fmt.Errorf("decode %s: %w", tool, err)
	}
	return nil
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// ideasSchema is the JSON Schema of Codex's answer (--output-schema). Every
// field is required, as structured output wants; the optional ones may be
// null. Lengths and counts are checked afterwards (checkIdeas).
const ideasSchema = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["ideas"],
  "properties": {
    "ideas": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["title", "ingredients", "steps", "servings", "tags", "methods", "reason", "source", "sourceUrl"],
        "properties": {
          "title": {"type": "string"},
          "ingredients": {
            "type": "array",
            "items": {
              "type": "object",
              "additionalProperties": false,
              "required": ["name", "amount"],
              "properties": {"name": {"type": "string"}, "amount": {"type": ["string", "null"]}}
            }
          },
          "steps": {"type": "array", "items": {"type": "string"}},
          "servings": {"type": ["integer", "null"]},
          "tags": {"type": "array", "items": {"type": "string"}},
          "methods": {"type": "array", "items": {"type": "string", "enum": ["air_fryer", "oven", "stovetop", "slow_cooker", "grill", "microwave", "no_cook"]}},
          "reason": {"type": "string"},
          "source": {"type": "string", "enum": ["ai", "web"]},
          "sourceUrl": {"type": ["string", "null"]}
        }
      }
    }
  }
}`

// ideasPromptRules are the job's fixed instructions; the run's kitchen and,
// when web search is on, its rule follow.
const ideasPromptRules = "You suggest recipe ideas to the owner of Life OS, who cooks at home. Return only JSON matching the given schema, with up to 3 ideas. The kitchen below is the owner's: today, cooking preferences, the food in stock (pantry, with expiry dates), the food expiring soon (expiringSoon, within 3 days), saved recipes (savedRecipes), ideas already suggested (existingIdeas), and meals planned recently or soon (recentMeals). Rules: suggest realistic home recipes for a weeknight, each different from the others. Use the food that expires soonest first: each idea should use at least one item of expiringSoon when there is any, and other food in stock where it fits. Name an ingredient that is in stock exactly as the pantry names it, so Life OS sees it is at home. Never use a disliked ingredient (preferences.dislikes), in any form, language, or word, and follow preferences.dietaryNotes. Prefer the preferred cooking methods, the first most: when the air fryer is preferred, cook in the air fryer where it suits and give its temperature and time in the steps. Do not suggest a saved recipe, an idea already suggested, or a meal planned recently. Write titles, ingredients, and steps in the owner's language: the language most pantry names are in, else English. steps: 3 to 10 concise lines, one action each, without numbers. servings: for how many people (2 when nothing says otherwise). tags: 1 to 4 short lower-case words. methods: the cooking methods the steps use. reason: one sentence on why this idea now, such as which expiring item it uses and how it is cooked. "

// ideasPrompt is the prompt of one run: the rules, the source rule for its
// web search mode, and the kitchen.
func ideasPrompt(kitchen ideasContext, webSearch string) string {
	source := "source is \"ai\" and sourceUrl is null: write the ideas yourself; do not search the web. "
	if webSearch == WebSearchCached || webSearch == WebSearchLive {
		source = "You may search the web for recipes that fit these rules. An idea written by you has source \"ai\" and sourceUrl null; one found on a page has source \"web\" and sourceUrl that page's http or https address, its ingredients as the page lists them (named as the pantry names them where they are in stock), and its steps summarized in your own words, never copied from the page. "
	}
	encoded, _ := json.Marshal(kitchen)
	return ideasPromptRules + source + "Kitchen: " + string(encoded)
}

// idea is one checked recipe idea.
type idea struct {
	Title       string     `json:"title"`
	Ingredients []ideaPart `json:"ingredients"`
	Steps       []string   `json:"steps"`
	Servings    *int       `json:"servings"`
	Tags        []string   `json:"tags"`
	Methods     []string   `json:"methods"`
	Reason      string     `json:"reason"`
	Source      string     `json:"source"`
	SourceURL   *string    `json:"sourceUrl"`
}

type ideaPart struct {
	Name   string  `json:"name"`
	Amount *string `json:"amount"`
}

// Limits of a checked idea, sane for a home recipe and within Food's.
const (
	maxIdeaTitle      = 120
	minIdeaParts      = 2
	maxIdeaParts      = 25
	maxIdeaPartName   = 100
	maxIdeaPartAmount = 50
	minIdeaSteps      = 2
	maxIdeaSteps      = 15
	maxIdeaStep       = 300
	maxIdeaTags       = 8
	maxIdeaTag        = 40
	maxIdeaServings   = 12
	minIdeaReason     = 10
	maxIdeaReason     = 300
)

var ideaMethods = []string{"air_fryer", "oven", "stovetop", "slow_cooker", "grill", "microwave", "no_cook"}

// checkIdeas decodes Codex's answer and keeps the ideas that pass every
// check, at most ideasPerRun: sane lengths and counts, Food's methods, a
// web idea only with web search on and its page, no disliked ingredient in
// the title or an ingredient, and nothing already saved, suggested, or in
// the same answer. It returns how many it rejected; an answer that is not
// the schema's JSON is an error.
func checkIdeas(raw string, kitchen ideasContext, webSearch string) ([]idea, int, error) {
	clean := strings.TrimSpace(raw)
	clean = strings.TrimPrefix(clean, "```json")
	clean = strings.TrimPrefix(clean, "```")
	clean = strings.TrimSuffix(strings.TrimSpace(clean), "```")
	var answer struct {
		Ideas []idea `json:"ideas"`
	}
	if err := json.Unmarshal([]byte(clean), &answer); err != nil {
		return nil, 0, fmt.Errorf("decode ideas: %w", err)
	}
	taken := map[string]bool{}
	for _, title := range append(append([]string{}, kitchen.SavedRecipes...), kitchen.Ideas...) {
		taken[foodNormalize(title)] = true
	}
	var kept []idea
	rejected := 0
	for _, candidate := range answer.Ideas {
		checked, ok := checkIdea(candidate, kitchen.Preferences.Dislikes, webSearch)
		if !ok || taken[foodNormalize(checked.Title)] || len(kept) == ideasPerRun {
			rejected++
			continue
		}
		taken[foodNormalize(checked.Title)] = true
		kept = append(kept, checked)
	}
	return kept, rejected, nil
}

// checkIdea trims one idea and checks it.
func checkIdea(in idea, dislikes []string, webSearch string) (idea, bool) {
	runes := utf8.RuneCountInString
	out := idea{Title: strings.TrimSpace(in.Title), Reason: strings.TrimSpace(in.Reason), Source: in.Source, Servings: in.Servings}
	if runes(out.Title) < 3 || runes(out.Title) > maxIdeaTitle || runes(out.Reason) < minIdeaReason || runes(out.Reason) > maxIdeaReason {
		return idea{}, false
	}
	if len(in.Ingredients) < minIdeaParts || len(in.Ingredients) > maxIdeaParts {
		return idea{}, false
	}
	for _, part := range in.Ingredients {
		name := strings.TrimSpace(part.Name)
		if name == "" || runes(name) > maxIdeaPartName {
			return idea{}, false
		}
		checked := ideaPart{Name: name}
		if amount := deref(part.Amount); amount != "" {
			if runes(amount) > maxIdeaPartAmount {
				return idea{}, false
			}
			checked.Amount = &amount
		}
		out.Ingredients = append(out.Ingredients, checked)
	}
	for _, step := range in.Steps {
		if step = strings.TrimSpace(strings.ReplaceAll(step, "\n", " ")); step != "" {
			if runes(step) > maxIdeaStep {
				return idea{}, false
			}
			out.Steps = append(out.Steps, step)
		}
	}
	if len(out.Steps) < minIdeaSteps || len(out.Steps) > maxIdeaSteps {
		return idea{}, false
	}
	if out.Servings != nil && (*out.Servings < 1 || *out.Servings > maxIdeaServings) {
		return idea{}, false
	}
	for _, tag := range in.Tags {
		if tag = strings.TrimSpace(tag); tag != "" {
			if runes(tag) > maxIdeaTag {
				return idea{}, false
			}
			out.Tags = append(out.Tags, tag)
		}
	}
	if len(out.Tags) > maxIdeaTags {
		return idea{}, false
	}
	for _, method := range in.Methods {
		if !slices.Contains(ideaMethods, method) {
			return idea{}, false
		}
		if !slices.Contains(out.Methods, method) {
			out.Methods = append(out.Methods, method)
		}
	}
	switch out.Source {
	case "ai":
	case "web":
		link := deref(in.SourceURL)
		parsed, err := url.Parse(link)
		if (webSearch != WebSearchCached && webSearch != WebSearchLive) || err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return idea{}, false
		}
		out.SourceURL = &link
	default:
		return idea{}, false
	}
	for _, dislike := range dislikes {
		if containsDislike(out.Title, dislike) {
			return idea{}, false
		}
		for _, part := range out.Ingredients {
			if containsDislike(part.Name, dislike) {
				return idea{}, false
			}
		}
	}
	return out, true
}

// containsDislike reports whether text names a disliked ingredient: every
// word of the dislike matches a word of the text, compared without case,
// diacritics, or punctuation, where words match when they are equal or,
// from four letters on, one begins or ends the other, also without a plural
// or case ending ("Pilze" in "Pilz", "Steinpilz", and "Pilzpfanne",
// "mushrooms" in "mushroom", "грибы" in "грибной"). It errs on the side of
// leaving an idea out.
func containsDislike(text, dislike string) bool {
	words := strings.Fields(comparableTitle(text))
	wanted := strings.Fields(comparableTitle(dislike))
	if len(wanted) == 0 {
		return false
	}
	for _, want := range wanted {
		found := false
		for _, word := range words {
			if wordsMatch(word, want) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func wordsMatch(a, b string) bool {
	for _, x := range wordForms(a) {
		for _, y := range wordForms(b) {
			shorter, longer := x, y
			if utf8.RuneCountInString(shorter) > utf8.RuneCountInString(longer) {
				shorter, longer = longer, shorter
			}
			if x == y || (utf8.RuneCountInString(shorter) >= 4 && (strings.HasPrefix(longer, shorter) || strings.HasSuffix(longer, shorter))) {
				return true
			}
		}
	}
	return false
}

// wordForms is a word and its stems without a common German, English, or
// Russian plural or case ending, each of at least four letters.
func wordForms(word string) []string {
	forms := []string{word}
	for _, ending := range []string{"en", "er", "es", "e", "n", "s", "ов", "ей", "ы", "и", "а", "я"} {
		if stem, ok := strings.CutSuffix(word, ending); ok && utf8.RuneCountInString(stem) >= 4 {
			forms = append(forms, stem)
		}
	}
	return forms
}

// arguments is the food_create_idea call for a checked idea.
func (i idea) arguments() map[string]any {
	ingredients := make([]any, 0, len(i.Ingredients))
	for _, part := range i.Ingredients {
		entry := map[string]any{"name": part.Name}
		if part.Amount != nil {
			entry["amount"] = *part.Amount
		}
		ingredients = append(ingredients, entry)
	}
	arguments := map[string]any{"title": i.Title, "reason": i.Reason, "source": i.Source, "ingredients": ingredients, "steps": strings.Join(i.Steps, "\n")}
	if i.Servings != nil {
		arguments["servings"] = *i.Servings
	}
	if len(i.Tags) > 0 {
		arguments["tags"] = i.Tags
	}
	if len(i.Methods) > 0 {
		arguments["methods"] = i.Methods
	}
	if i.SourceURL != nil {
		arguments["sourceUrl"] = *i.SourceURL
	}
	return arguments
}

// ideaKey is the Idempotency-Key of an idea: the run and the idea's
// normalised title, so a repeated write of the same run is answered with
// the first, and another title, or another run, is a new write.
func ideaKey(runID, title string) string {
	return "ideas-" + runKey(runID+"\x00"+foodNormalize(title))
}

func runKey(value string) string {
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", sum[:16])
}

// deref is a string pointer's trimmed value, or "".
func deref(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}
