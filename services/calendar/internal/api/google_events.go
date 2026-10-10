package api

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/oapi-codegen/runtime/types"

	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/services/calendar/internal/api/generated"
	"github.com/GAMch1k/life-os/services/calendar/internal/google"
	store "github.com/GAMch1k/life-os/services/calendar/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/calendar/internal/sources"
)

// Two-way Google Calendar sync (P11-03) as the API serves it: which events
// may be changed and why not, where an event is in Google, and the changes
// to synced Google events, which go to Google first (see internal/google).
// Life OS events are changed locally and their change queued for export.

func googleReadOnly(reason string) error {
	return httpserver.NewError(fiber.StatusConflict, "google_read_only", reason)
}

// googleWriteError turns a failed write to Google into a response.
func googleWriteError(err error) error {
	var readOnly *google.ReadOnlyError
	switch {
	case errors.As(err, &readOnly):
		return googleReadOnly(readOnly.Reason)
	case errors.Is(err, google.ErrConflict), errors.Is(err, google.ErrOccurrenceNotInGoogle):
		return httpserver.NewError(fiber.StatusConflict, "google_conflict", err.Error())
	case errors.Is(err, google.ErrDeletedInGoogle):
		return httpserver.NewError(fiber.StatusConflict, "google_deleted", err.Error())
	case errors.Is(err, google.ErrReminderNotInGoogle), errors.Is(err, google.ErrTooManyGoogleReminders):
		return badRequest(err.Error())
	}
	return googleError(err)
}

