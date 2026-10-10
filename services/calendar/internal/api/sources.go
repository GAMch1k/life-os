package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5"
	"github.com/oapi-codegen/runtime/types"

	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/services/calendar/internal/api/generated"
	store "github.com/GAMch1k/life-os/services/calendar/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/calendar/internal/sources"
)

// maxSourceName is the longest name the owner gives a source of Calendar's
// own (mirrored in the OpenAPI contract).
const maxSourceName = 100

// resolveSources completes each item from its source and the owner's
// settings: an imported event's source name, its defaultReminders (its
// source's defaults, or for a Life OS event the owner's defaults for timed
// or all-day events), its effectiveReminders (the defaults when it uses
// them, then its own, each reminder once), its defaultPriority (its
// source's, else defaultPriority), and its priority when it has none of its
// own (see event). Sources and settings are read only when an item needs
// them.
func (h *Handler) resolveSources(ctx context.Context, items []generated.CalendarEvent) error {
	type summary struct {
		name                  string
		reminders             []generated.Reminder
		priority              generated.EventPriority
		integration, provider *string
		readOnly              bool
	}
	var summaries map[types.UUID]summary
	var owner *ownerDefaults
	for index := range items {
		item := &items[index]
		defaults := []generated.Reminder{}
		priority := defaultPriority
		switch {
		case item.Source.Kind == generated.EventSourceKindManual:
			if owner == nil {
				loaded, err := h.ownerDefaults(ctx)
				if err != nil {
					return err
				}
				owner = &loaded
			}
			defaults = owner.timed
			if item.AllDay {
				defaults = owner.allDay
			}
		case item.Source.SourceId != nil:
			if summaries == nil {
				rows, err := h.queries.ListSourceSummaries(ctx)
				if err != nil {
					return err
				}
				summaries = make(map[types.UUID]summary, len(rows))
				for _, row := range rows {
					reminders, err := decodeReminders(row.DefaultReminders)
					if err != nil {
						return err
					}
					summaries[uuid(row.ID)] = summary{
						name: row.Name, reminders: remindersOrNone(reminders), priority: generated.EventPriority(row.DefaultPriority),
						integration: row.Integration, provider: row.Provider, readOnly: row.ReadOnly,
					}
				}
			}
			if found, ok := summaries[*item.Source.SourceId]; ok {
				name, readOnly := found.name, found.readOnly
				item.Source.Name, item.Source.Integration, item.Source.Provider, item.Source.ReadOnly = &name, found.integration, found.provider, &readOnly
				defaults, priority = found.reminders, found.priority
			}
		}
		item.DefaultReminders = defaults
		item.EffectiveReminders = effectiveReminders(item.UseDefaultReminders, defaults, item.Reminders)
		item.DefaultPriority = priority
		if item.Priority == "" {
			item.Priority = priority
		}
	}
	return h.resolveEditable(ctx, items)
}

// effectiveReminders is every reminder that applies: the defaults when they
// are used, then the event's own, each reminder once.
func effectiveReminders(useDefaults bool, defaults, own []generated.Reminder) []generated.Reminder {
	effective := make([]generated.Reminder, 0, len(defaults)+len(own))
	add := func(reminder generated.Reminder) {
		for _, existing := range effective {
			if sameReminder(existing, reminder) {
				return
			}
		}
		effective = append(effective, reminder)
	}
	if useDefaults {
		for _, reminder := range defaults {
			add(reminder)
		}
	}
	for _, reminder := range own {
		add(reminder)
	}
	return effective
}

func sameReminder(a, b generated.Reminder) bool {
	sameInt := func(x, y *int) bool { return (x == nil && y == nil) || (x != nil && y != nil && *x == *y) }
	return a.Kind == b.Kind && sameInt(a.MinutesBefore, b.MinutesBefore) && sameInt(a.DaysBefore, b.DaysBefore) && sameString(a.LocalTime, b.LocalTime)
}

// managedByIntegration refuses what only the integration that syncs a feed
// does: changing its URL or filter (calendar-feeds keeps them, and refreshes
// the feed).
func managedByIntegration(name string) error {
	return httpserver.NewError(fiber.StatusConflict, "managed_by_integration",
		fmt.Sprintf("“%s” is synced by an integration, which keeps its URL and filter and refreshes it (feeds: the calendar-feeds service); its name, default reminders, default priority, and enabled can be changed here", name))
}

