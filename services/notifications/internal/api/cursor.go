package api

import (
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

// A cursor is the last row's (createdAt, id) keyset position, opaque to
// clients.
func encodeCursor(createdAt time.Time, id uuid.UUID) string {
	raw := createdAt.UTC().Format(time.RFC3339Nano) + "|" + id.String()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(cursor string) (time.Time, uuid.UUID, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, uuid.UUID{}, err
	}
	at, id, ok := strings.Cut(string(raw), "|")
	if !ok {
		return time.Time{}, uuid.UUID{}, errors.New("malformed cursor")
	}
	createdAt, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return time.Time{}, uuid.UUID{}, err
	}
	parsedID, err := uuid.Parse(id)
	if err != nil {
		return time.Time{}, uuid.UUID{}, err
	}
	return createdAt, parsedID, nil
}
