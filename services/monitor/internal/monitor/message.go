package monitor

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/GAMch1k/life-os/pkg/notify"
)

const (
	statusLink = "/status"
	maxTitle   = 200
	maxBody    = 2000
	maxKey     = 50
)

// category is the notification category of a check: status.<key>.
func category(key string) string {
	return "status." + key
}

// checkKey turns a check id into its category part: lowercase letters,
// digits, and '_', starting with a letter (processing stays processing).
func checkKey(id string) string {
	key := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			return r
		case r >= 'A' && r <= 'Z':
			return unicode.ToLower(r)
		}
		return '_'
	}, strings.TrimSpace(id))
	key = strings.Trim(key, "_")
	if key == "" || key[0] < 'a' || key[0] > 'z' || key == dashboardKey {
		key = "check_" + key
	}
	if len(key) > maxKey {
		key = strings.TrimRight(key[:maxKey], "_")
	}
	return key
}

// notification is what the owner receives for transition: high for a
// problem, medium for a warning, low for a recovery, always linking to the
// Status page.
func notification(transition Transition) notify.Notification {
	check := transition.Check
	n := notify.Notification{
		Category:   category(check.Key),
		Link:       statusLink,
		DedupeKey:  transition.DedupeKey(),
		OccurredAt: transition.Since,
	}
	title := oneLine(check.Title)
	if title == "" {
		title = check.Key
	}
	var lines []string
	switch check.State {
	case Problem:
		n.Importance = notify.High
		n.Title = title + " has a problem"
		lines = []string{check.Summary, check.Action}
	case Warning:
		n.Importance = notify.Medium
		n.Title = title + " needs attention"
		lines = []string{check.Summary, check.Action}
	default:
		n.Importance = notify.Low
		n.Title = title + " recovered"
		lines = []string{check.Summary}
		if !transition.EpisodeSince.IsZero() && transition.Since.After(transition.EpisodeSince) {
			lines = append(lines, "Back to normal after about "+formatDuration(transition.Since.Sub(transition.EpisodeSince))+".")
		}
	}
	if check.Key == dashboardKey {
		n.Title = "Life OS status is unreachable"
		if check.State == OK {
			n.Title = "Life OS status is reachable again"
		}
	}
	n.Title = truncate(n.Title, maxTitle)
	n.Body = truncate(plainText(lines), maxBody)
	return n
}

// unreachable is the dashboard pseudo-check's observation when the runtime
// status cannot be read.
func unreachable(err error) Observation {
	reason := truncate(oneLine(err.Error()), 300)
	return Observation{
		Key:     dashboardKey,
		Title:   "Life OS status",
		State:   Problem,
		Summary: "The monitor cannot read the runtime status from the Dashboard (" + reason + ").",
		Action:  "The Dashboard may be down, and with it the Status page; until it is back the monitor cannot check anything else. Check the dashboard service and its logs.",
	}
}

// reachable is the dashboard pseudo-check's observation when the runtime
// status was read.
func reachable() Observation {
	return Observation{Key: dashboardKey, Title: "Life OS status", State: OK, Summary: "The monitor reads the runtime status from the Dashboard again."}
}

// observations turns a runtime status into one observation per check, plus
// the dashboard pseudo-check.
func observations(status Status) []Observation {
	result := make([]Observation, 0, len(status.Checks)+1)
	for _, check := range status.Checks {
		result = append(result, Observation{
			Key:     checkKey(check.ID),
			Title:   check.Title,
			State:   parseState(check.State),
			Summary: check.Summary,
			Action:  check.Action,
		})
	}
	return append(result, reachable())
}

// formatDuration is a rough duration for people, such as "12 min" or
// "3 h 5 min".
func formatDuration(d time.Duration) string {
	minutes := int(d / time.Minute)
	switch {
	case minutes < 1:
		return "less than a minute"
	case minutes < 60:
		return fmt.Sprintf("%d min", minutes)
	case minutes < 48*60:
		if minutes%60 == 0 {
			return fmt.Sprintf("%d h", minutes/60)
		}
		return fmt.Sprintf("%d h %d min", minutes/60, minutes%60)
	}
	return fmt.Sprintf("%d days", minutes/(24*60))
}

// oneLine replaces control characters (line breaks included) with spaces.
func oneLine(value string) string {
	return strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)), " ")
}

// plainText joins the non-empty lines as paragraphs, keeping only line
// breaks and tabs among control characters.
func plainText(lines []string) string {
	var paragraphs []string
	for _, line := range lines {
		line = strings.TrimSpace(strings.Map(func(r rune) rune {
			if unicode.IsControl(r) && r != '\n' && r != '\t' {
				return ' '
			}
			return r
		}, line))
		if line != "" {
			paragraphs = append(paragraphs, line)
		}
	}
	return strings.Join(paragraphs, "\n\n")
}

// truncate shortens value to at most limit characters, ending in "…".
func truncate(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return strings.TrimSpace(string(runes[:limit-1])) + "…"
}
