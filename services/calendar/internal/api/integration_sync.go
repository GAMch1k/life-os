package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5"
	"github.com/oapi-codegen/runtime/types"

	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/services/calendar/internal/api/generated"
	"github.com/GAMch1k/life-os/services/calendar/internal/recurrence"
	store "github.com/GAMch1k/life-os/services/calendar/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/calendar/internal/sources"
)

// Syncing an integration's source: one batch of its events (a snapshot or
// a delta), validated like events created in Life OS and reconciled by
// package sources, so the owner's own reminders and priority survive every
// sync.

// Limits of a sync batch (mirrored in the OpenAPI contract).
const (
	maxSyncItems         = 10000
	maxItemExceptions    = 1000
	maxExternalUID       = 2048
	maxImportedTitle     = 500
	maxImportedLocation  = 500
	maxImportedNotes     = 10000
	maxReadOnlyReason    = 500
	maxRecurrenceIDRunes = 64
)

// lifeOSUIDPrefix starts the external uid of an event Calendar creates in a
// source (the new series of a split) until the integration re-keys it.
const lifeOSUIDPrefix = "lifeos:"

func invalidSync(message string) error {
	return httpserver.NewError(fiber.StatusBadRequest, "invalid_sync_request", message)
}

// itemError is a 400 naming the item (by index and external uid).
func itemError(code string, index int, uid, message string) error {
	label := fmt.Sprintf("items[%d]", index)
	if uid != "" {
		shown := uid
		if utf8.RuneCountInString(shown) > 100 {
			shown = string([]rune(shown)[:100]) + "…"
		}
		label += fmt.Sprintf(" (externalUid %q)", shown)
	}
	return httpserver.NewError(fiber.StatusBadRequest, code, label+": "+message)
}

// syncBatch is a decoded, validated SyncIntegrationSourceRequest.
type syncBatch struct {
	mode       generated.SyncIntegrationSourceRequestMode
	items      []sources.Item
	rekeys     []rekey
	deleted    []string
	window     syncWindow
	emptyGuard bool
	// feedCursor is the change-feed position the integration has handled
	// (nil when not given).
	feedCursor *int64
}

type syncWindow struct {
	start        *time.Time
	upcomingOnly bool
}

// rekey gives a source's event the uid the source knows it by.
type rekey struct {
	previous, uid string
}

// rules are the batch's reconcile rules: which stored events it speaks for.
func (b syncBatch) rules(now time.Time, owner *time.Location) sources.Rules {
	switch {
	case b.window.start != nil:
		return sources.SyncRules(*b.window.start, owner)
	case b.window.upcomingOnly:
		return sources.FeedRules(now, owner)
	}
	return sources.AuthoritativeRules()
}