// syncedByGoogle reports whether row is an event of a Google calendar that
// Calendar's in-process syncer keeps, changed in Google first. The events
// of a source an integration syncs (see integration.go) are read-only here
// except for their reminders and priority, whatever their kind.
func (h *Handler) syncedByGoogle(ctx context.Context, row store.Event) (bool, error) {
	if h.google == nil || row.SourceKind != string(generated.EventSourceKindGoogle) || !row.SourceID.Valid {
		return false, nil
	}
	source, err := h.queries.GetSource(ctx, row.SourceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return source.InProcess && source.GoogleCalendarID.Valid, nil
}

// readOnlyFeed is why a feed event is read-only.
func readOnlyFeed(name *string) string {
	from := "an ICS feed"
	if name != nil && strings.TrimSpace(*name) != "" {
		from = "“" + strings.TrimSpace(*name) + "”"
	}
	return "Imported from " + from + ", so it can only be changed there. Its reminders and priority can still be adjusted here."
}

// resolveGoogle completes each item with what it allows (editable,
// readOnlyReason) and where it is in Google (google): a Life OS event's
// export to the Life OS calendar, a Google event's calendar.
func (h *Handler) resolveGoogle(ctx context.Context, items []generated.CalendarEvent) error {
	var manual []pgtype.UUID
	var sourceIDs []pgtype.UUID
	var uids []string
	for index := range items {
		item := &items[index]
		switch item.Source.Kind {
		case generated.EventSourceKindManual:
			item.Editable = true
			manual = append(manual, pgUUID(item.Id))
		case generated.EventSourceKindGoogle:
			if item.Source.SourceId != nil && item.Source.ExternalUid != nil {
				sourceIDs = append(sourceIDs, pgUUID(*item.Source.SourceId))
				uids = append(uids, *item.Source.ExternalUid)
			}
		default:
			reason := readOnlyFeed(item.Source.Name)
			item.Editable, item.ReadOnlyReason = false, &reason
		}
	}
	if h.google == nil {
		return nil
	}
	if len(manual) > 0 {
		if err := h.resolveExports(ctx, items, manual); err != nil {
			return err
		}
	}
	if len(sourceIDs) == 0 {
		return nil
	}
	rows, err := h.queries.ListGoogleEventLinks(ctx, store.ListGoogleEventLinksParams{SourceIds: sourceIDs, ExternalUids: uids})
	if err != nil {
		return err
	}
	links := make(map[[16]byte]store.ListGoogleEventLinksRow, len(rows))
	for _, row := range rows {
		links[row.SourceID.Bytes] = row
	}
	for index := range items {
		item := &items[index]
		if item.Source.Kind != generated.EventSourceKindGoogle || item.Source.SourceId == nil || item.Source.ExternalUid == nil {
			continue
		}
		link, ok := links[[16]byte(*item.Source.SourceId)]
		if !ok {
			// A source an integration syncs: read-only here.
			reason := readOnlyFeed(item.Source.Name)
			item.Editable, item.ReadOnlyReason = false, &reason
			continue
		}
		calendarID := uuid(link.CalendarID)
		item.Google = &generated.EventGoogleLink{CalendarId: &calendarID, CalendarName: link.Summary, State: generated.EventGoogleLinkStateSynced}
		uid := *item.Source.ExternalUid
		master := slices.Contains(link.Masters, uid)
		restorable := item.RemovedFromSourceAt != nil && !strings.Contains(uid, "_")
		var reason string
		switch {
		case !google.Writable(link.AccessRole):
			reason = google.ReaderReason(link.Summary, link.AccessRole)
		case master || restorable:
		default:
			reason = google.ExpandedReason(link.Summary)
		}
		item.Editable = reason == ""
		if reason != "" {
			item.ReadOnlyReason = &reason
		}
	}
	return nil
}

// resolveExports sets google on Life OS events exported (or waiting to be
// exported) to the Life OS calendar.
func (h *Handler) resolveExports(ctx context.Context, items []generated.CalendarEvent, ids []pgtype.UUID) error {
	exports, err := h.queries.ListGoogleExports(ctx, ids)
	if err != nil || len(exports) == 0 {
		return err
	}
	connection, found, err := h.google.Connection(ctx)
	if err != nil || !found {
		return err
	}
	byEvent := make(map[[16]byte]store.GoogleExport, len(exports))
	for _, export := range exports {
		byEvent[export.EventID.Bytes] = export
	}
	name := google.LifeOSCalendarName
	var calendarID *types.UUID
	if row, listed, err := h.google.LifeOSCalendar(ctx, connection); err != nil {
		return err
	} else if listed {
		id := uuid(row.ID)
		name, calendarID = row.Summary, &id
	}
	for index := range items {
		item := &items[index]
		if item.Source.Kind != generated.EventSourceKindManual {
			continue
		}
		export, ok := byEvent[[16]byte(item.Id)]
		if !ok || export.Deleted || export.ConnectionID != connection.ID {
			continue
		}
		state := generated.EventGoogleLinkState(export.State)
		if export.GoogleEventID == nil && state == generated.EventGoogleLinkStateSynced {
			continue // not in Google (cancelled, or its copy deleted there)
		}
		if !connection.ExportEnabled && export.GoogleEventID == nil {
			continue
		}
		item.Google = &generated.EventGoogleLink{CalendarId: calendarID, CalendarName: name, LifeOs: true, State: state}
		if state == generated.EventGoogleLinkStateFailed {
			item.Google.Error = export.LastError
		}
	}
	return nil
}

// googleFields is an event's fields for writing to Google.
func googleFields(f eventFields) google.Fields {
	out := google.Fields{
		Title: f.Title, Description: f.Description, Location: f.Location, AllDay: f.AllDay, TimeZone: f.TimeZone,
		UseDefaultReminders: f.UseDefaultReminders, Reminders: sourceReminders(f.Reminders), Priority: priorityColumn(f.Priority),
	}
	if f.AllDay {
		if f.StartDate != nil && f.EndDate != nil {
			out.StartDate, out.EndDate = f.StartDate.Time, f.EndDate.Time
		}
	} else if f.StartAt != nil && f.EndAt != nil {
		out.StartAt, out.EndAt = *f.StartAt, *f.EndAt
	}
	if rule := f.Recurrence; rule != nil {
		columns := recurrenceColumns(rule)
		recurrence := &sources.Recurrence{Frequency: string(rule.Frequency), Interval: rule.Interval, ByWeekday: columns.byWeekday, Until: rule.Until}
		if rule.Count != nil {
			recurrence.Count = *rule.Count
		}
		out.Recurrence = recurrence
	}
	return out
}

func sourceReminders(list []generated.Reminder) []sources.Reminder {
	out := make([]sources.Reminder, 0, len(list))
	for _, reminder := range list {
		out = append(out, sources.Reminder{Kind: string(reminder.Kind), MinutesBefore: reminder.MinutesBefore, DaysBefore: reminder.DaysBefore, LocalTime: reminder.LocalTime})
	}
	return out
}

// googleTarget is what a change to the synced Google event current writes
// to; for a read-only one, a patch of only its reminders (ok false) is
// stored in Life OS as before, anything else is refused.
func (h *Handler) googleTarget(ctx context.Context, current store.Event, patch *eventPatch) (google.Target, bool, error) {
	target, err := h.google.Target(ctx, current)
	var readOnly *google.ReadOnlyError
	if errors.As(err, &readOnly) && patch != nil && patch.importedEditable() {
		return google.Target{}, false, nil
	}
	if err != nil {
		return google.Target{}, false, googleWriteError(err)
	}
	return target, true, nil
}

// updateGoogleEvent changes a synced Google event (All events for a
// series): the patch is merged and validated as for any event, written to
// Google with the event's etag, and the event then shows Google's answer.
// Cancelling deletes it in Google.
func (h *Handler) updateGoogleEvent(c fiber.Ctx, current store.Event, target google.Target, patch eventPatch, status int) error {
	fields, err := fieldsFromStore(current)
	if err != nil {
		return internal(err)
	}
	merged, err := patch.apply(fields, h.now(), h.ownerLocation)
	if err != nil {
		return badRequest(err.Error())
	}
	if patch.present["priority"] {
		// Google keeps the priority in a Life OS extended property; the
		// source's default is written as none.
		fallback, err := priorityDefault(c.Context(), h.queries, current)
		if err != nil {
			return internal(err)
		}
		merged = merged.withPriorityDefault(fallback)
	}
	ctx := h.traced(c)
	switch {
	case target.Deleted && merged.Status != generated.Active:
		return httpserver.NewError(fiber.StatusConflict, "google_deleted", "This event was deleted in Google; restore it (status active) before changing it")
	case merged.Status == generated.Cancelled && current.Status != string(generated.Cancelled):
		err = h.google.DeleteEvent(ctx, target)
	default:
		body, buildErr := google.EventPatch(googleFields(merged), patch.present, h.ownerLocation, target.KeptRecurrence())
		if buildErr != nil {
			return googleWriteError(buildErr)
		}
		if merged.Status == generated.Active && (current.Status != string(generated.Active) || target.Deleted) {
			body["status"] = "confirmed"
		}
		if len(body) > 0 {
			err = h.google.UpdateEvent(ctx, target, body)
		}
	}
	if err != nil {
		return googleWriteError(err)
	}
	item, err := h.queries.GetEvent(c.Context(), current.ID)
	if err != nil {
		return databaseError(err)
	}
	if eventType, changes := classifyUpdate(current, item); eventType != "" {
		h.recordEvent(c, eventType, item, &current, changes)
	}
	return h.respond(c, status, item)
}

// deleteGoogleEvent deletes a synced Google event in Google, then in Life
// OS.
func (h *Handler) deleteGoogleEvent(c fiber.Ctx, current store.Event, target google.Target) error {
	if err := h.google.DeleteEvent(h.traced(c), target); err != nil {
		return googleWriteError(err)
	}
	if _, err := h.queries.DeleteEvent(c.Context(), current.ID); err != nil {
		return internal(err)
	}
	h.recordEvent(c, eventDeleted, current, nil, nil)
	return c.SendStatus(fiber.StatusNoContent)
}

// googleOccurrence finds the occurrence rid of a synced Google series.
func (h *Handler) googleOccurrence(ctx context.Context, row store.Event, rid string) (expansion, located, error) {
	if row.RecurrenceFrequency == nil {
		return expansion{}, located{}, doesNotRepeat()
	}
	e, err := h.loadExpansion(ctx, h.queries, row)
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

// updateGoogleOccurrence changes one occurrence of a synced Google series
// (This event) through its instance in Google.
func (h *Handler) updateGoogleOccurrence(c fiber.Ctx, row store.Event, rid string, patch eventPatch) error {
	target, err := h.google.Target(c.Context(), row)
	if err != nil {
		return googleWriteError(err)
	}
	e, found, err := h.googleOccurrence(c.Context(), row, rid)
	if err != nil {
		return err
	}
	if patch.present["allDay"] && *patch.body.AllDay != row.AllDay {
		return badRequest("an occurrence cannot switch between timed and all-day; change all events instead")
	}
	o := found.occurrence
	before, err := e.fields(o)
	if err != nil {
		return internal(err)
	}
	after, err := patch.apply(before, h.now(), h.ownerLocation)
	if err != nil {
		return badRequest(err.Error())
	}
	present := map[string]bool{}
	for name := range patch.present {
		if name != "status" {
			present[name] = true
		}
	}
	body, err := google.EventPatch(googleFields(after), present, h.ownerLocation, nil)
	if err != nil {
		return googleWriteError(err)
	}
	switch {
	case after.Status == generated.Cancelled && before.Status != generated.Cancelled:
		body = map[string]any{"status": "cancelled"}
	case after.Status == generated.Active && before.Status == generated.Cancelled:
		body["status"] = "confirmed"
	}
	if len(body) > 0 {
		if err := h.google.UpdateOccurrence(h.traced(c), target, o.key, row.AllDay, body); err != nil {
			return googleWriteError(err)
		}
	}
	latest, err := h.queries.GetEvent(c.Context(), row.ID)
	if err != nil {
		return databaseError(err)
	}
	e, found, err = h.googleOccurrence(c.Context(), latest, rid)
	if err != nil {
		return err
	}
	shown, err := e.fields(found.occurrence)
	if err != nil {
		return internal(err)
	}
	stamp := latest.UpdatedAt.Time
	if found.occurrence.ex != nil {
		stamp = found.occurrence.ex.updatedAt
	}
	h.recordOccurrence(c, latest, found.occurrence.key, before, shown, stamp)
	return h.respondItem(c, fiber.StatusOK, e.item(found.occurrence))
}

// timingFields are the changes that move a series' occurrences.
var timingFields = []string{"allDay", "startAt", "endAt", "startDate", "endDate", "timeZone", "recurrence"}

// splitGoogleSeries changes this and following occurrences of a synced
// Google series: the new series is created in Google (carrying the skips
// and changed occurrences from the split on when the change keeps their
// times), then the old one ends before the occurrence.
func (h *Handler) splitGoogleSeries(c fiber.Ctx, row store.Event, target google.Target, rid string, patch eventPatch) error {
	e, found, err := h.googleOccurrence(c.Context(), row, rid)
	if err != nil {
		return err
	}
	if found.index == 0 {
		return h.updateGoogleEvent(c, row, target, patch, fiber.StatusOK)
	}
	endedFields, started, err := h.splitAt(e, found)
	if err != nil {
		return internal(err)
	}
	next, err := patch.apply(started, h.now(), h.ownerLocation)
	if err != nil {
		return badRequest(err.Error())
	}
	retimed := slices.ContainsFunc(timingFields, func(name string) bool { return patch.present[name] })
	var exdates []string
	var carried []google.Carried
	if !retimed && next.Recurrence != nil {
		for _, key := range keysFrom(e, found) {
			ex := e.exceptions[key]
			if ex.cancelledAt != nil {
				if line, ok := google.ExDate(key, row.AllDay); ok {
					exdates = append(exdates, line)
				}
				continue
			}
			changes := ex.changes
			if key == found.occurrence.key {
				changes = changes.without(patch.present)
			}
			natural, ok := e.naturalAt(key)
			if changes.empty() || !ok {
				continue
			}
			occurrence := next
			occurrence.Recurrence = nil
			if row.AllDay {
				first, last := types.Date{Time: natural.Wall}, types.Date{Time: natural.LastDay}
				occurrence.StartDate, occurrence.EndDate = &first, &last
			} else {
				start, end := natural.Start.UTC(), natural.End.UTC()
				occurrence.StartAt, occurrence.EndAt = &start, &end
			}
			body, err := google.InstanceBody(googleFields(changes.applyTo(occurrence)), h.ownerLocation)
			if err != nil {
				return googleWriteError(err)
			}
			carried = append(carried, google.Carried{Key: key, Body: body})
		}
	}
	startedBody, err := google.SeriesBody(googleFields(next), h.ownerLocation, exdates)
	if err != nil {
		return googleWriteError(err)
	}
	ended := google.RecurrenceLines(googleFields(endedFields).Recurrence, row.AllDay, h.ownerLocation, target.KeptRecurrence())
	newID, err := h.google.SplitSeries(h.traced(c), target, ended, startedBody, carried, found.occurrence.key)
	if err != nil {
		return googleWriteError(err)
	}
	endedRow, err := h.queries.GetEvent(c.Context(), row.ID)
	if err != nil {
		return databaseError(err)
	}
	newSeries, err := h.queries.GetEventBySourceUID(c.Context(), store.GetEventBySourceUIDParams{SourceID: row.SourceID, ExternalUid: &newID})
	if err != nil {
		return databaseError(err)
	}
	h.recordEvent(c, eventUpdated, endedRow, &row, []string{"recurrence"})
	h.emitEvent(c, eventCreated, newSeries, nil, nil, map[string]any{"splitFrom": uuid(row.ID).String()})
	return h.respond(c, fiber.StatusCreated, newSeries)
}

// truncateGoogleSeries deletes this and following occurrences of a synced
// Google series: it ends before the occurrence in Google (on the first
// occurrence the series is deleted).
func (h *Handler) truncateGoogleSeries(c fiber.Ctx, row store.Event, target google.Target, rid string) error {
	e, found, err := h.googleOccurrence(c.Context(), row, rid)
	if err != nil {
		return err
	}
	if found.index == 0 {
		return h.deleteGoogleEvent(c, row, target)
	}
	endedFields, _, err := h.splitAt(e, found)
	if err != nil {
		return internal(err)
	}
	ended := google.RecurrenceLines(googleFields(endedFields).Recurrence, row.AllDay, h.ownerLocation, target.KeptRecurrence())
	if err := h.google.TruncateSeries(h.traced(c), target, ended, found.occurrence.key); err != nil {
		return googleWriteError(err)
	}
	endedRow, err := h.queries.GetEvent(c.Context(), row.ID)
	if err != nil {
		return databaseError(err)
	}
	h.recordEvent(c, eventUpdated, endedRow, &row, []string{"recurrence"})
	return c.SendStatus(fiber.StatusNoContent)
}

// UpdateGoogleConnection changes the connection's settings: exporting Life
// OS events on or off.
func (h *Handler) UpdateGoogleConnection(c fiber.Ctx) error {
	var body struct {
		ExportEnabled *bool `json:"exportEnabled"`
	}
	if err := json.Unmarshal(c.Body(), &body); err != nil || body.ExportEnabled == nil {
		return badRequest("the update must set exportEnabled to true or false")
	}
	if h.google == nil {
		return googleError(google.ErrNotConnected)
	}
	row, err := h.google.SetExportEnabled(h.traced(c), *body.ExportEnabled)
	if err != nil {
		return googleError(err)
	}
	view, err := h.googleConnectionView(c.Context(), row, h.google.Configured())
	if err != nil {
		return internal(err)
	}
	return c.JSON(view)
}

// googleConnectionView is the connection with its export state and Life OS
// calendar.
func (h *Handler) googleConnectionView(ctx context.Context, row store.GoogleConnection, configured bool) (generated.GoogleConnection, error) {
	view := googleConnection(row, configured)
	summary, err := h.google.ExportSummary(ctx, row)
	if err != nil {
		return view, err
	}
	view.Export = generated.GoogleExportState{Pending: int(summary.Pending), Failed: int(summary.Failed), LastError: summary.LastError, LastExportedAt: nullableTime(summary.LastExportedAt)}
	if row.LifeosCalendarID == nil && row.LifeosCalendarNote != nil && row.ExportEnabled {
		// The Life OS calendar could not be created: nothing is exported.
		view.Export.LastError = row.LifeosCalendarNote
	}
	if row.LifeosCalendarID != nil {
		calendar := generated.LifeOsCalendar{GoogleCalendarId: *row.LifeosCalendarID, Summary: google.LifeOSCalendarName, CreatedAt: nullableTime(row.LifeosCalendarCreatedAt), Note: row.LifeosCalendarNote}
		listed, found, err := h.google.LifeOSCalendar(ctx, row)
		if err != nil {
			return view, err
		}
		if found {
			id := uuid(listed.ID)
			calendar.CalendarId, calendar.Summary = &id, listed.Summary
		}
		view.LifeOsCalendar = &calendar
	}
	return view, nil
}
