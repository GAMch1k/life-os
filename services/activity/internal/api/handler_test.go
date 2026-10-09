package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/GAMch1k/life-os/services/activity/internal/api/generated"
	store "github.com/GAMch1k/life-os/services/activity/internal/repository/generated"
)

type fakeStore struct {
	events map[[16]byte]store.ActivityEvent
	listed store.ListActivityEventsParams
	rows   []store.ActivityEvent
}

func (f *fakeStore) RecordActivityEvent(_ context.Context, p store.RecordActivityEventParams) (store.ActivityEvent, error) {
	if _, ok := f.events[p.ID.Bytes]; ok {
		return store.ActivityEvent{}, pgx.ErrNoRows
	}
	event := store.ActivityEvent{ID: p.ID, Type: p.Type, Source: p.Source, SubjectType: p.SubjectType, SubjectID: p.SubjectID, CorrelationID: p.CorrelationID, CausationID: p.CausationID, ActorKind: p.ActorKind, ActorName: p.ActorName, Outcome: p.Outcome, Summary: p.Summary, Data: p.Data, OccurredAt: p.OccurredAt, RecordedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true}}
	f.events[p.ID.Bytes] = event
	return event, nil
}

func (f *fakeStore) GetActivityEvent(_ context.Context, id pgtype.UUID) (store.ActivityEvent, error) {
	event, ok := f.events[id.Bytes]
	if !ok {
		return store.ActivityEvent{}, pgx.ErrNoRows
	}
	return event, nil
}

func (f *fakeStore) ListActivityEvents(_ context.Context, p store.ListActivityEventsParams) ([]store.ActivityEvent, error) {
	f.listed = p
	if int(p.PageLimit) < len(f.rows) {
		return f.rows[:p.PageLimit], nil
	}
	return f.rows, nil
}

func newApp(s *fakeStore) *fiber.App {
	app := fiber.New()
	generated.RegisterHandlers(app, NewHandler(s))
	return app
}

const validEvent = `{"id":"7b0b8c5e-4c4f-5d6e-8f00-0a1b2c3d4e5f","type":"task.completed","source":"tasks","subject":{"type":"task","id":"t-1"},"correlationId":"corr-1","actor":{"kind":"user"},"outcome":"succeeded","summary":"Task completed","data":{"title":"Buy milk"},"occurredAt":"2026-10-09T12:00:00Z"}`

func post(t *testing.T, app *fiber.App, body string) (int, string) {
	t.Helper()
	request := httptest.NewRequest("POST", "/v1/activity-events", strings.NewReader(body))
	request.Header.Set("content-type", "application/json")
	response, err := app.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(response.Body)
	return response.StatusCode, string(raw)
}

func TestRecordIsIdempotentByEventID(t *testing.T) {
	app := newApp(&fakeStore{events: map[[16]byte]store.ActivityEvent{}})
	if status, body := post(t, app, validEvent); status != 201 {
		t.Fatalf("first record: %d %s", status, body)
	}
	status, body := post(t, app, validEvent)
	if status != 200 {
		t.Fatalf("replay: %d %s", status, body)
	}
	var event generated.ActivityEvent
	if err := json.Unmarshal([]byte(body), &event); err != nil || event.Data["title"] != "Buy milk" || event.CorrelationId != "corr-1" {
		t.Fatalf("replay returned %s (%v)", body, err)
	}
}

func TestRecordRejectsMalformedEvents(t *testing.T) {
	app := newApp(&fakeStore{events: map[[16]byte]store.ActivityEvent{}})
	for name, body := range map[string]string{
		"undotted type":  strings.Replace(validEvent, `"task.completed"`, `"TaskCompleted"`, 1),
		"missing source": strings.Replace(validEvent, `"source":"tasks"`, `"source":" "`, 1),
		"bad actor":      strings.Replace(validEvent, `"kind":"user"`, `"kind":"robot"`, 1),
		"bad outcome":    strings.Replace(validEvent, `"succeeded"`, `"done"`, 1),
		"no correlation": strings.Replace(validEvent, `"corr-1"`, `""`, 1),
		"large data":     strings.Replace(validEvent, `"Buy milk"`, `"`+strings.Repeat("x", maxDataBytes)+`"`, 1),
	} {
		if status, response := post(t, app, body); status != 400 {
			t.Errorf("%s: got %d %s", name, status, response)
		}
	}
}

func TestListPagesWithKeysetCursor(t *testing.T) {
	base := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	rows := make([]store.ActivityEvent, 3)
	for index := range rows {
		rows[index] = store.ActivityEvent{ID: pgtype.UUID{Bytes: [16]byte{byte(index + 1)}, Valid: true}, Type: "task.updated", Source: "tasks", SubjectType: "task", SubjectID: "t", CorrelationID: "c", ActorKind: "user", Outcome: "succeeded", Summary: "s", Data: []byte(`{}`), OccurredAt: pgtype.Timestamptz{Time: base.Add(-time.Duration(index) * time.Minute), Valid: true}}
	}
	fake := &fakeStore{rows: rows}
	app := newApp(fake)
	response, err := app.Test(httptest.NewRequest("GET", "/v1/activity-events?limit=2&correlationId=c&outcome=succeeded", nil))
	if err != nil {
		t.Fatal(err)
	}
	var page generated.ActivityEventList
	if err := json.NewDecoder(response.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.NextCursor == nil {
		t.Fatalf("page = %+v", page)
	}
	if fake.listed.PageLimit != 3 || *fake.listed.CorrelationID != "c" || *fake.listed.Outcome != "succeeded" {
		t.Fatalf("query = %+v", fake.listed)
	}
	at, id, err := decodeCursor(*page.NextCursor)
	if err != nil || !at.Equal(rows[1].OccurredAt.Time) || id != rows[1].ID.Bytes {
		t.Fatalf("cursor decoded to %v %v %v", at, id, err)
	}
	response, _ = app.Test(httptest.NewRequest("GET", "/v1/activity-events?cursor="+*page.NextCursor, nil))
	if response.StatusCode != 200 || !fake.listed.CursorAt.Valid || fake.listed.CursorID.Bytes != rows[1].ID.Bytes {
		t.Fatalf("cursor request: %d %+v", response.StatusCode, fake.listed)
	}
	response, _ = app.Test(httptest.NewRequest("GET", "/v1/activity-events?cursor=not-a-cursor", nil))
	if response.StatusCode != 400 {
		t.Fatalf("invalid cursor status = %d", response.StatusCode)
	}
}
