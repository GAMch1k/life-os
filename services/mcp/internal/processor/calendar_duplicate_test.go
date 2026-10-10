package processor

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

// restates makes the duplicate check's search for title (on the planned
// start's day) find these events.
func restates(tools *calendarTools, title string, events ...map[string]any) {
	if tools.dayMatches == nil {
		tools.dayMatches = map[string][]map[string]any{}
	}
	tools.dayMatches[titleSearch(title)] = append(tools.dayMatches[titleSearch(title)], events...)
	for _, event := range events {
		tools.events[event["id"].(string)] = event
	}
}

// dateWithIra is the event of the first capture: "Свидание с Ирой" tonight,
// 20:00-23:00 Berlin, with no reminders of its own.
func dateWithIra() map[string]any {
	event := timedEvent("event-date", "Свидание с Ирой", "2026-10-10T18:00:00Z", "2026-10-10T21:00:00Z")
	event["reminders"] = []any{}
	event["useDefaultReminders"] = true
	event["priority"] = "medium"
	return event
}

// restatedDatePlan is what Codex planned for the second capture: the same
// event, with Codex's default hour, and the reminder.
const restatedDatePlan = `{"actions":[{"tool":"calendar_create","arguments":{"title":"Свидание с Ирой","allDay":false,"startAt":"2026-10-10T20:00:00+02:00","endAt":"2026-10-10T21:00:00+02:00","reminders":[{"kind":"relative","minutesBefore":120}]}}],"confidence":0.95}`

func lastAudit(t *testing.T, tools *calendarTools) map[string]any {
	t.Helper()
	processed := tools.records[len(tools.records)-1]
	actions := processed["actions"].([]map[string]any)
	if processed["status"] != "processed" || len(actions) != 1 {
		t.Fatalf("audit = %#v", tools.records)
	}
	return actions[0]
}

func TestARestatedEventWithAReminderUpdatesThatEventInsteadOfCreatingASecond(t *testing.T) {
	// BUG-33, live: "Сегодня в 8 вечера начало свидания с Ирой. Ну, пускай
	// будет 3 часа." created the event; four minutes later this capture was
	// planned as another calendar_create.
	tools := newCalendarTools("Сегодня в восемь вечера свидания с Ирой. Напомни мне за два часа до этого, что у меня свидание.")
	restates(tools, "Свидание с Ирой", dateWithIra())
	runCalendarPlan(t, tools, restatedDatePlan)

	if contains(tools.calls, "calendar_create") || tools.review != nil || !contains(tools.calls, "inbox_resolve") {
		t.Fatalf("calls = %#v, review = %#v", tools.calls, tools.review)
	}
	// Only the reminder: the event keeps its 23:00 end and everything else.
	want := map[string]any{"eventId": "event-date", "reminders": []any{map[string]any{"kind": "relative", "minutesBefore": 120.0}}}
	if got := tools.inputs["calendar_update"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("calendar_update input = %#v, want %#v", got, want)
	}
	// One search: the planned title on the planned start's day in Berlin,
	// among the events the AI may change, like every event lookup.
	if len(tools.searches) != 1 {
		t.Fatalf("searches = %v", tools.searches)
	}
	if search := tools.searches[0]; search.Get("from") != "2026-10-09T22:00:00Z" || search.Get("to") != "2026-10-10T22:00:00Z" || search.Get("timeZone") != "Europe/Berlin" || !searchesChangeableEvents(search) || search.Get("q") == "" {
		t.Fatalf("search = %v", search)
	}
	// The attempt shows the conversion.
	audit := lastAudit(t, tools)
	if audit["tool"] != "calendar_update" || audit["convertedFrom"] != "calendar_create" || audit["arguments"].(map[string]any)["eventId"] != "event-date" || audit["plannedArguments"].(map[string]any)["title"] != "Свидание с Ирой" {
		t.Fatalf("audit = %#v", audit)
	}
}

