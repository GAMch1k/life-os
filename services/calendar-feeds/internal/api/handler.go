// Package api serves the calendar-feeds HTTP API (api/openapi.yaml).
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gofiber/fiber/v3"
	googleuuid "github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/oapi-codegen/runtime/types"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/services/calendar-feeds/internal/api/generated"
	"github.com/GAMch1k/life-os/services/calendar-feeds/internal/calendar"
	"github.com/GAMch1k/life-os/services/calendar-feeds/internal/feeds"
	store "github.com/GAMch1k/life-os/services/calendar-feeds/internal/repository/generated"
)

// Feed configuration limits (mirrored in the OpenAPI contract).
const (
	maxFeedName      = 100
	maxFeedURL       = 2048
	maxFilterTerms   = 50
	maxFilterLength  = 100
	maxReminders     = 10
	updateAttempts   = 3
	calendarDeadline = 30 * time.Second
)

// Handler serves the feeds API. The refresher holds the feeds' database,
// the fetcher (which also validates feed URLs), and the Calendar client.
type Handler struct {
	queries   *store.Queries
	refresher *feeds.Refresher
	// wake tells the scheduler a feed needs a refresh now.
	wake func()
	now  func() time.Time
}

var _ generated.ServerInterface = (*Handler)(nil)

// NewHandler serves the feeds of refresher; wake, if not nil, prompts the
// scheduler.
func NewHandler(queries *store.Queries, refresher *feeds.Refresher, wake func()) *Handler {
	if wake == nil {
		wake = func() {}
	}
	return &Handler{queries: queries, refresher: refresher, wake: wake, now: refresher.Now}
}

// Register mounts the API on router. Every write must say who makes it in
// X-Life-OS-Actor; activity.RequireActor rejects it otherwise.
func Register(router fiber.Router, handler *Handler) {
	router.Use(activity.RequireActor())
	generated.RegisterHandlers(router, handler)
}

func badRequest(message string) error {
	return httpserver.NewError(fiber.StatusBadRequest, "bad_request", message)
}

func internal(err error) error {
	return httpserver.WrapError(err, fiber.StatusInternalServerError, "internal_error", "calendar feeds query failed")
}

func feedNotFound() error {
	return httpserver.NewError(fiber.StatusNotFound, "not_found", "feed not found")
}

// calendarUnavailable is a 502 for a call to Calendar that failed; Calendar's
// own refusal of the request's values is the caller's 400.
func calendarFailure(err error) error {
	var answer *calendar.StatusError
	if errors.As(err, &answer) && answer.Status == http.StatusBadRequest && answer.Message != "" {
		return badRequest(answer.Message)
	}
	message := "Calendar could not be reached; try again shortly"
	if errors.As(err, &answer) {
		message = fmt.Sprintf("Calendar refused the request (%d %s): %s", answer.Status, answer.Code, answer.Message)
	}
	return httpserver.WrapError(err, fiber.StatusBadGateway, "calendar_unavailable", message)
}

// calendarContext carries the request's trace to Calendar, bounded.
func calendarContext(c fiber.Ctx) (context.Context, context.CancelFunc) {
	ctx := activity.WithTrace(context.WithoutCancel(c.Context()), activity.FromRequest(c))
	return context.WithTimeout(ctx, calendarDeadline)
}

func pgUUID(value types.UUID) pgtype.UUID { return pgtype.UUID{Bytes: [16]byte(value), Valid: true} }

