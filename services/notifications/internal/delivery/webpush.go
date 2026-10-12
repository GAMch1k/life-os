package delivery

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	store "github.com/GAMch1k/life-os/services/notifications/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/notifications/internal/webpush"
)

// WebPushConfig configures the webpush channel: Web Push to every browser
// that turned on notifications in the Dashboard, above all the installed
// Life OS app on the owner's phone. Pushes travel through the browser
// vendor's push service (FCM for Chrome on Android), end-to-end encrypted.
type WebPushConfig struct {
	// PublicKey and PrivateKey are the VAPID key pair, base64url (go run
	// ./cmd/vapid-keys). The private key is a secret: never logged or put in
	// an error.
	PublicKey  string
	PrivateKey string
	// Subject is how the push service's operator reaches the sender: a
	// mailto: address or Life OS's https URL.
	Subject string
	// Store holds the subscriptions and each device's outcome per
	// notification.
	Store PushStore
	// Client sends the pushes; nil uses one that never follows redirects.
	Client *http.Client
	Logger *slog.Logger
}

// Set reports whether a VAPID key is configured at all: then web push is
// meant to work, and a missing or broken setting stops the service.
func (c WebPushConfig) Set() bool {
	return strings.TrimSpace(c.PublicKey) != "" || strings.TrimSpace(c.PrivateKey) != ""
}

// PushStore is the persistence the webpush channel needs; *store.Queries
// satisfies it.
type PushStore interface {
	ListPushTargets(context.Context, store.ListPushTargetsParams) ([]store.PushSubscription, error)
	RecordPushSent(context.Context, store.RecordPushSentParams) error
	RecordPushFailed(context.Context, store.RecordPushFailedParams) error
	CountPushOutcomes(context.Context, pgtype.UUID) (store.CountPushOutcomesRow, error)
	MarkPushSuccess(context.Context, pgtype.UUID) error
	MarkPushFailure(context.Context, store.MarkPushFailureParams) error
	DeletePushSubscription(context.Context, pgtype.UUID) (int64, error)
}

// WebPushChannel delivers each notification to every subscribed device.
//
// A notification has one webpush delivery, whatever the number of devices.
// Each attempt sends only to the devices that have no outcome for it yet,
// all at once, and records each device's outcome: accepted (sent), refused
// for good (failed), or gone (404 or 410: the subscription is deleted). A
// device that failed for now (unreachable, 429, 5xx) gets no outcome, so
// the delivery is retried for it alone; the devices that have one never get
// the notification twice.
type WebPushChannel struct {
	sender *webpush.Sender
	store  PushStore
	logger *slog.Logger
}

// pushConcurrency bounds the pushes in flight for one notification.
const pushConcurrency = 4

// pushBookkeepingTimeout bounds recording the outcomes after the pushes,
// which may have used up the attempt's time.
const pushBookkeepingTimeout = 5 * time.Second