func TestADuplicateStartsWhenThePlanDoesAndHasItsTitle(t *testing.T) {
	for _, tc := range []struct {
		name, title, startAt string
		duplicate            bool
	}{
		{"same time and title", "Свидание с Ирой", "2026-10-10T20:00:00+02:00", true},
		{"another case", "свидание с ирой", "2026-10-10T20:00:00+02:00", true},
		{"punctuation and spacing", "Свидание  с Ирой!", "2026-10-10T20:00:00+02:00", true},
		{"the same instant in UTC", "Свидание с Ирой", "2026-10-10T18:00:00Z", true},
		{"five minutes later", "Свидание с Ирой", "2026-10-10T20:05:00+02:00", true},
		{"six minutes later", "Свидание с Ирой", "2026-10-10T20:06:00+02:00", false},
		{"another time the same day", "Свидание с Ирой", "2026-10-10T18:00:00+02:00", false},
		{"another title", "Свидание с Олей", "2026-10-10T20:00:00+02:00", false},
		{"a longer title", "Свидание с Ирой в кино", "2026-10-10T20:00:00+02:00", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tools := newCalendarTools("Свидание с Ирой в 8, напомни за час")
			// Calendar's search finds the event for any of these titles.
			restates(tools, tc.title, dateWithIra())
			runCalendarPlan(t, tools, `{"actions":[{"tool":"calendar_create","arguments":{"title":"`+tc.title+`","allDay":false,"startAt":"`+tc.startAt+`","reminders":[{"kind":"relative","minutesBefore":60}]}}],"confidence":0.95}`)
			if tc.duplicate != contains(tools.calls, "calendar_update") || tc.duplicate == contains(tools.calls, "calendar_create") || !contains(tools.calls, "inbox_resolve") {
				t.Fatalf("calls = %#v", tools.calls)
			}
		})
	}
}

func TestSeveralDuplicatesGoToHumanReviewAndTheChosenOneIsUpdated(t *testing.T) {
	first, second := dateWithIra(), dateWithIra()
	second["id"] = "event-date-2"
	second["location"] = "Kino"
	tools := newCalendarTools("Сегодня в восемь вечера свидания с Ирой. Напомни мне за два часа до этого.")
	restates(tools, "Свидание с Ирой", first, second)
	runCalendarPlan(t, tools, restatedDatePlan)

	if contains(tools.calls, "calendar_create") || contains(tools.calls, "calendar_update") || contains(tools.calls, "inbox_resolve") {
		t.Fatalf("calls = %#v", tools.calls)
	}
	if tools.review == nil || tools.review["errorSummary"] != duplicateReason || !reflect.DeepEqual(tools.candidateIDs(), []string{"event-date", "event-date-2"}) {
		t.Fatalf("review = %#v", tools.review)
	}
	if label := tools.candidates()[1]["title"]; label != "Свидание с Ирой · Sat 10 Oct 2026 20:00–23:00 · Kino" {
		t.Fatalf("label = %q", label)
	}
	proposed := tools.review["proposedAction"].(plannedAction)
	if proposed.Tool != "calendar_create" || proposed.Arguments["title"] != "Свидание с Ирой" {
		t.Fatalf("proposed = %#v", proposed)
	}

	// Choosing one runs the create as an update of that event, without
	// planning or searching again.
	tools = newCalendarTools("Сегодня в восемь вечера свидания с Ирой. Напомни мне за два часа до этого.", first, second)
	approved := map[string]any{"tool": "calendar_create", "arguments": map[string]any{"title": "Свидание с Ирой", "allDay": false, "startAt": "2026-10-10T20:00:00+02:00", "endAt": "2026-10-10T21:00:00+02:00", "reminders": []any{map[string]any{"kind": "relative", "minutesBefore": 120}}, "taskId": "event-date-2"}}
	approvedItem(tools, approved)
	runner := Runner{Planner: fakePlanner{err: errors.New("an approved item is not planned again")}, Tools: tools, Model: "test", TimeZone: berlin(t)}
	if _, err := runner.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(tools.searches) != 0 || !reflect.DeepEqual(tools.gets, []string{"event-date-2"}) || contains(tools.calls, "calendar_create") {
		t.Fatalf("searches = %v, gets = %v, calls = %#v", tools.searches, tools.gets, tools.calls)
	}
	want := map[string]any{"eventId": "event-date-2", "reminders": []any{map[string]any{"kind": "relative", "minutesBefore": 120.0}}}
	if got := tools.inputs["calendar_update"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("calendar_update input = %#v, want %#v", got, want)
	}
	if audit := lastAudit(t, tools); audit["convertedFrom"] != "calendar_create" || !contains(tools.calls, "inbox_resolve") {
		t.Fatalf("audit = %#v, calls = %#v", audit, tools.calls)
	}
}

