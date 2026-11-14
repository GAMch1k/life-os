package api

import (
	"context"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/GAMch1k/life-os/pkg/identity"
	"github.com/GAMch1k/life-os/services/calendar/internal/api/generated"
	store "github.com/GAMch1k/life-os/services/calendar/internal/repository/generated"
)

// ReminderStatus reports reminder delivery's health (internal/reminders).
type ReminderStatus interface {
	Status(ctx context.Context) (generated.ReminderDeliveryStatus, error)
}

// WithReminders serves GET /v1/reminders/status from status. Without it
// reminder delivery reads as not enabled.
func (h *Handler) WithReminders(status ReminderStatus) *Handler {
	h.reminders = status
	return h
}

// GetReminderStatus is Status's details, so the server's admins'
// (docs/architecture/multi-user.md#roles-and-checks): a member's identity
// token is refused; the Dashboard reads it without one.
func (h *Handler) GetReminderStatus(c fiber.Ctx) error {
	if err := identity.RequireAdmin(c, identity.AdminPolicy{}); err != nil {
		return err
	}
	if h.reminders == nil {
		return c.JSON(generated.ReminderDeliveryStatus{})
	}
	status, err := h.reminders.Status(c.Context())
	if err != nil {
		return internal(err)
	}
	return c.JSON(status)
}

// ReminderOccurrences returns every active event and occurrence that starts
// in [from, to), as listing renders it: an occurrence with its own changes
// (a skipped one is left out, a moved one is placed at its new time), and
// every item with its source name, defaults, and effective reminders.
// Reminder delivery reads it, so reminders fire for exactly what the
// Calendar shows. An all-day event starts at midnight of its first day in
// the owner's timezone. The order is unspecified.
func (h *Handler) ReminderOccurrences(ctx context.Context, from, to time.Time) ([]generated.CalendarEvent, error) {
	owner := h.ownerLocation
	rows, err := h.queries.ListReminderEvents(ctx, store.ListReminderEventsParams{
		RangeStart: timestamp(&from), RangeEnd: timestamp(&to),
		FirstDate: pgtype.Date{Time: localDay(from, owner), Valid: true}, LastDate: pgtype.Date{Time: localDay(to, owner), Valid: true},
	})
	if err != nil {
		return nil, err
	}
	items := make([]generated.CalendarEvent, 0, len(rows))
	for _, row := range rows {
		item, err := event(row)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	recurring, err := h.queries.ListRecurringEvents(ctx, store.ListRecurringEventsParams{
		TimeZone: h.ownerTimeZone, RangeStart: timestamp(&from), RangeEnd: timestamp(&to),
	})
	if err != nil {
		return nil, err
	}
	ids := make([]pgtype.UUID, 0, len(recurring))
	for _, row := range recurring {
		ids = append(ids, row.Event.ID)
	}
	exceptions, err := h.recurringExceptions(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, row := range recurring {
		e, err := h.expand(row.Event, owner, exceptions[row.Event.ID.Bytes], false)
		if err != nil {
			return nil, err
		}
		err = e.walk(&from, &to, func(o seriesOccurrence) bool {
			items = append(items, e.item(o))
			return true
		})
		if err != nil {
			return nil, err
		}
		for _, o := range e.movedWithin(&from, &to) {
			items = append(items, e.item(o))
		}
	}
	// Listing takes what overlaps the range; reminders need what starts in it.
	kept := items[:0]
	for _, item := range items {
		start := itemStart(item, owner)
		if item.Status == generated.Active && !start.Before(from) && start.Before(to) {
			kept = append(kept, item)
		}
	}
	if err := h.resolveSources(ctx, kept); err != nil {
		return nil, err
	}
	return kept, nil
}

// itemStart is when a listed item starts: a timed event's instant, an
// all-day event's first day at midnight in owner.
func itemStart(item generated.CalendarEvent, owner *time.Location) time.Time {
	if item.AllDay && item.StartDate != nil {
		day := item.StartDate.Time
		return time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, owner)
	}
	if item.StartAt != nil {
		return *item.StartAt
	}
	return time.Time{}
}

// localDay is instant's date in owner, at midnight UTC like stored dates.
func localDay(instant time.Time, owner *time.Location) time.Time {
	local := instant.In(owner)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
}
