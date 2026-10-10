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

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/services/calendar/internal/api/generated"
	store "github.com/GAMch1k/life-os/services/calendar/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/calendar/internal/sources"
)

// Source configuration limits (mirrored in the OpenAPI contract).
const (
	maxSourceName   = 100
	maxSourceURL    = 2048
	maxFilterTerms  = 50
	maxFilterLength = 100
)

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
		name      string
		reminders []generated.Reminder
		priority  generated.EventPriority
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
					summaries[uuid(row.ID)] = summary{name: row.Name, reminders: remindersOrNone(reminders), priority: generated.EventPriority(row.DefaultPriority)}
				}
			}
			if found, ok := summaries[*item.Source.SourceId]; ok {
				name := found.name
				item.Source.Name = &name
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
	return nil
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
	if errors.Is(err, pgx.ErrNoRows) {
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

// sourceConfig is a source's editable configuration.
type sourceConfig struct {
	Name             string
	URL              string
	Enabled          bool
	Include, Exclude []string
	DefaultReminders *[]generated.Reminder
	DefaultPriority  generated.EventPriority
}

// validate checks the configuration and normalizes it (trimmed name and
// terms, webcal:// as https://).
func (h *Handler) validate(config *sourceConfig) error {
	config.Name = strings.TrimSpace(config.Name)
	if config.Name == "" || utf8.RuneCountInString(config.Name) > maxSourceName {
		return fmt.Errorf("name must be 1 to %d characters", maxSourceName)
	}
	if len(config.URL) > maxSourceURL {
		return fmt.Errorf("url must be at most %d characters", maxSourceURL)
	}
	normalized, err := h.fetcher.NormalizeURL(config.URL)
	if err != nil {
		return err
	}
	config.URL = normalized
	for label, terms := range map[string]*[]string{"filter.include": &config.Include, "filter.exclude": &config.Exclude} {
		if len(*terms) > maxFilterTerms {
			return fmt.Errorf("%s takes at most %d terms", label, maxFilterTerms)
		}
		cleaned := make([]string, 0, len(*terms))
		for _, term := range *terms {
			term = strings.TrimSpace(term)
			if term == "" || utf8.RuneCountInString(term) > maxFilterLength {
				return fmt.Errorf("%s terms must be 1 to %d characters", label, maxFilterLength)
			}
			cleaned = append(cleaned, term)
		}
		*terms = cleaned
	}
	if !config.DefaultPriority.Valid() {
		return errors.New("defaultPriority must be low, medium, or high")
	}
	if config.DefaultReminders != nil {
		return validateReminders(*config.DefaultReminders, "a source takes at most %d default reminders")
	}
	return nil
}

func (h *Handler) CreateSource(c fiber.Ctx) error {
	var body generated.CreateSourceRequest
	if err := c.Bind().Body(&body); err != nil {
		return badRequest("invalid source request")
	}
	if body.Kind != nil && *body.Kind != generated.CalendarSourceKindIcs {
		return badRequest("kind must be ics")
	}
	config := sourceConfig{
		Name: body.Name, URL: body.Url, Enabled: body.Enabled == nil || *body.Enabled, Include: []string{}, Exclude: []string{},
		DefaultReminders: body.DefaultReminders, DefaultPriority: priorityOr(body.DefaultPriority, defaultPriority),
	}
	if body.Filter != nil {
		config.Include, config.Exclude = orEmpty(body.Filter.Include), orEmpty(body.Filter.Exclude)
	}
	if err := h.validate(&config); err != nil {
		return badRequest(err.Error())
	}
	reminders, err := encodeReminders(config.DefaultReminders)
	if err != nil {
		return internal(err)
	}
	now := h.now().UTC()
	row, err := h.queries.CreateSource(c.Context(), store.CreateSourceParams{
		Kind: string(generated.CalendarSourceKindIcs), Name: config.Name, Url: config.URL, Enabled: config.Enabled,
		FilterInclude: config.Include, FilterExclude: config.Exclude, DefaultReminders: reminders, DefaultPriority: priorityColumn(&config.DefaultPriority),
		// The first import is due right away.
		NextRefreshAt: timestamp(&now),
	})
	if err != nil {
		return internal(err)
	}
	if config.Enabled {
		h.wake()
	}
	return h.respondSource(c, fiber.StatusCreated, row)
}

// updatableSourceFields are the UpdateSourceRequest properties; only
// defaultReminders may be null.
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
		if errors.Is(err, pgx.ErrNoRows) {
			return sourceNotFound()
		}
		if err != nil {
			return internal(err)
		}
		reminders, err := decodeReminders(current.DefaultReminders)
		if err != nil {
			return internal(err)
		}
		next := sourceConfig{
			Name: current.Name, URL: current.Url, Enabled: current.Enabled, Include: current.FilterInclude, Exclude: current.FilterExclude,
			DefaultReminders: reminders, DefaultPriority: generated.EventPriority(current.DefaultPriority),
		}
		if present["name"] {
			next.Name = *body.Name
		}
		if present["url"] {
			next.URL = *body.Url
		}
		if present["enabled"] {
			next.Enabled = *body.Enabled
		}
		if present["filter"] {
			next.Include, next.Exclude = orEmpty(body.Filter.Include), orEmpty(body.Filter.Exclude)
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
		// A new URL or filter needs a full download and reconcile; so does a
		// source that is switched back on. Either is due right away.
		reconfigured := next.URL != current.Url || !equalTerms(next.Include, current.FilterInclude) || !equalTerms(next.Exclude, current.FilterExclude)
		params := store.UpdateSourceParams{
			ID: current.ID, ExpectedUpdatedAt: current.UpdatedAt, Name: next.Name, Url: next.URL, Enabled: next.Enabled,
			FilterInclude: next.Include, FilterExclude: next.Exclude, DefaultReminders: encoded, DefaultPriority: priorityColumn(&next.DefaultPriority),
			ResetValidators: reconfigured,
		}
		due := next.Enabled && (reconfigured || !current.Enabled)
		if due {
			now := h.now().UTC()
			params.NextRefreshAt = timestamp(&now)
		}
		row, err := h.queries.UpdateSource(c.Context(), params)
		if errors.Is(err, pgx.ErrNoRows) {
			continue // changed or deleted meanwhile: read it again
		}
		if err != nil {
			return internal(err)
		}
		if due {
			h.wake()
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

// RefreshSource runs a refresh now and returns the source with its outcome.
func (h *Handler) RefreshSource(c fiber.Ctx, id types.UUID) error {
	if h.refresher == nil {
		return internal(errors.New("source refresh is not configured"))
	}
	row, err := h.refresher.Refresh(c.Context(), id, sources.Trigger{Manual: true, Trace: activity.FromRequest(c)})
	switch {
	case errors.Is(err, sources.ErrNotFound):
		return sourceNotFound()
	case errors.Is(err, sources.ErrDisabled):
		return httpserver.NewError(fiber.StatusConflict, "source_disabled", "the source is disabled; enable it to refresh it")
	case errors.Is(err, sources.ErrInProgress):
		return httpserver.NewError(fiber.StatusConflict, "refresh_in_progress", "a refresh of this source is already running; try again shortly")
	case errors.Is(err, sources.ErrSuperseded):
		return httpserver.NewError(fiber.StatusConflict, "conflict", sources.ErrSuperseded.Error())
	case err != nil:
		return internal(err)
	}
	return h.respondSource(c, fiber.StatusOK, row)
}

// source renders a stored source.
func (h *Handler) source(row store.CalendarSource) (generated.CalendarSource, error) {
	reminders, err := decodeReminders(row.DefaultReminders)
	if err != nil {
		return generated.CalendarSource{}, err
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
		Id: uuid(row.ID), Kind: generated.CalendarSourceKind(row.Kind), Name: row.Name, Url: row.Url, Enabled: row.Enabled, Preset: row.Preset,
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

func equalTerms(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}
