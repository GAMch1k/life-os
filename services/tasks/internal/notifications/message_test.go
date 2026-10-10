package notifications

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/GAMch1k/life-os/pkg/notify"
)

func TestTimedReminderMessage(t *testing.T) {
	call := task("7f8d2b1e-1c1a-4b7e-9a51-2f0c6d1e9a10", "Call  mom\n", "2026-10-10T13:00:00Z") // 15:00 CEST
	due := Due{Kind: KindDue, At: instant("2026-10-10T12:30:00Z"), Key: dueKey(call), Task: call}
	got := dueMessage(due, due.At, false, berlin)
	want := notify.Notification{
		Title: "Call mom", Body: "Due in 30 minutes · 15:00", Importance: notify.Medium, Category: "tasks.due",
		Link: "/tasks", DedupeKey: "tasks:due:7f8d2b1e-1c1a-4b7e-9a51-2f0c6d1e9a10:2026-10-10T13:00:00Z", OccurredAt: due.At,
	}
	if got != want {
		t.Fatalf("message = %+v\nwant %+v", got, want)
	}
	// Sent late: phrased from the time it is sent.
	for sentAt, body := range map[string]string{
		"2026-10-10T12:50:00Z": "Due in 10 minutes · 15:00 (late)",
		"2026-10-10T13:00:00Z": "Due now · 15:00 (late)",
		"2026-10-10T13:20:00Z": "Was due 20 minutes ago · 15:00 (late)",
		"2026-10-10T14:30:00Z": "Was due 1 hour 30 minutes ago · 15:00 (late)",
	} {
		if got := dueMessage(due, instant(sentAt), true, berlin); got.Body != body || got.OccurredAt != due.At {
			t.Errorf("late at %s: %q", sentAt, got.Body)
		}
	}
	// A due time on another day than the reminder names its day.
	night := task("n", "Take out bins", "2026-10-10T22:10:00Z") // 00:10 on Sun 11 Oct
	if got := dueMessage(Due{Kind: KindDue, At: night.Due.Add(-30 * time.Minute), Task: night, Key: "k"}, time.Time{}, false, berlin); got.Body != "Due in 30 minutes · Sun 11 Oct 00:10" {
		t.Errorf("next day: %q", got.Body)
	}
	// A longer lead.
	if got := dueMessage(Due{Kind: KindDue, At: call.Due.Add(-90 * time.Minute), Task: call, Key: "k"}, time.Time{}, false, berlin); got.Body != "Due in 1 hour 30 minutes · 15:00" {
		t.Errorf("lead: %q", got.Body)
	}
	blank := task("b", " ", "2026-10-10T13:00:00Z")
	if got := dueMessage(Due{Kind: KindDue, At: blank.Due, Task: blank, Key: "k"}, time.Time{}, false, berlin); got.Title != "Task due" || got.Body != "Due now · 15:00" {
		t.Errorf("blank title: %+v", got)
	}
	long := task("l", strings.Repeat("x", 300), "2026-10-10T13:00:00Z")
	if got := dueMessage(Due{Kind: KindDue, At: long.Due, Task: long, Key: "k"}, time.Time{}, false, berlin); len([]rune(got.Title)) != maxTitle || !strings.HasSuffix(got.Title, "…") {
		t.Errorf("long title: %d runes", len([]rune(got.Title)))
	}
}

// morningDue is the 09:00 morning list on Sat 10 Oct.
var morningDue = Due{Kind: KindMorning, At: instant("2026-10-10T07:00:00Z"), Key: "tasks:morning:2026-10-10", Day: date("2026-10-10")}

func TestMorningListNamesTodaysDateOnlyAndOverdueTasks(t *testing.T) {
	lists := Lists{
		DateOnlyToday: []Task{task("p", "renew passport", "2026-10-09T22:00:00Z")},
		Overdue: []Task{
			task("r", "pay rent", "2026-10-05T22:00:00Z"),
			task("b", "Call bank", "2026-10-09T15:00:00Z"),
			task("e", "Take pills", "2026-10-10T06:30:00Z"),
		},
		TimedToday: []Task{task("m", "Call mom", "2026-10-10T13:00:00Z")},
		Tomorrow:   []Task{task("g", "Buy gift", "2026-10-10T22:00:00Z")},
	}
	got, ok := morningMessage(morningDue, lists, morningDue.At, berlin)
	want := notify.Notification{
		Title: "Tasks for today",
		Body: "Today: renew passport\n" +
			"Overdue since Tue 6 Oct: pay rent\n" +
			"Overdue since Fri 9 Oct: Call bank\n" +
			"Overdue since 08:30: Take pills",
		Importance: notify.Medium, Category: "tasks.morning", Link: "/tasks", DedupeKey: "tasks:morning:2026-10-10", OccurredAt: morningDue.At,
	}
	if !ok || got != want {
		t.Fatalf("message = %+v\nwant %+v", got, want)
	}
	// Only overdue Tasks.
	got, ok = morningMessage(morningDue, Lists{Overdue: lists.Overdue[:1]}, morningDue.At, berlin)
	if !ok || got.Title != "Overdue tasks" || got.Body != "Overdue since Tue 6 Oct: pay rent" {
		t.Errorf("overdue only = %+v", got)
	}
	// Nothing to list (timed and tomorrow's Tasks are not in it): no message.
	if _, ok := morningMessage(morningDue, Lists{TimedToday: lists.TimedToday, Tomorrow: lists.Tomorrow}, morningDue.At, berlin); ok {
		t.Error("an empty morning list was sent")
	}
	// Sent late on another day, it names its day.
	got, _ = morningMessage(morningDue, lists, instant("2026-10-10T22:30:00Z"), berlin)
	if got.Title != "Tasks for Sat 10 Oct" || !strings.HasPrefix(got.Body, "Sat 10 Oct: renew passport\n") {
		t.Errorf("late on another day = %+v", got)
	}
	// An overdue Task from last year shows the year.
	old := task("o", "Old", "2025-12-30T23:00:00Z")
	got, _ = morningMessage(morningDue, Lists{Overdue: []Task{old}}, morningDue.At, berlin)
	if got.Body != "Overdue since Wed 31 Dec 2025: Old" {
		t.Errorf("last year = %q", got.Body)
	}
}

