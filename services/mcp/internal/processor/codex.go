package processor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// planPrompt instructs Codex to classify one Inbox item into a constrained
// JSON plan. Codex only plans; the processor validates and executes actions
// through MCP. Each run appends the owner's local time (timeContext) and then
// the item, so the fixed instructions stay a stable prompt prefix.
const planPrompt = "Classify this Inbox item. Return only JSON in this schema: {\"actions\":[{\"tool\":\"task_create|task_update|task_complete|task_reopen|calendar_create|calendar_update|calendar_cancel|finance_create_manual_transaction|food_add_shopping_items|food_mark_bought|food_add_pantry_items|food_update_pantry_item|food_plan_meal|food_clear_meal|food_add_missing_ingredients|recipe_import|food_mark_running_low|food_add_staple|food_remove_staple\",\"arguments\":{...}}],\"ambiguous\":boolean,\"confidence\":0.0,\"reason\":string}. Existing Task mutations require a taskQuery and should add taskQueryAlternatives. Each term is matched as a case-insensitive substring of existing Task titles and notes, so use the shortest distinctive word or phrase that would literally appear in the Task title (for example \"chicken\" or \"Anna\"), never a paraphrase or a whole sentence. The owner writes Tasks in English, Russian, and German, and the title may be in another language or word form than the item, so taskQueryAlternatives is an array of up to 6 more terms for the same thing: its translations into the other two languages and word stems that also match inflected forms, long enough to stay distinctive (\"куриц\", not \"кур\"). For \"I already cooked the chicken\": taskQuery \"chicken\", taskQueryAlternatives [\"куриц\", \"Hähnchen\", \"Huhn\"]; for \"Я уже вынес мусор\": \"мусор\", [\"trash\", \"garbage\", \"Müll\"]. Alternatives name the same thing as taskQuery, never a more generic word or a fragment that other Tasks would share; omit them when the item quotes the exact Task title. The processor searches open Tasks (completed ones for task_reopen) with every term and proceeds only when exactly one Task matches in total; otherwise it asks the user, so you do not need to know the existing Tasks. A statement that something has been done (for example \"I already took out the trash\" or \"I called the dentist\") is task_complete for the Task it refers to, with taskQuery set to that distinctive word (\"trash\", \"dentist\"); that groceries were bought is food_mark_bought (below). Set ambiguous true and actions to [] only when no Task can be identified from the words at all. task_update may only use taskQuery, taskQueryAlternatives, title, notes, dueAt (RFC3339), clearDueAt. task_complete/task_reopen may only use taskQuery, taskQueryAlternatives. Never create a Task as fallback for an uncertain existing Task. task_create arguments may only use title, notes, dueAt (RFC3339). calendar_create arguments may only use title, description, location (where it takes place, such as \"Dr. Weber\" or an address; keep it out of the title), allDay, startAt/endAt (RFC3339) or startDate/endDate (ISO date; give the end only when the item states it or a duration, otherwise the processor makes a timed event one hour and an all-day event one day), reminders (only when the item asks for one: an array of {\"kind\":\"relative\",\"minutesBefore\":30} or {\"kind\":\"localTime\",\"daysBefore\":1,\"localTime\":\"19:00\"}; they are added to the owner's default reminders), and priority (only when the item says how important it is: \"high\" for important or urgent, such as \"wichtig\" or \"важно\", \"low\" for not important or low priority; otherwise leave it out). Tasks are to-dos (call, buy, renew, pay, …) with an optional due date (groceries to buy go on the shopping list instead, see below); Calendar events are appointments and meetings with a time and maybe a place. Moving or rescheduling something changes a Task when it reads as a to-do (\"Move the electrician call to Friday\" is task_update) and an event when it reads as an appointment or meeting. Changing or moving an existing event is calendar_update, and cancelling, deleting, or removing one is calendar_cancel. A request to be reminded of, or to add a reminder to, an event that already exists (even when the item restates the event) is calendar_update with eventQuery and reminders, not calendar_create. Both require an eventQuery and should add eventQueryAlternatives, chosen exactly like taskQuery and taskQueryAlternatives but matched against event titles, locations, and notes: for \"Move the dentist appointment to Monday at 9\": eventQuery \"dentist\", eventQueryAlternatives [\"Zahnarzt\", \"стоматолог\", \"зубн\"]. The processor searches active events from today on and changes one only when exactly one matches, otherwise it asks the owner, so you do not need to know the existing events; the owner approves every cancellation. calendar_update may only use eventQuery, eventQueryAlternatives, and the fields that change: title, description, location, allDay, startAt/endAt or startDate/endDate, reminders (added to the event's own; to replace them, also name reminders in clear), priority, and clear (an array naming description, location, or reminders to remove). To reschedule, give the new start, and the end only when the item states it or a duration; the processor otherwise keeps the event's length. calendar_cancel may only use eventQuery, eventQueryAlternatives, and onDate (the ISO date of the day the item names for the event, such as the capture's date for \"Cancel gym today\"; leave it out when it names no day): for a repeating event the processor cancels only its occurrence on that day, else its next one. Never create an event as a fallback for an uncertain existing event. Repeating events are set up only in the Calendar: for an item asking for a new repeating event (\"every Saturday\"), set ambiguous true and actions to [] with reason \"Recurring events are created in the Calendar.\". finance_create_manual_transaction records cash the owner spent or received, such as \"spent 12 euros at the bakery\" or \"потратил 20 евро наличкой в Lidl\"; propose it only when the item states the amount. It may only use amount ({\"amount\": signed integer in cents, negative for money spent, such as -1200 for 12 euros spent, \"currency\": three-letter code, EUR when the item names none}), counterparty (who was paid or who paid, such as \"Bäckerei\" or \"Lidl\"), description (what it was for, only when the item says), occurredOn (ISO date, only when the item names another day than the capture's, such as yesterday), and note. Leave out accountId: the processor records it in the owner's Cash account. Card and bank payments arrive from the bank connection by themselves, so for an item about paying by card or bank transfer set ambiguous true and actions to [] with reason \"Card and bank payments arrive from the bank connection.\". Groceries go on the shopping list; other purchases stay Tasks. Food, drinks, household, and drugstore items to buy (\"buy milk\", \"купить хлеб\", \"Zahnpasta kaufen\") are food_add_shopping_items, never task_create, also when the item gives a date or time (\"buy milk tomorrow\" only puts milk on the list). Other purchases (\"buy a flash drive tomorrow\", \"купить зарядку для телефона\") stay task_create, with dueAt when a date is given. An item that explicitly asks for a Task or a reminder (\"Create a task: buy milk\", \"Создай задачу\", \"Erstelle eine Aufgabe\", \"Remind me to buy milk at 18:00\") is task_create. Split an item that mixes them: \"buy milk and call mom\" is food_add_shopping_items with milk plus task_create \"Call mom\". When it is genuinely unclear whether something is a grocery or household item or another purchase, set ambiguous true and actions to [] instead of guessing. food_add_shopping_items may only use items: an array of at most 30 objects, one per thing to buy, each with name (the item as the owner wrote it, in their language and words, short and without the quantity; never translate it: \"купить молоко и яйца\" is \"молоко\" and \"яйца\", \"Milch und 6 Eier\" is \"Milch\" and \"Eier\" with amount \"6\"), amount (the quantity as free text, such as \"6\", \"2 l\", or \"500 g\", only when the item states one), note (only a detail the item adds about it), storeName (only when the item names the store, as written: \"buy toothpaste at dm\" or \"в Lidl купить хлеб\"; an unknown store is created), and aisle (only when you are certain: produce, bakery, dairy, meat_fish, frozen, pantry, drinks, snacks, household, drugstore, or other; otherwise leave it out and the aisle rules place it). A statement that groceries were bought (\"bought the milk\", \"купил молоко\", \"Milch gekauft\", \"I already bought the chicken\") is food_mark_bought, one per item, which may only use itemQuery and itemQueryAlternatives, chosen like taskQuery and taskQueryAlternatives but matched against the names on the shopping list: for \"I bought the milk\": itemQuery \"milk\", itemQueryAlternatives [\"молок\", \"Milch\"]. The processor marks the one needed list item that matches; when none does, it completes the one open Task that matches instead, and otherwise does nothing; several matches go to the owner. So a purchase stated as done that may not be a grocery (\"I already bought the light bulbs\") is food_mark_bought too, never ambiguous for that reason. Never add an item to the list or create a Task as a fallback for something bought. Groceries the owner has at home, usually with when they expire (\"I have 6 eggs until Friday\", \"в холодильнике йогурт до 14-го\", \"Joghurt läuft am Freitag ab\"), are food_add_pantry_items, which may only use items: at most 30 objects with name and amount as for the list, location (fridge, freezer, pantry, or other, only when the item says where it is kept), and expiresOn (the ISO date it expires, resolved like any other date: \"until Friday\" is the coming Friday, \"до 14-го\" the next 14th). A pantry item used up or finished (\"used up the milk\", \"молоко закончилось\", \"die Milch ist alle\") is food_update_pantry_item with itemQuery, itemQueryAlternatives, and state \"used_up\", or state \"thrown_away\" for one thrown away or spoiled; it may only use those three. The processor changes the one in-stock pantry item that matches, does nothing when none does, and asks the owner when several do. When the item also says to buy more (\"молоко закончилось, надо купить\"), add food_add_shopping_items for it too (when it is a staple, Food has already put it back on the list and the processor leaves it out). Something running low but not finished yet (\"almost out of milk\", \"мясо заканчивается\", \"почти закончилось молоко\", \"fast keine Milch mehr\", \"Milch wird knapp\") is food_mark_running_low, never food_update_pantry_item or task_create. It may only use itemQuery (the item as the owner wrote it, in their language and words, without the quantity; never translate it: \"мясо\", \"молоко\", \"Milch\"), itemQueryAlternatives (up to 6 more terms, chosen like taskQueryAlternatives: its translations into the other two languages and word stems that also match inflected forms; for \"мясо заканчивается\": itemQuery \"мясо\", itemQueryAlternatives [\"мяс\", \"meat\", \"Fleisch\"]), and addToList (true only when the item also says to buy it or put it on the list, such as \"мясо заканчивается, купи\" or \"almost out of milk, buy some\"; otherwise leave it out). The processor marks the one staple or in-stock pantry item those words name, and asks the owner when several match. A staple goes back on the shopping list by itself, so never add food_add_shopping_items for something running low: buying it is addToList. Saying that something should always be at home (\"milk is a staple\", \"молоко всегда должно быть дома\", \"Milch immer da haben\", \"Eier sollen immer im Haus sein\") is food_add_staple, which may only use name (as the owner wrote it, never translated), amount (only when the item says how much to buy each time), and storeName (only when it names the store); it puts nothing on the list now. Saying that something no longer needs to be at home (\"remove milk from staples\", \"молоко больше не нужно держать дома\", \"Milch muss nicht mehr immer da sein\") is food_remove_staple with only itemQuery and itemQueryAlternatives, chosen as for running low; the processor removes the one staple they name. Meals come before the grocery rule. Planning what to eat at a meal of a day (\"Lasagne for dinner on Thursday\", \"на ужин в среду паста\", \"Mittwoch Abendessen: Pasta\") is food_plan_meal, never task_create, calendar_create, or food_add_shopping_items; an appointment to eat with someone at a time or place (\"dinner with Anna at 19:00\") stays a Calendar event. food_plan_meal may only use recipeQuery (the meal as the owner names it, in their words and language, never translated, such as \"Lasagne\", \"паста\", or \"суп\"), recipeQueryAlternatives (up to 6 more terms: the meal's name in the other two languages and its most distinctive word, such as [\"Lasagne\", \"lasagna\"] for \"лазанья\"), date (the meal's ISO date, resolved like any other date; the capture's date when the item names no day), slot (breakfast for breakfast, завтрак, or Frühstück; lunch for lunch, обед, or Mittagessen; dinner for dinner, supper, ужин, or Abendessen), servings (only when the item says for how many), and note (only a detail the item adds). The processor plans the one recipe whose title the words name, or the only one whose title contains them, asks the owner when several do, and otherwise plans recipeQuery as free text; a meal already planned in that slot is replaced. When the item does not say which meal of the day it is, set ambiguous true and actions to [] with reason \"Say which meal it is: breakfast, lunch, or dinner.\". Cancelling or removing a planned meal (\"cancel dinner on Friday\", \"убери ужин в пятницу\", \"Samstag kein Frühstück\") is food_clear_meal, never calendar_cancel; it may only use date and slot. Putting what a recipe needs on the shopping list (\"add the ingredients for the lasagne to the list\", \"купи всё для борща\", \"alles für die Lasagne einkaufen\") is food_add_missing_ingredients with only recipeQuery and recipeQueryAlternatives, chosen as for food_plan_meal; for the planned meals (\"buy what I need for this week's meals\") it may only use week (\"this\" for the current week, Monday to Sunday, or \"next\"), or from and to (ISO dates, at most 31 days) for other days. Food leaves out what is in the pantry or already on the list. Without such meal or recipe words the grocery rule stands: \"buy pasta\" is food_add_shopping_items. Recipe links. A capture whose main content is a link to a recipe, or that asks to save a recipe from a link, in any language (\"save this recipe https://www.chefkoch.de/rezepte/…\", \"Rezept speichern: https://…\", \"сохрани рецепт https://…\"), is recipe_import, never task_create, calendar_create, or food_add_shopping_items; so is a link to a recipe site (chefkoch.de, lecker.de, bbcgoodfood.com, allrecipes.com, eda.ru, povarenok.ru, …) and a bare instagram.com, tiktok.com, youtube.com, or youtu.be link, such as one shared from the phone. recipe_import may only use url (the http or https link as written; add https:// when it has no scheme), caption (the text that came with the link and may hold the recipe, such as a pasted caption with ingredients and steps, as written; leave it out when there is none), title (only when the owner names the recipe: \"save this as Tante Ingas Käsekuchen\"), and saveLink (true only when the owner explicitly asks to save it, such as \"save this\", \"speichern\", or \"сохрани\", so a post with no readable recipe is kept to review). The processor reads the link and saves the page's recipe, else the caption's, and otherwise asks the owner with the reason. A capture shared from the phone (sourceMetadata.via \"share-target\") is the shared title, text, and link, one per line: a short line naming the page or post is its title, not a caption and not a title the owner gave. A link clearly about something else (a news article, a shop's product page) follows the other rules; for a bare link with nothing saying what to do and no sign of food or cooking, set ambiguous true and actions to [] with reason \"What should I do with this link?\". Photos of letters and receipts. When the item's images show a document (the owner lives in Germany and photographs paper letters, mostly in German, from authorities, insurers, the landlord, or doctors, and receipts), act on what it asks; the item's text steers and wins over these defaults (\"just save the deadline\" plans only that Task, \"это оплачено\" means nothing is left to pay). A text of only \"Photo\" or \"2 photos\" is the placeholder of a capture without words, so no text. A letter with a deadline (\"bis zum 30.10.2026\", \"innerhalb von 14 Tagen ab Zugang\", Frist, Zahlungsziel) is task_create due that day (00:00 in the owner's timezone unless the letter names a time), titled with the sender as printed and the action, in the language of the item's text, else in English (\"Finanzamt: send tax documents\"), with notes of 2 to 4 short lines: what the letter is about, its reference numbers (Aktenzeichen, Steuernummer, Kundennummer, Rechnungsnummer), any amount, and for a relative deadline whether it counts from the letter's printed date or, when it has none, from the capture date. An appointment (Termin with a date, a time, and a place) is calendar_create with the place as location; a letter with both is both actions. A bill (Rechnung with an amount, an IBAN, and a due date) is task_create \"Pay <sender> <amount>\" due on the due date: Life OS never pays. A cash receipt (Kassenbon showing BAR, Bargeld, or cash) is finance_create_manual_transaction with the total (SUMME) as the negative amount in cents, the receipt's currency, the merchant as counterparty, and occurredOn the receipt's date; a card receipt (EC, Karte, girocard, Visa, Mastercard, kontaktlos) is a card payment: no action, with reason \"Card and bank payments arrive from the bank connection.\". Never copy a document's text into a title or notes: summarize, and keep only the key numbers. For a document you cannot read or classify, or one with nothing to do, set ambiguous true and actions to [] with a short description of it as the reason, such as \"A letter from AOK with no deadline: what should I do with it?\". Other photos (a handwritten note, a shopping list, a product) follow the usual rules for what they show. Never claim an action succeeded or execute an action. If information is materially unclear or confidence is below 0.8, set ambiguous true and actions to []. "

