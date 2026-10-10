package sources

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	store "github.com/GAMch1k/life-os/services/calendar/internal/repository/generated"
)

// Reconciling a source's stored events with the items its integration
// gives through the integration API (sync.go): inside the caller's
// transaction, which holds the source row. A feed's sync (window
// upcomingOnly) reconciles with FeedRules, a synced calendar's (window
// start) with SyncRules, any other with AuthoritativeRules.

// reconcileStored reconciles existing, stored events of source (held by the
// transaction of queries), with items.
func reconcileStored(ctx context.Context, queries *store.Queries, source store.CalendarSource, existing []store.Event, items []Item, rules Rules, now time.Time) (Counts, error) {
	var series []pgtype.UUID
	for _, event := range existing {
		if event.RecurrenceFrequency != nil {
			series = append(series, event.ID)
		}
	}
	exceptions := map[[16]byte][]store.EventException{}
	if len(series) > 0 {
		rows, err := queries.ListEventExceptions(ctx, series)
		if err != nil {
			return Counts{}, err
		}
		for _, row := range rows {
			exceptions[row.EventID.Bytes] = append(exceptions[row.EventID.Bytes], row)
		}
	}
	rules.defaultPriority = source.DefaultPriority
	changes := reconcileItems(existing, exceptions, items, rules)
	if err := apply(ctx, queries, source, changes, now); err != nil {
		return Counts{}, err
	}
	return changes.counts, nil
}

// apply writes a plan inside the refresh transaction.
func apply(ctx context.Context, queries *store.Queries, source store.CalendarSource, changes plan, now time.Time) error {
	for _, item := range changes.inserts {
		fields := importedFields(item, pgtype.Timestamptz{}, now)
		uid := item.ExternalUID
		params := store.InsertImportedEventParams{
			Title: fields.title, Description: item.Description, Location: item.Location, AllDay: item.AllDay,
			StartAt: fields.startAt, EndAt: fields.endAt, StartDate: fields.startDate, EndDate: fields.endDate, TimeZone: item.TimeZone,
			RecurrenceFrequency: fields.rule.frequency, RecurrenceInterval: fields.rule.interval, RecurrenceUntil: fields.rule.until,
			RecurrenceByWeekday: fields.rule.byWeekday, RecurrenceCount: fields.rule.count,
			Reminders: []byte("[]"), UseDefaultReminders: true,
			Status: fields.status, CancelledAt: fields.cancelledAt, SourceKind: source.Kind, SourceID: source.ID, ExternalUid: &uid,
			Priority: ownPriority(item, source.DefaultPriority), ReadOnlyReason: item.ReadOnlyReason,
		}
		if item.Reminders != nil {
			own, whole, err := item.Reminders.encode()
			if err != nil {
				return err
			}
			params.Reminders, params.UseDefaultReminders, params.SourceReminders = own, item.Reminders.UseDefault, whole
		}
		id, err := queries.InsertImportedEvent(ctx, params)
		if err != nil {
			return fmt.Errorf("insert imported event: %w", err)
		}
		writes := make(map[string]exceptionWrite, len(item.Exceptions))
		for key, exception := range item.Exceptions {
			writes[key] = exceptionWrite{exception: exception}
		}
		if err := writeExceptions(ctx, queries, id, writes, nil, now); err != nil {
			return err
		}
	}
	for _, change := range changes.updates {
		fields := importedFields(change.item, change.cancelledAt, now)
		params := store.UpdateImportedEventParams{
			ID: change.id, SourceID: source.ID, Title: fields.title, Description: change.item.Description, Location: change.item.Location,
			AllDay: change.item.AllDay, StartAt: fields.startAt, EndAt: fields.endAt, StartDate: fields.startDate, EndDate: fields.endDate,
			TimeZone: change.item.TimeZone, Status: fields.status, CancelledAt: fields.cancelledAt,
			RecurrenceFrequency: fields.rule.frequency, RecurrenceInterval: fields.rule.interval, RecurrenceUntil: fields.rule.until,
			RecurrenceByWeekday: fields.rule.byWeekday, RecurrenceCount: fields.rule.count,
			SetPriority: change.item.Priority != nil, Priority: ownPriority(change.item, source.DefaultPriority),
			ReadOnlyReason: change.item.ReadOnlyReason,
		}
		if change.item.Reminders != nil {
			own, whole, err := change.item.Reminders.encode()
			if err != nil {
				return err
			}
			params.SourceReminders = whole
			if change.ownReminders {
				useDefault := change.item.Reminders.UseDefault
				params.Reminders, params.UseDefaultReminders = own, &useDefault
			}
		}
		if err := queries.UpdateImportedEvent(ctx, params); err != nil {
			return fmt.Errorf("update imported event: %w", err)
		}
		if err := writeExceptions(ctx, queries, change.id, change.exceptions, change.dropExceptions, now); err != nil {
			return err
		}
	}
	for _, id := range changes.removals {
		if err := queries.MarkImportedEventRemoved(ctx, store.MarkImportedEventRemovedParams{ID: id, SourceID: source.ID, Now: at(now)}); err != nil {
			return fmt.Errorf("mark imported event removed: %w", err)
		}
	}
	return nil
}

