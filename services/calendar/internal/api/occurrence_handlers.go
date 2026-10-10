package api

import (
	"context"
	"slices"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/oapi-codegen/runtime/types"

	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/services/calendar/internal/api/generated"
	"github.com/GAMch1k/life-os/services/calendar/internal/recurrence"
	store "github.com/GAMch1k/life-os/services/calendar/internal/repository/generated"
)

// The scopes of a change to a recurring event, as in Google Calendar: This
// event (one occurrence's own changes or skip), This and following (split
// or truncate the series at an occurrence), and All events (UpdateEvent and
// DeleteEvent).

func doesNotRepeat() error {
	return httpserver.NewError(fiber.StatusNotFound, "not_found", "this event does not repeat")
}

func occurrenceNotFound() error {
	return httpserver.NewError(fiber.StatusNotFound, "not_found", "occurrence not found")
}

// lockOccurrence holds a recurring Life OS event in the transaction of
// queries and finds the occurrence rid names.
func (h *Handler) lockOccurrence(ctx context.Context, queries *store.Queries, id types.UUID, rid string) (expansion, located, error) {
	row, err := queries.LockEvent(ctx, pgUUID(id))
	if err != nil {
		return expansion{}, located{}, databaseError(err)
	}
	if row.SourceKind != string(generated.EventSourceKindManual) {
		return expansion{}, located{}, managedBySource()
	}
	if row.RecurrenceFrequency == nil {
		return expansion{}, located{}, doesNotRepeat()
	}
	e, err := h.loadExpansion(ctx, queries, row)
	if err != nil {
		return expansion{}, located{}, internal(err)
	}
	found, ok, err := e.find(rid)
	if err != nil {
		return expansion{}, located{}, internal(err)
	}
	if !ok {
		return expansion{}, located{}, occurrenceNotFound()
	}
	return e, found, nil
}

// respondItem renders one listed item, with its effective reminders and
// source.
func (h *Handler) respondItem(c fiber.Ctx, status int, item generated.CalendarEvent) error {
	items := []generated.CalendarEvent{item}
	if err := h.resolveSources(c.Context(), items); err != nil {
		return internal(err)
	}
	return c.Status(status).JSON(items[0])
}

func (h *Handler) GetEventOccurrence(c fiber.Ctx, id types.UUID, rid string) error {
	row, err := h.queries.GetEvent(c.Context(), pgUUID(id))
	if err != nil {
		return databaseError(err)
	}
	if row.RecurrenceFrequency == nil {
		return doesNotRepeat()
	}
	e, err := h.loadExpansion(c.Context(), h.queries, row)
	if err != nil {
		return internal(err)
	}
	found, ok, err := e.find(rid)
	if err != nil {
		return internal(err)
	}
	if !ok {
		return occurrenceNotFound()
	}
	return h.respondItem(c, fiber.StatusOK, e.item(found.occurrence))
}