// CodexCLI is the processor's agent runtime: the machine's existing,
// authenticated Codex CLI run as a managed subprocess. It keeps the processor
// out of the API-key and direct-model credential business.
//
// Each run gets an explicit working directory, a sanitized environment (no
// inherited secrets or API keys, and no host MCP servers), its own process
// group, a deadline, graceful termination (SIGTERM, then SIGKILL after Grace)
// with cleanup of every child, and structured JSONL output from which the
// Codex session id and failure reason are read.
type CodexCLI struct {
	Command, Model, WorkDir string
	// ReasoningEffort, when set, overrides the Codex model_reasoning_effort.
	ReasoningEffort string
	// Timeout bounds one planning run (default 5 minutes).
	Timeout time.Duration
	// Grace is how long a cancelled or timed-out run may take to exit after
	// SIGTERM before its process group is killed (default 5 seconds).
	Grace time.Duration
	// Slots limits concurrent planning runs; nil means unlimited.
	Slots chan struct{}
	// TimeZone is the owner's configured timezone. Relative dates in a
	// capture are resolved in the capture's own timezone when it has one
	// (WithCaptureTimeZone), else in this one, and every Codex run gets the
	// zone it uses as TZ. Nil means UTC.
	TimeZone *time.Location
	// Now is the clock for the planning prompt; tests replace it.
	Now func() time.Time
	// Run builds the command; tests replace it.
	Run func(context.Context, string, ...string) *exec.Cmd
}