func decodeSyncBatch(raw []byte, owner *time.Location) (syncBatch, error) {
	var present map[string]json.RawMessage
	if err := json.Unmarshal(raw, &present); err != nil || present == nil {
		return syncBatch{}, invalidSync("the batch must be a JSON object")
	}
	var body struct {
		Mode   string            `json:"mode"`
		Items  []json.RawMessage `json:"items"`
		Window *struct {
			Start        *time.Time `json:"start"`
			UpcomingOnly bool       `json:"upcomingOnly"`
		} `json:"window"`
		Deleted    []string `json:"deleted"`
		EmptyGuard bool     `json:"emptyGuard"`
		FeedCursor *string  `json:"feedCursor"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return syncBatch{}, invalidSync("invalid batch: " + err.Error())
	}
	batch := syncBatch{mode: generated.SyncIntegrationSourceRequestMode(body.Mode), emptyGuard: body.EmptyGuard}
	if !batch.mode.Valid() {
		return syncBatch{}, invalidSync("mode must be snapshot or delta")
	}
	if _, ok := present["items"]; !ok || body.Items == nil {
		return syncBatch{}, invalidSync("items is required (an empty list for none)")
	}
	if len(body.Items) > maxSyncItems || len(body.Deleted) > maxSyncItems {
		return syncBatch{}, invalidSync(fmt.Sprintf("a batch takes at most %d items and %d deleted uids; send a large delta in several batches", maxSyncItems, maxSyncItems))
	}
	snapshot := batch.mode == generated.SyncIntegrationSourceRequestModeSnapshot
	if _, ok := present["deleted"]; ok && snapshot {
		return syncBatch{}, invalidSync("deleted is only for a delta: a snapshot removes the events it leaves out")
	}
	if body.EmptyGuard && !snapshot {
		return syncBatch{}, invalidSync("emptyGuard is only for a snapshot")
	}
	if body.Window != nil {
		if body.Window.Start != nil && body.Window.UpcomingOnly {
			return syncBatch{}, invalidSync("window takes start or upcomingOnly, not both")
		}
		batch.window = syncWindow{start: body.Window.Start, upcomingOnly: body.Window.UpcomingOnly}
	}
	if body.FeedCursor != nil {
		position, err := decodeFeedCursor(*body.FeedCursor)
		if err != nil || position.snapshot {
			return syncBatch{}, invalidSync("feedCursor must be a change-feed cursor of phase changes (a nextCursor the integration has handled)")
		}
		seq := position.seq
		batch.feedCursor = &seq
	}
	seen := map[string]bool{}
	for index, raw := range body.Items {
		item, previous, err := decodeSourceEvent(index, raw, owner)
		if err != nil {
			return syncBatch{}, err
		}
		if seen[item.ExternalUID] {
			return syncBatch{}, itemError("duplicate_uid", index, item.ExternalUID, "the batch already has an event with this externalUid")
		}
		seen[item.ExternalUID] = true
		if previous != "" {
			if seen[previous] {
				return syncBatch{}, itemError("duplicate_uid", index, item.ExternalUID, "previousExternalUid is another event's externalUid in the batch")
			}
			seen[previous] = true
			batch.rekeys = append(batch.rekeys, rekey{previous: previous, uid: item.ExternalUID})
		}
		batch.items = append(batch.items, item)
	}
	for index, uid := range body.Deleted {
		if !validExternalID(uid, maxExternalUID) {
			return syncBatch{}, invalidSync(fmt.Sprintf("deleted[%d] must be an externalUid of 1 to %d characters without control characters", index, maxExternalUID))
		}
		if seen[uid] {
			return syncBatch{}, httpserver.NewError(fiber.StatusBadRequest, "duplicate_uid", fmt.Sprintf("deleted[%d] (%q) is also in items, or deleted twice", index, uid))
		}
		seen[uid] = true
		batch.deleted = append(batch.deleted, uid)
	}
	if batch.items == nil {
		batch.items = []sources.Item{}
	}
	return batch, nil
}

// sourceEventBody is a SourceEvent as sent.
type sourceEventBody struct {
	ExternalUID         string                `json:"externalUid"`
	PreviousExternalUID *string               `json:"previousExternalUid"`
	Title               *string               `json:"title"`
	Description         *string               `json:"description"`
	Location            *string               `json:"location"`
	AllDay              *bool                 `json:"allDay"`
	StartAt             *time.Time            `json:"startAt"`
	EndAt               *time.Time            `json:"endAt"`
	StartDate           *types.Date           `json:"startDate"`
	EndDate             *types.Date           `json:"endDate"`
	TimeZone            *string               `json:"timeZone"`
	Status              *string               `json:"status"`
	Recurrence          *generated.Recurrence `json:"recurrence"`
	Exceptions          []struct {
		RecurrenceID string          `json:"recurrenceId"`
		Cancelled    bool            `json:"cancelled"`
		Changes      json.RawMessage `json:"changes"`
	} `json:"exceptions"`
	Reminders *struct {
		UseDefault *bool                `json:"useDefault"`
		Reminders  []generated.Reminder `json:"reminders"`
	} `json:"reminders"`
	Priority       *string `json:"priority"`
	ReadOnlyReason *string `json:"readOnlyReason"`
}

// decodeSourceEvent reads and validates one item: the source item and the
// uid it is re-keyed from ("" for none).
func decodeSourceEvent(index int, raw json.RawMessage, owner *time.Location) (sources.Item, string, error) {
	var body sourceEventBody
	if err := json.Unmarshal(raw, &body); err != nil {
		return sources.Item{}, "", itemError("invalid_item", index, "", "not a valid event: "+err.Error())
	}
	uid := body.ExternalUID
	fail := func(message string) error { return itemError("invalid_item", index, uid, message) }
	if !validExternalID(uid, maxExternalUID) {
		return sources.Item{}, "", fail(fmt.Sprintf("externalUid must be 1 to %d characters without control characters", maxExternalUID))
	}
	previous := ""
	if body.PreviousExternalUID != nil {
		previous = *body.PreviousExternalUID
		if !validExternalID(previous, maxExternalUID) || previous == uid {
			return sources.Item{}, "", fail("previousExternalUid must be another externalUid, of 1 to 2048 characters")
		}
	}
	if body.Title == nil || strings.TrimSpace(*body.Title) == "" || utf8.RuneCountInString(*body.Title) > maxImportedTitle {
		return sources.Item{}, "", fail(fmt.Sprintf("title must be 1 to %d characters", maxImportedTitle))
	}
	description, location := nonEmpty(body.Description), nonEmpty(body.Location)
	if description != nil && utf8.RuneCountInString(*description) > maxImportedNotes {
		return sources.Item{}, "", fail(fmt.Sprintf("description must be at most %d characters", maxImportedNotes))
	}
	if location != nil && utf8.RuneCountInString(*location) > maxImportedLocation {
		return sources.Item{}, "", fail(fmt.Sprintf("location must be at most %d characters", maxImportedLocation))
	}
	if body.AllDay == nil {
		return sources.Item{}, "", fail("allDay is required")
	}
	status := generated.Active
	if body.Status != nil {
		status = generated.EventStatus(*body.Status)
	}
	fields := eventFields{
		Title: *body.Title, Description: description, Location: location, AllDay: *body.AllDay,
		StartAt: body.StartAt, EndAt: body.EndAt, StartDate: body.StartDate, EndDate: body.EndDate, TimeZone: body.TimeZone,
		Recurrence: body.Recurrence, Reminders: []generated.Reminder{}, UseDefaultReminders: true, Status: status,
	}
	var reminders *sources.Reminders
	if body.Reminders != nil {
		if body.Reminders.UseDefault == nil {
			return sources.Item{}, "", fail("reminders.useDefault is required")
		}
		fields.Reminders = remindersOrNone(&body.Reminders.Reminders)
		reminders = &sources.Reminders{UseDefault: *body.Reminders.UseDefault, Reminders: sourceReminders(fields.Reminders)}
	}
	if err := fields.validate(owner); err != nil {
		return sources.Item{}, "", fail(err.Error())
	}
	item := sources.Item{
		Event: sources.Event{
			ExternalUID: uid, Title: fields.Title, Description: description, Location: location, AllDay: fields.AllDay,
			TimeZone: fields.TimeZone, Cancelled: status == generated.Cancelled,
		},
		Reminders: reminders,
	}
	if fields.AllDay {
		item.StartDate, item.EndDate = fields.StartDate.Time, fields.EndDate.Time
	} else {
		item.StartAt, item.EndAt = fields.StartAt.UTC(), fields.EndAt.UTC()
	}
	if body.Priority != nil {
		switch value := *body.Priority; value {
		case "low", "medium", "high":
			item.Priority = &value
		case string(generated.SourcePriorityDefault):
			none := ""
			item.Priority = &none
		default:
			return sources.Item{}, "", fail("priority must be low, medium, high, or default")
		}
	}
	if body.ReadOnlyReason != nil {
		reason := strings.TrimSpace(*body.ReadOnlyReason)
		if reason == "" || utf8.RuneCountInString(reason) > maxReadOnlyReason {
			return sources.Item{}, "", fail(fmt.Sprintf("readOnlyReason must be 1 to %d characters (leave it out when the event is not read-only)", maxReadOnlyReason))
		}
		item.ReadOnlyReason = &reason
	}
	if rule := fields.Recurrence; rule != nil {
		series := &sources.Recurrence{Frequency: string(rule.Frequency), Interval: rule.Interval}
		if rule.ByWeekday != nil && len(*rule.ByWeekday) > 0 {
			codes := make([]string, 0, len(*rule.ByWeekday))
			for _, day := range *rule.ByWeekday {
				codes = append(codes, string(day))
			}
			if normalized, ok := recurrence.NormalizeWeekdays(codes); ok {
				codes = normalized
			}
			series.ByWeekday = codes
		}
		if rule.Count != nil {
			series.Count = *rule.Count
		}
		if rule.Until != nil {
			until := rule.Until.UTC()
			series.Until = &until
		}
		item.Recurrence = series
	}
	if len(body.Exceptions) > 0 {
		if item.Recurrence == nil {
			return sources.Item{}, "", itemError("invalid_exception", index, uid, "exceptions are only for a series (an event with recurrence)")
		}
		if len(body.Exceptions) > maxItemExceptions {
			return sources.Item{}, "", itemError("invalid_exception", index, uid, fmt.Sprintf("a series takes at most %d exceptions", maxItemExceptions))
		}
		item.Exceptions = make(map[string]sources.Exception, len(body.Exceptions))
		for position, sent := range body.Exceptions {
			key, exception, err := decodeSourceException(sent.RecurrenceID, sent.Cancelled, sent.Changes, fields.AllDay)
			if err == nil {
				if _, twice := item.Exceptions[key]; twice {
					err = errors.New("the series already has an exception for this occurrence")
				}
			}
			if err != nil {
				return sources.Item{}, "", itemError("invalid_exception", index, uid, fmt.Sprintf("exceptions[%d] (recurrenceId %q): %s", position, clipRunes(sent.RecurrenceID, maxRecurrenceIDRunes), err.Error()))
			}
			item.Exceptions[key] = exception
		}
	}
	return item, previous, nil
}

// nonEmpty is value, or nil for none or empty.
func nonEmpty(value *string) *string {
	if value == nil || *value == "" {
		return nil
	}
	return value
}

func clipRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	return string([]rune(value)[:limit]) + "…"
}

// decodeSourceException validates one occurrence exception of a series
// (allDay: the series' kind) and returns its canonical recurrence id.
func decodeSourceException(rid string, cancelled bool, raw json.RawMessage, allDay bool) (string, sources.Exception, error) {
	key, _, ok := canonicalKey(rid, allDay)
	if !ok {
		if allDay {
			return "", sources.Exception{}, errors.New("recurrenceId must be the occurrence's original date (YYYY-MM-DD) in an all-day series")
		}
		return "", sources.Exception{}, errors.New("recurrenceId must be the occurrence's original start as an RFC 3339 instant in a timed series")
	}
	present := map[string]json.RawMessage{}
	if trimmed := strings.TrimSpace(string(raw)); trimmed != "" && trimmed != "null" {
		if err := json.Unmarshal(raw, &present); err != nil {
			return "", sources.Exception{}, errors.New("changes must be an object")
		}
	}
	_, hasStartAt := present["startAt"]
	_, hasEndAt := present["endAt"]
	_, hasStartDate := present["startDate"]
	_, hasEndDate := present["endDate"]
	switch {
	case allDay && (hasStartAt || hasEndAt):
		return "", sources.Exception{}, errors.New("an occurrence of an all-day series moves with startDate and endDate")
	case !allDay && (hasStartDate || hasEndDate):
		return "", sources.Exception{}, errors.New("an occurrence of a timed series moves with startAt and endAt")
	case hasStartAt != hasEndAt || hasStartDate != hasEndDate:
		return "", sources.Exception{}, errors.New("a moved occurrence gives both its start and its end")
	}
	changes, err := decodeChanges(mustJSON(present))
	if err != nil {
		return "", sources.Exception{}, errors.New("changes: " + strings.TrimPrefix(err.Error(), "decode occurrence changes: "))
	}
	switch {
	case changes.StartAt != nil && !changes.EndAt.After(*changes.StartAt):
		return "", sources.Exception{}, errors.New("endAt must be after startAt")
	case changes.StartDate != nil && changes.EndDate.Time.Before(changes.StartDate.Time):
		return "", sources.Exception{}, errors.New("endDate must not precede startDate")
	case changes.Title != nil && (strings.TrimSpace(*changes.Title) == "" || utf8.RuneCountInString(*changes.Title) > maxImportedTitle):
		return "", sources.Exception{}, fmt.Errorf("title must be 1 to %d characters", maxImportedTitle)
	case changes.Description.value != nil && utf8.RuneCountInString(*changes.Description.value) > maxImportedNotes:
		return "", sources.Exception{}, fmt.Errorf("description must be at most %d characters", maxImportedNotes)
	case changes.Location.value != nil && utf8.RuneCountInString(*changes.Location.value) > maxImportedLocation:
		return "", sources.Exception{}, fmt.Errorf("location must be at most %d characters", maxImportedLocation)
	case changes.Priority != nil && !changes.Priority.Valid():
		return "", sources.Exception{}, errors.New("priority must be low, medium, or high")
	}
	if changes.Reminders != nil {
		if err := validateReminders(*changes.Reminders, "an occurrence takes at most %d reminders of its own"); err != nil {
			return "", sources.Exception{}, err
		}
	}
	if changes.empty() && !cancelled {
		return "", sources.Exception{}, errors.New("an exception must skip the occurrence (cancelled) or change something")
	}
	out := sources.Exception{Cancelled: cancelled}
	out.Changes.Title = changes.Title
	if changes.Description.set {
		out.Changes.Description = &sources.Text{Value: nonEmpty(changes.Description.value)}
	}
	if changes.Location.set {
		out.Changes.Location = &sources.Text{Value: nonEmpty(changes.Location.value)}
	}
	if changes.StartAt != nil {
		start, end := changes.StartAt.UTC(), changes.EndAt.UTC()
		out.Changes.StartAt, out.Changes.EndAt = &start, &end
	}
	if changes.StartDate != nil {
		first, last := changes.StartDate.Time, changes.EndDate.Time
		out.Changes.StartDate, out.Changes.EndDate = &first, &last
	}
	if changes.Reminders != nil {
		list := sourceReminders(*changes.Reminders)
		out.Changes.Reminders = &list
	}
	out.Changes.UseDefaultReminders = changes.UseDefaultReminders
	out.Changes.Priority = priorityColumn(changes.Priority)
	return key, out, nil
}

func mustJSON(value any) []byte {
	raw, err := json.Marshal(value)
	if err != nil {
		return []byte("{}")
	}
	return raw
}

func unhandledChanges() error {
	return httpserver.NewError(fiber.StatusConflict, "unhandled_changes",
		"the change feed has changes to this source's events after feedCursor that the integration has not handled; handle them (write them to the provider), then sync again")
}

// SyncIntegrationSource writes one batch of an integration source's events
// in one transaction holding the source.
func (h *Handler) SyncIntegrationSource(c fiber.Ctx, integration, externalID string) error {
	if err := authorizeIntegration(c, integration); err != nil {
		return err
	}
	if err := checkExternalID(externalID); err != nil {
		return err
	}
	batch, err := decodeSyncBatch(c.Body(), h.ownerLocation)
	if err != nil {
		return err
	}
	now := h.now().UTC()
	var counts sources.Counts
	err = h.inTx(c, func(queries *store.Queries) error {
		ctx := c.Context()
		source, err := queries.LockIntegrationSource(ctx, store.LockIntegrationSourceParams{Integration: integration, ExternalID: externalID})
		if errors.Is(err, pgx.ErrNoRows) {
			return integrationSourceNotFound()
		}
		if err != nil {
			return internal(err)
		}
		if !source.Enabled {
			return httpserver.NewError(fiber.StatusConflict, "source_disabled", "the owner disabled this source; its events stay as they are until it is enabled")
		}
		if batch.feedCursor != nil {
			// Hold the source's events first: an edit still being made
			// either committed (and is seen below) or waits for this sync.
			if _, err := queries.ListSourceEvents(ctx, source.ID); err != nil {
				return internal(err)
			}
			unhandled, err := queries.HasUnhandledChanges(ctx, store.HasUnhandledChangesParams{After: *batch.feedCursor, SourceID: source.ID, Actor: requestActor(c)})
			if err != nil {
				return internal(err)
			}
			if unhandled {
				return unhandledChanges()
			}
		}
		for _, change := range batch.rekeys {
			if _, err := queries.RekeyImportedEvent(ctx, store.RekeyImportedEventParams{ExternalUid: change.uid, SourceID: source.ID, PreviousUid: change.previous}); err != nil {
				return internal(err)
			}
		}
		rules := batch.rules(now, h.ownerLocation)
		if batch.mode == generated.SyncIntegrationSourceRequestModeSnapshot {
			counts, err = sources.ReconcileSnapshot(ctx, queries, source, batch.items, rules, batch.emptyGuard, now, h.ownerLocation)
		} else {
			counts, err = sources.ReconcileDelta(ctx, queries, source, batch.items, batch.deleted, rules, now)
		}
		if errors.Is(err, sources.ErrEmptySnapshot) {
			return httpserver.NewError(fiber.StatusConflict, "empty_snapshot", sources.ErrEmptySnapshot.Error())
		}
		if err != nil {
			return internal(err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return c.JSON(generated.SyncIntegrationSourceResult{
		Mode: generated.SyncIntegrationSourceResultMode(batch.mode), Received: counts.Imported, Inserted: counts.Inserted,
		Updated: counts.Updated, Removed: counts.Removed, Restored: counts.Restored, Unchanged: counts.Unchanged,
	})
}