// NewWebPush returns the webpush channel, or an error naming what is
// missing or wrong.
func NewWebPush(cfg WebPushConfig) (*WebPushChannel, error) {
	var problems []error
	var keys *webpush.Keys
	if strings.TrimSpace(cfg.PublicKey) == "" || strings.TrimSpace(cfg.PrivateKey) == "" {
		problems = append(problems, errors.New("NOTIFICATIONS_WEBPUSH_VAPID_PUBLIC_KEY and NOTIFICATIONS_WEBPUSH_VAPID_PRIVATE_KEY must both be set; generate them with `go run ./cmd/vapid-keys` in services/notifications"))
	} else if parsed, err := webpush.ParseKeys(cfg.PublicKey, cfg.PrivateKey); err != nil {
		problems = append(problems, fmt.Errorf("NOTIFICATIONS_WEBPUSH_VAPID_PUBLIC_KEY and NOTIFICATIONS_WEBPUSH_VAPID_PRIVATE_KEY: %w", err))
	} else {
		keys = parsed
	}
	if !webpush.ValidSubject(cfg.Subject) {
		problems = append(problems, errors.New("NOTIFICATIONS_WEBPUSH_SUBJECT must be a mailto: address or Life OS's https URL (by default NOTIFICATIONS_LINK_BASE_URL)"))
	}
	if cfg.Store == nil {
		problems = append(problems, errors.New("web push needs its subscription store"))
	}
	if len(problems) > 0 {
		return nil, errors.Join(problems...)
	}
	sender, err := webpush.NewSender(keys, cfg.Subject, cfg.Client)
	if err != nil {
		return nil, err
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &WebPushChannel{sender: sender, store: cfg.Store, logger: logger}, nil
}

func (*WebPushChannel) Name() string { return "webpush" }

// String never shows the private key.
func (c *WebPushChannel) String() string { return "webpush(" + c.sender.PublicKey() + ")" }

// PublicKey is the VAPID public key browsers subscribe with.
func (c *WebPushChannel) PublicKey() string { return c.sender.PublicKey() }

// PushPayload is what the service worker receives (apps/dashboard/public/
// sw.js), as JSON of at most webpush.MaxPayload bytes.
type PushPayload struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Body  string `json:"body"`
	// Link is the Life OS path to open on a tap, such as /calendar?date=…;
	// /notifications when the notification has none.
	Link string `json:"link"`
	// Tag collapses notifications on the device: one with the tag of a
	// shown one replaces it. It is the notification's id, so a repeated
	// delivery replaces the first instead of showing twice, and two
	// notifications of one kind (two reminders) both stay.
	Tag        string `json:"tag"`
	Importance string `json:"importance"`
	Category   string `json:"category"`
	// Timestamp is when Notifications received it, in Unix milliseconds.
	Timestamp int64 `json:"timestamp"`
}

// PushUrgency is how urgently the push service wakes the device for an
// importance, and how long it keeps the push for a device that is offline:
// high is urgent and kept for 24 hours (the maximum delivery age), medium
// normal for 12 hours, and low may wait for the device to wake by itself
// and is kept for 4 hours.
func PushUrgency(importance string) (webpush.Urgency, time.Duration) {
	switch importance {
	case "high":
		return webpush.UrgencyHigh, 24 * time.Hour
	case "low":
		return webpush.UrgencyLow, 4 * time.Hour
	default:
		return webpush.UrgencyNormal, 12 * time.Hour
	}
}

// pushTag is the device-side tag of a notification.
func pushTag(id uuid.UUID) string { return "lifeos-" + id.String() }

// pushTopic is the push service topic of a notification (32 hex
// characters): a repeated push replaces one the push service still holds.
func pushTopic(id uuid.UUID) string { return hex.EncodeToString(id[:]) }

// EncodePushPayload is n as the service worker reads it. A body too long
// for one push is cut (on a character boundary, with "…") to the longest
// that fits.
func EncodePushPayload(n Notification, tag string) ([]byte, error) {
	link := n.Link
	if link == "" {
		link = NtfyFallbackLink
	}
	payload := PushPayload{
		ID:         n.ID.String(),
		Title:      n.Title,
		Body:       n.Body,
		Link:       link,
		Tag:        tag,
		Importance: n.Importance,
		Category:   n.Category,
		Timestamp:  n.CreatedAt.UnixMilli(),
	}
	raw, err := encodePayload(payload)
	if err != nil || len(raw) <= webpush.MaxPayload {
		return raw, err
	}
	// The longest cut of the body that fits: escaping makes the JSON
	// longer than the text by a varying amount, so search for it.
	body := n.Body
	var best []byte
	low, high := len("…")+1, len(body)-1
	for low <= high {
		limit := (low + high) / 2
		payload.Body = truncateBytes(body, limit)
		if raw, err = encodePayload(payload); err != nil {
			return nil, err
		}
		if len(raw) <= webpush.MaxPayload {
			best, low = raw, limit+1
		} else {
			high = limit - 1
		}
	}
	if best != nil {
		return best, nil
	}
	payload.Body = ""
	if raw, err = encodePayload(payload); err != nil {
		return nil, err
	}
	if len(raw) > webpush.MaxPayload {
		return nil, fmt.Errorf("the push payload is %d bytes without a body, more than %d", len(raw), webpush.MaxPayload)
	}
	return raw, nil
}

