package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	googleuuid "github.com/google/uuid"
	"github.com/oapi-codegen/runtime/types"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/calendar/internal/api/generated"
	"github.com/GAMch1k/life-os/services/calendar/internal/changefeed"
	"github.com/GAMch1k/life-os/services/calendar/internal/google"
	store "github.com/GAMch1k/life-os/services/calendar/internal/repository/generated"
)

// feed reads one page of the change feed as actor after cursor ("" starts
// with a snapshot).
func (c *integrationClient) feed(actor, cursor string, extra ...string) changeFeedPage {
	c.t.Helper()
	query := url.Values{}
	if cursor != "" {
		query.Set("cursor", cursor)
	}
	for index := 0; index+1 < len(extra); index += 2 {
		query.Set(extra[index], extra[index+1])
	}
	var page changeFeedPage
	c.expect(actor, http.MethodGet, "/v1/integrations/changes?"+query.Encode(), nil, http.StatusOK, &page)
	return page
}

// drain reads every page of the current phase after cursor and returns the
// entries and the cursor to continue from.
func (c *integrationClient) drain(actor, cursor string, extra ...string) ([]changeFeedEntry, string) {
	c.t.Helper()
	var entries []changeFeedEntry
	for pages := 0; ; pages++ {
		if pages > 100 {
			c.t.Fatal("too many feed pages")
		}
		page := c.feed(actor, cursor, extra...)
		entries = append(entries, page.Items...)
		cursor = page.NextCursor
		if !page.HasMore {
			return entries, cursor
		}
	}
}

// feedLabels lists entries as "<name> <actors> [deleted]", naming events
// by names (tombstones have no title).
func feedLabels(entries []changeFeedEntry, names map[types.UUID]string) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := names[entry.EventID]
		if name == "" && entry.Event != nil {
			name = entry.Event.Title
		}
		label := name + " " + strings.Join(entry.Actors, ",")
		if entry.Deleted {
			label += " deleted"
		}
		out = append(out, strings.TrimSpace(label))
	}
	return out
}

