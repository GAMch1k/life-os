package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/GAMch1k/life-os/services/calendar/internal/api/generated"
)

func (c apiClient) settings(method string, body any, wantStatus int) generated.CalendarSettings {
	c.t.Helper()
	status, raw := c.do(method, "/v1/settings", body)
	if status != wantStatus {
		c.t.Fatalf("%s /v1/settings: %d %s, want %d", method, status, raw, wantStatus)
	}
	var out generated.CalendarSettings
	if err := json.Unmarshal(raw, &out); err != nil {
		c.t.Fatalf("%s /v1/settings: %v in %s", method, err, raw)
	}
	return out
}

// minutes lists the minutesBefore of relative reminders (localTime ones as
// -1), for compact comparisons.
func minutes(reminders []generated.Reminder) []int {
	out := make([]int, 0, len(reminders))
	for _, reminder := range reminders {
		if reminder.MinutesBefore == nil {
			out = append(out, -1)
			continue
		}
		out = append(out, *reminder.MinutesBefore)
	}
	return out
}

func sameMinutes(got []generated.Reminder, want ...int) bool {
	values := minutes(got)
	if len(values) != len(want) {
		return false
	}
	for index := range want {
		if values[index] != want[index] {
			return false
		}
	}
	return true
}

// The owner's defaults apply to the events they create, timed and all-day
// apart, together with each event's own reminders, like Google Calendar's
// notifications.
func TestOwnerDefaultRemindersAddToEventReminders(t *testing.T) {
	c := newAPIClient(t)
	initial := c.settings(http.MethodGet, nil, http.StatusOK)
	if len(initial.DefaultReminders.Timed) != 0 || len(initial.DefaultReminders.AllDay) != 0 || initial.UpdatedAt != nil {
		t.Fatalf("initial settings = %+v, want no defaults", initial)
	}

	// Before any default, an event with its own reminders has exactly those.
	dentist := c.event(http.MethodPost, "/v1/events", `{"title":"Dentist","allDay":false,"startAt":"2026-11-17T15:00:00+01:00","endAt":"2026-11-17T16:00:00+01:00",
		"reminders":[{"kind":"relative","minutesBefore":1440},{"kind":"relative","minutesBefore":180}]}`, http.StatusCreated)
	if !dentist.UseDefaultReminders || len(dentist.DefaultReminders) != 0 || !sameMinutes(dentist.EffectiveReminders, 1440, 180) {
		t.Fatalf("dentist = %+v", dentist)
	}

	updated := c.settings(http.MethodPatch, `{"defaultReminders":{"timed":[{"kind":"relative","minutesBefore":30}]}}`, http.StatusOK)
	if !sameMinutes(updated.DefaultReminders.Timed, 30) || len(updated.DefaultReminders.AllDay) != 0 || updated.UpdatedAt == nil {
		t.Fatalf("updated settings = %+v", updated)
	}
	// 1 day, 3 hours, and 30 minutes before: the default plus its own.
	dentist = c.event(http.MethodGet, "/v1/events/"+dentist.Id.String(), nil, http.StatusOK)
	if !sameMinutes(dentist.DefaultReminders, 30) || !sameMinutes(dentist.EffectiveReminders, 30, 1440, 180) {
		t.Fatalf("dentist with the default = %+v", dentist.EffectiveReminders)
	}
	// An own reminder equal to a default counts once.
	dentist = c.event(http.MethodPatch, "/v1/events/"+dentist.Id.String(), `{"reminders":[{"kind":"relative","minutesBefore":30},{"kind":"relative","minutesBefore":180}]}`, http.StatusOK)
	if !sameMinutes(dentist.EffectiveReminders, 30, 180) {
		t.Fatalf("repeated default = %+v", dentist.EffectiveReminders)
	}
	// Turning the defaults off keeps the event's own.
	dentist = c.event(http.MethodPatch, "/v1/events/"+dentist.Id.String(), `{"useDefaultReminders":false}`, http.StatusOK)
	if dentist.UseDefaultReminders || !sameMinutes(dentist.EffectiveReminders, 30, 180) || !sameMinutes(dentist.Reminders, 30, 180) {
		t.Fatalf("defaults off = %+v", dentist)
	}
	dentist = c.event(http.MethodPatch, "/v1/events/"+dentist.Id.String(), `{"reminders":[{"kind":"relative","minutesBefore":180}]}`, http.StatusOK)
	if !sameMinutes(dentist.EffectiveReminders, 180) {
		t.Fatalf("own only = %+v", dentist.EffectiveReminders)
	}
	// null goes back to the defaults alone.
	dentist = c.event(http.MethodPatch, "/v1/events/"+dentist.Id.String(), `{"reminders":null}`, http.StatusOK)
	if !dentist.UseDefaultReminders || len(dentist.Reminders) != 0 || !sameMinutes(dentist.EffectiveReminders, 30) {
		t.Fatalf("back to the defaults = %+v", dentist)
	}

	// All-day events have their own defaults; the timed list leaves them alone.
	c.settings(http.MethodPatch, `{"defaultReminders":{"allDay":[{"kind":"localTime","daysBefore":1,"localTime":"19:00"}]}}`, http.StatusOK)
	birthday := c.event(http.MethodPost, "/v1/events", `{"title":"Birthday","allDay":true,"startDate":"2026-11-20","endDate":"2026-11-20"}`, http.StatusCreated)
	if !sameMinutes(birthday.EffectiveReminders, -1) || *birthday.EffectiveReminders[0].LocalTime != "19:00" {
		t.Fatalf("all-day defaults = %+v", birthday.EffectiveReminders)
	}
	if got := c.settings(http.MethodGet, nil, http.StatusOK); !sameMinutes(got.DefaultReminders.Timed, 30) || len(got.DefaultReminders.AllDay) != 1 {
		t.Fatalf("settings after both = %+v", got)
	}
	// An event created without the defaults has only its own reminders, or none.
	quiet := c.event(http.MethodPost, "/v1/events", `{"title":"Quiet","allDay":false,"startAt":"2026-11-18T09:00:00Z","endAt":"2026-11-18T10:00:00Z","useDefaultReminders":false}`, http.StatusCreated)
	if quiet.UseDefaultReminders || len(quiet.EffectiveReminders) != 0 || !sameMinutes(quiet.DefaultReminders, 30) {
		t.Fatalf("quiet = %+v", quiet)
	}
	// Listing resolves the same way.
	for _, item := range c.list("/v1/events?" + q("from", "2026-11-01T00:00:00Z", "to", "2026-12-01T00:00:00Z")).Items {
		want := map[string]int{"Dentist": 1, "Birthday": 1, "Quiet": 0}[item.Title]
		if len(item.EffectiveReminders) != want {
			t.Errorf("listed %s: %+v, want %d reminders", item.Title, item.EffectiveReminders, want)
		}
	}
}