func TestAnAllDayDuplicateIsUpdated(t *testing.T) {
	birthday := map[string]any{"id": "event-birthday", "title": "Anna's birthday", "allDay": true, "startDate": "2026-10-12", "endDate": "2026-10-12", "status": "active", "source": map[string]any{"kind": "manual"}, "reminders": []any{}}
	tools := newCalendarTools("Anna's birthday on Monday, remind me the evening before at 19:00")
	restates(tools, "Anna's Birthday", birthday)
	runCalendarPlan(t, tools, `{"actions":[{"tool":"calendar_create","arguments":{"title":"Anna's Birthday","allDay":true,"startDate":"2026-10-12","endDate":"2026-10-12","reminders":[{"kind":"localTime","daysBefore":1,"localTime":"19:00"}]}}],"confidence":0.95}`)

	want := map[string]any{"eventId": "event-birthday", "reminders": []any{map[string]any{"kind": "localTime", "daysBefore": 1.0, "localTime": "19:00"}}}
	if got := tools.inputs["calendar_update"]; !reflect.DeepEqual(got, want) || contains(tools.calls, "calendar_create") {
		t.Fatalf("calendar_update input = %#v, want %#v; calls = %#v", got, want, tools.calls)
	}
	if search := tools.searches[0]; search.Get("from") != "2026-10-11T22:00:00Z" || search.Get("to") != "2026-10-12T22:00:00Z" {
		t.Fatalf("search = %v", search)
	}

	// A timed event that day is not the all-day one.
	tools = newCalendarTools("Anna's birthday party on Monday at 18:00")
	restates(tools, "Anna's birthday", birthday)
	runCalendarPlan(t, tools, `{"actions":[{"tool":"calendar_create","arguments":{"title":"Anna's birthday","allDay":false,"startAt":"2026-10-12T00:00:00+02:00"}}],"confidence":0.95}`)
	if !contains(tools.calls, "calendar_create") || contains(tools.calls, "calendar_update") {
		t.Fatalf("calls = %#v", tools.calls)
	}
}

func TestARestatedEventWithANewDurationGetsTheNewEnd(t *testing.T) {
	// "Tomorrow at 19:00 dinner with Anna" made a one-hour event; "Tomorrow
	// at 19:00 dinner with Anna for two hours" ends it at 21:00.
	dinner := timedEvent("event-dinner", "Dinner with Anna", "2026-10-11T17:00:00Z", "2026-10-11T18:00:00Z")
	tools := newCalendarTools("Tomorrow at 19:00 dinner with Anna for two hours")
	restates(tools, "Dinner with Anna", dinner)
	runCalendarPlan(t, tools, `{"actions":[{"tool":"calendar_create","arguments":{"title":"Dinner with Anna","allDay":false,"startAt":"2026-10-11T19:00:00+02:00","endAt":"2026-10-11T21:00:00+02:00"}}],"confidence":0.95}`)
	want := map[string]any{"eventId": "event-dinner", "endAt": "2026-10-11T21:00:00+02:00"}
	if got := tools.inputs["calendar_update"]; !reflect.DeepEqual(got, want) || contains(tools.calls, "calendar_create") {
		t.Fatalf("calendar_update input = %#v, want %#v; calls = %#v", got, want, tools.calls)
	}
}

