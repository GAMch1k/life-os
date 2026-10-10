package api

import (
	"encoding/base64"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCursorRoundTrip(t *testing.T) {
	at := time.Date(2026, 10, 10, 12, 0, 0, 123456000, time.FixedZone("CEST", 2*3600))
	id := uuid.New()
	gotAt, gotID, err := decodeCursor(encodeCursor(at, id))
	if err != nil {
		t.Fatal(err)
	}
	if !gotAt.Equal(at) || gotID != id {
		t.Fatalf("decoded (%v, %v), want (%v, %v)", gotAt, gotID, at, id)
	}
}

func TestCursorRejectsGarbage(t *testing.T) {
	for _, cursor := range []string{
		"not base64!",
		base64.RawURLEncoding.EncodeToString([]byte("no separator")),
		base64.RawURLEncoding.EncodeToString([]byte("yesterday|" + uuid.NewString())),
		base64.RawURLEncoding.EncodeToString([]byte(time.Now().Format(time.RFC3339Nano) + "|not-a-uuid")),
	} {
		if _, _, err := decodeCursor(cursor); err == nil {
			t.Errorf("decodeCursor(%q) error = nil", cursor)
		}
	}
}
