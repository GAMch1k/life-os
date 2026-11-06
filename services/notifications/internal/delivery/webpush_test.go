package delivery

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	store "github.com/GAMch1k/life-os/services/notifications/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/notifications/internal/webpush"
)

func TestNewWebPushNamesWhatIsMissing(t *testing.T) {
	public, private, _ := webpush.GenerateKeys()
	otherPublic, _, _ := webpush.GenerateKeys()
	queries := store.New(nil)
	for name, test := range map[string]struct {
		cfg  WebPushConfig
		want string
	}{
		"no keys":      {WebPushConfig{Subject: "mailto:owner@example.com", Store: queries}, "must both be set"},
		"public only":  {WebPushConfig{PublicKey: public, Subject: "mailto:owner@example.com", Store: queries}, "must both be set"},
		"another pair": {WebPushConfig{PublicKey: otherPublic, PrivateKey: private, Subject: "mailto:owner@example.com", Store: queries}, "does not belong"},
		"bad subject":  {WebPushConfig{PublicKey: public, PrivateKey: private, Subject: "owner@example.com", Store: queries}, "NOTIFICATIONS_WEBPUSH_SUBJECT"},
		"no store":     {WebPushConfig{PublicKey: public, PrivateKey: private, Subject: "mailto:owner@example.com"}, "store"},
	} {
		_, err := NewWebPush(test.cfg)
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("%s: %v, want an error about %q", name, err, test.want)
		}
		if err != nil && strings.Contains(err.Error(), private) {
			t.Errorf("%s: the error shows the private key", name)
		}
	}
	channel, err := NewWebPush(WebPushConfig{PublicKey: public, PrivateKey: private, Subject: "https://lifeos.example.com", Store: queries})
	if err != nil {
		t.Fatal(err)
	}
	if channel.Name() != "webpush" || channel.PublicKey() != public || strings.Contains(channel.String(), private) {
		t.Fatalf("channel %s, key %s", channel, channel.PublicKey())
	}
	if (WebPushConfig{}).Set() || !(WebPushConfig{PrivateKey: private}).Set() {
		t.Fatal("Set reports a key wrongly")
	}
}

func TestBuildNeedsWebPushKeysForTheChannel(t *testing.T) {
	if _, err := Build([]string{"webpush"}, Settings{}); err == nil || !strings.Contains(err.Error(), "NOTIFICATIONS_WEBPUSH_VAPID_PUBLIC_KEY") {
		t.Fatalf("Build(webpush) without keys = %v", err)
	}
	public, private, _ := webpush.GenerateKeys()
	channel, err := NewWebPush(WebPushConfig{PublicKey: public, PrivateKey: private, Subject: "https://lifeos.example.com", Store: store.New(nil)})
	if err != nil {
		t.Fatal(err)
	}
	channels, err := Build([]string{"ntfy", "webpush"}, Settings{
		Ntfy:    NtfyConfig{URL: "http://ntfy:80", Topic: "lifeos", Token: "tk_test", LinkBaseURL: "https://lifeos.example.com"},
		WebPush: channel,
	})
	if err != nil || len(channels) != 2 || channels[1] != Channel(channel) {
		t.Fatalf("Build(ntfy, webpush) = %v, %v", channels, err)
	}
}

func TestPushUrgencyFollowsImportance(t *testing.T) {
	for importance, want := range map[string]struct {
		urgency webpush.Urgency
		ttl     time.Duration
	}{
		"high":   {webpush.UrgencyHigh, 24 * time.Hour},
		"medium": {webpush.UrgencyNormal, 12 * time.Hour},
		"low":    {webpush.UrgencyLow, 4 * time.Hour},
	} {
		if urgency, ttl := PushUrgency(importance); urgency != want.urgency || ttl != want.ttl {
			t.Errorf("%s: %s %s, want %s %s", importance, urgency, ttl, want.urgency, want.ttl)
		}
	}
}

func TestEncodePushPayload(t *testing.T) {
	id := uuid.MustParse("7f8d2b1e-1c1a-4b7e-9a51-2f0c6d1e9a10")
	created := time.Date(2026, 10, 12, 9, 30, 0, 0, time.UTC)
	raw, err := EncodePushPayload(Notification{
		ID: id, Title: "Dentist <in 30 minutes>", Body: "Line one\nLine two", Importance: "medium",
		Category: "calendar.reminder", CreatedAt: created,
	}, pushTag(id))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"id":"7f8d2b1e-1c1a-4b7e-9a51-2f0c6d1e9a10","title":"Dentist <in 30 minutes>","body":"Line one\nLine two",` +
		`"link":"/notifications","tag":"lifeos-7f8d2b1e-1c1a-4b7e-9a51-2f0c6d1e9a10","importance":"medium",` +
		`"category":"calendar.reminder","timestamp":1791797400000}`
	if string(raw) != want {
		t.Fatalf("payload\n got %s\nwant %s", raw, want)
	}
	if topic := pushTopic(id); topic != "7f8d2b1e1c1a4b7e9a512f0c6d1e9a10" || len(topic) != 32 {
		t.Fatalf("topic = %s", topic)
	}

	// The longest notification still fits one push, its body cut.
	long := Notification{ID: id, Title: strings.Repeat("€", 200), Body: strings.Repeat("\"€", 2000), Link: "/" + strings.Repeat("a", 499),
		Importance: "high", Category: "status.services", CreatedAt: created}
	raw, err = EncodePushPayload(long, pushTag(id))
	if err != nil {
		t.Fatal(err)
	}
	var payload PushPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if len(raw) > webpush.MaxPayload || len(raw) < webpush.MaxPayload-16 || !strings.HasSuffix(payload.Body, "…") || payload.Title != long.Title {
		t.Fatalf("%d bytes, body ends %q", len(raw), payload.Body[max(0, len(payload.Body)-8):])
	}
}