// ErrRunTimedOut marks a planning run stopped at its deadline.
var ErrRunTimedOut = errors.New("Codex run timed out")

// TokenUsage is what Codex reports a run consumed (turn.completed usage).
type TokenUsage struct {
	Input       int `json:"input"`
	CachedInput int `json:"cachedInput"`
	Output      int `json:"output"`
	Reasoning   int `json:"reasoning"`
}

// RunObserver receives lifecycle events of one planning run.
type RunObserver interface {
	// Spawned is called once the Codex process has started.
	Spawned()
	// Session is called when Codex reports its session (thread) id.
	Session(id string)
}

type observerKey struct{}

// WithRunObserver attaches an observer to the planning runs started with ctx.
func WithRunObserver(ctx context.Context, observer RunObserver) context.Context {
	return context.WithValue(ctx, observerKey{}, observer)
}

// tokenObserver is implemented by observers that record token usage.
type tokenObserver interface {
	Tokens(TokenUsage)
}

func observerFrom(ctx context.Context) RunObserver {
	if observer, ok := ctx.Value(observerKey{}).(RunObserver); ok {
		return observer
	}
	return nil
}

type capturedAtKey struct{}

// WithCapturedAt tells planning runs started with ctx when the item was
// captured, so "tomorrow" in a capture that waited for a retry still means
// the day after it was captured.
func WithCapturedAt(ctx context.Context, at time.Time) context.Context {
	return context.WithValue(ctx, capturedAtKey{}, at)
}

