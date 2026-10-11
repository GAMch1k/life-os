package notifications

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/GAMch1k/life-os/pkg/notify"
)

func item(name, expires string) Item {
	return Item{Name: name, Normalized: strings.ToLower(name), ExpiresOn: date(expires)}
}

// due10 is the reminder for 10 October 2026 at 08:00 in Berlin.
var due10 = Due{At: instant("2026-10-10T06:00:00Z"), Key: "food:expiry:2026-10-10", Day: date("2026-10-10")}

func TestTheReminderNamesTodayFirstThenTomorrow(t *testing.T) {
	message, ok := expiryMessage(due10, []Item{
		item("Milch", "2026-10-10"),
		item("Joghurt", "2026-10-11"),
		item("joghurt", "2026-10-11"),
		item("Brot", "2026-10-10"),
	}, instant("2026-10-10T06:00:10Z"), berlin)
	if !ok {
		t.Fatal("nothing to send")
	}
	want := notify.Notification{
		Title: "Food expiring today", Body: "Today: Milch\nToday: Brot\nTomorrow: Joghurt (2)", Importance: notify.Medium,
		Category: "food.expiry", Link: "/food?tab=pantry", DedupeKey: "food:expiry:2026-10-10", OccurredAt: due10.At,
	}
	if message != want {
		t.Fatalf("message = %+v\nwant %+v", message, want)
	}
}

func TestOnlyTomorrowIsLowImportance(t *testing.T) {
	message, ok := expiryMessage(due10, []Item{item("Joghurt", "2026-10-11")}, instant("2026-10-10T06:00:10Z"), berlin)
	if !ok || message.Title != "Food expiring tomorrow" || message.Body != "Tomorrow: Joghurt" || message.Importance != notify.Low {
		t.Fatalf("message = %+v, %v", message, ok)
	}
}

func TestNothingExpiringSendsNothing(t *testing.T) {
	if _, ok := expiryMessage(due10, nil, instant("2026-10-10T06:00:10Z"), berlin); ok {
		t.Fatal("an empty reminder was built")
	}
	// Food that expired before the day, or expires later, is not named.
	if _, ok := expiryMessage(due10, []Item{item("Old", "2026-10-09"), item("Later", "2026-10-12")}, instant("2026-10-10T06:00:10Z"), berlin); ok {
		t.Fatal("expired or later food was named")
	}
}

func TestALateReminderOnAnotherDayNamesTheDates(t *testing.T) {
	// Sent at 01:00 on 11 October, after Food was down overnight.
	message, ok := expiryMessage(due10, []Item{item("Milch", "2026-10-10"), item("Joghurt", "2026-10-11")}, instant("2026-10-10T23:00:00Z"), berlin)
	if !ok || message.Title != "Food expiring Sat 10 Oct" || message.Body != "Sat 10 Oct: Milch\nToday: Joghurt" || message.Importance != notify.Medium {
		t.Fatalf("message = %+v", message)
	}
}

func TestTheReminderIsCapped(t *testing.T) {
	var items []Item
	for index := range 13 {
		items = append(items, item(fmt.Sprintf("Item %02d", index), "2026-10-10"))
	}
	long := strings.Repeat("Сыр ", 60)
	items[0] = Item{Name: long + "\n\tend", Normalized: "long", ExpiresOn: date("2026-10-10")}
	message, _ := expiryMessage(due10, items, instant("2026-10-10T06:00:10Z"), berlin)
	lines := strings.Split(message.Body, "\n")
	if len(lines) != 11 || lines[10] != "and 3 more" {
		t.Fatalf("lines = %q", lines)
	}
	// A name is one line, at most 120 characters.
	if name := strings.TrimPrefix(lines[0], "Today: "); len([]rune(name)) != 120 || !strings.HasSuffix(name, "…") || strings.ContainsAny(name, "\n\t") {
		t.Fatalf("long name = %q (%d)", name, len([]rune(name)))
	}
	if len([]rune(message.Body)) > maxBody {
		t.Fatalf("body is %d characters", len([]rune(message.Body)))
	}
}

func TestDayNames(t *testing.T) {
	sentOn := date("2026-10-10")
	for day, want := range map[string]string{
		"2026-10-10": "today", "2026-10-11": "tomorrow", "2026-10-09": "Fri 9 Oct", "2027-01-02": "Sat 2 Jan 2027",
	} {
		if got := relativeDay(date(day), sentOn); got != want {
			t.Errorf("relativeDay(%s) = %q, want %q", day, got, want)
		}
	}
	_ = time.UTC
}