func TestChangeFeedRecordsEveryChangeInCommitOrderWithItsActor(t *testing.T) {
	c := newIntegrationClient(t)
	var dentist, gym, lunch, later generated.CalendarEvent
	c.expect("user", http.MethodPost, "/v1/events", map[string]any{"title": "Dentist", "allDay": false, "startAt": "2026-10-12T09:30:00+02:00", "endAt": "2026-10-12T10:15:00+02:00"}, http.StatusCreated, &dentist)
	c.expect("user", http.MethodPost, "/v1/events", map[string]any{
		"title": "Gym", "allDay": false, "startAt": "2026-10-13T18:00:00+02:00", "endAt": "2026-10-13T19:00:00+02:00",
		"recurrence": map[string]any{"frequency": "weekly", "interval": 1},
	}, http.StatusCreated, &gym)

	// Without a cursor: a snapshot of every Life OS event, page by page,
	// then the changes made since it started.
	first := c.feed(googleActor, "", "limit", "1")
	if first.Phase != "snapshot" || !first.HasMore || len(first.Items) != 1 || first.Items[0].Actor != nil || len(first.Items[0].Actors) != 0 ||
		first.Items[0].Sequence != nil || first.Items[0].Event == nil || first.Items[0].Source != nil {
		t.Fatalf("first snapshot page = %+v", first)
	}
	second := c.feed(googleActor, first.NextCursor, "limit", "1")
	if second.Phase != "snapshot" || second.HasMore || len(second.Items) != 1 || second.Items[0].EventID == first.Items[0].EventID {
		t.Fatalf("second snapshot page = %+v", second)
	}
	start := second.NextCursor
	if quiet := c.feed(googleActor, start); quiet.Phase != "changes" || len(quiet.Items) != 0 || quiet.HasMore || quiet.NextCursor != start {
		t.Fatalf("no changes yet = %+v", quiet)
	}

	// Every kind of change, by several actors.
	events := "/v1/events/"
	c.expect("agent:life-os", http.MethodPatch, events+dentist.Id.String(), map[string]any{"title": "Dentist (Dr. Weber)"}, http.StatusOK, nil)
	c.expect("user", http.MethodPatch, occurrencePath(gym.Id, "2026-10-20T16:00:00Z", ""), map[string]any{"status": "cancelled"}, http.StatusOK, nil)
	c.expect("dev:test", http.MethodPost, "/v1/events", map[string]any{"title": "Lunch", "allDay": false, "startAt": "2026-10-14T12:00:00+02:00", "endAt": "2026-10-14T13:00:00+02:00"}, http.StatusCreated, &lunch)
	c.expect("user", http.MethodDelete, events+lunch.Id.String(), nil, http.StatusNoContent, nil)
	c.expect("user", http.MethodPost, occurrencePath(gym.Id, "2026-10-27T17:00:00Z", "/split"), map[string]any{"title": "Gym (new place)"}, http.StatusCreated, &later)
	c.expect("user", http.MethodPost, occurrencePath(later.Id, "2026-11-10T17:00:00Z", "/truncate"), nil, http.StatusNoContent, nil)
	c.expect("user", http.MethodPatch, events+dentist.Id.String(), map[string]any{"status": "cancelled"}, http.StatusOK, nil)
	// A change that changes nothing is no change.
	c.expect("user", http.MethodPatch, events+dentist.Id.String(), map[string]any{"title": "Dentist (Dr. Weber)"}, http.StatusOK, nil)

	names := map[types.UUID]string{dentist.Id: "Dentist", gym.Id: "Gym", lunch.Id: "Lunch", later.Id: "Later"}
	one, end := c.drain(googleActor, start, "limit", "1")
	equal(t, "one change a page", feedLabels(one, names), []string{
		"Dentist agent:life-os", "Gym user", "Lunch dev:test deleted", "Lunch user deleted", "Later user", "Gym user", "Later user", "Dentist user",
	})
	for index := 1; index < len(one); index++ {
		if *one[index].Sequence <= *one[index-1].Sequence {
			t.Fatalf("entries out of order: %d after %d", *one[index].Sequence, *one[index-1].Sequence)
		}
	}

	// Read at once, several changes of an event are one entry with its
	// current state and every actor.
	all, collapsedEnd := c.drain(googleActor, start)
	if collapsedEnd != end {
		t.Fatalf("cursors differ: %s, %s", collapsedEnd, end)
	}
	equal(t, "one entry an event", feedLabels(all, names), []string{"Lunch dev:test,user deleted", "Gym user", "Later user", "Dentist agent:life-os,user"})
	byID := map[types.UUID]changeFeedEntry{}
	for _, entry := range all {
		byID[entry.EventID] = entry
	}
	if entry := byID[dentist.Id]; entry.Event.Status != generated.Cancelled || entry.Event.Title != "Dentist (Dr. Weber)" || deref(entry.Actor) != "user" {
		t.Fatalf("dentist = %+v", entry)
	}
	gymEntry := byID[gym.Id]
	if gymEntry.Event.Recurrence == nil || gymEntry.Event.Recurrence.Until == nil || len(gymEntry.Exceptions) != 1 ||
		gymEntry.Exceptions[0].RecurrenceID != "2026-10-20T16:00:00Z" || !gymEntry.Exceptions[0].Cancelled || string(gymEntry.Exceptions[0].Changes) != "{}" {
		t.Fatalf("gym = %+v", gymEntry)
	}
	if entry := byID[lunch.Id]; entry.Event != nil || entry.Source != nil || len(entry.Exceptions) != 0 {
		t.Fatalf("lunch tombstone = %+v", entry)
	}

	// The cursor survives a restart.
	c.restart()
	if quiet := c.feed(googleActor, end); len(quiet.Items) != 0 {
		t.Fatalf("after a restart = %+v", quiet.Items)
	}
	c.expect("user", http.MethodPatch, events+gym.Id.String(), map[string]any{"location": "Studio"}, http.StatusOK, nil)
	next, latest := c.drain(googleActor, end)
	equal(t, "after a restart", feedLabels(next, names), []string{"Gym user"})

	// Pruned past the retention, an old cursor has expired; the latest one
	// still works, and a fresh start lists everything again.
	pruner := changefeed.NewPruner(c.pool, 30*24*time.Hour, nil)
	if result, err := pruner.Prune(context.Background()); err != nil || result.Pruned != 0 {
		t.Fatalf("pruned within the retention: %+v, %v", result, err)
	}
	result, err := pruner.WithClock(func() time.Time { return time.Now().Add(31 * 24 * time.Hour) }).Prune(context.Background())
	// Two creations, eight changes, and one after the restart.
	if err != nil || result.Pruned != 11 {
		t.Fatalf("pruned = %+v, %v", result, err)
	}
	c.refused(googleActor, http.MethodGet, "/v1/integrations/changes?cursor="+url.QueryEscape(start), nil, http.StatusGone, "cursor_expired")
	c.refused(googleActor, http.MethodGet, "/v1/integrations/changes?cursor="+url.QueryEscape(first.NextCursor), nil, http.StatusGone, "cursor_expired")
	if quiet := c.feed(googleActor, latest); len(quiet.Items) != 0 {
		t.Fatalf("latest cursor after pruning = %+v", quiet)
	}
	again, resumed := c.drain(googleActor, "")
	if len(again) != 3 {
		t.Fatalf("snapshot after pruning = %v", feedLabels(again, names))
	}
	c.expect("user", http.MethodPatch, events+gym.Id.String(), map[string]any{"location": "Gym hall"}, http.StatusOK, nil)
	resumedEntries, _ := c.drain(googleActor, resumed)
	equal(t, "after the new snapshot", feedLabels(resumedEntries, names), []string{"Gym user"})

	c.refused(googleActor, http.MethodGet, "/v1/integrations/changes?cursor=garbage", nil, http.StatusBadRequest, "invalid_cursor")
	c.refused(googleActor, http.MethodGet, "/v1/integrations/changes?limit=501", nil, http.StatusBadRequest, "bad_request")
	c.refused("dev:test", http.MethodGet, "/v1/integrations/changes?scope=own", nil, http.StatusBadRequest, "bad_request")
}