func TestSettingsValidation(t *testing.T) {
	c := newAPIClient(t)
	for body, want := range map[string]string{
		`{}`:                                  "defaultReminders.timed or defaultReminders.allDay",
		`{"defaultReminders":{}}`:             "defaultReminders.timed or defaultReminders.allDay",
		`{"defaultReminders":{"timed":null}}`: "must be a list",
		`{"defaultReminders":{"weekly":[]}}`:  "only timed and allDay",
		`{"defaultReminders":{"timed":[{"kind":"relative"}]}}`:                                      "relative reminder",
		`{"defaultReminders":{"allDay":[{"kind":"localTime","daysBefore":1,"localTime":"25:00"}]}}`: "localTime",
	} {
		if status, raw := c.do(http.MethodPatch, "/v1/settings", body); status != http.StatusBadRequest || !strings.Contains(string(raw), want) {
			t.Errorf("PATCH %s: %d %s, want 400 with %q", body, status, raw, want)
		}
	}
	tooMany := `{"defaultReminders":{"timed":[` + strings.Repeat(`{"kind":"relative","minutesBefore":5},`, maxReminders) + `{"kind":"relative","minutesBefore":5}]}}`
	if status, raw := c.do(http.MethodPatch, "/v1/settings", tooMany); status != http.StatusBadRequest || !strings.Contains(string(raw), "at most 10") {
		t.Errorf("too many: %d %s", status, raw)
	}
	// A change says who makes it.
	if status, raw := c.do(http.MethodPatch, "/v1/settings", `{"defaultReminders":{"timed":[]}}`, "X-Life-OS-Actor", ""); status != http.StatusBadRequest || !strings.Contains(string(raw), "actor_required") {
		t.Errorf("without an actor: %d %s", status, raw)
	}
	if got := c.settings(http.MethodGet, nil, http.StatusOK); got.UpdatedAt != nil {
		t.Errorf("refused updates changed the settings: %+v", got)
	}
}
