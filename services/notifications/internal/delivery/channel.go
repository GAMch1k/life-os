// Package delivery sends stored notifications through pluggable channels.
//
// A Channel delivers one notification to one destination: the self-hosted
// ntfy server that reaches the owner's phone, or the service log. Life OS's
// own Android app later becomes another channel. The Worker claims due
// deliveries from the database with a lease, calls the channel, and records
// the outcome: sent, retried with exponential backoff, or ended (failed or
// skipped). Producers never see channels; they post notifications to the API.
package delivery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/google/uuid"
)

// Notification is what a channel receives: the stored notification without
// its delivery state.
type Notification struct {
	ID         uuid.UUID
	Title      string
	Body       string
	Importance string // low, medium, or high
	Category   string
	Source     string
	Link       string // a Life OS path such as /inbox, or empty
	OccurredAt time.Time
	CreatedAt  time.Time
}

// Channel delivers notifications to one destination.
//
// Send returns nil once the destination accepted the notification. Any other
// error is retryable unless wrapped with Permanent (the destination will
// never accept it, such as a rejected configuration) or Skip (the channel
// deliberately does not send it). Send must honour ctx's deadline.
type Channel interface {
	Name() string
	Send(ctx context.Context, notification Notification) error
}

type permanentError struct{ err error }

func (e permanentError) Error() string { return e.err.Error() }
func (e permanentError) Unwrap() error { return e.err }

// Permanent marks err as one no retry can fix; the delivery fails at once.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return permanentError{err: err}
}

// IsPermanent reports whether err was marked Permanent.
func IsPermanent(err error) bool {
	var permanent permanentError
	return errors.As(err, &permanent)
}

type skipError struct{ reason string }

func (e skipError) Error() string { return e.reason }

// Skip is returned by a channel that deliberately does not deliver a
// notification; the delivery ends as skipped with reason.
func Skip(reason string) error { return skipError{reason: reason} }

// IsSkip reports whether err is a Skip.
func IsSkip(err error) bool {
	var skip skipError
	return errors.As(err, &skip)
}

// LogChannel "delivers" by writing the notification's metadata to the
// service log. It is for development and verification: it exercises the
// whole delivery path without an external destination. The body is never
// logged.
type LogChannel struct{ Logger *slog.Logger }

func (LogChannel) Name() string { return "log" }

func (c LogChannel) Send(ctx context.Context, n Notification) error {
	logger := c.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger.InfoContext(ctx, "notification delivered to the log channel",
		"notification", n.ID, "importance", n.Importance, "category", n.Category,
		"source", n.Source, "title", n.Title)
	return nil
}

// Settings is what the channel constructors may use: the service logger and
// each channel's own configuration. A channel only reads its own part.
type Settings struct {
	Logger *slog.Logger
	Ntfy   NtfyConfig
}

// constructors builds each known channel.
var constructors = map[string]func(Settings) (Channel, error){
	"log":  func(settings Settings) (Channel, error) { return LogChannel{Logger: settings.Logger}, nil },
	"ntfy": func(settings Settings) (Channel, error) { return NewNtfy(settings.Ntfy) },
}

// Build returns the channels named in NOTIFICATIONS_CHANNELS, in order. An
// unknown name, or a channel missing its configuration, is an error, so a
// typo stops the service instead of silently delivering nothing.
func Build(names []string, settings Settings) ([]Channel, error) {
	channels := make([]Channel, 0, len(names))
	for _, name := range names {
		build, ok := constructors[name]
		if !ok {
			known := make([]string, 0, len(constructors))
			for key := range constructors {
				known = append(known, key)
			}
			sort.Strings(known)
			return nil, fmt.Errorf("unknown notification channel %q (known: %v)", name, known)
		}
		channel, err := build(settings)
		if err != nil {
			return nil, fmt.Errorf("configure notification channel %q: %w", name, err)
		}
		channels = append(channels, channel)
	}
	return channels, nil
}

// Names lists the channels' names.
func Names(channels []Channel) []string {
	names := make([]string, 0, len(channels))
	for _, channel := range channels {
		names = append(names, channel.Name())
	}
	return names
}