func TestDuplicateChangeCarriesOnlyWhatTheCaptureAdds(t *testing.T) {
	timed := func() map[string]any {
		event := timedEvent("e", "Dinner with Anna", "2026-10-11T17:00:00Z", "2026-10-11T20:00:00Z")
		event["reminders"] = []any{map[string]any{"kind": "relative", "minutesBefore": 30.0}, map[string]any{"kind": "localTime", "daysBefore": 1.0, "localTime": "19:00"}}
		event["useDefaultReminders"] = true
		event["priority"] = "medium"
		return event
	}
	located := timed()
	located["location"] = "Café Rosa"
	located["description"] = "Table for two"
	trip := map[string]any{"id": "t", "title": "Trip", "allDay": true, "startDate": "2026-10-16", "endDate": "2026-10-18", "priority": "medium"}
	start := map[string]any{"title": "Dinner with Anna", "startAt": "2026-10-11T19:00:00+02:00"}
	with := func(base map[string]any, fields map[string]any) map[string]any {
		planned := map[string]any{}
		for field, value := range base {
			planned[field] = value
		}
		for field, value := range fields {
			planned[field] = value
		}
		return planned
	}
	for _, tc := range []struct {
		name    string
		planned map[string]any
		event   map[string]any
		want    map[string]any
	}{
		{"reminders merge without duplicates", with(start, map[string]any{"reminders": []any{map[string]any{"kind": "relative", "minutesBefore": 30.0}, map[string]any{"kind": "relative", "minutesBefore": 120.0}}}), timed(),
			map[string]any{"reminders": []any{map[string]any{"kind": "relative", "minutesBefore": 30.0}, map[string]any{"kind": "localTime", "daysBefore": 1.0, "localTime": "19:00"}, map[string]any{"kind": "relative", "minutesBefore": 120.0}}}},
		{"reminders it already has", with(start, map[string]any{"reminders": []any{map[string]any{"kind": "localTime", "daysBefore": 1.0, "localTime": "19:00"}}}), timed(), map[string]any{}},
		{"a place and notes it lacks", with(start, map[string]any{"location": "Café Rosa", "description": "Table for two"}), timed(), map[string]any{"location": "Café Rosa", "description": "Table for two"}},
		{"its own place and notes stay", with(start, map[string]any{"location": "Rosa", "description": "Remind me"}), located, map[string]any{}},
		{"a priority", with(start, map[string]any{"priority": "high"}), timed(), map[string]any{"priority": "high"}},
		{"the priority it has", with(start, map[string]any{"priority": "medium"}), timed(), map[string]any{}},
		{"only the reminders it names", with(start, map[string]any{"useDefaultReminders": false}), timed(), map[string]any{"useDefaultReminders": false}},
		{"Codex's default hour keeps a longer event", with(start, map[string]any{"endAt": "2026-10-11T20:00:00+02:00"}), timed(), map[string]any{}},
		{"a stated end", with(start, map[string]any{"endAt": "2026-10-11T23:30:00+02:00"}), timed(), map[string]any{"endAt": "2026-10-11T23:30:00+02:00"}},
		{"the end it has", with(start, map[string]any{"endAt": "2026-10-11T22:00:00+02:00"}), timed(), map[string]any{}},
		{"an end before it starts", with(start, map[string]any{"endAt": "2026-10-11T18:00:00+02:00"}), timed(), map[string]any{}},
		{"all-day: one day keeps a longer trip", map[string]any{"title": "Trip", "allDay": true, "startDate": "2026-10-16", "endDate": "2026-10-16"}, trip, map[string]any{}},
		{"all-day: a stated last day", map[string]any{"title": "Trip", "allDay": true, "startDate": "2026-10-16", "endDate": "2026-10-20"}, trip, map[string]any{"endDate": "2026-10-20"}},
		{"nothing new", start, timed(), map[string]any{}},
	} {
		if got := duplicateChange(tc.planned, tc.event); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: %#v, want %#v", tc.name, got, tc.want)
		}
	}
}

func TestARestatementThatAddsNothingChangesAndCreatesNothing(t *testing.T) {
	// The same capture sent twice: the event already has it all.
	event := dateWithIra()
	event["reminders"] = []any{map[string]any{"kind": "relative", "minutesBefore": 120.0}}
	tools := newCalendarTools("Сегодня в восемь вечера свидания с Ирой. Напомни мне за два часа до этого.")
	restates(tools, "Свидание с Ирой", event)
	runCalendarPlan(t, tools, restatedDatePlan)

	if contains(tools.calls, "calendar_create") || contains(tools.calls, "calendar_update") || tools.review != nil || !contains(tools.calls, "inbox_resolve") {
		t.Fatalf("calls = %#v, review = %#v", tools.calls, tools.review)
	}
	if audit := lastAudit(t, tools); audit["tool"] != "none" || audit["convertedFrom"] != "calendar_create" || audit["duplicateOf"] != "event-date" || audit["reason"] != unchangedDuplicateReason {
		t.Fatalf("audit = %#v", audit)
	}
}