func capturedAtFrom(ctx context.Context) time.Time {
	at, _ := ctx.Value(capturedAtKey{}).(time.Time)
	return at
}

type captureTimeZoneKey struct{}

// WithCaptureTimeZone tells planning runs started with ctx the timezone the
// item was captured in, such as the capturing browser's (an IANA name). An
// empty or unknown name leaves the configured timezone in place.
func WithCaptureTimeZone(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, captureTimeZoneKey{}, name)
}

// location is the timezone one planning run resolves dates in: the
// capture's own when it is a valid IANA name, else the configured one.
func (c CodexCLI) location(ctx context.Context) *time.Location {
	name, _ := ctx.Value(captureTimeZoneKey{}).(string)
	if location, ok := loadTimeZone(name); ok {
		return location
	}
	return c.TimeZone
}

// delayedCapture is how long after its capture an item is planned before
// the prompt also states the current time.
const delayedCapture = 10 * time.Minute

// timeContext states the owner's timezone and local date, time, and weekday
// for one planning run. Without it Codex sees only the container's UTC clock,
// so "tomorrow at 10" landed two hours off in Berlin, or on the wrong day near
// midnight. The coming week is spelled out because weekday arithmetic is
// where models slip, and its offsets cover a daylight-saving change.
//
// Relative dates mean what they meant when the owner captured the item, so
// the capture time is the reference when known: a capture taken at 23:59 and
// planned at 00:00, or retried hours later, keeps its "tomorrow".
func timeContext(now, capturedAt time.Time, location *time.Location) string {
	if location == nil {
		location = time.UTC
	}
	local := func(t time.Time) string {
		return t.In(location).Format("Monday 2006-01-02 15:04 (-07:00)")
	}
	reference := now
	var b strings.Builder
	if capturedAt.IsZero() {
		fmt.Fprintf(&b, "The owner's timezone is %s; it is now %s there. ", location, local(now))
	} else {
		reference = capturedAt
		fmt.Fprintf(&b, "The owner's timezone is %s. The item was captured on %s there; resolve relative dates from that moment", location, local(capturedAt))
		if now.Sub(capturedAt) > delayedCapture {
			fmt.Fprintf(&b, " (it is now %s)", local(now))
		}
		b.WriteString(". ")
	}
	day := reference.In(location)
	b.WriteString("The coming days are ")
	for i := 1; i <= 7; i++ {
		next := time.Date(day.Year(), day.Month(), day.Day()+i, 12, 0, 0, 0, location)
		if i > 1 {
			b.WriteString(", ")
		}
		b.WriteString(next.Format("Monday 2006-01-02 (-07:00)"))
	}
	tomorrowAtTen := time.Date(day.Year(), day.Month(), day.Day()+1, 10, 0, 0, 0, location)
	fmt.Fprintf(&b, ". Interpret every date and time in the item in the owner's timezone: today, tomorrow, tonight, this or next week, and bare clock times are relative to that local date and time, and a weekday (\"on Friday\") means its next occurrence that is not already past. Write dueAt, startAt, and endAt as RFC3339 with the owner's UTC offset on that date (for example, tomorrow at 10:00 is %s), and startDate, endDate, occurredOn, expiresOn, and a meal's date as the owner's local date. ", tomorrowAtTen.Format(time.RFC3339))
	return b.String()
}