// listedSource reports whether row is served under /v1/sources: a feed or
// a source an integration syncs, not a legacy Google source of Calendar's
// former in-process sync (kind google; migration 000016 hands them over to
// the google-calendar integration).
func listedSource(row store.CalendarSource) bool {
	return row.Kind == string(generated.CalendarSourceKindIcs) || row.Kind == string(generated.CalendarSourceKindExternal)
}

func sourceNotFound() error {
	return httpserver.NewError(fiber.StatusNotFound, "not_found", "source not found")
}

func (h *Handler) ListSources(c fiber.Ctx) error {
	rows, err := h.queries.ListSources(c.Context())
	if err != nil {
		return internal(err)
	}
	result := generated.SourceList{Items: make([]generated.CalendarSource, 0, len(rows))}
	for _, row := range rows {
		item, err := h.source(row)
		if err != nil {
			return internal(err)
		}
		result.Items = append(result.Items, item)
	}
	return c.JSON(result)
}

func (h *Handler) GetSource(c fiber.Ctx, id types.UUID) error {
	row, err := h.queries.GetSource(c.Context(), pgUUID(id))
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !listedSource(row) {
		// A legacy in-process Google source is not served.
		return sourceNotFound()
	}
	if err != nil {
		return internal(err)
	}
	return h.respondSource(c, fiber.StatusOK, row)
}

func (h *Handler) respondSource(c fiber.Ctx, status int, row store.CalendarSource) error {
	item, err := h.source(row)
	if err != nil {
		return internal(err)
	}
	return c.Status(status).JSON(item)
}

// sourceConfig is the owner's settings of a source.
type sourceConfig struct {
	Name             string
	Enabled          bool
	DefaultReminders *[]generated.Reminder
	DefaultPriority  generated.EventPriority
	// Integration: an integration syncs the source and may name it longer.
	Integration bool
}

// validate checks the settings and normalizes them (a trimmed name).
func (h *Handler) validate(config *sourceConfig) error {
	config.Name = strings.TrimSpace(config.Name)
	limit := maxSourceName
	if config.Integration {
		limit = maxIntegrationName // as long as an integration may name it
	}
	if config.Name == "" || utf8.RuneCountInString(config.Name) > limit {
		return fmt.Errorf("name must be 1 to %d characters", limit)
	}
	if !config.DefaultPriority.Valid() {
		return errors.New("defaultPriority must be low, medium, or high")
	}
	if config.DefaultReminders != nil {
		return validateReminders(*config.DefaultReminders, "a source takes at most %d default reminders")
	}
	return nil
}

// updatableSourceFields are the UpdateSourceRequest properties; only
// defaultReminders may be null. url and filter belong to the integration
// that syncs the source (409 managed_by_integration).
var updatableSourceFields = map[string]bool{"name": true, "url": true, "enabled": true, "filter": true, "defaultReminders": true, "defaultPriority": true}