// UpdateEventOccurrence changes one occurrence (This event): the patch is
// merged into the occurrence as it is now and validated like an event; what
// then differs from the series is stored as the occurrence's own changes.
func (h *Handler) UpdateEventOccurrence(c fiber.Ctx, id types.UUID, rid string) error {
	patch, err := decodePatch(c.Body())
	if err != nil {
		return badRequest(err.Error())
	}
	if patch.present["recurrence"] {
		return badRequest("recurrence belongs to the series: change all events, or this and following")
	}
	if patch.present["timeZone"] {
		return badRequest("timeZone belongs to the series: change all events, or this and following")
	}
	if row, isGoogle, err := h.googleSeries(c, id); err != nil {
		return err
	} else if isGoogle {
		return h.updateGoogleOccurrence(c, row, rid, patch)
	}
	var (
		e             expansion
		o             seriesOccurrence
		before, after eventFields
		stamp         time.Time
	)
	err = h.inTx(c.Context(), func(queries *store.Queries) error {
		var found located
		e, found, err = h.lockOccurrence(c.Context(), queries, id, rid)
		if err != nil {
			return err
		}
		if patch.present["allDay"] && *patch.body.AllDay != e.row.AllDay {
			return badRequest("an occurrence cannot switch between timed and all-day; change all events instead")
		}
		o = found.occurrence
		natural, err := e.naturalFields(o)
		if err != nil {
			return internal(err)
		}
		if before, err = e.fields(o); err != nil {
			return internal(err)
		}
		if after, err = patch.apply(before, h.now(), h.ownerLocation); err != nil {
			return badRequest(err.Error())
		}
		changes := diffOccurrence(natural, after)
		var cancelledAt *time.Time
		if after.Status == generated.Cancelled {
			cancelledAt = after.CancelledAt
		}
		stamp = h.now()
		if changes.empty() && cancelledAt == nil {
			// The occurrence follows its series again.
			if o.ex != nil {
				if _, err := queries.DeleteEventExceptions(c.Context(), store.DeleteEventExceptionsParams{EventID: e.row.ID, RecurrenceIds: []string{o.key}}); err != nil {
					return internal(err)
				}
			}
			o.ex, o.at = nil, o.natural
			return nil
		}
		raw, err := changes.encode()
		if err != nil {
			return internal(err)
		}
		row, err := queries.UpsertEventException(c.Context(), store.UpsertEventExceptionParams{EventID: e.row.ID, RecurrenceID: o.key, Changes: raw, CancelledAt: timestamp(cancelledAt)})
		if err != nil {
			return internal(err)
		}
		if o.ex, err = exceptionFromStore(row); err != nil {
			return internal(err)
		}
		stamp = o.ex.updatedAt
		o.at = e.movedTimes(o.natural, o.ex.changes)
		return nil
	})
	if err != nil {
		return err
	}
	h.recordOccurrence(c, e.row, o.key, before, after, stamp)
	h.google.QueueExport(c.Context(), e.row.ID)
	return h.respondItem(c, fiber.StatusOK, e.item(o))
}

// googleSeries reads the event a change of an occurrence names and reports
// whether it is a synced Google event (changed through Google).
func (h *Handler) googleSeries(c fiber.Ctx, id types.UUID) (store.Event, bool, error) {
	if h.google == nil {
		return store.Event{}, false, nil
	}
	row, err := h.queries.GetEvent(c.Context(), pgUUID(id))
	if err != nil {
		return store.Event{}, false, databaseError(err)
	}
	return row, row.SourceKind == string(generated.EventSourceKindGoogle), nil
}

// untilBefore is the until that ends a series with the occurrence previous:
// its start, or for an all-day series midnight of its day in the owner's
// timezone (an all-day until is compared as that date).
func (h *Handler) untilBefore(previous recurrence.Occurrence, allDay bool) time.Time {
	if allDay {
		day := previous.Wall
		return time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, h.ownerLocation).UTC()
	}
	return previous.Start.UTC()
}

// splitAt is the series of e split before found: ended is the series
// ending with the occurrence before found (until at it, no count), started
// the series from found on, at found's original times (a count rule keeps
// the occurrences still to come). found must not be the first occurrence.
func (h *Handler) splitAt(e expansion, found located) (ended, started eventFields, err error) {
	if ended, err = fieldsFromStore(e.row); err != nil {
		return eventFields{}, eventFields{}, err
	}
	endedRule := *ended.Recurrence
	until := h.untilBefore(*found.previous, e.series.AllDay)
	endedRule.Until, endedRule.Count = &until, nil
	ended.Recurrence = &endedRule

	if started, err = fieldsFromStore(e.row); err != nil {
		return eventFields{}, eventFields{}, err
	}
	natural := found.occurrence.natural
	if e.series.AllDay {
		startDate, endDate := types.Date{Time: natural.Wall}, types.Date{Time: natural.LastDay}
		started.StartDate, started.EndDate = &startDate, &endDate
	} else {
		start, end := natural.Start.UTC(), natural.End.UTC()
		started.StartAt, started.EndAt = &start, &end
	}
	startedRule := *started.Recurrence
	if startedRule.Count != nil {
		remaining := *startedRule.Count - found.index
		startedRule.Count = &remaining
	}
	started.Recurrence = &startedRule
	return ended, started, nil
}

// writeEnded writes ended (see splitAt) over the series of e.
func (h *Handler) writeEnded(ctx context.Context, queries *store.Queries, e expansion, ended eventFields) (store.Event, error) {
	params, err := ended.updateParams(e.row.ID, e.row.UpdatedAt)
	if err != nil {
		return store.Event{}, internal(err)
	}
	item, err := queries.UpdateEvent(ctx, params)
	if err != nil {
		return store.Event{}, databaseError(err)
	}
	return item, nil
}