// searchesChangeableEvents reports whether a search asks Calendar for
// exactly the events every event lookup of the processor may change.
func searchesChangeableEvents(search url.Values) bool {
	for key, values := range changeableEvents {
		if !reflect.DeepEqual(search[key], values) {
			return false
		}
	}
	return true
}

// filteringTools answers calendar_search the way Calendar applies the
// processor's filter: sourceKind, and editable (Life OS events and events
// marked editable).
type filteringTools struct{ *calendarTools }

func (f filteringTools) Call(ctx context.Context, name string, args map[string]any) (json.RawMessage, error) {
	raw, err := f.calendarTools.Call(ctx, name, args)
	if name != "calendar_search" || err != nil {
		return raw, err
	}
	query, _ := url.ParseQuery(args["query"].(string))
	var result struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	kept := []map[string]any{}
	for _, event := range result.Items {
		source, _ := event["source"].(map[string]any)
		kind := text(source["kind"])
		if query.Has("sourceKind") && query.Get("sourceKind") != kind {
			continue
		}
		if query.Get("editable") == "true" && kind != "manual" && event["editable"] != true {
			continue
		}
		kept = append(kept, event)
	}
	return json.Marshal(map[string]any{"items": kept})
}

func TestTheDuplicateGuardNeverTouchesEventsManagedByTheirSource(t *testing.T) {
	// Feed and read-only Google events are their source's: a capture of the
	// same title and time still creates a Life OS event.
	ics := timedEvent("event-ics", "Restmüll", "2026-10-12T04:00:00Z", "2026-10-12T05:00:00Z")
	ics["source"] = map[string]any{"kind": "ics", "name": "ZAW"}
	google := timedEvent("event-google", "Restmüll", "2026-10-12T04:00:00Z", "2026-10-12T05:00:00Z")
	google["source"] = map[string]any{"kind": "google", "name": "Holidays"}
	google["editable"] = false
	tools := newCalendarTools("Monday at 6 Restmüll, remind me the evening before")
	restates(tools, "Restmüll", ics, google)
	runner := Runner{Planner: fakePlanner{output: `{"actions":[{"tool":"calendar_create","arguments":{"title":"Restmüll","allDay":false,"startAt":"2026-10-12T06:00:00+02:00","reminders":[{"kind":"localTime","daysBefore":1,"localTime":"19:00"}]}}],"confidence":0.95}`}, Tools: filteringTools{tools}, Model: "test", TimeZone: berlin(t)}
	if _, err := runner.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	if !contains(tools.calls, "calendar_create") || contains(tools.calls, "calendar_update") || tools.review != nil {
		t.Fatalf("calls = %#v, review = %#v", tools.calls, tools.review)
	}
	if len(tools.searches) != 1 || !searchesChangeableEvents(tools.searches[0]) {
		t.Fatalf("searches = %v", tools.searches)
	}
}

func TestARestatedRecurringOccurrenceIsLeftToTheCalendar(t *testing.T) {
	gym := timedEvent("event-gym", "Gym", "2026-10-11T05:00:00Z", "2026-10-11T06:00:00Z")
	gym["recurrence"] = map[string]any{"frequency": "weekly", "interval": 1.0}
	gym["occurrence"] = map[string]any{"seriesId": "event-gym", "recurrenceId": "2026-10-11T05:00:00Z"}
	tools := newCalendarTools("Tomorrow at 7 gym, remind me an hour before")
	restates(tools, "Gym", gym)
	runCalendarPlan(t, tools, `{"actions":[{"tool":"calendar_create","arguments":{"title":"Gym","allDay":false,"startAt":"2026-10-11T07:00:00+02:00","reminders":[{"kind":"relative","minutesBefore":60}]}}],"confidence":0.95}`)
	if contains(tools.calls, "calendar_create") || contains(tools.calls, "calendar_update") || tools.review == nil || tools.review["errorSummary"] != recurringChangeReason {
		t.Fatalf("calls = %#v, review = %#v", tools.calls, tools.review)
	}
}