// Plan plans one capture: the plan prompt, the owner's time, the capture's
// images, and the item. Web search is always off for captures.
func (c CodexCLI) Plan(ctx context.Context, content string) (string, error) {
	images := imagesFrom(ctx)
	return c.run(ctx, runSpec{
		images:    images,
		webSearch: WebSearchDisabled,
		prompt: func(c CodexCLI) string {
			return planPrompt + timeContext(c.now(), capturedAtFrom(ctx), c.TimeZone) + imagesNote(images) + "Inbox item: " + content
		},
	})
}

// Ideas asks for recipe ideas (the ideas job): the prompt as given, the
// final message shaped by the JSON schema, and web search as configured
// for this job alone.
func (c CodexCLI) Ideas(ctx context.Context, prompt, schema, webSearch string) (string, error) {
	return c.run(ctx, runSpec{schema: schema, webSearch: webSearch, prompt: func(CodexCLI) string { return prompt }})
}

// Categories asks for transaction categories (the AI categories job): the
// prompt as given and the final message shaped by the JSON schema. It never
// searches the web.
func (c CodexCLI) Categories(ctx context.Context, prompt, schema string) (string, error) {
	return c.run(ctx, runSpec{schema: schema, webSearch: WebSearchDisabled, prompt: func(CodexCLI) string { return prompt }})
}

