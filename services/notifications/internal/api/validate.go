package api

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/notifications/internal/api/generated"
	store "github.com/GAMch1k/life-os/services/notifications/internal/repository/generated"
)

const (
	maxTitle     = 200
	maxBody      = 2000
	maxCategory  = 100
	maxSource    = 64
	maxLink      = 500
	maxDedupeKey = 200
)

var (
	// category is a dotted lowercase kind with at least two parts, such as
	// calendar.reminder.
	category = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)
	// categoryFilter also accepts a single part, as a prefix such as calendar.
	categoryFilter = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$`)
	source         = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
)

// newNotification validates a send request from actor and returns the row
// to insert. now is the default occurredAt.
func newNotification(body generated.CreateNotificationRequest, actor activity.Actor, now time.Time) (store.InsertNotificationParams, error) {
	title := strings.TrimSpace(body.Title)
	switch {
	case title == "":
		return store.InsertNotificationParams{}, errors.New("title is required")
	case !utf8.ValidString(title) || utf8.RuneCountInString(title) > maxTitle:
		return store.InsertNotificationParams{}, fmt.Errorf("title must be at most %d characters", maxTitle)
	case strings.IndexFunc(title, unicode.IsControl) >= 0:
		return store.InsertNotificationParams{}, errors.New("title must be one line without control characters")
	}

	text := ""
	if body.Body != nil {
		text = strings.TrimSpace(strings.ReplaceAll(*body.Body, "\r\n", "\n"))
	}
	switch {
	case !utf8.ValidString(text) || utf8.RuneCountInString(text) > maxBody:
		return store.InsertNotificationParams{}, fmt.Errorf("body must be at most %d characters", maxBody)
	case strings.IndexFunc(text, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' }) >= 0:
		return store.InsertNotificationParams{}, errors.New("body must be plain text: only line breaks and tabs may be control characters")
	}

	if !body.Importance.Valid() {
		return store.InsertNotificationParams{}, errors.New("importance must be low, medium, or high")
	}
	if len(body.Category) > maxCategory || !category.MatchString(body.Category) {
		return store.InsertNotificationParams{}, errors.New("category must be a dotted lowercase name with at least two parts, such as calendar.reminder")
	}

	from := sourceOf(actor)
	if body.Source != nil {
		if !validSource(*body.Source) {
			return store.InsertNotificationParams{}, fmt.Errorf("source must be 1-%d lowercase letters, digits, '.', or '-', starting with a letter or digit, such as calendar", maxSource)
		}
		from = *body.Source
	}

	var link *string
	if body.Link != nil {
		if err := validLink(*body.Link); err != nil {
			return store.InsertNotificationParams{}, err
		}
		link = body.Link
	}

	key := body.DedupeKey
	switch {
	case strings.TrimSpace(key) == "":
		return store.InsertNotificationParams{}, errors.New("dedupeKey is required")
	case strings.TrimSpace(key) != key:
		return store.InsertNotificationParams{}, errors.New("dedupeKey must not start or end with spaces")
	case !utf8.ValidString(key) || utf8.RuneCountInString(key) > maxDedupeKey:
		return store.InsertNotificationParams{}, fmt.Errorf("dedupeKey must be at most %d characters", maxDedupeKey)
	case strings.IndexFunc(key, unicode.IsControl) >= 0:
		return store.InsertNotificationParams{}, errors.New("dedupeKey must not contain control characters")
	}

	occurredAt := now
	if body.OccurredAt != nil {
		if body.OccurredAt.IsZero() {
			return store.InsertNotificationParams{}, errors.New("occurredAt must be an RFC 3339 date-time")
		}
		occurredAt = *body.OccurredAt
	}

	return store.InsertNotificationParams{
		Title:      title,
		Body:       text,
		Importance: string(body.Importance),
		Category:   body.Category,
		Source:     from,
		Link:       link,
		DedupeKey:  key,
		OccurredAt: pgtype.Timestamptz{Time: occurredAt.UTC(), Valid: true},
	}, nil
}

// sourceOf is the default source for a notification sent by actor: the
// Dashboard for the owner, "dev" for any development tool, and otherwise the
// component's or agent's name (system:calendar → calendar).
func sourceOf(actor activity.Actor) string {
	switch actor.Kind {
	case "user":
		return "dashboard"
	case "dev":
		return "dev"
	case "system", "agent":
		name := strings.Trim(strings.ReplaceAll(strings.ToLower(actor.Name), "_", "-"), ".-")
		if validSource(name) {
			return name
		}
		return actor.Kind
	}
	return "unknown"
}

func validSource(value string) bool {
	return len(value) <= maxSource && source.MatchString(value)
}

// validLink accepts a Life OS path: one leading '/', no scheme or host, no
// backslashes, spaces, or control characters. Query and fragment are kept.
func validLink(link string) error {
	invalid := fmt.Errorf("link must be a Life OS path starting with a single /, such as /inbox (at most %d characters, no absolute URLs)", maxLink)
	if link == "" || len(link) > maxLink || link[0] != '/' || strings.HasPrefix(link, "//") ||
		strings.ContainsRune(link, '\\') || strings.IndexFunc(link, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return invalid
	}
	parsed, err := url.Parse(link)
	if err != nil || parsed.Scheme != "" || parsed.Host != "" || parsed.Opaque != "" || parsed.User != nil {
		return invalid
	}
	return nil
}
