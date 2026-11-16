package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/pkg/identity/identitytest"
	"github.com/GAMch1k/life-os/services/notifications/internal/api/generated"
	"github.com/GAMch1k/life-os/services/notifications/internal/delivery"
	store "github.com/GAMch1k/life-os/services/notifications/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/notifications/internal/testdb"
	"github.com/GAMch1k/life-os/services/notifications/internal/webpush"
	"github.com/GAMch1k/life-os/services/notifications/internal/webpush/webpushtest"
)

// A paired device's subscription remembers its device key (the identity
// token's sid, via "device"), moves to the key that subscribes it again,
// and goes when the Dashboard reports the key revoked
// (docs/api/devices.md#push-notifications-and-the-key).
func TestDeviceKeysOwnTheirSubscriptions(t *testing.T) {
	pool := testdb.Open(t)
	signer := identitytest.New(t)
	public, private, err := webpush.GenerateKeys()
	if err != nil {
		t.Fatal(err)
	}
	server := webpushtest.NewServer(t, public)
	channel, err := delivery.NewWebPush(delivery.WebPushConfig{
		PublicKey: public, PrivateKey: private, Subject: "mailto:owner@example.com",
		Store: store.New(pool), Client: server.Client(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(pool, []string{"webpush"}).WithPush(channel)
	app := httpserver.New("notifications", httpserver.WithLogger(httpserver.NewJSONLogger(io.Discard, nil)),
		httpserver.WithTrustedKeys(signer.Keys), httpserver.WithDiagnostics(nil))
	Register(app, handler)
	c := apiClient{t: t, pool: pool, app: app, handler: handler}

	keyOf := func(id string) string {
		return signer.TokenWith("", "owner", "device", map[string]any{"sid": id})
	}
	phoneKey, watchKey := uuid.NewString(), uuid.NewString()
	device := server.NewDevice("phone")
	const endpoint = "https://fcm.googleapis.com/fcm/send/device-key:APA91bExample"
	subscribe := func(token string) generated.PushSubscription {
		t.Helper()
		headers := []string{"User-Agent", chromeOnAndroid}
		if token != "" {
			headers = append(headers, "X-Life-OS-Identity", token)
		}
		status, raw := c.do(http.MethodPost, "/v1/push/subscriptions", browserSubscription(device, endpoint), headers...)
		if status != http.StatusCreated && status != http.StatusOK {
			t.Fatalf("subscribe: %d %s", status, raw)
		}
		return decode[generated.PushSubscription](t, raw)
	}

	// Registered before pairing (no key), then again with the phone's key:
	// the endpoint now belongs to the key.
	if got := subscribe(""); got.DeviceKeyId != nil {
		t.Fatalf("without a key: %+v", got.DeviceKeyId)
	}
	got := subscribe(keyOf(phoneKey))
	if got.DeviceKeyId == nil || got.DeviceKeyId.String() != phoneKey {
		t.Fatalf("with the phone's key: %+v", got.DeviceKeyId)
	}
	// A browser's session (the TWA) subscribing the endpoint clears it.
	if got := subscribe(signer.TwoFactor("", "owner")); got.DeviceKeyId != nil {
		t.Fatalf("a session: %+v", got.DeviceKeyId)
	}
	subscribe(keyOf(phoneKey))
	// The watch's own subscription.
	watchDevice := server.NewDevice("watch")
	status, raw := c.do(http.MethodPost, "/v1/push/subscriptions",
		browserSubscription(watchDevice, "https://fcm.googleapis.com/fcm/send/watch:APA91bExample"),
		"X-Life-OS-Identity", keyOf(watchKey))
	if status != http.StatusCreated {
		t.Fatalf("the watch: %d %s", status, raw)
	}
	// The list says which key owns which.
	owners := map[string]string{}
	for _, item := range c.devices() {
		if item.DeviceKeyId != nil {
			owners[item.Endpoint] = item.DeviceKeyId.String()
		}
	}
	if owners[endpoint] != phoneKey || len(owners) != 2 {
		t.Fatalf("owners: %v", owners)
	}

	// Only an internal caller deletes a key's subscriptions: through the
	// gateway every request has an identity token.
	status, raw = c.do(http.MethodDelete, "/v1/push/device-keys/"+phoneKey, nil, "X-Life-OS-Identity", signer.TwoFactor("", "owner"))
	if status != http.StatusForbidden || decode[errorEnvelope](t, raw).Error.Code != "internal_only" {
		t.Fatalf("with a token: %d %s", status, raw)
	}
	status, _ = c.do(http.MethodDelete, "/v1/push/device-keys/"+phoneKey, nil, "X-Life-OS-Actor", "system:dashboard")
	if status != http.StatusNoContent {
		t.Fatalf("the Dashboard: %d", status)
	}
	var left []string
	rows, err := pool.Query(context.Background(), `SELECT coalesce(device_key_id::text, '') FROM push_subscriptions`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			t.Fatal(err)
		}
		left = append(left, key)
	}
	if len(left) != 1 || left[0] != watchKey {
		t.Fatalf("left: %v, want only the watch's", left)
	}
	// Idempotent.
	if status, _ := c.do(http.MethodDelete, "/v1/push/device-keys/"+phoneKey, nil); status != http.StatusNoContent {
		t.Fatalf("again: %d", status)
	}
}