func TestListsAreCapped(t *testing.T) {
	var overdue []Task
	for i := range 14 {
		overdue = append(overdue, task(fmt.Sprint(i), fmt.Sprintf("Task %d", i), "2026-10-05T22:00:00Z"))
	}
	got, _ := morningMessage(morningDue, Lists{Overdue: overdue}, morningDue.At, berlin)
	lines := strings.Split(got.Body, "\n")
	if len(lines) != 11 || lines[9] != "Overdue since Tue 6 Oct: Task 9" || lines[10] != "and 4 more" {
		t.Errorf("capped = %q", lines)
	}
	evening := Due{Kind: KindEvening, At: instant("2026-10-10T17:00:00Z"), Key: "tasks:evening:2026-10-10", Day: date("2026-10-10")}
	got, _ = eveningMessage(evening, Lists{Tomorrow: overdue[:11]}, evening.At, berlin)
	if lines := strings.Split(got.Body, "\n"); len(lines) != 11 || lines[10] != "and 1 more" {
		t.Errorf("evening capped = %q", lines)
	}
	summary := Due{Kind: KindSummary, At: instant("2026-10-10T06:00:00Z"), Key: "summary:morning:2026-10-10", Day: date("2026-10-10")}
	got, _ = summaryMessage(summary, Lists{Overdue: overdue[:12]}, nil, EventsListed, false, summary.At, berlin)
	if !strings.HasSuffix(got.Body, "Task 9 (since Tue 6 Oct) · and 2 more") {
		t.Errorf("summary capped = %q", got.Body)
	}
	// Long titles are shortened in lists.
	got, _ = eveningMessage(evening, Lists{Tomorrow: []Task{task("l", strings.Repeat("y", 200), "2026-10-10T22:00:00Z")}}, evening.At, berlin)
	if len([]rune(got.Body)) != maxItemTitle {
		t.Errorf("long title = %d runes", len([]rune(got.Body)))
	}
}

func TestEveningListNamesTomorrowsDateOnlyTasks(t *testing.T) {
	evening := Due{Kind: KindEvening, At: instant("2026-10-10T17:00:00Z"), Key: "tasks:evening:2026-10-10", Day: date("2026-10-10")}
	lists := Lists{Tomorrow: []Task{task("g", "Buy gift", "2026-10-10T22:00:00Z"), task("w", "Water plants", "2026-10-11T00:00:00Z")}}
	got, ok := eveningMessage(evening, lists, evening.At, berlin)
	want := notify.Notification{
		Title: "Tasks for tomorrow", Body: "Buy gift\nWater plants", Importance: notify.Medium,
		Category: "tasks.evening", Link: "/tasks", DedupeKey: "tasks:evening:2026-10-10", OccurredAt: evening.At,
	}
	if !ok || got != want {
		t.Fatalf("message = %+v\nwant %+v", got, want)
	}
	if _, ok := eveningMessage(evening, Lists{Overdue: lists.Tomorrow}, evening.At, berlin); ok {
		t.Error("an empty evening list was sent")
	}
	// Sent late after midnight, tomorrow is today.
	if got, _ := eveningMessage(evening, lists, instant("2026-10-10T22:30:00Z"), berlin); got.Title != "Tasks for today" {
		t.Errorf("after midnight = %q", got.Title)
	}
}