// BudgetPlan asks for a month's budget plan (the AI budget plan job): the
// prompt as given and the final message shaped by the JSON schema. It never
// searches the web.
func (c CodexCLI) BudgetPlan(ctx context.Context, prompt, schema string) (string, error) {
	return c.run(ctx, runSpec{schema: schema, webSearch: WebSearchDisabled, prompt: func(CodexCLI) string { return prompt }})
}

// Codex's web_search modes the processor uses. Capture planning always
// runs with search disabled; the ideas job runs with LIFE_OS_IDEAS_WEB_SEARCH.
const (
	WebSearchDisabled = "disabled"
	WebSearchCached   = "cached"
	WebSearchLive     = "live"
)

// runSpec is what one Codex run gets beyond the runtime's controls.
type runSpec struct {
	// prompt builds the prompt from the run's CodexCLI, whose TimeZone is
	// the run's own.
	prompt func(CodexCLI) string
	images CaptureImages
	// schema, when set, is the JSON Schema of the final message
	// (--output-schema).
	schema string
	// webSearch is Codex's web_search mode for the run.
	webSearch string
}

// run starts one Codex run with the runtime's controls (a slot, the
// deadline, the sanitized environment, the process group, graceful
// termination) and returns its final message.
func (c CodexCLI) run(ctx context.Context, spec runSpec) (string, error) {
	if c.Slots != nil {
		select {
		case c.Slots <- struct{}{}:
			defer func() { <-c.Slots }()
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	file, err := os.CreateTemp("", "life-os-codex-plan-*")
	if err != nil {
		return "", err
	}
	name := file.Name()
	_ = file.Close()
	defer os.Remove(name)

	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	webSearch := spec.webSearch
	if webSearch != WebSearchCached && webSearch != WebSearchLive {
		webSearch = WebSearchDisabled
	}
	args := []string{"exec", "--json", "--ephemeral", "--skip-git-repo-check", "-s", "read-only", "-C", c.WorkDir, "-m", c.Model, "-c", "mcp_servers={}", "-c", fmt.Sprintf("web_search=%q", webSearch)}
	if c.ReasoningEffort != "" {
		args = append(args, "-c", "model_reasoning_effort="+c.ReasoningEffort)
	}
	if spec.schema != "" {
		schema, err := os.CreateTemp("", "life-os-codex-schema-*.json")
		if err != nil {
			return "", err
		}
		defer os.Remove(schema.Name())
		_, writeErr := schema.WriteString(spec.schema)
		if closeErr := schema.Close(); writeErr != nil || closeErr != nil {
			return "", fmt.Errorf("write output schema: %w", errors.Join(writeErr, closeErr))
		}
		args = append(args, "--output-schema", schema.Name())
	}
	// The capture's images go to Codex as files in a directory of the
	// workdir that lives as long as the run.
	images := spec.images
	if len(images.Images) > 0 {
		dir, err := os.MkdirTemp(c.WorkDir, "capture-images-")
		if err != nil {
			return "", fmt.Errorf("prepare capture images: %w", err)
		}
		defer os.RemoveAll(dir)
		paths, err := writeImages(dir, images.Images)
		if err != nil {
			return "", fmt.Errorf("prepare capture images: %w", err)
		}
		for _, path := range paths {
			args = append(args, "--image", path)
		}
	}
	// This run's prompt and its TZ use the capture's own zone when known.
	c.TimeZone = c.location(ctx)
	prompt := spec.prompt(c)
	// -o ends the list of --image values before the prompt.
	cmd := c.command(runCtx, append(args, "-o", name, prompt)...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	var stderr strings.Builder
	cmd.Stderr = &limitedWriter{w: &stderr, limit: 64 * 1024}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start Codex CLI: %w", err)
	}
	observer := observerFrom(ctx)
	if observer != nil {
		observer.Spawned()
	}
	events := readRunEvents(stdout, observer)
	waitErr := cmd.Wait()
	killGroup(cmd)
	switch {
	case ctx.Err() != nil:
		return "", fmt.Errorf("Codex run cancelled: %w", ctx.Err())
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		return "", fmt.Errorf("%w after %s", ErrRunTimedOut, timeout)
	case waitErr != nil:
		return "", fmt.Errorf("run Codex CLI: %w: %s", waitErr, events.failure(stderr.String()))
	}
	raw, err := os.ReadFile(name)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// writeImages writes a run's images to dir as image-1.jpg, image-2.png, …
// and returns their paths.
func writeImages(dir string, images []Image) ([]string, error) {
	paths := make([]string, 0, len(images))
	for index, image := range images {
		path := filepath.Join(dir, fmt.Sprintf("image-%d%s", index+1, imageExtensions[image.ContentType]))
		if err := os.WriteFile(path, image.Data, 0o600); err != nil {
			return nil, err
		}
		paths = append(paths, path)
	}
	return paths, nil
}

// command builds a Codex CLI command with the runtime's controls: sanitized
// environment, own process group, and graceful termination on cancellation.
func (c CodexCLI) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := c.runner()(ctx, c.binary(), args...)
	cmd.Env = sanitizedEnv(os.Environ())
	if c.TimeZone != nil {
		// Codex's own environment context then reports the owner's zone too,
		// rather than contradicting the prompt with the container's UTC.
		cmd.Env = append(withoutKey(cmd.Env, "TZ"), "TZ="+c.TimeZone.String())
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// Ask the whole group to stop; Wait kills it after WaitDelay.
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
	cmd.WaitDelay = c.grace()
	return cmd
}

func (c CodexCLI) grace() time.Duration {
	if c.Grace > 0 {
		return c.Grace
	}
	return 5 * time.Second
}

// killGroup removes anything the run left behind in its process group.
func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// allowedEnv is everything a Codex run inherits; credentials come only from
// the Codex home (the existing ChatGPT sign-in), never from API keys.
var allowedEnv = map[string]bool{"PATH": true, "HOME": true, "USER": true, "CODEX_HOME": true, "LANG": true, "LC_ALL": true, "TZ": true, "TMPDIR": true, "SSL_CERT_FILE": true, "SSL_CERT_DIR": true}

func withoutKey(environ []string, key string) []string {
	kept := environ[:0]
	for _, entry := range environ {
		if !strings.HasPrefix(entry, key+"=") {
			kept = append(kept, entry)
		}
	}
	return kept
}

func sanitizedEnv(environ []string) []string {
	env := make([]string, 0, len(allowedEnv))
	for _, entry := range environ {
		if key, _, ok := strings.Cut(entry, "="); ok && allowedEnv[key] {
			env = append(env, entry)
		}
	}
	return env
}

// runEvents is what a run's JSONL output said about itself.
type runEvents struct {
	sessionID string
	// turnFailure is the reason of a turn.failed event.
	turnFailure string
	// errors are error events other than transport reconnects.
	errors []string
	// text is non-JSON output, for example from a CLI that predates --json.
	text strings.Builder
}

func readRunEvents(stdout io.Reader, observer RunObserver) *runEvents {
	events := &runEvents{}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		var event struct {
			Type     string `json:"type"`
			ThreadID string `json:"thread_id"`
			Message  string `json:"message"`
			Error    struct {
				Message string `json:"message"`
			} `json:"error"`
			Usage *struct {
				Input       int `json:"input_tokens"`
				CachedInput int `json:"cached_input_tokens"`
				Output      int `json:"output_tokens"`
				Reasoning   int `json:"reasoning_output_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(line, &event) != nil || event.Type == "" {
			if events.text.Len() < 64*1024 {
				events.text.Write(line)
				events.text.WriteByte('\n')
			}
			continue
		}
		switch event.Type {
		case "thread.started":
			if event.ThreadID != "" && events.sessionID == "" {
				events.sessionID = event.ThreadID
				if observer != nil {
					observer.Session(event.ThreadID)
				}
			}
		case "turn.completed":
			if tokens, ok := observer.(tokenObserver); ok && event.Usage != nil {
				tokens.Tokens(TokenUsage{Input: event.Usage.Input, CachedInput: event.Usage.CachedInput, Output: event.Usage.Output, Reasoning: event.Usage.Reasoning})
			}
		case "turn.failed":
			events.turnFailure = event.Error.Message
		case "error":
			if !strings.HasPrefix(event.Message, "Reconnecting") {
				events.errors = append(events.errors, event.Message)
			}
		}
	}
	_, _ = io.Copy(io.Discard, stdout)
	return events
}

// failure is the reason a run failed: Codex's own turn.failed message, else
// its last error event, else the CLI's ERROR lines from text output.
func (e *runEvents) failure(stderr string) string {
	if e.turnFailure != "" {
		return "ERROR: " + e.turnFailure
	}
	if len(e.errors) > 0 {
		return "ERROR: " + e.errors[len(e.errors)-1]
	}
	return codexFailure([]byte(e.text.String() + stderr))
}

type limitedWriter struct {
	w     io.Writer
	limit int
	n     int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if remaining := l.limit - l.n; remaining > 0 {
		chunk := p
		if len(chunk) > remaining {
			chunk = chunk[:remaining]
		}
		written, _ := l.w.Write(chunk)
		l.n += written
	}
	return len(p), nil
}

// codexFailure keeps the CLI's own ERROR lines. The full output also echoes
// the banner and the prompt (including the user's capture), which buries the
// reason, such as a usage limit, behind text that gets truncated.
func codexFailure(out []byte) string {
	var errs []string
	seen := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		// "Reconnecting... n/5" lines are transport retries, not the reason.
		if strings.HasPrefix(line, "ERROR:") && !strings.HasPrefix(line, "ERROR: Reconnecting") && !seen[line] {
			seen[line] = true
			errs = append(errs, line)
		}
	}
	if len(errs) > 0 {
		return strings.Join(errs, " ")
	}
	text := []rune(strings.TrimSpace(string(out)))
	if len(text) > 500 {
		return "…" + string(text[len(text)-500:])
	}
	return string(text)
}

// LoggedIn reports whether `codex login status` succeeds for the configured
// Codex home. It reads local credentials only and uses no model quota.
func (c CodexCLI) LoggedIn(ctx context.Context) bool {
	cmd := c.command(ctx, "login", "status")
	err := cmd.Run()
	killGroup(cmd)
	return err == nil
}

func (c CodexCLI) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c CodexCLI) binary() string {
	if c.Command != "" {
		return c.Command
	}
	return "codex"
}
func (c CodexCLI) runner() func(context.Context, string, ...string) *exec.Cmd {
	if c.Run != nil {
		return c.Run
	}
	return exec.CommandContext
}
