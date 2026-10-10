package api

import (
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	googleuuid "github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/GAMch1k/life-os/pkg/httpserver"
	store "github.com/GAMch1k/life-os/services/tasks/internal/repository/generated"
)

func TestCursorKeepsTheLastRowsPosition(t *testing.T) {
	at := func(value string) pgtype.Timestamptz {
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			t.Fatal(err)
		}
		return pgtype.Timestamptz{Time: parsed, Valid: true}
	}
	id := googleuuid.New()
	for name, item := range map[string]store.Task{
		"open and undated": {ID: pgtype.UUID{Bytes: id, Valid: true}, CreatedAt: at("2026-10-01T08:00:00.123456Z")},
		"open and dated":   {ID: pgtype.UUID{Bytes: id, Valid: true}, DueAt: at("2026-10-12T09:30:00Z"), CreatedAt: at("2026-10-01T08:00:00Z")},
		"completed":        {ID: pgtype.UUID{Bytes: id, Valid: true}, CompletedAt: at("2026-10-09T17:45:12.5Z"), DueAt: at("2026-10-08T00:00:00Z"), CreatedAt: at("2026-10-01T08:00:00Z")},
	} {
		position, err := decodeCursor(encodeCursor(item))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		want := cursorPosition{CompletedAt: item.CompletedAt, DueAt: item.DueAt, CreatedAt: item.CreatedAt, ID: item.ID}
		if !samePosition(position, want) {
			t.Errorf("%s: decoded %+v, want %+v", name, position, want)
		}
	}
}

func samePosition(a, b cursorPosition) bool {
	same := func(x, y pgtype.Timestamptz) bool { return x.Valid == y.Valid && x.Time.Equal(y.Time) }
	return same(a.CompletedAt, b.CompletedAt) && same(a.DueAt, b.DueAt) && same(a.CreatedAt, b.CreatedAt) && a.ID == b.ID
}

func TestListRejectsAMalformedCursorBeforeQuerying(t *testing.T) {
	app := httpserver.New("tasks", httpserver.WithLogger(httpserver.NewJSONLogger(io.Discard, nil)))
	// No database: a request that reached the query would fail with 500.
	Register(app, NewHandler(nil, nil))
	encode := func(raw string) string { return base64.RawURLEncoding.EncodeToString([]byte(raw)) }
	for _, cursor := range []string{
		"not base64!",
		encode("only|three|fields"),
		encode("||2026-10-01T08:00:00Z|not-a-uuid"),
		encode("||" + "|" + googleuuid.NewString()),
		encode("yesterday||2026-10-01T08:00:00Z|" + googleuuid.NewString()),
	} {
		req := httptest.NewRequest(http.MethodGet, "/v1/tasks?"+url.Values{"cursor": {cursor}}.Encode(), nil)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("cursor %q: status %d, want 400", cursor, resp.StatusCode)
		}
	}
}
