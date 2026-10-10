package api

import (
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	googleuuid "github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// Cursors are opaque to clients: base64url of "|"-separated fields, led by
// "list" or "search" so one kind cannot be replayed as the other.

// listPosition is an event's place in the list order (start instant, id).
type listPosition struct {
	StartsAt time.Time
	ID       googleuuid.UUID
}

func encodeListCursor(position listPosition) string {
	return encodeCursor("list", position.StartsAt.UTC().Format(time.RFC3339Nano), position.ID.String())
}

func decodeListCursor(cursor string) (listPosition, error) {
	fields, err := decodeCursor(cursor, "list", 2)
	if err != nil {
		return listPosition{}, err
	}
	startsAt, err := time.Parse(time.RFC3339Nano, fields[0])
	if err != nil {
		return listPosition{}, err
	}
	id, err := googleuuid.Parse(fields[1])
	if err != nil {
		return listPosition{}, err
	}
	return listPosition{StartsAt: startsAt, ID: id}, nil
}

// searchPosition is an event's place in the search order, plus the anchor
// instant that split upcoming from past events on the first page.
type searchPosition struct {
	Anchor    time.Time
	Relevance int32
	Bucket    int32
	SortKey   int64
	ID        googleuuid.UUID
}

func encodeSearchCursor(position searchPosition) string {
	return encodeCursor("search",
		position.Anchor.UTC().Format(time.RFC3339Nano),
		strconv.FormatInt(int64(position.Relevance), 10),
		strconv.FormatInt(int64(position.Bucket), 10),
		strconv.FormatInt(position.SortKey, 10),
		position.ID.String(),
	)
}

func decodeSearchCursor(cursor string) (searchPosition, error) {
	fields, err := decodeCursor(cursor, "search", 5)
	if err != nil {
		return searchPosition{}, err
	}
	var position searchPosition
	if position.Anchor, err = time.Parse(time.RFC3339Nano, fields[0]); err != nil {
		return searchPosition{}, err
	}
	relevance, err := strconv.ParseInt(fields[1], 10, 32)
	if err != nil {
		return searchPosition{}, err
	}
	bucket, err := strconv.ParseInt(fields[2], 10, 32)
	if err != nil || (bucket != 0 && bucket != 1) {
		return searchPosition{}, errors.New("malformed cursor")
	}
	if position.SortKey, err = strconv.ParseInt(fields[3], 10, 64); err != nil {
		return searchPosition{}, err
	}
	if position.ID, err = googleuuid.Parse(fields[4]); err != nil {
		return searchPosition{}, err
	}
	position.Relevance, position.Bucket = int32(relevance), int32(bucket)
	return position, nil
}

func encodeCursor(kind string, fields ...string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(kind + "|" + strings.Join(fields, "|")))
}

func decodeCursor(cursor, kind string, count int) ([]string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return nil, err
	}
	fields := strings.Split(string(raw), "|")
	if len(fields) != count+1 || fields[0] != kind {
		return nil, errors.New("malformed cursor")
	}
	return fields[1:], nil
}

func pgID(id googleuuid.UUID) pgtype.UUID { return pgtype.UUID{Bytes: id, Valid: true} }

// Search queries are at most this many words of at most this many characters.
const (
	maxSearchWords  = 10
	maxSearchLength = 200
)

// searchPatterns turns a query into one ILIKE pattern per distinct word
// (compared case-insensitively), each matching the word anywhere in a field.
// LIKE wildcards in the words match literally.
func searchPatterns(query string) ([]string, error) {
	if utf8.RuneCountInString(query) > maxSearchLength {
		return nil, errors.New("q must be at most 200 characters")
	}
	words := strings.Fields(query)
	if len(words) == 0 {
		return nil, errors.New("q must contain a word")
	}
	seen := map[string]bool{}
	patterns := make([]string, 0, len(words))
	for _, word := range words {
		key := strings.ToLower(word)
		if seen[key] {
			continue
		}
		seen[key] = true
		patterns = append(patterns, "%"+likeEscaper.Replace(word)+"%")
	}
	if len(patterns) > maxSearchWords {
		return nil, errors.New("q must have at most 10 words")
	}
	return patterns, nil
}

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