// encodePayload is the payload's JSON, without HTML escaping.
func encodePayload(payload PushPayload) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(payload); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n")), nil
}

func (c *WebPushChannel) message(n Notification, tag, topic string) (webpush.Message, error) {
	payload, err := EncodePushPayload(n, tag)
	if err != nil {
		return webpush.Message{}, err
	}
	urgency, ttl := PushUrgency(n.Importance)
	return webpush.Message{Payload: payload, TTL: ttl, Urgency: urgency, Topic: topic}, nil
}

type pushResult struct {
	device store.PushSubscription
	err    error
}

// sendAll pushes message to every device, a few at a time.
func (c *WebPushChannel) sendAll(ctx context.Context, devices []store.PushSubscription, message webpush.Message) []pushResult {
	results := make([]pushResult, len(devices))
	slots := make(chan struct{}, pushConcurrency)
	var wg sync.WaitGroup
	for i, device := range devices {
		wg.Go(func() {
			slots <- struct{}{}
			defer func() { <-slots }()
			subscription := webpush.Subscription{Endpoint: device.Endpoint, P256dh: device.P256dh, Auth: device.Auth}
			results[i] = pushResult{device: device, err: c.sender.Send(ctx, subscription, message)}
		})
	}
	wg.Wait()
	return results
}

// Send pushes n to the devices that were subscribed when it was created and
// have no outcome for it yet. It returns nil once every device has an
// outcome and at least one accepted it; a retryable error while any device
// failed for now (only those are tried again); Permanent when every device
// refused it or is gone; and Skip when no device is subscribed.
func (c *WebPushChannel) Send(ctx context.Context, n Notification) error {
	id := pgtype.UUID{Bytes: n.ID, Valid: true}
	devices, err := c.store.ListPushTargets(ctx, store.ListPushTargetsParams{
		CreatedBefore:  pgtype.Timestamptz{Time: n.CreatedAt, Valid: true},
		NotificationID: id,
	})
	if err != nil {
		return fmt.Errorf("load the subscribed devices: %w", err)
	}
	message, err := c.message(n, pushTag(n.ID), pushTopic(n.ID))
	if err != nil {
		return Permanent(err)
	}
	results := c.sendAll(ctx, devices, message)

	bookkeeping, cancel := context.WithTimeout(context.WithoutCancel(ctx), pushBookkeepingTimeout)
	defer cancel()
	var retry, refused []string
	var gone int
	var recordErrors []error
	for _, result := range results {
		device := result.device
		var recordErr error
		switch {
		case result.err == nil:
			recordErr = c.store.RecordPushSent(bookkeeping, store.RecordPushSentParams{NotificationID: id, SubscriptionID: device.ID})
		case webpush.IsGone(result.err):
			gone++
			recordErr = c.forget(bookkeeping, device, result.err)
		case webpush.IsRetryable(result.err):
			text := boundError(result.err)
			retry = append(retry, device.DeviceLabel+": "+text)
			recordErr = c.store.MarkPushFailure(bookkeeping, store.MarkPushFailureParams{ID: device.ID, Error: text})
		default:
			text := boundError(result.err)
			refused = append(refused, device.DeviceLabel+": "+text)
			recordErr = c.store.RecordPushFailed(bookkeeping, store.RecordPushFailedParams{NotificationID: id, SubscriptionID: device.ID, Error: text})
		}
		if recordErr != nil {
			recordErrors = append(recordErrors, recordErr)
		}
	}
	if len(recordErrors) > 0 {
		// Retried: a device whose success was not recorded may get the
		// notification again (delivery is at least once; the tag makes the
		// phone replace it rather than show it twice).
		return fmt.Errorf("record the web push outcomes: %w", errors.Join(recordErrors...))
	}
	if len(retry) > 0 {
		return fmt.Errorf("%d of %d devices failed for now: %s", len(retry), len(devices), strings.Join(retry, "; "))
	}
	counts, err := c.store.CountPushOutcomes(bookkeeping, id)
	if err != nil {
		return fmt.Errorf("count the web push outcomes: %w", err)
	}
	switch {
	case counts.Sent > 0:
		return nil
	case counts.Failed > 0:
		if len(refused) == 0 {
			return Permanent(errors.New("every device refused the notification"))
		}
		return Permanent(fmt.Errorf("every device refused the notification: %s", strings.Join(refused, "; ")))
	case gone > 0:
		return Permanent(errors.New("every subscribed device has unsubscribed or expired (404/410); turn notifications on again in Life OS"))
	default:
		return Skip("no device has turned on notifications in Life OS")
	}
}

