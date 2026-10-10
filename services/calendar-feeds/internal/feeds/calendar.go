package feeds

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	googleuuid "github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/GAMch1k/life-os/services/calendar-feeds/internal/calendar"
	"github.com/GAMch1k/life-os/services/calendar-feeds/internal/ics"
	store "github.com/GAMch1k/life-os/services/calendar-feeds/internal/repository/generated"
)

// Talking to Calendar: a feed's source is registered under the feed's id
// (its externalId), its events are synced as snapshots, and what the owner
// sets in Calendar (the source's name, enabled) is mirrored here.

// Provider labels of a feed's source, shown with its events ("ZAW · ZAW").
const (
	ProviderICS = "ICS"
	ProviderZAW = "ZAW"
)

// Limits of Calendar's SourceEvent (services/calendar/api/openapi.yaml).
const (
	maxExternalUID   = 2048
	maxEventTitle    = 500
	maxEventLocation = 500
	maxEventNotes    = 10000
)

// errUnknownSources: Calendar has sources of this integration the feeds
// database does not know (such as after calendar_feeds_db was replaced);
// registering a new preset source now would import its events a second
// time.
var errUnknownSources = errors.New("Calendar has calendar-feeds sources this service does not know (was calendar_feeds_db replaced?); delete them in Calendar, or restore the feeds database, before new feeds are registered")

// SourceEvents turns feed items into the events Calendar's sync takes. An
// item Calendar would refuse (beyond its limits, such as an over-long UID)
// is left out and counted, so one odd event cannot fail the whole feed.
func SourceEvents(items []ics.Item) ([]calendar.Event, int) {
	events := make([]calendar.Event, 0, len(items))
	skipped := 0
	for _, item := range items {
		event, ok := sourceEvent(item)
		if !ok {
			skipped++
			continue
		}
		events = append(events, event)
	}
	return events, skipped
}

func sourceEvent(item ics.Item) (calendar.Event, bool) {
	uid := item.ExternalUID
	if uid == "" || len(uid) > maxExternalUID || !utf8.ValidString(uid) || strings.IndexFunc(uid, unicode.IsControl) >= 0 {
		return calendar.Event{}, false
	}
	title := item.Title
	if strings.TrimSpace(title) == "" || utf8.RuneCountInString(title) > maxEventTitle {
		return calendar.Event{}, false
	}
	event := calendar.Event{
		ExternalUID: uid, Title: title, AllDay: item.AllDay,
		Description: nonEmpty(item.Description), Location: nonEmpty(item.Location),
	}
	if event.Description != nil && utf8.RuneCountInString(*event.Description) > maxEventNotes ||
		event.Location != nil && utf8.RuneCountInString(*event.Location) > maxEventLocation {
		return calendar.Event{}, false
	}
	if item.Cancelled {
		event.Status = "cancelled"
	}
	if item.AllDay {
		if item.EndDate.Before(item.StartDate) {
			return calendar.Event{}, false
		}
		first, last := item.StartDate.Format(time.DateOnly), item.EndDate.Format(time.DateOnly)
		event.StartDate, event.EndDate = &first, &last
		return event, true
	}
	if !item.EndAt.After(item.StartAt) {
		return calendar.Event{}, false
	}
	start, end := item.StartAt.UTC(), item.EndAt.UTC()
	event.StartAt, event.EndAt = &start, &end
	if zone := nonEmpty(item.TimeZone); zone != nil {
		if *zone == "Local" || strings.TrimSpace(*zone) != *zone {
			zone = nil
		} else if _, err := time.LoadLocation(*zone); err != nil {
			zone = nil
		}
		event.TimeZone = zone
	}
	return event, true
}

func nonEmpty(value *string) *string {
	if value == nil || *value == "" {
		return nil
	}
	return value
}

// Registration is how a feed's source is registered in Calendar: read-only,
// labelled ZAW for the ZAW preset and ICS otherwise. A preset also sends the
// name and default reminders it configures (Calendar changes them only when
// they differ from what was sent before, so the owner's edits stay);
// another feed sends its name and says nothing about the owner's settings.
// The ZAW feed's event locations are the owner's own street, so its source
// says they are private (reminders leave them out).
func Registration(feed store.Feed) calendar.Registration {
	registration := calendar.Registration{Name: feed.Name, Provider: ProviderICS, ReadOnly: true}
	if feed.Preset == nil {
		return registration
	}
	if *feed.Preset == PresetZAW {
		private := true
		registration.Provider, registration.PrivateLocation = ProviderZAW, &private
	}
	var config PresetConfig
	if json.Unmarshal(feed.PresetConfig, &config) == nil {
		if config.Name != "" {
			registration.Name = config.Name
		}
		if len(config.DefaultReminders) > 0 {
			registration.DefaultReminders = config.DefaultReminders
		}
	}
	return registration
}