func TestChangeFeedFollowsTheEventsOfWritableIntegrationSources(t *testing.T) {
	c := newIntegrationClient(t)
	c.register(googleActor, "google-calendar", "owner@example.com", map[string]any{"name": "Owner", "provider": "Google", "readOnly": false}, http.StatusCreated)
	c.register(googleActor, "google-calendar", "family", map[string]any{"name": "Family", "provider": "Google", "readOnly": true}, http.StatusCreated)
	c.register(feedsActor, "calendar-feeds", "club", map[string]any{"name": "Club", "provider": "ICS", "readOnly": false}, http.StatusCreated)
	owner := func(body map[string]any) generated.SyncIntegrationSourceResult {
		return c.sync(googleActor, "google-calendar", "owner@example.com", body)
	}
	dentist := map[string]any{"externalUid": "dentist", "title": "Dentist", "allDay": false, "startAt": "2026-10-12T09:30:00+02:00", "endAt": "2026-10-12T10:15:00+02:00"}
	standup := map[string]any{
		"externalUid": "standup", "title": "Standup", "allDay": false, "startAt": "2026-10-12T09:00:00+02:00", "endAt": "2026-10-12T09:15:00+02:00",
		"timeZone": "Europe/Berlin", "recurrence": map[string]any{"frequency": "weekly", "interval": 1},
	}
	expanded := map[string]any{
		"externalUid": "club_20261013T160000Z", "title": "Book club", "allDay": false, "startAt": "2026-10-13T18:00:00+02:00", "endAt": "2026-10-13T20:00:00+02:00",
		"readOnlyReason": "One occurrence of a repeating event Life OS cannot hold; change it in Google Calendar.",
	}
	owner(map[string]any{"mode": "snapshot", "items": []any{dentist, standup, expanded}})
	c.sync(googleActor, "google-calendar", "family", map[string]any{"mode": "snapshot", "items": []any{
		map[string]any{"externalUid": "trip", "title": "Trip", "allDay": true, "startDate": "2026-10-20", "endDate": "2026-10-22"},
	}})
	c.sync(feedsActor, "calendar-feeds", "club", map[string]any{"mode": "snapshot", "items": []any{
		map[string]any{"externalUid": "meeting", "title": "Club meeting", "allDay": false, "startAt": "2026-10-16T19:00:00+02:00", "endAt": "2026-10-16T21:00:00+02:00"},
	}})
	items := c.events()

	// The integration's own events of a source that accepts changes are in
	// its snapshot, with their source; a read-only source's are not, nor
	// (with scope own) another integration's.
	snapshot, start := c.drain(googleActor, "")
	labels := map[string]bool{}
	for _, entry := range snapshot {
		labels[entry.Event.Title] = true
		if entry.Source == nil || entry.Source.Integration != "google-calendar" || entry.Source.ExternalId != "owner@example.com" || entry.Source.ExternalUid == "" {
			t.Fatalf("snapshot entry source = %+v", entry.Source)
		}
	}
	if len(snapshot) != 3 || !labels["Dentist"] || !labels["Standup"] || !labels["Book club"] {
		t.Fatalf("google snapshot = %v", feedLabels(snapshot, nil))
	}
	everything, _ := c.drain("dev:test", "")
	if len(everything) != 4 {
		t.Fatalf("dev snapshot = %v", feedLabels(everything, nil))
	}

	// Writable: editable in Life OS like a Life OS event (also through
	// editable=true), unless read-only on its own.
	ownerDentist := byTitle(t, items, "Dentist")
	if !ownerDentist.Editable || ownerDentist.ReadOnlyReason != nil || ownerDentist.Source.ReadOnly == nil || *ownerDentist.Source.ReadOnly {
		t.Fatalf("dentist = %+v", ownerDentist)
	}
	club := byTitle(t, items, "Book club")
	if club.Editable || deref(club.ReadOnlyReason) != deref(stringOf(expanded["readOnlyReason"])) {
		t.Fatalf("expanded occurrence = %+v", club)
	}
	c.refused("user", http.MethodPatch, "/v1/events/"+club.Id.String(), map[string]any{"title": "Mine"}, http.StatusConflict, "managed_by_source")
	trip := byTitle(t, items, "Trip")
	c.refused("user", http.MethodPatch, "/v1/events/"+trip.Id.String(), map[string]any{"title": "Mine"}, http.StatusConflict, "managed_by_source")
	c.expect("user", http.MethodPatch, "/v1/events/"+trip.Id.String(), map[string]any{"priority": "high"}, http.StatusOK, nil)
	editable := map[string]bool{}
	for _, item := range c.events("editable", "true") {
		editable[item.Title] = true
	}
	if len(editable) != 3 || !editable["Dentist"] || !editable["Standup"] || !editable["Club meeting"] {
		t.Fatalf("editable = %v", editable)
	}

	// The owner's edit comes with its source identity; a sync refuses to
	// overwrite it until the integration has handled it.
	c.expect("user", http.MethodPatch, "/v1/events/"+ownerDentist.Id.String(), map[string]any{"title": "Dentist (moved)", "startAt": "2026-10-12T11:00:00+02:00", "endAt": "2026-10-12T11:45:00+02:00"}, http.StatusOK, nil)
	refusedBody := map[string]any{"mode": "delta", "items": []any{dentist}, "feedCursor": start}
	c.refused(googleActor, http.MethodPost, sourcePath("google-calendar", "owner@example.com")+"/sync", refusedBody, http.StatusConflict, "unhandled_changes")
	edits, handled := c.drain(googleActor, start)
	equal(t, "the owner's edit", feedLabels(edits, nil), []string{"Dentist (moved) user"})
	if source := edits[0].Source; source == nil || source.ExternalUid != "dentist" || source.ExternalId != "owner@example.com" {
		t.Fatalf("edit source = %+v", source)
	}
	// The integration writes it to Google and syncs Google's answer: its
	// own write comes back with only itself as actor.
	moved := map[string]any{"externalUid": "dentist", "title": "Dentist (moved)", "allDay": false, "startAt": "2026-10-12T11:00:00+02:00", "endAt": "2026-10-12T11:45:00+02:00", "location": "Praxis"}
	owner(map[string]any{"mode": "delta", "items": []any{moved}, "feedCursor": handled})
	own, handled := c.drain(googleActor, handled)
	equal(t, "the integration's own write", feedLabels(own, nil), []string{"Dentist (moved) system:google-calendar"})

	// A series split in Life OS: the new series is in the source under a
	// placeholder uid until the integration gives it Google's.
	standupEvent := byTitle(t, items, "Standup")
	var later generated.CalendarEvent
	c.expect("user", http.MethodPost, occurrencePath(standupEvent.Id, "2026-10-26T08:00:00Z", "/split"), map[string]any{"title": "Standup (new time)", "startAt": "2026-10-26T10:00:00+01:00", "endAt": "2026-10-26T10:15:00+01:00"}, http.StatusCreated, &later)
	split, handled := c.drain(googleActor, handled)
	var placeholder string
	for _, entry := range split {
		if entry.EventID == later.Id {
			placeholder = entry.Source.ExternalUid
		}
	}
	if len(split) != 2 || !strings.HasPrefix(placeholder, "lifeos:") || later.Source.Kind != generated.EventSourceKindExternal {
		t.Fatalf("split = %v, placeholder %q, new series %+v", feedLabels(split, nil), placeholder, later.Source)
	}
	created := map[string]any{
		"externalUid": "standup_R20261026", "previousExternalUid": placeholder, "title": "Standup (new time)", "allDay": false,
		"startAt": "2026-10-26T10:00:00+01:00", "endAt": "2026-10-26T10:15:00+01:00", "timeZone": "Europe/Berlin",
		"recurrence": map[string]any{"frequency": "weekly", "interval": 1},
	}
	if got := syncCountsOf(owner(map[string]any{"mode": "delta", "items": []any{created}})); got != "received=1 inserted=0 updated=0 removed=0 restored=0 unchanged=1" {
		t.Fatalf("re-keyed series: %s", got)
	}
	rekeyed, handled := c.drain(googleActor, handled)
	if len(rekeyed) != 1 || rekeyed[0].EventID != later.Id || rekeyed[0].Source.ExternalUid != "standup_R20261026" {
		t.Fatalf("re-keyed = %+v", rekeyed)
	}

	// Deleted in Life OS: a tombstone that still says where it was.
	c.expect("user", http.MethodDelete, "/v1/events/"+ownerDentist.Id.String(), nil, http.StatusNoContent, nil)
	deleted, handled := c.drain(googleActor, handled)
	if len(deleted) != 1 || !deleted[0].Deleted || deleted[0].Source == nil || deleted[0].Source.ExternalUid != "dentist" || deleted[0].Source.Integration != "google-calendar" {
		t.Fatalf("tombstone = %+v", deleted)
	}

	// Another integration's events stay out of its feed with scope own.
	var meeting generated.CalendarEvent
	for _, item := range c.events() {
		if item.Title == "Club meeting" {
			meeting = item
		}
	}
	c.expect("user", http.MethodPatch, "/v1/events/"+meeting.Id.String(), map[string]any{"title": "Club meeting (room 3)"}, http.StatusOK, nil)
	if others, _ := c.drain(googleActor, handled); len(others) != 0 {
		t.Fatalf("another integration's edit in the feed: %v", feedLabels(others, nil))
	}
	if mine, _ := c.drain(feedsActor, handled); len(mine) != 1 || mine[0].Source.Integration != "calendar-feeds" {
		t.Fatalf("calendar-feeds' own edit = %v", feedLabels(mine, nil))
	}
	if all, _ := c.drain(googleActor, handled, "scope", "all"); len(all) != 1 {
		t.Fatalf("scope all = %v", feedLabels(all, nil))
	}

	// Deleting a source deletes its events without tombstones: nothing is
	// deleted at the provider.
	c.expect(googleActor, http.MethodDelete, sourcePath("google-calendar", "owner@example.com"), nil, http.StatusNoContent, nil)
	if after, _ := c.drain(googleActor, handled); len(after) != 0 {
		t.Fatalf("deleting the source left entries: %v", feedLabels(after, nil))
	}
}

