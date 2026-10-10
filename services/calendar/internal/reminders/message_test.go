package reminders

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/GAMch1k/life-os/pkg/notify"
	"github.com/GAMch1k/life-os/services/calendar/internal/api/generated"
)

// dueOf is item's due reminder (its first effective one).
func dueOf(t *testing.T, item generated.CalendarEvent) Due {
	t.Helper()
	due := dueBetween([]generated.CalendarEvent{item}, berlin, time.Time{}, instant("2100-01-01T00:00:00Z"))
	if len(due) == 0 {
		t.Fatal("nothing due")
	}
	return due[0]
}

func TestNotificationBodies(t *testing.T) {
	dentist := func(reminder generated.Reminder) generated.CalendarEvent {
		item := timed("Dentist", "2026-10-11T13:00:00Z", 60, reminder)
		item.Location = ptr("Dr. Weber")
		return item
	}
	overnight := timed("Night shift", "2026-10-11T20:00:00Z", 600, relative(30))
	nextYear := timed("Conference", "2027-01-07T08:00:00Z", 480, localTime(28, "09:00"))
	for _, tc := range []struct {
		name string
		item generated.CalendarEvent
		want string
	}{
		{"30 minutes before", dentist(relative(30)), "In 30 minutes · 15:00–16:00 · Dr. Weber"},
		{"1 minute before", dentist(relative(1)), "In 1 minute · 15:00–16:00 · Dr. Weber"},
		{"at the start", dentist(relative(0)), "Now · 15:00–16:00 · Dr. Weber"},
		{"3 hours before", dentist(relative(180)), "In 3 hours · 15:00–16:00 · Dr. Weber"},
		{"90 minutes before", dentist(relative(90)), "In 1 hour 30 minutes · 15:00–16:00 · Dr. Weber"},
		{"1 day before", dentist(relative(1440)), "Tomorrow at 15:00 · Dr. Weber"},
		{"the evening before", dentist(localTime(1, "19:00")), "Tomorrow at 15:00 · Dr. Weber"},
		{"a week before", dentist(relative(7 * 1440)), "Sun 11 Oct at 15:00 · Dr. Weber"},
		{"ending the next day", overnight, "In 30 minutes · 22:00–Mon 12 Oct 08:00"},
		{"next year", nextYear, "Thu 7 Jan 2027 at 09:00"},
		{"all-day the evening before", allDay("Birthday", "2026-10-15", "2026-10-15", localTime(1, "19:00")), "Tomorrow, all day"},
		{"all-day on the day", allDay("Birthday", "2026-10-15", "2026-10-15", localTime(0, "08:00")), "Today, all day"},
		{"all-day a week before", allDay("Birthday", "2026-10-15", "2026-10-15", localTime(7, "08:00")), "Thu 15 Oct, all day"},
		{"several days", allDay("Holiday", "2026-12-24", "2026-12-27", localTime(1, "19:00")), "Tomorrow until Sun 27 Dec"},
		{"several days, on the first", allDay("Holiday", "2026-12-24", "2026-12-27", localTime(0, "09:00")), "Today until Sun 27 Dec"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			due := dueOf(t, tc.item)
			got := notification(due, due.At, false, true, berlin)
			if got.Body != tc.want {
				t.Fatalf("body = %q, want %q", got.Body, tc.want)
			}
		})
	}
}

func TestNotificationFields(t *testing.T) {
	item := timed("Dentist", "2026-10-11T13:00:00Z", 60, relative(30))
	item.Location = ptr("Dr. Weber")
	due := dueOf(t, item)
	got := notification(due, due.At, false, true, berlin)
	want := notify.Notification{
		Title: "Dentist", Body: "In 30 minutes · 15:00–16:00 · Dr. Weber", Importance: notify.Medium, Category: "calendar.reminder",
		Link: "/calendar?date=2026-10-11", DedupeKey: due.Key, OccurredAt: instant("2026-10-11T12:30:00Z"),
	}
	if got != want {
		t.Fatalf("notification = %+v\nwant %+v", got, want)
	}
	// The link names the owner's date of the start, not UTC's.
	night := timed("Night train", "2026-10-13T22:30:00Z", 60, relative(30))
	if got := notification(dueOf(t, night), time.Time{}, false, true, berlin); got.Link != "/calendar?date=2026-10-14" {
		t.Fatalf("link = %q", got.Link)
	}
	birthday := allDay("Birthday", "2026-10-15", "2026-10-15", localTime(1, "19:00"))
	if got := notification(dueOf(t, birthday), time.Time{}, false, true, berlin); got.Link != "/calendar?date=2026-10-15" {
		t.Fatalf("all-day link = %q", got.Link)
	}
}

