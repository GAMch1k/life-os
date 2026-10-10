package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/services/notifications/internal/api/generated"
	"github.com/GAMch1k/life-os/services/notifications/internal/testdb"
)

// These tests drive the real HTTP API (actor gate included) against a fresh,
// fully migrated schema on NOTIFICATIONS_TEST_DATABASE_URL, with an
// in-process Activity service that records what it is sent.

type recordedEvents struct {
	mu     sync.Mutex
	events []map[string]any
}

func (r *recordedEvents) all() []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]map[string]any(nil), r.events...)
}

type apiClient struct {
	t        *testing.T
	app      *httpserver.App
	handler  *Handler
	activity *recordedEvents
	woken    *int
}

func newAPIClient(t *testing.T, channels ...string) apiClient {
	t.Helper()
	pool := testdb.Open(t)
	recorded := &recordedEvents{}
	activityServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var event map[string]any
		_ = json.NewDecoder(r.Body).Decode(&event)
		recorded.mu.Lock()
		recorded.events = append(recorded.events, event)
		recorded.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(activityServer.Close)
	woken := new(int)
	handler := NewHandler(pool, channels).
		WithWake(func() { *woken++ }).
		WithActivity(activity.NewEmitter(activityServer.URL, "notifications", nil))
	return apiClient{t: t, app: newTestApp(handler), handler: handler, activity: recorded, woken: woken}
}

func (c apiClient) do(method, target string, body any, headers ...string) (int, []byte) {
	c.t.Helper()
	var reader io.Reader
	switch value := body.(type) {
	case nil:
	case string:
		reader = strings.NewReader(value)
	default:
		raw, err := json.Marshal(value)
		if err != nil {
			c.t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, target, reader)
	req.Header.Set("content-type", "application/json")
	req.Header.Set(activity.ActorHeader, "dev:test")
	for index := 0; index+1 < len(headers); index += 2 {
		req.Header.Set(headers[index], headers[index+1])
	}
	resp, err := c.app.Test(req, fiberTestConfig)
	if err != nil {
		c.t.Fatal(err)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		c.t.Fatal(err)
	}
	return resp.StatusCode, raw
}

func decode[T any](t *testing.T, raw []byte) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("%v in %s", err, raw)
	}
	return out
}

func (c apiClient) create(body map[string]any, wantStatus int, headers ...string) generated.Notification {
	c.t.Helper()
	status, raw := c.do(http.MethodPost, "/v1/notifications", body, headers...)
	if status != wantStatus {
		c.t.Fatalf("POST /v1/notifications: %d %s, want %d", status, raw, wantStatus)
	}
	return decode[generated.Notification](c.t, raw)
}

func (c apiClient) list(query string) generated.NotificationList {
	c.t.Helper()
	status, raw := c.do(http.MethodGet, "/v1/notifications"+query, nil)
	if status != http.StatusOK {
		c.t.Fatalf("GET /v1/notifications%s: %d %s", query, status, raw)
	}
	return decode[generated.NotificationList](c.t, raw)
}

func (c apiClient) titles(query string) []string {
	c.t.Helper()
	titles := []string{}
	for _, item := range c.list(query).Items {
		titles = append(titles, item.Title)
	}
	return titles
}

func (c apiClient) unread() generated.UnreadCount {
	c.t.Helper()
	status, raw := c.do(http.MethodGet, "/v1/notifications/unread-count", nil)
	if status != http.StatusOK {
		c.t.Fatalf("unread-count: %d %s", status, raw)
	}
	return decode[generated.UnreadCount](c.t, raw)
}

func notification(title, importance, category, key string) map[string]any {
	return map[string]any{"title": title, "importance": importance, "category": category, "dedupeKey": key}
}