// keysFrom are the recurrence ids of e's exceptions at or after found.
func keysFrom(e expansion, found located) []string {
	start := keyMoment(found.occurrence.natural, e.series.AllDay)
	var keys []string
	for key := range e.exceptions {
		if _, moment, ok := canonicalKey(key, e.series.AllDay); ok && !moment.Before(start) {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	return keys
}

// SplitEventSeries changes this and following occurrences: the series ends
// before the occurrence, and a new series with the patch starts at it. On
// the first occurrence it changes the series itself.
func (h *Handler) SplitEventSeries(c fiber.Ctx, id types.UUID, rid string) error {
	patch, err := decodePatch(c.Body())
	if err != nil {
		return badRequest(err.Error())
	}
	if row, isGoogle, err := h.googleSeries(c, id); err != nil {
		return err
	} else if isGoogle {
		target, err := h.google.Target(c.Context(), row)
		if err != nil {
			return googleWriteError(err)
		}
		return h.splitGoogleSeries(c, row, target, rid, patch)
	}
	var (
		first                    bool
		before, ended, newSeries store.Event
	)
	err = h.inTx(c.Context(), func(queries *store.Queries) error {
		e, found, err := h.lockOccurrence(c.Context(), queries, id, rid)
		if err != nil {
			return err
		}
		before = e.row
		if found.index == 0 {
			first = true
			ended, err = h.updateLocked(c.Context(), queries, e.row, patch)
			return err
		}
		// The new series is the old one from this occurrence on, with the
		// patch.
		endedFields, started, err := h.splitAt(e, found)
		if err != nil {
			return internal(err)
		}
		next, err := patch.apply(started, h.now(), h.ownerLocation)
		if err != nil {
			return badRequest(err.Error())
		}
		params, err := next.withPriorityDefault(defaultPriority).createParams()
		if err != nil {
			return internal(err)
		}
		if newSeries, err = queries.CreateEvent(c.Context(), params); err != nil {
			return internal(err)
		}
		if ended, err = h.writeEnded(c.Context(), queries, e, endedFields); err != nil {
			return err
		}
		return h.handOverExceptions(c.Context(), queries, e, found, newSeries, patch)
	})
	if err != nil {
		return err
	}
	h.google.QueueExport(c.Context(), ended.ID)
	if first {
		if eventType, changes := classifyUpdate(before, ended); eventType != "" {
			h.recordEvent(c, eventType, ended, &before, changes)
		}
		return h.respond(c, fiber.StatusOK, ended)
	}
	h.google.QueueExport(c.Context(), newSeries.ID)
	h.recordEvent(c, eventUpdated, ended, &before, []string{"recurrence"})
	h.emitEvent(c, eventCreated, newSeries, nil, nil, map[string]any{"splitFrom": uuid(before.ID).String()})
	return h.respond(c, fiber.StatusCreated, newSeries)
}

// handOverExceptions moves the exceptions from found on to the new series
// of a split where their occurrence still exists there (the same original
// start), and drops the others. The split occurrence loses its own changes
// to the fields the patch sets: the patch is what it now shows.
func (h *Handler) handOverExceptions(ctx context.Context, queries *store.Queries, e expansion, found located, newSeries store.Event, patch eventPatch) error {
	keys := keysFrom(e, found)
	if len(keys) == 0 {
		return nil
	}
	exists := map[string]bool{}
	if newSeries.RecurrenceFrequency != nil {
		next, err := h.expand(newSeries, h.ownerLocation, nil, true)
		if err != nil {
			return internal(err)
		}
		if exists, err = next.existing(keys); err != nil {
			return internal(err)
		}
	}
	if own := e.exceptions[found.occurrence.key]; own != nil && exists[own.key] {
		changes := own.changes.without(patch.present)
		if changes.empty() && own.cancelledAt == nil {
			exists[own.key] = false
		} else {
			raw, err := changes.encode()
			if err != nil {
				return internal(err)
			}
			if _, err := queries.UpsertEventException(ctx, store.UpsertEventExceptionParams{EventID: e.row.ID, RecurrenceID: own.key, Changes: raw, CancelledAt: timestamp(own.cancelledAt)}); err != nil {
				return internal(err)
			}
		}
	}
	var move, drop []string
	for _, key := range keys {
		if exists[key] {
			move = append(move, key)
		} else {
			drop = append(drop, key)
		}
	}
	if len(move) > 0 {
		if _, err := queries.MoveEventExceptions(ctx, store.MoveEventExceptionsParams{ToEventID: newSeries.ID, FromEventID: e.row.ID, RecurrenceIds: move}); err != nil {
			return internal(err)
		}
	}
	if len(drop) > 0 {
		if _, err := queries.DeleteEventExceptions(ctx, store.DeleteEventExceptionsParams{EventID: e.row.ID, RecurrenceIds: drop}); err != nil {
			return internal(err)
		}
	}
	return nil
}

// TruncateEventSeries deletes this and following occurrences: the series
// ends before the occurrence, and the exceptions from it on go. On the
// first occurrence the series is deleted.
func (h *Handler) TruncateEventSeries(c fiber.Ctx, id types.UUID, rid string) error {
	if row, isGoogle, err := h.googleSeries(c, id); err != nil {
		return err
	} else if isGoogle {
		target, err := h.google.Target(c.Context(), row)
		if err != nil {
			return googleWriteError(err)
		}
		return h.truncateGoogleSeries(c, row, target, rid)
	}
	var (
		deleted       bool
		before, ended store.Event
	)
	err := h.inTx(c.Context(), func(queries *store.Queries) error {
		e, found, err := h.lockOccurrence(c.Context(), queries, id, rid)
		if err != nil {
			return err
		}
		before = e.row
		if found.index == 0 {
			deleted = true
			if _, err := queries.DeleteEvent(c.Context(), e.row.ID); err != nil {
				return internal(err)
			}
			return nil
		}
		endedFields, _, err := h.splitAt(e, found)
		if err != nil {
			return internal(err)
		}
		if ended, err = h.writeEnded(c.Context(), queries, e, endedFields); err != nil {
			return err
		}
		if keys := keysFrom(e, found); len(keys) > 0 {
			if _, err := queries.DeleteEventExceptions(c.Context(), store.DeleteEventExceptionsParams{EventID: e.row.ID, RecurrenceIds: keys}); err != nil {
				return internal(err)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if deleted {
		h.recordEvent(c, eventDeleted, before, nil, nil)
		h.google.QueueDeletion(c.Context(), before.ID)
	} else {
		h.recordEvent(c, eventUpdated, ended, &before, []string{"recurrence"})
		h.google.QueueExport(c.Context(), ended.ID)
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// dropLostExceptions removes, after an All events change from before to
// after, the exceptions of occurrences that no longer exist: a change of
// the start, the kind, the time zone, or the rule can end or move them.
func (h *Handler) dropLostExceptions(ctx context.Context, queries *store.Queries, before, after store.Event) error {
	if before.RecurrenceFrequency == nil {
		return nil
	}
	same := before.AllDay == after.AllDay && sameInstant(before.StartAt, after.StartAt) && sameDate(before.StartDate, after.StartDate) &&
		sameString(before.TimeZone, after.TimeZone) && !slices.Contains(changedFields(before, after), "recurrence")
	if same {
		return nil
	}
	rows, err := queries.ListEventExceptions(ctx, []pgtype.UUID{before.ID})
	if err != nil || len(rows) == 0 {
		return err
	}
	keys := make([]string, 0, len(rows))
	for _, row := range rows {
		keys = append(keys, row.RecurrenceID)
	}
	exists := map[string]bool{}
	if after.RecurrenceFrequency != nil {
		e, err := h.expand(after, h.ownerLocation, nil, true)
		if err != nil {
			return err
		}
		if exists, err = e.existing(keys); err != nil {
			return err
		}
	}
	var lost []string
	for _, key := range keys {
		if !exists[key] {
			lost = append(lost, key)
		}
	}
	if len(lost) == 0 {
		return nil
	}
	_, err = queries.DeleteEventExceptions(ctx, store.DeleteEventExceptionsParams{EventID: before.ID, RecurrenceIds: lost})
	return err
}