func TestZAWPickupReminder(t *testing.T) {
	// A ZAW pickup at 06:00, reminded the evening before by its source's
	// default; the location (the owner's street) is left out.
	pickup := timed("Papier", "2026-10-14T04:00:00Z", 15, localTime(1, "19:00"))
	pickup.Location = ptr("Roßdorf, Darmstädter Str.")
	pickup.Source = generated.EventSource{Kind: generated.EventSourceKindIcs, Name: ptr("ZAW")}
	due := dueOf(t, pickup)
	got := notification(due, due.At, false, false, berlin)
	if got.Title != "Papier" || got.Body != "Tomorrow at 06:00" || !got.OccurredAt.Equal(instant("2026-10-13T17:00:00Z")) {
		t.Fatalf("notification = %+v", got)
	}
}

func TestLateNotificationsArePhrasedFromNow(t *testing.T) {
	item := timed("Dentist", "2026-10-11T13:00:00Z", 60, relative(30))
	due := dueOf(t, item)
	for _, tc := range []struct {
		sent string
		want string
	}{
		{"2026-10-11T12:50:00Z", "In 10 minutes · 15:00–16:00 (late)"},
		{"2026-10-11T13:20:00Z", "Started 20 minutes ago · 15:00–16:00 (late)"},
		{"2026-10-11T14:30:00Z", "Started 1 hour 30 minutes ago · 15:00–16:00 (late)"},
	} {
		got := notification(due, instant(tc.sent), true, true, berlin)
		if got.Body != tc.want || !got.OccurredAt.Equal(due.At) {
			t.Errorf("sent %s: %q at %s, want %q at the due time", tc.sent, got.Body, got.OccurredAt, tc.want)
		}
	}
	birthday := allDay("Birthday", "2026-10-15", "2026-10-15", localTime(1, "23:00"))
	if got := notification(dueOf(t, birthday), instant("2026-10-14T22:30:00Z"), true, true, berlin); got.Body != "Today, all day (late)" {
		t.Fatalf("late all-day body = %q", got.Body)
	}
}

func TestNotificationTextIsCleanedAndBounded(t *testing.T) {
	item := timed("  Team\nsync\t"+strings.Repeat("x", 300), "2026-10-11T13:00:00Z", 60, relative(30))
	item.Location = ptr(strings.Repeat("Room ", 500))
	got := notification(dueOf(t, item), time.Time{}, false, true, berlin)
	if strings.ContainsAny(got.Title, "\n\t") || !strings.HasPrefix(got.Title, "Team sync x") || utf8.RuneCountInString(got.Title) != 200 || !strings.HasSuffix(got.Title, "…") {
		t.Fatalf("title = %q", got.Title)
	}
	if utf8.RuneCountInString(got.Body) > 2000 {
		t.Fatalf("body has %d characters", utf8.RuneCountInString(got.Body))
	}
}

func TestImportance(t *testing.T) {
	if got := reminderImportance(timed("Dentist", "2026-10-11T13:00:00Z", 60)); got != notify.Medium {
		t.Fatalf("reminderImportance = %s, want medium", got)
	}
	for priority, want := range map[string]notify.Importance{"high": notify.High, "HIGH": notify.High, "medium": notify.Medium, "low": notify.Low, "": notify.Medium, "urgent": notify.Medium} {
		if got := priorityImportance(priority); got != want {
			t.Errorf("priorityImportance(%q) = %s, want %s", priority, got, want)
		}
	}
}

func TestDurationWords(t *testing.T) {
	for d, want := range map[time.Duration]string{
		time.Minute: "1 minute", 45 * time.Minute: "45 minutes", time.Hour: "1 hour", 150 * time.Minute: "2 hours 30 minutes",
		61 * time.Minute: "1 hour 1 minute", 24 * time.Hour: "1 day", 51 * time.Hour: "2 days 3 hours",
	} {
		if got := duration(d); got != want {
			t.Errorf("duration(%s) = %q, want %q", d, got, want)
		}
	}
}