func TestSendStoresTheNotificationAndDedupes(t *testing.T) {
	c := newAPIClient(t)
	body := notification("Dentist in 30 minutes", "medium", "calendar.reminder", "calendar.reminder:e1:30")
	body["body"] = "Dr. Weber, Darmstadt"
	body["link"] = "/calendar?date=2026-10-10"
	occurred := time.Date(2026, 10, 10, 8, 30, 0, 0, time.UTC)
	body["occurredAt"] = occurred

	first := c.create(body, http.StatusCreated, activity.ActorHeader, "system:calendar", activity.CorrelationIDHeader, "workflow-1")
	if first.Title != "Dentist in 30 minutes" || first.Body != "Dr. Weber, Darmstadt" || first.Importance != generated.Medium ||
		first.Category != "calendar.reminder" || first.Source != "calendar" || first.Link == nil || *first.Link != "/calendar?date=2026-10-10" ||
		first.DedupeKey != "calendar.reminder:e1:30" || !first.OccurredAt.Equal(occurred) || first.ReadAt != nil ||
		first.CreatedAt.IsZero() || first.Deliveries == nil || len(first.Deliveries) != 0 {
		t.Fatalf("created = %+v", first)
	}
	if *c.woken != 0 {
		t.Error("with no channel, the worker is not woken")
	}

	// A repeat (even with other fields) returns the original unchanged.
	repeat := notification("Changed title", "high", "calendar.reminder", "calendar.reminder:e1:30")
	again := c.create(repeat, http.StatusOK, activity.ActorHeader, "system:calendar")
	if again.Id != first.Id || again.Title != first.Title || again.Importance != generated.Medium {
		t.Fatalf("repeat = %+v, want the original %+v", again, first)
	}
	if got := c.list(""); len(got.Items) != 1 {
		t.Fatalf("list after repeat = %d items, want 1", len(got.Items))
	}

	events := c.activity.all()
	if len(events) != 1 {
		t.Fatalf("activity events = %d, want only notification.created (not for the repeat): %v", len(events), events)
	}
	event := events[0]
	data, _ := event["data"].(map[string]any)
	actor, _ := event["actor"].(map[string]any)
	subject, _ := event["subject"].(map[string]any)
	if event["type"] != "notification.created" || event["source"] != "notifications" || event["correlationId"] != "workflow-1" ||
		actor["kind"] != "system" || actor["name"] != "calendar" || subject["type"] != "notification" || subject["id"] != first.Id.String() ||
		data["importance"] != "medium" || data["category"] != "calendar.reminder" || data["source"] != "calendar" ||
		data["link"] != "/calendar?date=2026-10-10" || !strings.Contains(event["summary"].(string), "Dentist in 30 minutes") {
		t.Fatalf("notification.created = %v", event)
	}
	raw, _ := json.Marshal(event)
	if strings.Contains(string(raw), "Weber") {
		t.Fatalf("the activity event carries the body: %s", raw)
	}
}

func TestConcurrentSendsWithOneKeyStoreOneNotification(t *testing.T) {
	c := newAPIClient(t, "log")
	const senders = 6
	ids := make(chan string, senders)
	statuses := make(chan int, senders)
	var wg sync.WaitGroup
	for range senders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, raw := c.do(http.MethodPost, "/v1/notifications", notification("Service down", "high", "status.service_down", "status.service_down:tasks:1"))
			statuses <- status
			var out generated.Notification
			_ = json.Unmarshal(raw, &out)
			ids <- out.Id.String()
		}()
	}
	wg.Wait()
	close(ids)
	close(statuses)
	created, seen := 0, map[string]bool{}
	for status := range statuses {
		if status == http.StatusCreated {
			created++
		} else if status != http.StatusOK {
			t.Errorf("status = %d", status)
		}
	}
	for id := range ids {
		seen[id] = true
	}
	if created != 1 || len(seen) != 1 {
		t.Fatalf("created = %d, distinct ids = %v; want exactly one notification", created, seen)
	}
	if items := c.list("").Items; len(items) != 1 || len(items[0].Deliveries) != 1 {
		t.Fatalf("items = %+v, want one notification with one delivery", items)
	}
}