func (h *Handler) UpdateSource(c fiber.Ctx, id types.UUID) error {
	var properties map[string]json.RawMessage
	if err := json.Unmarshal(c.Body(), &properties); err != nil || properties == nil {
		return badRequest("invalid source request")
	}
	var body generated.UpdateSourceRequest
	if err := json.Unmarshal(c.Body(), &body); err != nil {
		return badRequest("invalid source request")
	}
	present := map[string]bool{}
	for name, raw := range properties {
		if !updatableSourceFields[name] {
			continue
		}
		if name != "defaultReminders" && string(raw) == "null" {
			return badRequest(name + " cannot be null")
		}
		present[name] = true
	}
	if len(present) == 0 {
		return badRequest("the update names no source field")
	}
	for range updateAttempts {
		current, err := h.queries.GetSource(c.Context(), pgUUID(id))
		if errors.Is(err, pgx.ErrNoRows) || err == nil && !listedSource(current) {
			return sourceNotFound()
		}
		if err != nil {
			return internal(err)
		}
		if present["url"] || present["filter"] {
			return managedByIntegration(current.Name)
		}
		reminders, err := decodeReminders(current.DefaultReminders)
		if err != nil {
			return internal(err)
		}
		next := sourceConfig{
			Name: current.Name, Enabled: current.Enabled, DefaultReminders: reminders,
			DefaultPriority: generated.EventPriority(current.DefaultPriority), Integration: !current.InProcess,
		}
		if present["name"] {
			next.Name = *body.Name
		}
		if present["enabled"] {
			next.Enabled = *body.Enabled
		}
		if present["defaultReminders"] {
			next.DefaultReminders = body.DefaultReminders
		}
		if present["defaultPriority"] {
			next.DefaultPriority = *body.DefaultPriority
		}
		if err := h.validate(&next); err != nil {
			return badRequest(err.Error())
		}
		encoded, err := encodeReminders(next.DefaultReminders)
		if err != nil {
			return internal(err)
		}
		row, err := h.queries.UpdateSource(c.Context(), store.UpdateSourceParams{
			ID: current.ID, ExpectedUpdatedAt: current.UpdatedAt, Name: next.Name, Enabled: next.Enabled,
			DefaultReminders: encoded, DefaultPriority: priorityColumn(&next.DefaultPriority),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			continue // changed or deleted meanwhile: read it again
		}
		if err != nil {
			return internal(err)
		}
		return h.respondSource(c, fiber.StatusOK, row)
	}
	return httpserver.NewError(fiber.StatusConflict, "conflict", "the source kept changing; retry the update")
}

func (h *Handler) DeleteSource(c fiber.Ctx, id types.UUID) error {
	affected, err := h.queries.DeleteSource(c.Context(), pgUUID(id))
	if err != nil {
		return internal(err)
	}
	if affected == 0 {
		return sourceNotFound()
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// source renders a stored source. A source an integration syncs shows the
// owner's settings only: its URL, filter, preset, and refresh state are the
// integration's (the legacy columns of a handed-over feed are left
// unread until P11-09 drops them).
func (h *Handler) source(row store.CalendarSource) (generated.CalendarSource, error) {
	reminders, err := decodeReminders(row.DefaultReminders)
	if err != nil {
		return generated.CalendarSource{}, err
	}
	if !row.InProcess {
		return generated.CalendarSource{
			Id: uuid(row.ID), Kind: generated.CalendarSourceKind(row.Kind), Name: row.Name, Enabled: row.Enabled,
			Integration: row.Integration, ExternalId: row.ExternalID, Provider: row.Provider, ReadOnly: &row.ReadOnly,
			Filter:           generated.SourceFilter{Include: []string{}, Exclude: []string{}},
			DefaultReminders: reminders, DefaultPriority: generated.EventPriority(row.DefaultPriority),
			CreatedAt: row.CreatedAt.Time.UTC(), UpdatedAt: row.UpdatedAt.Time.UTC(),
		}, nil
	}
	refresh := generated.SourceRefreshState{
		NextRefreshAt: nullableTime(row.NextRefreshAt), LastAttemptAt: nullableTime(row.LastAttemptAt), LastSuccessAt: nullableTime(row.LastSuccessAt),
		LastError: row.LastError, ConsecutiveFailures: int(row.ConsecutiveFailures), Etag: row.Etag, LastModified: row.LastModified,
		Refreshing: row.RefreshingUntil.Valid && row.RefreshingUntil.Time.After(h.now()),
	}
	if row.LastOutcome != nil {
		outcome := generated.SourceRefreshOutcome(*row.LastOutcome)
		refresh.LastOutcome = &outcome
	}
	if row.LastCounts != nil {
		var counts sources.Counts
		if err := json.Unmarshal(row.LastCounts, &counts); err != nil {
			return generated.CalendarSource{}, fmt.Errorf("decode refresh counts: %w", err)
		}
		skipped := counts.Skipped
		refresh.LastCounts = &generated.SourceRefreshCounts{
			FeedEvents: counts.FeedEvents, Imported: counts.Imported, Inserted: counts.Inserted, Updated: counts.Updated,
			Removed: counts.Removed, Restored: counts.Restored, Unchanged: counts.Unchanged, Skipped: &skipped,
		}
	}
	return generated.CalendarSource{
		Id: uuid(row.ID), Kind: generated.CalendarSourceKind(row.Kind), Name: row.Name, Url: deref(row.Url), Enabled: row.Enabled, Preset: row.Preset,
		Integration: row.Integration, ExternalId: row.ExternalID, Provider: row.Provider, ReadOnly: &row.ReadOnly,
		Filter:           generated.SourceFilter{Include: orEmpty(row.FilterInclude), Exclude: orEmpty(row.FilterExclude)},
		DefaultReminders: reminders, DefaultPriority: generated.EventPriority(row.DefaultPriority), Refresh: refresh,
		CreatedAt: row.CreatedAt.Time.UTC(), UpdatedAt: row.UpdatedAt.Time.UTC(),
	}, nil
}

func orEmpty(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