func TestAFailedDuplicateCheckIsRetriedAndCreatesNothing(t *testing.T) {
	tools := newCalendarTools("Сегодня в восемь вечера свидания с Ирой. Напомни мне за два часа до этого.")
	tools.failSearch = true
	runCalendarPlan(t, tools, restatedDatePlan)
	if contains(tools.calls, "calendar_create") || contains(tools.calls, "inbox_resolve") {
		t.Fatalf("calls = %#v", tools.calls)
	}
	if last := tools.records[len(tools.records)-1]; last["status"] != "retryable" || !strings.HasPrefix(last["errorSummary"].(string), "calendar search failed") {
		t.Fatalf("audit = %#v", tools.records)
	}
}

func TestTitlesCompareWithoutCaseDiacriticsOrPunctuation(t *testing.T) {
	for _, pair := range [][2]string{
		{"Свидание с Ирой", "свидание с Ирой"},
		{"Café Rosa", "cafe rosa"},
		{"Ёлка", "елка"},
		{"Dinner  with Anna!", "dinner with anna"},
		{"Zahnärztin", "ZAHNARZTIN"},
	} {
		if comparableTitle(pair[0]) != comparableTitle(pair[1]) {
			t.Errorf("%q and %q differ: %q, %q", pair[0], pair[1], comparableTitle(pair[0]), comparableTitle(pair[1]))
		}
	}
	if comparableTitle("Dinner with Anna") == comparableTitle("Dinner with Hanna") {
		t.Error("different titles compare equal")
	}
}

func TestTitleSearchFindsEveryVariantWithinCalendarsLimits(t *testing.T) {
	for _, tc := range []struct{ title, want string }{
		{"Dinner with Anna", "Dinner with Anna"},
		// Calendar compares diacritics, so each word is searched by its
		// longest part without them.
		{"Zahnärztin", "rztin"},
		{"Café Rosa", "Caf Rosa"},
		{"Ёлка", "лка"},
		{"Anna's birthday!", "Anna s birthday"},
		{"Meeting meeting MEETING", "Meeting"},
		{"!!!", ""},
		{"one two three four five six seven eight nine ten eleven twelve", "one two three four five six seven eight nine ten"},
	} {
		if got := titleSearch(tc.title); got != tc.want {
			t.Errorf("titleSearch(%q) = %q, want %q", tc.title, got, tc.want)
		}
	}
	var long []string
	for letter := 'a'; letter < 'i'; letter++ {
		long = append(long, strings.Repeat(string(letter), 30))
	}
	if got := titleSearch(strings.Join(long, " ")); got == "" || len(got) > maxEventSearchLength {
		t.Errorf("titleSearch is %d characters", len(got))
	}
}

// dentistWithReminder is the one dentist event, with a reminder of its own
// a day before.
func dentistWithReminder() map[string]any {
	event := timedEvent("event-dentist", "Dentist", "2026-10-16T13:00:00Z", "2026-10-16T14:00:00Z")
	event["reminders"] = []any{map[string]any{"kind": "relative", "minutesBefore": 1440.0}}
	return event
}

