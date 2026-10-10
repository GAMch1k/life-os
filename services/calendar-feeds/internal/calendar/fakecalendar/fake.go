// Package fakecalendar is an in-memory stand-in for Calendar's integration
// API (/v1/integrations/calendar-feeds/sources/...) for calendar-feeds'
// tests: it registers sources with Calendar's rules for the owner's
// settings, and reconciles snapshots like Calendar does for a feed (window
// upcomingOnly, emptyGuard), counting what changed. Calendar's own tests
// cover the real reconciliation.
package fakecalendar

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/GAMch1k/life-os/services/calendar-feeds/internal/calendar"
)

// Event is a stored event of a source.
type Event struct {
	Item    calendar.Event
	Removed bool
}

// Source is a stored source with its events.
type Source struct {
	calendar.Source
	// Config is what the integration last sent of the owner's settings.
	Config map[string]json.RawMessage
	Events map[string]*Event
	Syncs  int
	order  int
}

// Fake serves the integration API. Now and Owner are the clock and zone of
// its reconciliation.
type Fake struct {
	mu      sync.Mutex
	Now     func() time.Time
	Owner   *time.Location
	sources map[string]*Source
	next    int
	// Down answers every request with 503, as an unreachable Calendar.
	Down bool
	// Requests records "METHOD path" of every request.
	Requests []string
	// Actors records the X-Life-OS-Actor of every request.
	Actors []string
}

// New returns an empty fake on clock now in owner.
func New(now func() time.Time, owner *time.Location) *Fake {
	return &Fake{Now: now, Owner: owner, sources: map[string]*Source{}}
}

const prefix = "/v1/integrations/" + calendar.Integration + "/sources"

func (f *Fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Requests = append(f.Requests, r.Method+" "+r.URL.Path)
	f.Actors = append(f.Actors, r.Header.Get("X-Life-OS-Actor"))
	if f.Down {
		fail(w, http.StatusServiceUnavailable, "unavailable", "down")
		return
	}
	if r.Header.Get("X-Life-OS-Actor") != calendar.Actor {
		fail(w, http.StatusForbidden, "forbidden", "only system:calendar-feeds")
		return
	}
	rest, ok := strings.CutPrefix(r.URL.EscapedPath(), prefix)
	if !ok {
		fail(w, http.StatusNotFound, "not_found", "no route")
		return
	}
	if rest == "" && r.Method == http.MethodGet {
		f.list(w)
		return
	}
	rest = strings.TrimPrefix(rest, "/")
	escaped, sync := strings.CutSuffix(rest, "/sync")
	externalID, err := url.PathUnescape(escaped)
	if err != nil || externalID == "" {
		fail(w, http.StatusBadRequest, "bad_request", "externalId")
		return
	}
	switch {
	case sync && r.Method == http.MethodPost:
		f.sync(w, r, externalID)
	case r.Method == http.MethodGet:
		source, found := f.sources[externalID]
		if !found {
			fail(w, http.StatusNotFound, "not_found", "the integration has no source with this externalId")
			return
		}
		respond(w, http.StatusOK, source.Source)
	case r.Method == http.MethodPut:
		f.register(w, r, externalID)
	case r.Method == http.MethodDelete:
		if _, found := f.sources[externalID]; !found {
			fail(w, http.StatusNotFound, "not_found", "the integration has no source with this externalId")
			return
		}
		delete(f.sources, externalID)
		w.WriteHeader(http.StatusNoContent)
	default:
		fail(w, http.StatusMethodNotAllowed, "method_not_allowed", r.Method)
	}
}

func (f *Fake) list(w http.ResponseWriter) {
	items := make([]*Source, 0, len(f.sources))
	for _, source := range f.sources {
		items = append(items, source)
	}
	sort.Slice(items, func(a, b int) bool { return items[a].order < items[b].order })
	out := struct {
		Items []calendar.Source `json:"items"`
	}{Items: []calendar.Source{}}
	for _, source := range items {
		out.Items = append(out.Items, source.Source)
	}
	respond(w, http.StatusOK, out)
}

func (f *Fake) newSource(externalID string) *Source {
	f.next++
	source := &Source{
		Source: calendar.Source{
			ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", f.next), Integration: calendar.Integration, ExternalID: externalID,
			Enabled: true, ReadOnly: true, DefaultPriority: "medium", DefaultReminders: json.RawMessage("null"),
		},
		Config: map[string]json.RawMessage{}, Events: map[string]*Event{}, order: f.next,
	}
	f.sources[externalID] = source
	return source
}