func nullableTime(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	instant := value.Time.UTC()
	return &instant
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

func (h *Handler) ListFeeds(c fiber.Ctx) error {
	rows, err := h.queries.ListFeeds(c.Context())
	if err != nil {
		return internal(err)
	}
	result := generated.FeedList{Items: make([]generated.Feed, 0, len(rows))}
	for _, row := range rows {
		item, err := h.feed(row)
		if err != nil {
			return internal(err)
		}
		result.Items = append(result.Items, item)
	}
	return c.JSON(result)
}

func (h *Handler) GetFeed(c fiber.Ctx, id types.UUID) error {
	row, err := h.queries.GetFeed(c.Context(), pgUUID(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return feedNotFound()
	}
	if err != nil {
		return internal(err)
	}
	return h.respond(c, fiber.StatusOK, row)
}

func (h *Handler) respond(c fiber.Ctx, status int, row store.Feed) error {
	item, err := h.feed(row)
	if err != nil {
		return internal(err)
	}
	return c.Status(status).JSON(item)
}

// feedConfig is a feed's editable configuration.
type feedConfig struct {
	URL              string
	Include, Exclude []string
}

// validate checks the configuration and normalizes it (trimmed terms,
// webcal:// as https://).
func (h *Handler) validate(config *feedConfig) error {
	if len(config.URL) > maxFeedURL {
		return fmt.Errorf("url must be at most %d characters", maxFeedURL)
	}
	normalized, err := h.refresher.Fetcher().NormalizeURL(config.URL)
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
	return nil
}

func (h *Handler) CreateFeed(c fiber.Ctx) error {
	var present map[string]json.RawMessage
	if err := json.Unmarshal(c.Body(), &present); err != nil || present == nil {
		return badRequest("invalid feed request")
	}
	var body generated.CreateFeedRequest
	if err := json.Unmarshal(c.Body(), &body); err != nil {
		return badRequest("invalid feed request")
	}
	name := strings.TrimSpace(body.Name)
	if name == "" || utf8.RuneCountInString(name) > maxFeedName {
		return badRequest(fmt.Sprintf("name must be 1 to %d characters", maxFeedName))
	}
	config := feedConfig{URL: body.Url, Include: []string{}, Exclude: []string{}}
	if body.Filter != nil {
		config.Include, config.Exclude = orEmpty(body.Filter.Include), orEmpty(body.Filter.Exclude)
	}
	if err := h.validate(&config); err != nil {
		return badRequest(err.Error())
	}
	// The source's default reminders and priority are the owner's settings
	// in Calendar, sent once with the registration; Calendar validates them.
	registration := calendar.Registration{Name: name, Provider: feeds.ProviderICS, ReadOnly: true}
	if raw, ok := present["defaultReminders"]; ok {
		if body.DefaultReminders != nil && len(*body.DefaultReminders) > maxReminders {
			return badRequest(fmt.Sprintf("a source takes at most %d default reminders", maxReminders))
		}
		registration.DefaultReminders = raw
	}
	if body.DefaultPriority != nil {
		if !body.DefaultPriority.Valid() {
			return badRequest("defaultPriority must be low, medium, or high")
		}
		registration.DefaultPriority = string(*body.DefaultPriority)
	}

	id := googleuuid.New()
	ctx, cancel := calendarContext(c)
	defer cancel()
	source, err := h.refresher.Calendar().RegisterSource(ctx, id.String(), registration)
	if err != nil {
		return calendarFailure(err)
	}
	sourceID, err := googleuuid.Parse(source.ID)
	if err != nil {
		h.forget(ctx, id)
		return calendarFailure(fmt.Errorf("Calendar returned the source id %q: %w", source.ID, err))
	}
	now := h.now().UTC()
	row, err := h.queries.CreateFeed(c.Context(), store.CreateFeedParams{
		ID: pgtype.UUID{Bytes: id, Valid: true}, SourceID: pgtype.UUID{Bytes: sourceID, Valid: true}, Name: source.Name,
		Url: config.URL, Enabled: source.Enabled, FilterInclude: config.Include, FilterExclude: config.Exclude,
		// The first import is due right away.
		NextRefreshAt: pgtype.Timestamptz{Time: now, Valid: true},
	})
	if err != nil {
		h.forget(ctx, id)
		return internal(err)
	}
	if row.Enabled {
		h.wake()
	}
	return h.respond(c, fiber.StatusCreated, row)
}

// forget removes a source Calendar registered for a feed that could not be
// stored (best effort).
func (h *Handler) forget(ctx context.Context, id googleuuid.UUID) {
	_ = h.refresher.Calendar().DeleteSource(ctx, id.String())
}

// updatableFeedFields are the UpdateFeedRequest properties.
var updatableFeedFields = map[string]bool{"url": true, "filter": true}

func (h *Handler) UpdateFeed(c fiber.Ctx, id types.UUID) error {
	var properties map[string]json.RawMessage
	if err := json.Unmarshal(c.Body(), &properties); err != nil || properties == nil {
		return badRequest("invalid feed request")
	}
	var body generated.UpdateFeedRequest
	if err := json.Unmarshal(c.Body(), &body); err != nil {
		return badRequest("invalid feed request")
	}
	present := map[string]bool{}
	for name, raw := range properties {
		if !updatableFeedFields[name] {
			continue
		}
		if string(raw) == "null" {
			return badRequest(name + " cannot be null")
		}
		present[name] = true
	}
	if len(present) == 0 {
		return badRequest("the update names no feed field (url, filter); the name, enabled, default reminders, and default priority are changed in Calendar (/v1/sources)")
	}
	for range updateAttempts {
		current, err := h.queries.GetFeed(c.Context(), pgUUID(id))
		if errors.Is(err, pgx.ErrNoRows) {
			return feedNotFound()
		}
		if err != nil {
			return internal(err)
		}
		next := feedConfig{URL: current.Url, Include: current.FilterInclude, Exclude: current.FilterExclude}
		if present["url"] {
			next.URL = *body.Url
		}
		if present["filter"] {
			next.Include, next.Exclude = orEmpty(body.Filter.Include), orEmpty(body.Filter.Exclude)
		}
		if err := h.validate(&next); err != nil {
			return badRequest(err.Error())
		}
		// A new URL or filter needs a full download and sync, right away.
		reconfigured := next.URL != current.Url || !equalTerms(next.Include, current.FilterInclude) || !equalTerms(next.Exclude, current.FilterExclude)
		params := store.UpdateFeedConfigParams{
			ID: current.ID, ExpectedUpdatedAt: current.UpdatedAt, Url: next.URL,
			FilterInclude: next.Include, FilterExclude: next.Exclude, ResetValidators: reconfigured,
		}
		due := reconfigured && current.Enabled
		if due {
			params.NextRefreshAt = pgtype.Timestamptz{Time: h.now().UTC(), Valid: true}
		}
		row, err := h.queries.UpdateFeedConfig(c.Context(), params)
		if errors.Is(err, pgx.ErrNoRows) {
			continue // changed or deleted meanwhile: read it again
		}
		if err != nil {
			return internal(err)
		}
		if due {
			h.wake()
		}
		return h.respond(c, fiber.StatusOK, row)
	}
	return httpserver.NewError(fiber.StatusConflict, "conflict", "the feed kept changing; retry the update")
}

func (h *Handler) DeleteFeed(c fiber.Ctx, id types.UUID) error {
	current, err := h.queries.GetFeed(c.Context(), pgUUID(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return feedNotFound()
	}
	if err != nil {
		return internal(err)
	}
	// Calendar first: its source and events go with the feed, and a feed
	// stays while Calendar cannot be reached.
	ctx, cancel := calendarContext(c)
	defer cancel()
	if err := h.refresher.Calendar().DeleteSource(ctx, googleuuid.UUID(current.ID.Bytes).String()); err != nil && !calendar.IsNotFound(err) {
		return calendarFailure(err)
	}
	if _, err := h.queries.DeleteFeed(c.Context(), current.ID); err != nil {
		return internal(err)
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// RefreshFeed runs a refresh now and returns the feed with its outcome.
// Calendar is asked first whether the owner enabled the source (and what it
// is called now), so "Refresh now" right after enabling it works.
func (h *Handler) RefreshFeed(c fiber.Ctx, id types.UUID) error {
	current, err := h.queries.GetFeed(c.Context(), pgUUID(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return feedNotFound()
	}
	if err != nil {
		return internal(err)
	}
	trace := activity.FromRequest(c)
	if current.SourceID.Valid {
		ctx, cancel := calendarContext(c)
		err := h.refresher.MirrorOne(ctx, current)
		cancel()
		if errors.Is(err, feeds.ErrNotFound) {
			return feedNotFound()
		}
		// Calendar unreachable: the refresh records the failure.
	}
	row, err := h.refresher.Refresh(activity.WithTrace(c.Context(), trace), id, feeds.Trigger{Manual: true, Trace: trace})
	switch {
	case errors.Is(err, feeds.ErrNotFound):
		return feedNotFound()
	case errors.Is(err, feeds.ErrDisabled):
		return httpserver.NewError(fiber.StatusConflict, "source_disabled", "the feed's source is disabled in Calendar; enable it there to refresh it")
	case errors.Is(err, feeds.ErrInProgress):
		return httpserver.NewError(fiber.StatusConflict, "refresh_in_progress", "a refresh of this feed is already running; try again shortly")
	case errors.Is(err, feeds.ErrSuperseded):
		return httpserver.NewError(fiber.StatusConflict, "conflict", feeds.ErrSuperseded.Error())
	case err != nil:
		return internal(err)
	}
	return h.respond(c, fiber.StatusOK, row)
}

// feed renders a stored feed.
func (h *Handler) feed(row store.Feed) (generated.Feed, error) {
	refresh := generated.FeedRefreshState{
		NextRefreshAt: nullableTime(row.NextRefreshAt), LastAttemptAt: nullableTime(row.LastAttemptAt), LastSuccessAt: nullableTime(row.LastSuccessAt),
		LastError: row.LastError, ConsecutiveFailures: int(row.ConsecutiveFailures), Etag: row.Etag, LastModified: row.LastModified,
		Refreshing: row.RefreshingUntil.Valid && row.RefreshingUntil.Time.After(h.now()),
	}
	if row.LastOutcome != nil {
		outcome := generated.FeedRefreshOutcome(*row.LastOutcome)
		refresh.LastOutcome = &outcome
	}
	if row.LastCounts != nil {
		var counts feeds.Counts
		if err := json.Unmarshal(row.LastCounts, &counts); err != nil {
			return generated.Feed{}, fmt.Errorf("decode refresh counts: %w", err)
		}
		skipped := counts.Skipped
		refresh.LastCounts = &generated.FeedRefreshCounts{
			FeedEvents: counts.FeedEvents, Imported: counts.Imported, Inserted: counts.Inserted, Updated: counts.Updated,
			Removed: counts.Removed, Restored: counts.Restored, Unchanged: counts.Unchanged, Skipped: &skipped,
		}
	}
	item := generated.Feed{
		Id: types.UUID(row.ID.Bytes), Name: row.Name, Url: row.Url, Enabled: row.Enabled, Preset: row.Preset,
		Filter:  generated.FeedFilter{Include: orEmpty(row.FilterInclude), Exclude: orEmpty(row.FilterExclude)},
		Refresh: refresh, CreatedAt: row.CreatedAt.Time.UTC(), UpdatedAt: row.UpdatedAt.Time.UTC(),
	}
	if row.SourceID.Valid {
		sourceID := types.UUID(row.SourceID.Bytes)
		item.SourceId = &sourceID
	}
	return item, nil
}