func stringOf(value any) *string {
	text, ok := value.(string)
	if !ok {
		return nil
	}
	return &text
}

// The in-process Google sync applies changes made to the copies of Life OS
// events in Google: those changes are the sync's in the feed.
func TestChangeFeedRecordsTheGoogleReverseSync(t *testing.T) {
	c := newSyncingClient(t)
	c.syncDue()
	// The sources the in-process syncer adds have their identity too.
	source, err := c.queries.GetIntegrationSource(context.Background(), store.GetIntegrationSourceParams{Integration: "google-calendar", ExternalID: ownerCalendar.ID})
	if err != nil || !source.InProcess || deref(source.Provider) != "Google" || source.ReadOnly || source.Kind != "google" {
		t.Fatalf("synced calendar's source = %+v, %v", source, err)
	}
	haircut := c.createEvent(map[string]any{"title": "Haircut", "allDay": false, "startAt": "2026-10-18T11:00:00+02:00", "endAt": "2026-10-18T12:00:00+02:00"})
	c.exportDue()
	read := func(cursor string) changeFeedPage {
		query := ""
		if cursor != "" {
			query = "?cursor=" + url.QueryEscape(cursor)
		}
		status, raw := c.do(http.MethodGet, "/v1/integrations/changes"+query, nil, activity.ActorHeader, "dev:test")
		var page changeFeedPage
		if status != http.StatusOK || json.Unmarshal(raw, &page) != nil {
			t.Fatalf("feed: %d %s", status, raw)
		}
		return page
	}
	snapshot := read("")
	if len(snapshot.Items) != 1 || snapshot.HasMore {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	lifeOS := c.lifeOSCalendar()
	c.fake.ChangeEvent(lifeOS, google.ExportID(googleuuid.UUID(haircut.Id)), func(event *google.Event) {
		event.Start, event.End = gtimed("2026-10-19T11:00:00+02:00"), gtimed("2026-10-19T12:00:00+02:00")
	})
	c.clock.Advance(2 * time.Minute)
	c.syncDue()
	changes := read(snapshot.NextCursor)
	if len(changes.Items) != 1 || changes.Items[0].EventID != haircut.Id || strings.Join(changes.Items[0].Actors, ",") != "system:google-calendar" ||
		!changes.Items[0].Event.StartAt.Equal(time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("reverse sync entry = %+v", changes.Items)
	}
}
