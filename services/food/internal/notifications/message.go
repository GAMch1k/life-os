package notifications

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/GAMch1k/life-os/pkg/notify"
)

// CategoryExpiry is the expiry reminder's Notifications category.
const CategoryExpiry = "food.expiry"

// Message limits: Notifications' own (docs/services/notifications.md), and
// how many lines the reminder names before "and N more".
const (
	maxTitle    = 200
	maxBody     = 2000
	maxItems    = 10
	maxItemName = 120
)

// pantryLink is the page the reminder opens.
const pantryLink = "/food?tab=pantry"

// Item is an in-stock pantry item as the reminder sees it.
type Item struct {
	Name string
	// Normalized is the name as Food compares it: items with one name
	// expiring on one day share a line.
	Normalized string
	// ExpiresOn is the date it expires (at midnight UTC).
	ExpiresOn time.Time
}

// expiryMessage is the day's expiry reminder: one line per item that
// expires on the reminder's day or the next, that day's first, "Today: Milch"
// and "Tomorrow: Joghurt (2)" (two items of one name expiring that day), at
// most maxItems lines and "and N more". Titled "Food expiring today", or
// "Food expiring tomorrow" when nothing expires on the day itself. Medium
// importance when something expires on the day, else low. A reminder sent
// late on another day names the days from then ("Sat 10 Oct"). false when
// nothing expires: no reminder is sent.
func expiryMessage(due Due, items []Item, sentAt time.Time, owner *time.Location) (notify.Notification, bool) {
	type line struct {
		day        time.Time
		normalized string
		name       string
		count      int
	}
	var lines []*line
	index := map[string]*line{}
	today := false
	for _, item := range items {
		if item.ExpiresOn.Before(due.Day) || item.ExpiresOn.After(due.Day.AddDate(0, 0, 1)) {
			continue
		}
		today = today || item.ExpiresOn.Equal(due.Day)
		key := item.ExpiresOn.Format(time.DateOnly) + "\x00" + item.Normalized
		if existing, ok := index[key]; ok {
			existing.count++
			continue
		}
		entry := &line{day: item.ExpiresOn, normalized: item.Normalized, name: item.Name, count: 1}
		index[key] = entry
		lines = append(lines, entry)
	}
	if len(lines) == 0 {
		return notify.Notification{}, false
	}
	// Today first; within a day, as listed.
	sort.SliceStable(lines, func(a, b int) bool { return lines[a].day.Before(lines[b].day) })
	sentOn := dateOf(sentAt, owner)
	texts := make([]string, 0, len(lines))
	for _, entry := range lines {
		text := capitalize(relativeDay(entry.day, sentOn)) + ": " + itemName(entry.name)
		if entry.count > 1 {
			text += fmt.Sprintf(" (%d)", entry.count)
		}
		texts = append(texts, text)
	}
	importance := notify.Low
	if today {
		importance = notify.Medium
	}
	return notify.Notification{
		Title:      clip("Food expiring "+relativeDay(lines[0].day, sentOn), maxTitle),
		Body:       clip(strings.Join(capped(texts), "\n"), maxBody),
		Importance: importance, Category: CategoryExpiry, Link: pantryLink, DedupeKey: due.Key, OccurredAt: due.At,
	}, true
}

// relativeDay names day as seen on the owner-local date sentOn: "today",
// "tomorrow", or "Sat 10 Oct" (a reminder sent late, on another day).
func relativeDay(day, sentOn time.Time) string {
	switch {
	case day.Equal(sentOn):
		return "today"
	case day.Equal(sentOn.AddDate(0, 0, 1)):
		return "tomorrow"
	}
	if day.Year() != sentOn.Year() {
		return day.Format("Mon 2 Jan 2006")
	}
	return day.Format("Mon 2 Jan")
}

// capped keeps at most maxItems lines, adding "and N more" for the rest.
func capped(lines []string) []string {
	if len(lines) <= maxItems {
		return lines
	}
	return append(lines[:maxItems:maxItems], fmt.Sprintf("and %d more", len(lines)-maxItems))
}

func itemName(name string) string {
	text := oneLine(name)
	if text == "" {
		text = "(unnamed)"
	}
	return clip(text, maxItemName)
}

func capitalize(text string) string {
	if text == "" {
		return text
	}
	runes := []rune(text)
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}

// oneLine collapses whitespace and control characters into single spaces.
func oneLine(text string) string {
	return strings.Join(strings.FieldsFunc(text, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
}

// clip shortens text to limit runes, ending with an ellipsis.
func clip(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit-1]) + "…"
}
