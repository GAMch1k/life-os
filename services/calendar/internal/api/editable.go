package api

import (
	"context"
	"strings"

	"github.com/GAMch1k/life-os/services/calendar/internal/api/generated"
	"github.com/GAMch1k/life-os/services/calendar/internal/sources"
)

// Which events may be changed here beyond their reminders and priority,
// and why not: Life OS events may; an integration's event may when its
// source accepts changes and it is not read-only on its own (its
// readOnlyReason, given by the integration); the events of sources that do
// not accept changes (such as feeds) may not.

// readOnlyFeed is why an imported event is read-only.
func readOnlyFeed(name *string) string {
	from := "another calendar"
	if name != nil && strings.TrimSpace(*name) != "" {
		from = "“" + strings.TrimSpace(*name) + "”"
	}
	return "Imported from " + from + ", so it can only be changed there. Its reminders and priority can still be adjusted here."
}

// resolveEditable completes each item (with its source resolved) with what
// it allows: editable, and readOnlyReason when it is not.
func (h *Handler) resolveEditable(_ context.Context, items []generated.CalendarEvent) error {
	for index := range items {
		item := &items[index]
		switch item.Source.Kind {
		case generated.EventSourceKindManual:
			item.Editable = true
		case generated.EventSourceKindExternal:
			// Editable when its source accepts changes and it is not
			// read-only on its own (readOnlyReason, set by event).
			writable := item.Source.ReadOnly != nil && !*item.Source.ReadOnly
			item.Editable = writable && item.ReadOnlyReason == nil
			if !item.Editable && item.ReadOnlyReason == nil {
				reason := readOnlyFeed(item.Source.Name)
				item.ReadOnlyReason = &reason
			}
		default:
			reason := readOnlyFeed(item.Source.Name)
			item.Editable, item.ReadOnlyReason = false, &reason
		}
	}
	return nil
}

func sourceReminders(list []generated.Reminder) []sources.Reminder {
	out := make([]sources.Reminder, 0, len(list))
	for _, reminder := range list {
		out = append(out, sources.Reminder{Kind: string(reminder.Kind), MinutesBefore: reminder.MinutesBefore, DaysBefore: reminder.DaysBefore, LocalTime: reminder.LocalTime})
	}
	return out
}
