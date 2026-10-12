package delivery

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	store "github.com/GAMch1k/life-os/services/notifications/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/notifications/internal/webpush"
	"github.com/GAMch1k/life-os/services/notifications/internal/webpush/webpushtest"
)

// These tests deliver through the webpush channel to a fake push service
// that checks each push's VAPID token and decrypts its payload with the
// device's private key, against a fresh schema on
// NOTIFICATIONS_TEST_DATABASE_URL.

type pushHarness struct {
	harness
	server  *webpushtest.Server
	channel *WebPushChannel
}

func newPushHarness(t *testing.T) pushHarness {
	t.Helper()
	h := newHarness(t)
	public, private, err := webpush.GenerateKeys()
	if err != nil {
		t.Fatal(err)
	}
	server := webpushtest.NewServer(t, public)
	channel, err := NewWebPush(WebPushConfig{
		PublicKey: public, PrivateKey: private, Subject: "https://lifeos.example.com",
		Store: h.q, Client: server.Client(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return pushHarness{harness: h, server: server, channel: channel}
}

// subscribe stores a subscription for a fake device, as the API would.
func (h pushHarness) subscribe(device *webpushtest.Device) store.PushSubscription {
	h.t.Helper()
	return h.subscribeAt(device.Subscription.Endpoint, device.Subscription.P256dh, device.Subscription.Auth, device.Name)
}

func (h pushHarness) subscribeAt(endpoint, p256dh, auth, label string) store.PushSubscription {
	h.t.Helper()
	row, err := h.q.UpsertPushSubscription(context.Background(), store.UpsertPushSubscriptionParams{
		Endpoint: endpoint, P256dh: p256dh, Auth: auth, DeviceLabel: label,
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return store.PushSubscription{ID: row.ID, Endpoint: row.Endpoint, P256dh: row.P256dh, Auth: row.Auth, DeviceLabel: row.DeviceLabel, CreatedAt: row.CreatedAt}
}

func (h pushHarness) devices() map[string]store.PushSubscription {
	h.t.Helper()
	rows, err := h.q.ListPushSubscriptions(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	byLabel := map[string]store.PushSubscription{}
	for _, row := range rows {
		byLabel[row.DeviceLabel] = row
	}
	return byLabel
}

func (h pushHarness) outcomes(row store.Notification) map[string]string {
	h.t.Helper()
	rows, err := h.pool.Query(context.Background(),
		`SELECT s.device_label, o.status FROM push_outcomes o JOIN push_subscriptions s ON s.id = o.subscription_id WHERE o.notification_id = $1`, row.ID)
	if err != nil {
		h.t.Fatal(err)
	}
	defer rows.Close()
	result := map[string]string{}
	for rows.Next() {
		var label, status string
		if err := rows.Scan(&label, &status); err != nil {
			h.t.Fatal(err)
		}
		result[label] = status
	}
	return result
}

func decodePayload(t *testing.T, push webpushtest.Push) PushPayload {
	t.Helper()
	var payload PushPayload
	if err := json.Unmarshal(push.Payload, &payload); err != nil {
		t.Fatalf("payload %q: %v", push.Payload, err)
	}
	return payload
}

func TestWebPushDeliversToEveryDevice(t *testing.T) {
	h := newPushHarness(t)
	h.subscribe(h.server.NewDevice("phone"))
	h.subscribe(h.server.NewDevice("laptop"))
	row := h.seed("k1", "webpush")
	w := h.worker("w1", nil, h.channel)

	h.process(w)
	d := h.delivery(row, "webpush")
	if d.Status != StatusSent || d.Attempts != 1 {
		t.Fatalf("delivery = %+v (error %v)", d, d.LastError)
	}
	id := uuid.UUID(row.ID.Bytes)
	for _, name := range []string{"phone", "laptop"} {
		pushes := h.server.PushesTo(name)
		if len(pushes) != 1 {
			t.Fatalf("%s got %d pushes", name, len(pushes))
		}
		payload := decodePayload(t, pushes[0])
		if payload.Title != "Inbox needs you" || payload.Body != "private detail" || payload.Link != "/inbox" ||
			payload.Tag != "lifeos-"+id.String() || payload.Importance != "medium" || payload.Category != "inbox.human_review" ||
			payload.ID != id.String() || payload.Timestamp != row.CreatedAt.Time.UnixMilli() {
			t.Fatalf("%s payload = %+v", name, payload)
		}
		if pushes[0].TTL != "43200" || pushes[0].Urgency != "normal" || pushes[0].Topic != hex.EncodeToString(id[:]) {
			t.Fatalf("%s headers TTL %q Urgency %q Topic %q", name, pushes[0].TTL, pushes[0].Urgency, pushes[0].Topic)
		}
		if pushes[0].Subject != "https://lifeos.example.com" {
			t.Fatalf("VAPID subject = %q", pushes[0].Subject)
		}
	}
	if got := h.outcomes(row); got["phone"] != "sent" || got["laptop"] != "sent" {
		t.Fatalf("outcomes = %v", got)
	}
	for name, device := range h.devices() {
		if !device.LastSuccessAt.Valid || device.LastFailureAt.Valid {
			t.Fatalf("%s = %+v", name, device)
		}
	}
	events := h.recorded()
	if len(events) != 1 || events[0]["type"] != "notification.delivered" {
		t.Fatalf("events = %v", events)
	}
}

func TestWebPushPrunesGoneDevices(t *testing.T) {
	h := newPushHarness(t)
	h.subscribe(h.server.NewDevice("phone"))
	h.subscribe(h.server.NewDevice("old-phone"))
	h.server.Respond("old-phone", http.StatusGone)
	row := h.seed("k1", "webpush")
	w := h.worker("w1", nil, h.channel)

	h.process(w)
	if d := h.delivery(row, "webpush"); d.Status != StatusSent {
		t.Fatalf("delivery = %+v", d)
	}
	devices := h.devices()
	if _, ok := devices["old-phone"]; ok || len(devices) != 1 {
		t.Fatalf("devices after a 410 = %v", devices)
	}

	// With every device gone, the notification reached no one: failed.
	h.server.Respond("phone", http.StatusNotFound)
	second := h.seed("k2", "webpush")
	h.process(w)
	d := h.delivery(second, "webpush")
	if d.Status != StatusFailed || d.LastError == nil || !strings.Contains(*d.LastError, "unsubscribed or expired") {
		t.Fatalf("delivery with every device gone = %+v (%v)", d, d.LastError)
	}
	if len(h.devices()) != 0 {
		t.Fatalf("devices = %v", h.devices())
	}
}

func TestWebPushRetriesOnlyTheDeviceThatFailedForNow(t *testing.T) {
	h := newPushHarness(t)
	h.subscribe(h.server.NewDevice("phone"))
	h.subscribe(h.server.NewDevice("tablet"))
	// A third device whose push service cannot be reached at all.
	stray := h.server.NewDevice("stray")
	h.subscribeAt("https://127.0.0.1:1/push/stray", stray.Subscription.P256dh, stray.Subscription.Auth, "stray")
	h.server.Respond("tablet", http.StatusTooManyRequests, http.StatusServiceUnavailable)
	row := h.seed("k1", "webpush")
	w := h.worker("w1", nil, h.channel)

	h.process(w)
	d := h.delivery(row, "webpush")
	if d.Status != StatusRetrying || d.LastError == nil ||
		!strings.Contains(*d.LastError, "2 of 3 devices failed for now") || !strings.Contains(*d.LastError, "tablet: push service answered 429") {
		t.Fatalf("after the first attempt = %+v (%v)", d, d.LastError)
	}
	if got := h.outcomes(row); len(got) != 1 || got["phone"] != "sent" {
		t.Fatalf("outcomes = %v", got)
	}
	if tablet := h.devices()["tablet"]; !tablet.LastFailureAt.Valid || tablet.LastError == nil || !strings.Contains(*tablet.LastError, "429") {
		t.Fatalf("tablet = %+v", tablet)
	}

	// The retry goes to the failing devices only; the phone is not sent the
	// notification twice.
	h.makeDue(row)
	h.process(w)
	if d := h.delivery(row, "webpush"); d.Status != StatusRetrying || !strings.Contains(*d.LastError, "503") {
		t.Fatalf("after the second attempt = %+v (%v)", d, d.LastError)
	}
	// The unreachable device goes away; the tablet recovers.
	if _, err := h.q.DeletePushSubscription(context.Background(), h.devices()["stray"].ID); err != nil {
		t.Fatal(err)
	}
	h.makeDue(row)
	h.process(w)
	if d := h.delivery(row, "webpush"); d.Status != StatusSent || d.Attempts != 3 {
		t.Fatalf("after the third attempt = %+v (%v)", d, d.LastError)
	}
	if phone, tablet := len(h.server.PushesTo("phone")), len(h.server.PushesTo("tablet")); phone != 1 || tablet != 3 {
		t.Fatalf("pushes: phone %d, tablet %d; want 1 and 3", phone, tablet)
	}
	if got := h.outcomes(row); got["phone"] != "sent" || got["tablet"] != "sent" {
		t.Fatalf("outcomes = %v", got)
	}
}

func TestWebPushRefusalIsFinalForThatDevice(t *testing.T) {
	h := newPushHarness(t)
	h.subscribe(h.server.NewDevice("phone"))
	h.subscribe(h.server.NewDevice("laptop"))
	h.server.Respond("laptop", http.StatusForbidden)
	row := h.seed("k1", "webpush")
	w := h.worker("w1", nil, h.channel)

	h.process(w)
	if d := h.delivery(row, "webpush"); d.Status != StatusSent {
		t.Fatalf("delivery = %+v (%v)", d, d.LastError)
	}
	if got := h.outcomes(row); got["phone"] != "sent" || got["laptop"] != "failed" {
		t.Fatalf("outcomes = %v", got)
	}
	if laptop := h.devices()["laptop"]; laptop.LastError == nil || !strings.Contains(*laptop.LastError, "403 Forbidden") {
		t.Fatalf("laptop = %+v", laptop)
	}

	// When every device refuses, the delivery fails at once.
	h.server.Respond("phone", http.StatusBadRequest)
	h.server.Respond("laptop", http.StatusForbidden)
	second := h.seed("k2", "webpush")
	h.process(w)
	d := h.delivery(second, "webpush")
	if d.Status != StatusFailed || d.Attempts != 1 || !strings.Contains(*d.LastError, "every device refused the notification") {
		t.Fatalf("delivery refused everywhere = %+v (%v)", d, d.LastError)
	}
}

func TestWebPushSkipsWithoutDevices(t *testing.T) {
	h := newPushHarness(t)
	w := h.worker("w1", nil, h.channel)
	row := h.seed("k1", "webpush")
	// Subscribed after the notification was created: older notifications
	// are not delivered to it.
	h.subscribe(h.server.NewDevice("phone"))

	h.process(w)
	d := h.delivery(row, "webpush")
	if d.Status != StatusSkipped || d.LastError == nil || !strings.Contains(*d.LastError, "no device") {
		t.Fatalf("delivery = %+v (%v)", d, d.LastError)
	}
	if len(h.server.Pushes()) != 0 {
		t.Fatalf("pushes = %v", h.server.Pushes())
	}
}

func TestWebPushCutsALongBody(t *testing.T) {
	h := newPushHarness(t)
	h.subscribe(h.server.NewDevice("phone"))
	link := "/calendar?date=2026-10-12"
	row, err := h.q.InsertNotification(context.Background(), store.InsertNotificationParams{
		Title: strings.Repeat("Ü", 200), Body: strings.Repeat("ä\"", 1000), Importance: "high", Category: "status.services",
		Source: "monitor", Link: &link, DedupeKey: "long",
		OccurredAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.q.InsertDelivery(context.Background(), store.InsertDeliveryParams{NotificationID: row.ID, Channel: "webpush"}); err != nil {
		t.Fatal(err)
	}
	h.process(h.worker("w1", nil, h.channel))

	pushes := h.server.PushesTo("phone")
	if len(pushes) != 1 {
		t.Fatalf("pushes = %d", len(pushes))
	}
	payload := decodePayload(t, pushes[0])
	if len(pushes[0].Payload) > webpush.MaxPayload || !strings.HasSuffix(payload.Body, "…") || !utf8.ValidString(payload.Body) ||
		payload.Title != row.Title || payload.Link != link {
		t.Fatalf("payload of %d bytes: %+v", len(pushes[0].Payload), payload)
	}
	if pushes[0].Urgency != "high" || pushes[0].TTL != "86400" {
		t.Fatalf("high importance: Urgency %q TTL %q", pushes[0].Urgency, pushes[0].TTL)
	}
}

func TestWebPushTestReportsEachDevice(t *testing.T) {
	h := newPushHarness(t)
	phone := h.subscribe(h.server.NewDevice("phone"))
	gone := h.subscribe(h.server.NewDevice("gone"))
	broken := h.subscribe(h.server.NewDevice("broken"))
	h.server.Respond("gone", http.StatusGone)
	h.server.Respond("broken", http.StatusUnauthorized)

	results := h.channel.Test(context.Background(), []store.PushSubscription{phone, gone, broken}, time.Now())
	outcomes := map[string]string{}
	for _, result := range results {
		outcomes[result.Device.DeviceLabel] = result.Outcome
	}
	if outcomes["phone"] != PushTestSent || outcomes["gone"] != PushTestRemoved || outcomes["broken"] != PushTestFailed {
		t.Fatalf("outcomes = %v", outcomes)
	}
	payload := decodePayload(t, h.server.PushesTo("phone")[0])
	if payload.Title != "Test notification" || payload.Link != "/notifications" || payload.Tag != "lifeos-test" {
		t.Fatalf("test payload = %+v", payload)
	}
	devices := h.devices()
	if _, ok := devices["gone"]; ok {
		t.Fatal("a gone device was kept")
	}
	if !devices["phone"].LastSuccessAt.Valid || !devices["broken"].LastFailureAt.Valid {
		t.Fatalf("devices = %v", devices)
	}
	// A test is not a notification: nothing is recorded per notification.
	var outcomeRows int
	if err := h.pool.QueryRow(context.Background(), `SELECT count(*) FROM push_outcomes`).Scan(&outcomeRows); err != nil || outcomeRows != 0 {
		t.Fatalf("push outcomes after a test = %d, %v", outcomeRows, err)
	}
}

func TestSubscribingAgainKeepsTheDevice(t *testing.T) {
	h := newPushHarness(t)
	device := h.server.NewDevice("phone")
	first, err := h.q.UpsertPushSubscription(context.Background(), store.UpsertPushSubscriptionParams{
		Endpoint: device.Subscription.Endpoint, P256dh: device.Subscription.P256dh, Auth: device.Subscription.Auth, DeviceLabel: "Chrome on Android",
	})
	if err != nil || !first.Created {
		t.Fatalf("first = %+v, %v", first, err)
	}
	other := h.server.NewDevice("other")
	again, err := h.q.UpsertPushSubscription(context.Background(), store.UpsertPushSubscriptionParams{
		Endpoint: device.Subscription.Endpoint, P256dh: other.Subscription.P256dh, Auth: other.Subscription.Auth, DeviceLabel: "Chrome on Android",
	})
	if err != nil || again.Created || again.ID != first.ID || again.P256dh != other.Subscription.P256dh || !again.CreatedAt.Time.Equal(first.CreatedAt.Time) {
		t.Fatalf("again = %+v, %v", again, err)
	}
}
