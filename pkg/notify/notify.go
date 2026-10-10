// Package notify is the client services use to let the owner know about
// something. It sends notifications to the Notifications service, the one
// path for every notification in Life OS: Notifications stores each one for
// the Dashboard and delivers it through the channels configured there.
// Producers never talk to a channel (ntfy, the Android app) themselves.
//
// The Notifications OpenAPI contract
// (services/notifications/api/openapi.yaml) is the source of truth for the
// shape mirrored by Notification.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/GAMch1k/life-os/pkg/activity"
)

// EnvURL names the environment variable with the Notifications service's
// base URL, for example http://notifications:8080. Empty disables sending.
const EnvURL = "NOTIFICATIONS_SERVICE_URL"

// DefaultTimeout bounds one Send, retries included.
const DefaultTimeout = 5 * time.Second

// Importance says how urgently the owner should hear about something.
type Importance string

const (
	// High: something is broken or needs action now (a service went down).
	High Importance = "high"
	// Medium: worth knowing soon (Inbox needs Human Review, a Calendar
	// reminder).
	Medium Importance = "medium"
	// Low: informational; channels deliver it silently.
	Low Importance = "low"
)

// Notification is one message for the owner.
type Notification struct {
	// Title is one line of at most 200 characters.
	Title string
	// Body is optional plain text of at most 2000 characters.
	Body       string
	Importance Importance
	// Category is a dotted lowercase kind such as calendar.reminder or
	// inbox.human_review.
	Category string
	// Link is an optional Life OS path to open, such as /inbox.
	Link string
	// DedupeKey is required and unique across all notifications: sending
	// the same key again delivers nothing new, so a retry or a restart is
	// harmless. Namespace it with the category, for example
	// "calendar.reminder:<eventId>:<startAt>:<minutes>".
	DedupeKey string
	// OccurredAt is when the notified thing happened; zero means now.
	OccurredAt time.Time
}

// ErrInvalid is wrapped by errors for notifications Send refuses to send.
var ErrInvalid = errors.New("invalid notification")

// StatusError is a response from the Notifications service other than
// success.
type StatusError struct {
	Status  int
	Code    string
	Message string
}

func (e *StatusError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("notifications service returned %d %s: %s", e.Status, e.Code, e.Message)
	}
	return fmt.Sprintf("notifications service returned %d", e.Status)
}

// Temporary reports whether the same request may succeed later.
func (e *StatusError) Temporary() bool {
	return e.Status >= 500 || e.Status == http.StatusTooManyRequests
}

// Client sends notifications as system:<Service>. A nil Client or one
// without a URL sends nothing and returns nil, so services run unchanged
// when Notifications is not configured.
type Client struct {
	URL     string
	Service string
	HTTP    *http.Client
	// Timeout bounds one Send, retries included; zero uses DefaultTimeout.
	Timeout time.Duration
}

// New returns a Client for the Notifications service at url, sending as
// system:<service>.
func New(url, service string) *Client {
	return &Client{URL: strings.TrimRight(strings.TrimSpace(url), "/"), Service: service, HTTP: &http.Client{}}
}

// FromEnv returns a Client for the URL in NOTIFICATIONS_SERVICE_URL.
func FromEnv(service string) *Client {
	return New(os.Getenv(EnvURL), service)
}

// Enabled reports whether Send sends anything.
func (c *Client) Enabled() bool {
	return c != nil && c.URL != ""
}

var retryDelays = []time.Duration{0, 200 * time.Millisecond, 800 * time.Millisecond}

// Send stores n in the Notifications service, which delivers it. It retries
// briefly on network errors and 5xx responses (the dedupe key makes that
// safe) and returns the last error; a *StatusError for a 4xx is not retried.
// The trace in ctx (activity.WithTrace) supplies the correlation and
// causation; the actor is always system:<Service>.
func (c *Client) Send(ctx context.Context, n Notification) error {
	if !c.Enabled() {
		return nil
	}
	actor := "system:" + c.Service
	if _, ok := activity.ParseActor(actor); !ok {
		return fmt.Errorf("%w: service name %q is not a valid actor name", ErrInvalid, c.Service)
	}
	if err := n.validate(); err != nil {
		return err
	}
	body, err := json.Marshal(n.request())
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}

	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	trace := activity.TraceFrom(ctx)
	trace.Actor = activity.Actor{}

	for _, delay := range retryDelays {
		if delay > 0 {
			select {
			case <-ctx.Done():
				return fmt.Errorf("send notification %q: %w", n.DedupeKey, errors.Join(err, ctx.Err()))
			case <-time.After(delay):
			}
		}
		err = c.post(ctx, body, actor, trace)
		var status *StatusError
		if err == nil || (errors.As(err, &status) && !status.Temporary()) {
			break
		}
	}
	if err != nil {
		return fmt.Errorf("send notification %q: %w", n.DedupeKey, err)
	}
	return nil
}

func (c *Client) post(ctx context.Context, body []byte, actor string, trace activity.Trace) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+"/v1/notifications", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("content-type", "application/json")
	trace.Apply(request.Header)
	request.Header.Set(activity.ActorHeader, actor)
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if response.StatusCode == http.StatusOK || response.StatusCode == http.StatusCreated {
		return nil
	}
	status := &StatusError{Status: response.StatusCode}
	var envelope struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	if json.Unmarshal(raw, &envelope) == nil {
		status.Code, status.Message = envelope.Error.Code, envelope.Error.Message
	}
	return status
}

func (n Notification) validate() error {
	switch {
	case strings.TrimSpace(n.Title) == "":
		return fmt.Errorf("%w: title is required", ErrInvalid)
	case n.Importance != Low && n.Importance != Medium && n.Importance != High:
		return fmt.Errorf("%w: importance must be low, medium, or high", ErrInvalid)
	case strings.TrimSpace(n.Category) == "":
		return fmt.Errorf("%w: category is required", ErrInvalid)
	case strings.TrimSpace(n.DedupeKey) == "":
		return fmt.Errorf("%w: dedupe key is required", ErrInvalid)
	}
	return nil
}

type request struct {
	Title      string     `json:"title"`
	Body       string     `json:"body,omitempty"`
	Importance Importance `json:"importance"`
	Category   string     `json:"category"`
	Link       string     `json:"link,omitempty"`
	DedupeKey  string     `json:"dedupeKey"`
	OccurredAt *time.Time `json:"occurredAt,omitempty"`
}

func (n Notification) request() request {
	r := request{Title: n.Title, Body: n.Body, Importance: n.Importance, Category: n.Category, Link: n.Link, DedupeKey: n.DedupeKey}
	if !n.OccurredAt.IsZero() {
		at := n.OccurredAt.UTC()
		r.OccurredAt = &at
	}
	return r
}