func TestAnAIUpdateAddsRemindersToTheEventsOwn(t *testing.T) {
	// "Remind me 2 hours before the dentist" keeps the day-before reminder.
	tools := newCalendarTools("Remind me 2 hours before the dentist", dentistWithReminder())
	tools.matches["dentist"] = []string{"event-dentist"}
	runCalendarPlan(t, tools, `{"actions":[{"tool":"calendar_update","arguments":{"eventQuery":"dentist","reminders":[{"kind":"relative","minutesBefore":120}]}}],"confidence":0.95}`)
	want := map[string]any{"eventId": "event-dentist", "reminders": []any{map[string]any{"kind": "relative", "minutesBefore": 1440.0}, map[string]any{"kind": "relative", "minutesBefore": 120.0}}}
	if got := tools.inputs["calendar_update"]; !reflect.DeepEqual(got, want) || !contains(tools.calls, "inbox_resolve") {
		t.Fatalf("calendar_update input = %#v, want %#v; calls = %#v", got, want, tools.calls)
	}

	// The same for an update the owner approved in Human Review: the chosen
	// event is read for its reminders.
	tools = newCalendarTools("Remind me 2 hours before my appointment", dentistWithReminder())
	approvedItem(tools, map[string]any{"tool": "calendar_update", "arguments": map[string]any{"eventQuery": "appointment", "reminders": []any{map[string]any{"kind": "relative", "minutesBefore": 120}}, "taskId": "event-dentist"}})
	runner := Runner{Planner: fakePlanner{err: errors.New("not planned")}, Tools: tools, Model: "test", TimeZone: berlin(t)}
	if _, err := runner.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := tools.inputs["calendar_update"]; !reflect.DeepEqual(got, want) || !reflect.DeepEqual(tools.gets, []string{"event-dentist"}) {
		t.Fatalf("calendar_update input = %#v, want %#v; gets = %v", got, want, tools.gets)
	}
}

func TestAnAIUpdateWithRemindersTheEventHasChangesNothing(t *testing.T) {
	tools := newCalendarTools("Remind me a day before the dentist", dentistWithReminder())
	tools.matches["dentist"] = []string{"event-dentist"}
	runCalendarPlan(t, tools, `{"actions":[{"tool":"calendar_update","arguments":{"eventQuery":"dentist","reminders":[{"kind":"relative","minutesBefore":1440}]}}],"confidence":0.95}`)
	if contains(tools.calls, "calendar_update") || tools.review != nil || !contains(tools.calls, "inbox_resolve") {
		t.Fatalf("calls = %#v, review = %#v", tools.calls, tools.review)
	}
	if audit := lastAudit(t, tools); audit["tool"] != "none" || audit["plannedTool"] != "calendar_update" || audit["eventId"] != "event-dentist" || audit["reason"] != unchangedRemindersReason {
		t.Fatalf("audit = %#v", audit)
	}

	// With another change, only that change is sent.
	tools = newCalendarTools("The dentist is important, remind me a day before", dentistWithReminder())
	tools.matches["dentist"] = []string{"event-dentist"}
	runCalendarPlan(t, tools, `{"actions":[{"tool":"calendar_update","arguments":{"eventQuery":"dentist","reminders":[{"kind":"relative","minutesBefore":1440}],"priority":"high"}}],"confidence":0.95}`)
	if got, want := tools.inputs["calendar_update"], map[string]any{"eventId": "event-dentist", "priority": "high"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("calendar_update input = %#v, want %#v", got, want)
	}
}

func TestAnAIUpdateThatClearsRemindersReplacesThem(t *testing.T) {
	for _, tc := range []struct {
		name, clear string
		want        map[string]any
	}{
		{"replace", `["reminders"]`, map[string]any{"eventId": "event-dentist", "reminders": []any{map[string]any{"kind": "relative", "minutesBefore": 120.0}}}},
		{"replace and remove the place", `["reminders","location"]`, map[string]any{"eventId": "event-dentist", "reminders": []any{map[string]any{"kind": "relative", "minutesBefore": 120.0}}, "clear": []any{"location"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// "Only remind me 2 hours before the dentist"
			tools := newCalendarTools("Only remind me 2 hours before the dentist", dentistWithReminder())
			tools.matches["dentist"] = []string{"event-dentist"}
			runCalendarPlan(t, tools, `{"actions":[{"tool":"calendar_update","arguments":{"eventQuery":"dentist","reminders":[{"kind":"relative","minutesBefore":120}],"clear":`+tc.clear+`}}],"confidence":0.95}`)
			if got := tools.inputs["calendar_update"]; !reflect.DeepEqual(got, tc.want) || !contains(tools.calls, "inbox_resolve") {
				t.Fatalf("calendar_update input = %#v, want %#v; calls = %#v", got, tc.want, tools.calls)
			}
		})
	}
}