// Register registers (or registers again) the feed's source in Calendar
// and records what Calendar answered: the source's id, the owner's name for
// it, and whether it is enabled. A feed whose source Calendar has never
// registered waits while Calendar has sources of this integration that no
// feed here belongs to (errUnknownSources).
func (r *Refresher) Register(ctx context.Context, feed store.Feed) (store.Feed, error) {
	if !feed.SourceID.Valid {
		if err := r.checkKnownSources(ctx); err != nil {
			return feed, err
		}
	}
	source, err := r.calendar.RegisterSource(ctx, uuidString(feed.ID), Registration(feed))
	if err != nil {
		return feed, err
	}
	updated, err := r.mirror(ctx, feed, source)
	if err != nil {
		return feed, err
	}
	if err := r.queries.SetRegisterPending(ctx, store.SetRegisterPendingParams{ID: feed.ID, RegisterPending: false}); err != nil {
		return updated, err
	}
	updated.RegisterPending = false
	return updated, nil
}

// checkKnownSources refuses while Calendar has sources of this integration
// that no feed here belongs to.
func (r *Refresher) checkKnownSources(ctx context.Context) error {
	sources, err := r.calendar.ListSources(ctx)
	if err != nil {
		return err
	}
	for _, source := range sources {
		id, err := googleuuid.Parse(source.ExternalID)
		if err != nil {
			return errUnknownSources
		}
		if _, err := r.queries.GetFeed(ctx, pgtype.UUID{Bytes: id, Valid: true}); err != nil {
			return errUnknownSources
		}
	}
	return nil
}

// mirror records what Calendar reports of the feed's source.
func (r *Refresher) mirror(ctx context.Context, feed store.Feed, source calendar.Source) (store.Feed, error) {
	sourceID, err := googleuuid.Parse(source.ID)
	if err != nil {
		return feed, err
	}
	id := pgtype.UUID{Bytes: sourceID, Valid: true}
	if feed.SourceID == id && feed.Name == source.Name && feed.Enabled == source.Enabled {
		return feed, nil
	}
	name := source.Name
	if strings.TrimSpace(name) == "" {
		name = feed.Name
	}
	return r.queries.MirrorSource(ctx, store.MirrorSourceParams{ID: feed.ID, SourceID: id, Name: name, Enabled: source.Enabled, Now: at(r.now().UTC())})
}

// Mirror registers the feeds whose source is waiting to be registered, then
// reads the integration's sources from Calendar and records each feed's
// source id, name, and enabled: a source the owner switched back on is due
// right away. It asks Calendar nothing while there are no feeds.
func (r *Refresher) Mirror(ctx context.Context) error {
	feeds, err := r.queries.ListFeeds(ctx)
	if err != nil || len(feeds) == 0 {
		return err
	}
	var failed error
	for index, feed := range feeds {
		if !feed.RegisterPending {
			continue
		}
		registered, err := r.Register(ctx, feed)
		if err != nil {
			failed = errors.Join(failed, err)
			continue
		}
		feeds[index] = registered
	}
	sources, err := r.calendar.ListSources(ctx)
	if err != nil {
		return errors.Join(failed, err)
	}
	byExternalID := make(map[string]calendar.Source, len(sources))
	for _, source := range sources {
		byExternalID[source.ExternalID] = source
	}
	for _, feed := range feeds {
		source, found := byExternalID[uuidString(feed.ID)]
		if !found {
			continue
		}
		if _, err := r.mirror(ctx, feed, source); err != nil {
			failed = errors.Join(failed, err)
		}
	}
	return failed
}

// MirrorOne asks Calendar about the feed's source and records what it
// reports. A source Calendar no longer has (deleted there with its events)
// removes the feed: ErrNotFound.
func (r *Refresher) MirrorOne(ctx context.Context, feed store.Feed) error {
	source, err := r.calendar.GetSource(ctx, uuidString(feed.ID))
	if calendar.IsNotFound(err) {
		if _, err := r.queries.DeleteFeed(ctx, feed.ID); err != nil {
			return err
		}
		r.logger.Info("calendar feed removed: its Calendar source was deleted", "feed", uuidString(feed.ID))
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	_, err = r.mirror(ctx, feed, source)
	return err
}