func TestSendQueuesOneDeliveryPerChannel(t *testing.T) {
	c := newAPIClient(t, "log", "other")
	created := c.create(notification("Inbox needs you", "medium", "inbox.human_review", "inbox.human_review:i1"), http.StatusCreated)
	if len(created.Deliveries) != 2 || *c.woken != 1 {
		t.Fatalf("deliveries = %+v, woken = %d", created.Deliveries, *c.woken)
	}
	channels := map[string]bool{}
	for _, d := range created.Deliveries {
		channels[d.Channel] = true
		if d.Status != generated.Pending || d.Attempts != 0 || d.NextAttemptAt == nil || d.SentAt != nil || d.LastError != nil {
			t.Errorf("delivery = %+v, want a fresh pending one", d)
		}
	}
	if !channels["log"] || !channels["other"] {
		t.Fatalf("channels = %v", channels)
	}
	status, raw := c.do(http.MethodGet, "/v1/notifications/"+created.Id.String(), nil)
	got := decode[generated.Notification](t, raw)
	if status != http.StatusOK || got.Id != created.Id || len(got.Deliveries) != 2 {
		t.Fatalf("GET = %d %s", status, raw)
	}
	data, _ := c.activity.all()[0]["data"].(map[string]any)
	if fmt.Sprint(data["channels"]) != "[log other]" {
		t.Fatalf("activity channels = %v", data["channels"])
	}
}

func TestSourceDefaultsFromTheActor(t *testing.T) {
	c := newAPIClient(t)
	for index, test := range []struct{ actor, want string }{
		{"user", "dashboard"},
		{"system:calendar", "calendar"},
		{"agent:codex-inbox-processor", "codex-inbox-processor"},
		{"dev:claude-code", "dev"},
	} {
		got := c.create(notification("Hello", "low", "test.manual", fmt.Sprintf("test.manual:%d", index)), http.StatusCreated, activity.ActorHeader, test.actor)
		if got.Source != test.want {
			t.Errorf("source for %s = %q, want %q", test.actor, got.Source, test.want)
		}
	}
	explicit := notification("Hello", "low", "test.manual", "test.manual:explicit")
	explicit["source"] = "finance"
	if got := c.create(explicit, http.StatusCreated, activity.ActorHeader, "user"); got.Source != "finance" {
		t.Errorf("explicit source = %q", got.Source)
	}
}

func TestListPagesNewestFirstWithFilters(t *testing.T) {
	c := newAPIClient(t)
	seed := []struct{ title, importance, category, actor string }{
		{"one", "low", "finance.summary", "system:finance"},
		{"two", "medium", "calendar.reminder", "system:calendar"},
		{"three", "high", "status.service_down", "system:monitor"},
		{"four", "medium", "inbox.human_review", "system:inbox"},
		{"five", "medium", "calendar.reminder.extra", "system:calendar"},
		{"six", "low", "calendarx.other", "system:calendar"},
		{"seven", "high", "inbox.humanxreview", "system:inbox"},
	}
	for index, s := range seed {
		c.create(notification(s.title, s.importance, s.category, fmt.Sprintf("seed:%d", index)), http.StatusCreated, activity.ActorHeader, s.actor)
	}

	all := []string{"seven", "six", "five", "four", "three", "two", "one"}
	var paged []string
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > 10 {
			t.Fatal("paging does not end")
		}
		query := "?limit=3"
		if cursor != "" {
			query += "&cursor=" + url.QueryEscape(cursor)
		}
		page := c.list(query)
		for _, item := range page.Items {
			paged = append(paged, item.Title)
		}
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	if fmt.Sprint(paged) != fmt.Sprint(all) {
		t.Fatalf("paged = %v, want %v", paged, all)
	}
	if page := c.list("?limit=7"); page.NextCursor != nil {
		t.Error("a page holding the last item has no nextCursor")
	}

	for query, want := range map[string][]string{
		"?importance=medium":                   {"five", "four", "two"},
		"?category=calendar":                   {"five", "two"},
		"?category=calendar.reminder":          {"five", "two"},
		"?category=calendar.reminder.extra":    {"five"},
		"?category=inbox.human_review":         {"four"},
		"?source=calendar":                     {"six", "five", "two"},
		"?source=calendar&importance=low":      {"six"},
		"?category=calendar&importance=medium": {"five", "two"},
	} {
		if got := c.titles(query); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("GET %s = %v, want %v", query, got, want)
		}
	}
}