func TestMorningSummary(t *testing.T) {
	summary := Due{Kind: KindSummary, At: instant("2026-10-10T06:00:00Z"), Key: "summary:morning:2026-10-10", Day: date("2026-10-10")}
	lists := Lists{
		DateOnlyToday: []Task{task("p", "renew passport", "2026-10-09T22:00:00Z")},
		TimedToday:    []Task{task("m", "Call mom", "2026-10-10T13:00:00Z")},
		Overdue:       []Task{task("r", "pay rent", "2026-10-05T22:00:00Z")},
	}
	events := []Event{
		{Title: "Mom's birthday", AllDay: true},
		{Title: "Dentist", Start: instant("2026-10-10T07:00:00Z"), End: instant("2026-10-10T08:00:00Z")},
		{Title: "Свидание с Ирой", Start: instant("2026-10-10T18:00:00Z"), End: instant("2026-10-10T21:00:00Z")},
	}
	got, ok := summaryMessage(summary, lists, events, EventsListed, false, summary.At, berlin)
	want := notify.Notification{
		Title: "Your day",
		Body: "Events: Mom's birthday (all day) · 09:00 Dentist · 20:00 Свидание с Ирой\n" +
			"Tasks: renew passport · 15:00 Call mom\n" +
			"Overdue: pay rent (since Tue 6 Oct)",
		Importance: notify.Low, Category: "summary.morning", Link: "/", DedupeKey: "summary:morning:2026-10-10", OccurredAt: summary.At,
	}
	if !ok || got != want {
		t.Fatalf("message = %+v\nwant %+v", got, want)
	}
	// Calendar did not answer: said so, Tasks still listed.
	got, ok = summaryMessage(summary, lists, nil, EventsUnavailable, false, summary.At, berlin)
	if !ok || !strings.HasPrefix(got.Body, "Events: Calendar unavailable\nTasks: ") {
		t.Errorf("calendar down = %q", got.Body)
	}
	// No Calendar configured: no events line.
	got, _ = summaryMessage(summary, lists, nil, EventsOff, false, summary.At, berlin)
	if strings.Contains(got.Body, "Events") {
		t.Errorf("calendar off = %q", got.Body)
	}
	// Only events.
	got, ok = summaryMessage(summary, Lists{}, events[1:2], EventsListed, false, summary.At, berlin)
	if !ok || got.Body != "Events: 09:00 Dentist" {
		t.Errorf("events only = %q", got.Body)
	}
	// Nothing at all, or nothing but an unanswered Calendar: no message.
	if _, ok := summaryMessage(summary, Lists{}, nil, EventsListed, false, summary.At, berlin); ok {
		t.Error("an empty summary was sent")
	}
	if _, ok := summaryMessage(summary, Lists{Tomorrow: lists.DateOnlyToday}, nil, EventsUnavailable, false, summary.At, berlin); ok {
		t.Error("a summary with nothing but Calendar unavailable was sent")
	}
}

func TestMergedSummaryIsMediumWhenItCarriesTheMorningList(t *testing.T) {
	summary := Due{Kind: KindSummary, At: instant("2026-10-10T07:00:00Z"), Key: "summary:morning:2026-10-10", Day: date("2026-10-10")}
	timed := Lists{TimedToday: []Task{task("m", "Call mom", "2026-10-10T13:00:00Z")}}
	withList := Lists{DateOnlyToday: []Task{task("p", "renew passport", "2026-10-09T22:00:00Z")}}
	overdue := Lists{Overdue: []Task{task("r", "pay rent", "2026-10-05T22:00:00Z")}}
	for name, c := range map[string]struct {
		lists  Lists
		merged bool
		want   notify.Importance
	}{
		"merged with a date-only Task":  {withList, true, notify.Medium},
		"merged with an overdue Task":   {overdue, true, notify.Medium},
		"merged with timed Tasks only":  {timed, true, notify.Low},
		"separate with a date-only one": {withList, false, notify.Low},
	} {
		got, ok := summaryMessage(summary, c.lists, nil, EventsListed, c.merged, summary.At, berlin)
		if !ok || got.Importance != c.want || got.Category != "summary.morning" || got.Title != "Your day" {
			t.Errorf("%s: %+v", name, got)
		}
	}
}

func TestEventLabels(t *testing.T) {
	day := date("2026-10-10")
	cases := map[string]Event{
		"09:00 Dentist":                 {Title: "Dentist", Start: instant("2026-10-10T07:00:00Z"), End: instant("2026-10-10T08:00:00Z")},
		"Birthday (all day)":            {Title: "Birthday", AllDay: true},
		"Party (until 01:00)":           {Title: "Party", Start: instant("2026-10-09T20:00:00Z"), End: instant("2026-10-09T23:00:00Z")},
		"Conference (all day)":          {Title: "Conference", Start: instant("2026-10-09T07:00:00Z"), End: instant("2026-10-11T15:00:00Z")},
		"00:00 Midnight start":          {Title: "Midnight start", Start: instant("2026-10-09T22:00:00Z")},
		"23:30 Late show (other title)": {Title: "Late show (other title)", Start: instant("2026-10-10T21:30:00Z")},
	}
	for want, event := range cases {
		if got := event.label(day, berlin); got != want {
			t.Errorf("label = %q, want %q", got, want)
		}
	}
}
