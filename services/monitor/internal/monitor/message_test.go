package monitor

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/GAMch1k/life-os/pkg/notify"
)

var errTest = errors.New("dial tcp: connection refused")

func TestAProblemIsHighAndSaysWhatToDo(t *testing.T) {
	n := notification(Transition{
		Check: Observation{Key: "processing", Title: "Automatic processing", State: Problem, Summary: "Codex is not signed in.", Action: "Sign in with `codex login`."},
		From:  OK, Since: minute(0),
	})
	want := notify.Notification{
		Title: "Automatic processing has a problem", Body: "Codex is not signed in.\n\nSign in with `codex login`.",
		Importance: notify.High, Category: "status.processing", Link: "/status",
		DedupeKey: "status:processing:problem:2026-10-10T12:00:00Z", OccurredAt: minute(0),
	}
	if n != want {
		t.Fatalf("notification = %+v\nwant %+v", n, want)
	}
}

func TestAWarningIsMedium(t *testing.T) {
	n := notification(Transition{Check: Observation{Key: "inbox", Title: "Inbox backlog", State: Warning, Summary: "2 captures have failed and are being retried."}, From: OK, Since: minute(0)})
	if n.Importance != notify.Medium || n.Title != "Inbox backlog needs attention" || n.Body != "2 captures have failed and are being retried." || n.Category != "status.inbox" {
		t.Fatalf("notification = %+v", n)
	}
}

func TestARecoveryIsLowAndSaysHowLongItLasted(t *testing.T) {
	n := notification(Transition{
		Check: Observation{Key: "processing", Title: "Automatic processing", State: OK, Summary: "Processing normally."},
		From:  Problem, Since: minute(72), EpisodeSince: minute(0),
	})
	if n.Importance != notify.Low || n.Title != "Automatic processing recovered" ||
		n.Body != "Processing normally.\n\nBack to normal after about 1 h 12 min." || n.DedupeKey != "status:processing:ok:2026-10-10T13:12:00Z" {
		t.Fatalf("notification = %+v", n)
	}
	unknown := notification(Transition{Check: Observation{Key: "processing", Title: "Automatic processing", State: OK, Summary: "Processing normally."}, From: Problem, Since: minute(5)})
	if unknown.Body != "Processing normally." {
		t.Fatalf("without a known start the duration is left out: %q", unknown.Body)
	}
}

func TestTheUnreachableStatusHasItsOwnTitles(t *testing.T) {
	down := notification(Transition{Check: unreachable(errTest), From: OK, Since: minute(0)})
	if down.Importance != notify.High || down.Title != "Life OS status is unreachable" || down.Category != "status.dashboard" ||
		!strings.Contains(down.Body, "(dial tcp: connection refused)") || !strings.Contains(down.Body, "dashboard service") {
		t.Fatalf("notification = %+v", down)
	}
	up := notification(Transition{Check: reachable(), From: Problem, Since: minute(3), EpisodeSince: minute(0)})
	if up.Importance != notify.Low || up.Title != "Life OS status is reachable again" || !strings.HasSuffix(up.Body, "Back to normal after about 3 min.") {
		t.Fatalf("notification = %+v", up)
	}
}

func TestNotificationsStayWithinTheNotificationsLimits(t *testing.T) {
	n := notification(Transition{
		Check: Observation{Key: "processing", Title: "Automatic\nprocessing " + strings.Repeat("x", 300), State: Problem, Summary: strings.Repeat("s", 1500) + "\x00", Action: strings.Repeat("a", 1500)},
		From:  OK, Since: minute(0),
	})
	if utf8.RuneCountInString(n.Title) > maxTitle || strings.ContainsAny(n.Title, "\n\x00") || !strings.HasPrefix(n.Title, "Automatic processing x") {
		t.Fatalf("title = %q", n.Title)
	}
	if utf8.RuneCountInString(n.Body) > maxBody || strings.Contains(n.Body, "\x00") || !strings.HasSuffix(n.Body, "…") {
		t.Fatalf("body has %d characters", utf8.RuneCountInString(n.Body))
	}
}

func TestCheckKeysAreCategoryParts(t *testing.T) {
	for id, want := range map[string]string{
		"processing":            "processing",
		"Voice-Worker":          "voice_worker",
		" calendar ":            "calendar",
		"2fa":                   "check_2fa",
		"dashboard":             "check_dashboard",
		"":                      "check_",
		strings.Repeat("a", 80): strings.Repeat("a", maxKey),
	} {
		if got := checkKey(id); got != want {
			t.Errorf("checkKey(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		30 * time.Second:             "less than a minute",
		12 * time.Minute:             "12 min",
		2 * time.Hour:                "2 h",
		26*time.Hour + 5*time.Minute: "26 h 5 min",
		72 * time.Hour:               "3 days",
	} {
		if got := formatDuration(d); got != want {
			t.Errorf("formatDuration(%s) = %q, want %q", d, got, want)
		}
	}
}

func TestParseStateTreatsUnknownStatesAsOK(t *testing.T) {
	for value, want := range map[string]State{"ok": OK, "warning": Warning, "problem": Problem, "neutral": OK, "": OK} {
		if got := parseState(value); got != want {
			t.Errorf("parseState(%q) = %s, want %s", value, got, want)
		}
	}
}