// forget deletes a subscription its push service says is gone.
func (c *WebPushChannel) forget(ctx context.Context, device store.PushSubscription, cause error) error {
	if _, err := c.store.DeletePushSubscription(ctx, device.ID); err != nil {
		return err
	}
	c.logger.InfoContext(ctx, "web push subscription removed: its push service says it is gone",
		"subscription", uuid.UUID(device.ID.Bytes), "device", device.DeviceLabel,
		"pushService", webpush.ServiceHost(device.Endpoint), "error", boundError(cause))
	return nil
}

// Outcomes of a test push to one device.
const (
	PushTestSent    = "sent"
	PushTestRemoved = "removed"
	PushTestFailed  = "failed"
)

// PushTestResult is a test push's outcome for one device.
type PushTestResult struct {
	Device store.PushSubscription
	// Outcome is PushTestSent, PushTestRemoved (its push service says it is
	// gone, so it was deleted), or PushTestFailed.
	Outcome string
	Error   string
}

// pushTestTag collapses test pushes: a new one replaces the last on the
// device and at the push service.
const pushTestTag = "lifeos-test"

// Test sends a test notification to each device now, outside the delivery
// queue, and records the outcome on the device: its latest success or
// failure, or its removal when it is gone. now is when the test was asked.
func (c *WebPushChannel) Test(ctx context.Context, devices []store.PushSubscription, now time.Time) []PushTestResult {
	message, err := c.message(Notification{
		ID:         uuid.New(),
		Title:      "Test notification",
		Body:       "Web push reaches this device. Tap to open Life OS.",
		Importance: "medium",
		Category:   "test.push",
		Link:       NtfyFallbackLink,
		CreatedAt:  now,
	}, pushTestTag, "lifeostest")
	results := make([]PushTestResult, 0, len(devices))
	if err != nil {
		for _, device := range devices {
			results = append(results, PushTestResult{Device: device, Outcome: PushTestFailed, Error: boundError(err)})
		}
		return results
	}
	bookkeeping, cancel := context.WithTimeout(context.WithoutCancel(ctx), pushBookkeepingTimeout)
	defer cancel()
	for _, sent := range c.sendAll(ctx, devices, message) {
		result := PushTestResult{Device: sent.device, Outcome: PushTestSent}
		var recordErr error
		switch {
		case sent.err == nil:
			recordErr = c.store.MarkPushSuccess(bookkeeping, sent.device.ID)
		case webpush.IsGone(sent.err):
			result.Outcome, result.Error = PushTestRemoved, boundError(sent.err)
			recordErr = c.forget(bookkeeping, sent.device, sent.err)
		default:
			result.Outcome, result.Error = PushTestFailed, boundError(sent.err)
			recordErr = c.store.MarkPushFailure(bookkeeping, store.MarkPushFailureParams{ID: sent.device.ID, Error: result.Error})
		}
		if recordErr != nil {
			c.logger.WarnContext(ctx, "could not record a test push's outcome", "subscription", uuid.UUID(sent.device.ID.Bytes), "error", recordErr)
		}
		results = append(results, result)
	}
	return results
}