// writeExceptions stores a series' occurrence exceptions and deletes drops.
func writeExceptions(ctx context.Context, queries *store.Queries, id pgtype.UUID, writes map[string]exceptionWrite, drops []string, now time.Time) error {
	if len(drops) > 0 {
		if _, err := queries.DeleteEventExceptions(ctx, store.DeleteEventExceptionsParams{EventID: id, RecurrenceIds: drops}); err != nil {
			return fmt.Errorf("delete occurrence exceptions: %w", err)
		}
	}
	keys := make([]string, 0, len(writes))
	for key := range writes {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		write := writes[key]
		changes, err := write.exception.Changes.JSON()
		if err != nil {
			return err
		}
		var cancelledAt pgtype.Timestamptz
		if write.exception.Cancelled {
			cancelledAt = write.cancelledAt
			if !cancelledAt.Valid {
				cancelledAt = at(now)
			}
		}
		if _, err := queries.UpsertEventException(ctx, store.UpsertEventExceptionParams{EventID: id, RecurrenceID: key, Changes: changes, CancelledAt: cancelledAt}); err != nil {
			return fmt.Errorf("write occurrence exception: %w", err)
		}
	}
	return nil
}

type fields struct {
	title              string
	startAt, endAt     pgtype.Timestamptz
	startDate, endDate pgtype.Date
	status             string
	cancelledAt        pgtype.Timestamptz
	rule               ruleColumns
}

// ruleColumns are a series' stored rule; all null for a single event.
type ruleColumns struct {
	frequency *string
	interval  *int32
	until     pgtype.Timestamptz
	byWeekday []string
	count     *int32
}

func importedFields(item Item, keepCancelledAt pgtype.Timestamptz, now time.Time) fields {
	out := fields{title: item.Title, status: "active"}
	if item.AllDay {
		out.startDate = pgtype.Date{Time: item.StartDate, Valid: true}
		out.endDate = pgtype.Date{Time: item.EndDate, Valid: true}
	} else {
		out.startAt, out.endAt = at(item.StartAt), at(item.EndAt)
	}
	if item.Cancelled {
		out.status, out.cancelledAt = "cancelled", keepCancelledAt
		if !out.cancelledAt.Valid {
			out.cancelledAt = at(now)
		}
	}
	if rule := item.Recurrence; rule != nil {
		frequency, interval := rule.Frequency, int32(rule.Interval)
		out.rule = ruleColumns{frequency: &frequency, interval: &interval, byWeekday: rule.ByWeekday}
		if rule.Until != nil {
			out.rule.until = at(*rule.Until)
		}
		if rule.Count > 0 {
			count := int32(rule.Count)
			out.rule.count = &count
		}
	}
	return out
}

func at(value time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: value, Valid: true} }
