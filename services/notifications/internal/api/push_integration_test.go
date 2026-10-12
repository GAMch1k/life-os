package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/GAMch1k/life-os/services/notifications/internal/api/generated"
	"github.com/GAMch1k/life-os/services/notifications/internal/delivery"
	store "github.com/GAMch1k/life-os/services/notifications/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/notifications/internal/webpush"
	"github.com/GAMch1k/life-os/services/notifications/internal/webpush/webpushtest"
)

const chromeOnAndroid = "Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Mobile Safari/537.36"

// pushAPI is the API with web push configured, its pushes going to a fake
// push service.
func pushAPI(t *testing.T, channels ...string) (apiClient, *webpushtest.Server, string) {
	t.Helper()
	c := newAPIClient(t, channels...)
	public, private, err := webpush.GenerateKeys()
	if err != nil {
		t.Fatal(err)
	}
	server := webpushtest.NewServer(t, public)
	channel, err := delivery.NewWebPush(delivery.WebPushConfig{
		PublicKey: public, PrivateKey: private, Subject: "mailto:owner@example.com",
		Store: store.New(c.pool), Client: server.Client(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	c.handler.WithPush(channel)
	return c, server, public
}

// browserSubscription is a PushSubscription.toJSON() of a fake device.
func browserSubscription(device *webpushtest.Device, endpoint string) map[string]any {
	return map[string]any{
		"endpoint":       endpoint,
		"expirationTime": nil,
		"keys":           map[string]any{"p256dh": device.Subscription.P256dh, "auth": device.Subscription.Auth},
	}
}

func (c apiClient) devices() []generated.PushSubscription {
	c.t.Helper()
	status, raw := c.do(http.MethodGet, "/v1/push/subscriptions", nil)
	if status != http.StatusOK {
		c.t.Fatalf("GET /v1/push/subscriptions: %d %s", status, raw)
	}
	return decode[generated.PushSubscriptionList](c.t, raw).Items
}

func TestPushConfig(t *testing.T) {
	c := newAPIClient(t)
	status, raw := c.do(http.MethodGet, "/v1/push/config", nil)
	if config := decode[generated.PushConfig](t, raw); status != http.StatusOK || config.Configured || config.PublicKey != nil || config.Delivering {
		t.Fatalf("without keys = %d %s", status, raw)
	}

	c, _, public := pushAPI(t)
	_, raw = c.do(http.MethodGet, "/v1/push/config", nil)
	if config := decode[generated.PushConfig](t, raw); !config.Configured || config.PublicKey == nil || *config.PublicKey != public || config.Delivering {
		t.Fatalf("with keys, not a channel = %s", raw)
	}

	c, _, _ = pushAPI(t, "ntfy", "webpush")
	_, raw = c.do(http.MethodGet, "/v1/push/config", nil)
	if config := decode[generated.PushConfig](t, raw); !config.Configured || !config.Delivering {
		t.Fatalf("with keys and the channel = %s", raw)
	}
}

func TestSubscribeIsIdempotentByEndpoint(t *testing.T) {
	c, server, _ := pushAPI(t, "webpush")
	device := server.NewDevice("phone")
	const endpoint = "https://fcm.googleapis.com/fcm/send/abc:APA91bExample"
	status, raw := c.do(http.MethodPost, "/v1/push/subscriptions", browserSubscription(device, endpoint), "User-Agent", chromeOnAndroid)
	first := decode[generated.PushSubscription](t, raw)
	if status != http.StatusCreated || first.DeviceLabel != "Chrome on Android" || first.Endpoint != endpoint ||
		first.PushService != "fcm.googleapis.com" || first.LastSuccessAt != nil || first.LastError != nil {
		t.Fatalf("subscribe = %d %s", status, raw)
	}

	// Again, with new keys (and padded base64, as some browsers send): the
	// same device, updated.
	other := server.NewDevice("other")
	again := browserSubscription(other, endpoint)
	again["keys"].(map[string]any)["auth"] = other.Subscription.Auth + "=="
	status, raw = c.do(http.MethodPost, "/v1/push/subscriptions", again, "User-Agent", chromeOnAndroid)
	second := decode[generated.PushSubscription](t, raw)
	if status != http.StatusOK || second.Id != first.Id || !second.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("subscribe again = %d %s", status, raw)
	}
	var auth string
	if err := c.pool.QueryRow(context.Background(), `SELECT auth FROM push_subscriptions WHERE id = $1`, first.Id).Scan(&auth); err != nil || auth != other.Subscription.Auth {
		t.Fatalf("stored auth = %q, %v; want the new one, canonical", auth, err)
	}

	status, raw = c.do(http.MethodPost, "/v1/push/subscriptions", browserSubscription(device, "https://updates.push.services.mozilla.com/wpush/v2/gAAAA"),
		"User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:131.0) Gecko/20100101 Firefox/131.0")
	if status != http.StatusCreated || decode[generated.PushSubscription](t, raw).DeviceLabel != "Firefox on Windows" {
		t.Fatalf("a second device = %d %s", status, raw)
	}
	devices := c.devices()
	if len(devices) != 2 || devices[0].Id != first.Id {
		t.Fatalf("devices = %+v", devices)
	}

	status, _ = c.do(http.MethodDelete, "/v1/push/subscriptions/"+first.Id.String(), nil)
	if status != http.StatusNoContent || len(c.devices()) != 1 {
		t.Fatalf("delete = %d, devices %+v", status, c.devices())
	}
	status, raw = c.do(http.MethodDelete, "/v1/push/subscriptions/"+first.Id.String(), nil)
	if status != http.StatusNotFound || decode[errorEnvelope](t, raw).Error.Code != "not_found" {
		t.Fatalf("delete again = %d %s", status, raw)
	}
}

func TestSubscribeRefusesWhatCannotBePushedTo(t *testing.T) {
	c, server, _ := pushAPI(t)
	device := server.NewDevice("phone")
	valid := func(change func(map[string]any)) map[string]any {
		body := browserSubscription(device, "https://fcm.googleapis.com/fcm/send/abc")
		change(body)
		return body
	}
	setKey := func(name, value string) func(map[string]any) {
		return func(body map[string]any) { body["keys"].(map[string]any)[name] = value }
	}
	setEndpoint := func(value string) func(map[string]any) {
		return func(body map[string]any) { body["endpoint"] = value }
	}
	for name, test := range map[string]struct {
		body any
		want string
	}{
		"not json":        {"not json", "push subscription"},
		"http":            {valid(setEndpoint("http://fcm.googleapis.com/fcm/send/abc")), "endpoint"},
		"an address":      {valid(setEndpoint("https://10.0.0.5/push")), "endpoint"},
		"a container":     {valid(setEndpoint("https://notifications:8080/v1/notifications")), "endpoint"},
		"credentials":     {valid(setEndpoint("https://user:pw@fcm.googleapis.com/fcm/send/abc")), "endpoint"},
		"too long":        {valid(setEndpoint("https://fcm.googleapis.com/" + strings.Repeat("a", 2048))), "endpoint"},
		"short p256dh":    {valid(setKey("p256dh", "BCVxsr7N")), "p256dh"},
		"p256dh off P256": {valid(setKey("p256dh", webpush.EncodeBase64URL(append([]byte{4}, make([]byte, 64)...)))), "p256dh"},
		"short auth":      {valid(setKey("auth", "BTBZMqHH")), "auth"},
		"auth not base64": {valid(setKey("auth", "!!!!!!!!!!!!!!!!!!!!!!")), "auth"},
	} {
		status, raw := c.do(http.MethodPost, "/v1/push/subscriptions", test.body)
		envelope := decode[errorEnvelope](t, raw)
		if status != http.StatusBadRequest || envelope.Error.Code != "bad_request" || !strings.Contains(envelope.Error.Message, test.want) {
			t.Errorf("%s: %d %s, want 400 about %q", name, status, raw, test.want)
		}
	}
	if devices := c.devices(); len(devices) != 0 {
		t.Fatalf("devices = %+v", devices)
	}
}

func TestPushNeedsItsKeys(t *testing.T) {
	c := newAPIClient(t, "ntfy")
	server := webpushtest.NewServer(t, mustPublicKey(t))
	for _, request := range []struct {
		target string
		body   any
	}{
		{"/v1/push/subscriptions", browserSubscription(server.NewDevice("phone"), "https://fcm.googleapis.com/fcm/send/abc")},
		{"/v1/push/test", nil},
	} {
		status, raw := c.do(http.MethodPost, request.target, request.body)
		if status != http.StatusConflict || decode[errorEnvelope](t, raw).Error.Code != "push_not_configured" {
			t.Errorf("POST %s = %d %s, want 409 push_not_configured", request.target, status, raw)
		}
	}
	// Devices can still be listed and removed.
	if devices := c.devices(); len(devices) != 0 {
		t.Fatalf("devices = %+v", devices)
	}
}

func mustPublicKey(t *testing.T) string {
	t.Helper()
	public, _, err := webpush.GenerateKeys()
	if err != nil {
		t.Fatal(err)
	}
	return public
}

func TestPushTestReachesEveryDeviceOrOne(t *testing.T) {
	c, server, _ := pushAPI(t)
	q := store.New(c.pool)
	subscribe := func(device *webpushtest.Device, label string) string {
		t.Helper()
		// The fake push service listens on 127.0.0.1, which the API refuses
		// as an endpoint, so the test stores it directly.
		row, err := q.UpsertPushSubscription(context.Background(), store.UpsertPushSubscriptionParams{
			Endpoint: device.Subscription.Endpoint, P256dh: device.Subscription.P256dh, Auth: device.Subscription.Auth, DeviceLabel: label,
		})
		if err != nil {
			t.Fatal(err)
		}
		return row.ID.String()
	}
	phone := subscribe(server.NewDevice("phone"), "Chrome on Android")
	subscribe(server.NewDevice("gone"), "Chrome on Linux")
	server.Respond("gone", http.StatusGone)

	status, raw := c.do(http.MethodPost, "/v1/push/test", nil)
	results := decode[generated.PushTestResult](t, raw).Results
	if status != http.StatusOK || len(results) != 2 || results[0].Outcome != generated.PushTestOutcomeSent || results[0].Error != nil ||
		results[1].Outcome != generated.PushTestOutcomeRemoved || results[1].Error == nil || !strings.Contains(*results[1].Error, "410") {
		t.Fatalf("test all = %d %s", status, raw)
	}
	devices := c.devices()
	if len(devices) != 1 || devices[0].Id.String() != phone || devices[0].LastSuccessAt == nil {
		t.Fatalf("devices after the test = %+v", devices)
	}

	server.Respond("phone", http.StatusServiceUnavailable)
	status, raw = c.do(http.MethodPost, "/v1/push/test", map[string]any{"subscriptionId": phone})
	results = decode[generated.PushTestResult](t, raw).Results
	if status != http.StatusOK || len(results) != 1 || results[0].Outcome != generated.PushTestOutcomeFailed || !strings.Contains(*results[0].Error, "503") {
		t.Fatalf("test one = %d %s", status, raw)
	}
	if device := c.devices()[0]; device.LastFailureAt == nil || device.LastError == nil {
		t.Fatalf("device after a failed test = %+v", device)
	}
	if got := len(server.PushesTo("phone")); got != 2 {
		t.Fatalf("pushes to the phone = %d, want 2", got)
	}

	status, raw = c.do(http.MethodPost, "/v1/push/test", map[string]any{"subscriptionId": "7f8d2b1e-1c1a-4b7e-9a51-2f0c6d1e9a10"})
	if status != http.StatusNotFound || decode[errorEnvelope](t, raw).Error.Code != "not_found" {
		t.Fatalf("test an unknown device = %d %s", status, raw)
	}
	// No notification was stored for the tests.
	if items := c.list("").Items; len(items) != 0 {
		t.Fatalf("notifications = %+v", items)
	}
}