func (f *Fake) register(w http.ResponseWriter, r *http.Request, externalID string) {
	var present map[string]json.RawMessage
	var body calendar.Registration
	if err := json.NewDecoder(r.Body).Decode(&present); err != nil {
		fail(w, http.StatusBadRequest, "invalid_source", "not JSON")
		return
	}
	encoded, _ := json.Marshal(present)
	_ = json.Unmarshal(encoded, &body)
	if strings.TrimSpace(body.Name) == "" || strings.TrimSpace(body.Provider) == "" {
		fail(w, http.StatusBadRequest, "invalid_source", "name and provider are required")
		return
	}
	if reminders, ok := present["defaultReminders"]; ok {
		var list []map[string]any
		if string(reminders) != "null" && json.Unmarshal(reminders, &list) != nil {
			fail(w, http.StatusBadRequest, "invalid_source", "defaultReminders must be a list")
			return
		}
	}
	status := http.StatusOK
	source, found := f.sources[externalID]
	if !found {
		source = f.newSource(externalID)
		status = http.StatusCreated
	}
	source.Provider, source.ReadOnly = body.Provider, body.ReadOnly
	if body.PrivateLocation != nil {
		source.PrivateLocation = *body.PrivateLocation
	}
	sent := map[string]json.RawMessage{"name": jsonString(body.Name)}
	if value, ok := present["defaultReminders"]; ok {
		sent["defaultReminders"] = value
	}
	if value, ok := present["defaultPriority"]; ok {
		sent["defaultPriority"] = value
	}
	for key, value := range sent {
		if previous, ok := source.Config[key]; ok && string(previous) == string(value) {
			continue // what the integration said before: the owner's setting stays
		}
		source.Config[key] = value
		switch key {
		case "name":
			source.Name = body.Name
		case "defaultReminders":
			source.DefaultReminders = value
		case "defaultPriority":
			source.DefaultPriority = body.DefaultPriority
		}
	}
	respond(w, status, source.Source)
}

func (f *Fake) start(item calendar.Event) time.Time {
	if item.AllDay && item.StartDate != nil {
		day, _ := time.Parse(time.DateOnly, *item.StartDate)
		return time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, f.Owner)
	}
	if item.StartAt != nil {
		return *item.StartAt
	}
	return time.Time{}
}

func (f *Fake) sync(w http.ResponseWriter, r *http.Request, externalID string) {
	source, found := f.sources[externalID]
	if !found {
		fail(w, http.StatusNotFound, "not_found", "the integration has no source with this externalId")
		return
	}
	if !source.Enabled {
		fail(w, http.StatusConflict, calendar.CodeSourceDisabled, "the owner disabled this source")
		return
	}
	var batch calendar.Sync
	if err := json.NewDecoder(r.Body).Decode(&batch); err != nil || batch.Mode != "snapshot" || batch.Items == nil {
		fail(w, http.StatusBadRequest, "invalid_sync_request", "a snapshot with items")
		return
	}
	now := f.Now()
	upcoming := batch.Window != nil && batch.Window.UpcomingOnly
	if batch.EmptyGuard && len(batch.Items) == 0 {
		for _, event := range source.Events {
			if !event.Removed && event.Item.Status != "cancelled" && !f.start(event.Item).Before(now) {
				fail(w, http.StatusConflict, calendar.CodeEmptySnapshot, "the snapshot has no events while the source still has upcoming ones")
				return
			}
		}
	}
	result := calendar.SyncResult{Mode: "snapshot", Received: len(batch.Items)}
	seen := map[string]bool{}
	for _, item := range batch.Items {
		if seen[item.ExternalUID] {
			fail(w, http.StatusBadRequest, "duplicate_uid", item.ExternalUID)
			return
		}
		seen[item.ExternalUID] = true
	}
	for _, item := range batch.Items {
		stored, found := source.Events[item.ExternalUID]
		switch {
		case !found:
			source.Events[item.ExternalUID] = &Event{Item: item}
			result.Inserted++
		case upcoming && f.start(stored.Item).Before(now) && f.start(item).Before(now):
			result.Unchanged++
		case stored.Removed:
			stored.Item, stored.Removed = item, false
			result.Restored++
		case same(stored.Item, item):
			result.Unchanged++
		default:
			stored.Item = item
			result.Updated++
		}
	}
	for uid, stored := range source.Events {
		if seen[uid] || stored.Removed || upcoming && f.start(stored.Item).Before(now) {
			continue
		}
		stored.Removed = true
		result.Removed++
	}
	source.Syncs++
	respond(w, http.StatusOK, result)
}

func same(a, b calendar.Event) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return string(left) == string(right)
}

// Add adds a source of this integration that the service may not know,
// such as one registered before its database was replaced.
func (f *Fake) Add(externalID, name, provider string) calendar.Source {
	f.mu.Lock()
	defer f.mu.Unlock()
	source := f.newSource(externalID)
	source.Name, source.Provider = name, provider
	source.Config["name"] = jsonString(name)
	return source.Source
}

// Update changes a source as the owner does through /v1/sources.
func (f *Fake) Update(externalID string, change func(*calendar.Source)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change(&f.sources[externalID].Source)
}

// Remove deletes a source as the owner does through /v1/sources.
func (f *Fake) Remove(externalID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.sources, externalID)
}

// Source returns a copy of a stored source and its events (ok false when
// there is none).
func (f *Fake) Source(externalID string) (Source, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	source, found := f.sources[externalID]
	if !found {
		return Source{}, false
	}
	out := *source
	out.Events = map[string]*Event{}
	for uid, event := range source.Events {
		copied := *event
		out.Events[uid] = &copied
	}
	return out, true
}

// Count is the number of sources.
func (f *Fake) Count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sources)
}

// Active lists the uids of a source's events that are not removed, sorted.
func (s Source) Active() []string {
	var uids []string
	for uid, event := range s.Events {
		if !event.Removed {
			uids = append(uids, uid)
		}
	}
	sort.Strings(uids)
	return uids
}

func jsonString(value string) json.RawMessage {
	raw, _ := json.Marshal(value)
	return raw
}

func respond(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func fail(w http.ResponseWriter, status int, code, message string) {
	respond(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}