func TestReadStateAndUnreadCount(t *testing.T) {
	c := newAPIClient(t)
	low := c.create(notification("low", "low", "test.manual", "r:1"), http.StatusCreated)
	high := c.create(notification("high", "high", "test.manual", "r:2"), http.StatusCreated)
	c.create(notification("high two", "high", "test.manual", "r:3"), http.StatusCreated)
	if got := c.unread(); got.Count != 3 || got.HighCount != 2 {
		t.Fatalf("unread = %+v", got)
	}

	status, raw := c.do(http.MethodPost, "/v1/notifications/"+high.Id.String()+"/read", nil)
	read := decode[generated.Notification](t, raw)
	if status != http.StatusOK || read.ReadAt == nil {
		t.Fatalf("read = %d %s", status, raw)
	}
	_, raw = c.do(http.MethodPost, "/v1/notifications/"+high.Id.String()+"/read", nil)
	if again := decode[generated.Notification](t, raw); again.ReadAt == nil || !again.ReadAt.Equal(*read.ReadAt) {
		t.Fatalf("marking read again changed readAt: %v → %v", read.ReadAt, again.ReadAt)
	}
	if got := c.unread(); got.Count != 2 || got.HighCount != 1 {
		t.Fatalf("unread after one read = %+v", got)
	}
	if got := c.titles("?unread=true"); fmt.Sprint(got) != "[high two low]" {
		t.Fatalf("unread list = %v", got)
	}
	if got := c.titles("?unread=false"); fmt.Sprint(got) != "[high]" {
		t.Fatalf("read list = %v", got)
	}

	// before: only notifications created at or before the instant.
	_, raw = c.do(http.MethodPost, "/v1/notifications/read-all", map[string]any{"before": low.CreatedAt})
	if got := decode[generated.MarkAllReadResult](t, raw); got.Marked != 1 {
		t.Fatalf("read-all before = %+v, want 1", got)
	}
	if got := c.titles("?unread=true"); fmt.Sprint(got) != "[high two]" {
		t.Fatalf("unread after read-all before = %v", got)
	}
	status, raw = c.do(http.MethodPost, "/v1/notifications/read-all", nil)
	if got := decode[generated.MarkAllReadResult](t, raw); status != http.StatusOK || got.Marked != 1 {
		t.Fatalf("read-all = %d %s", status, raw)
	}
	if got := c.unread(); got.Count != 0 || got.HighCount != 0 {
		t.Fatalf("unread after read-all = %+v", got)
	}
}

func TestUnknownNotificationIsNotFound(t *testing.T) {
	c := newAPIClient(t)
	const missing = "/v1/notifications/7f8d2b1e-1c1a-4b7e-9a51-2f0c6d1e9a10"
	for _, request := range []struct{ method, target string }{{http.MethodGet, missing}, {http.MethodPost, missing + "/read"}} {
		status, raw := c.do(request.method, request.target, nil)
		if status != http.StatusNotFound || decode[errorEnvelope](t, raw).Error.Code != "not_found" {
			t.Errorf("%s %s = %d %s, want 404 not_found", request.method, request.target, status, raw)
		}
	}
}

func TestDatabaseRejectsWhatTheAPIRejects(t *testing.T) {
	pool := testdb.Open(t)
	for _, insert := range []string{
		`INSERT INTO notifications (title, importance, category, source, dedupe_key, occurred_at) VALUES ('x', 'urgent', 'a.b', 's', 'k1', now())`,
		`INSERT INTO notifications (title, importance, category, source, dedupe_key, occurred_at) VALUES ('x', 'low', 'nodot', 's', 'k2', now())`,
		`INSERT INTO notifications (title, importance, category, source, link, dedupe_key, occurred_at) VALUES ('x', 'low', 'a.b', 's', '//evil', 'k3', now())`,
		`INSERT INTO notifications (title, importance, category, source, link, dedupe_key, occurred_at) VALUES ('x', 'low', 'a.b', 's', '/\evil', 'k4', now())`,
		`INSERT INTO notifications (title, importance, category, source, dedupe_key, occurred_at) VALUES (' ', 'low', 'a.b', 's', 'k5', now())`,
	} {
		if _, err := pool.Exec(context.Background(), insert); err == nil {
			t.Errorf("accepted: %s", insert)
		}
	}
}

func TestMigrationsRollBack(t *testing.T) {
	pool := testdb.Open(t)
	for _, path := range testdb.Migrations(t, "down") {
		testdb.Apply(t, pool, path)
	}
	var tables int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM information_schema.tables WHERE table_schema = current_schema()`).Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("tables after rolling back = %d, %v", tables, err)
	}
	for _, path := range testdb.Migrations(t, "up") {
		testdb.Apply(t, pool, path)
	}
}
