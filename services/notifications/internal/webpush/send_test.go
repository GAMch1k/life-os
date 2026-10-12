package webpush_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/GAMch1k/life-os/services/notifications/internal/webpush"
	"github.com/GAMch1k/life-os/services/notifications/internal/webpush/webpushtest"
)

func newSender(t *testing.T) (*webpush.Sender, *webpushtest.Server) {
	t.Helper()
	public, private, err := webpush.GenerateKeys()
	if err != nil {
		t.Fatal(err)
	}
	keys, err := webpush.ParseKeys(public, private)
	if err != nil {
		t.Fatal(err)
	}
	server := webpushtest.NewServer(t, public)
	sender, err := webpush.NewSender(keys, "mailto:owner@example.com", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return sender, server
}

func TestSendDeliversAnEncryptedSignedPush(t *testing.T) {
	sender, server := newSender(t)
	phone := server.NewDevice("phone")
	err := sender.Send(context.Background(), phone.Subscription, webpush.Message{
		Payload: []byte(`{"title":"Dentist in 30 minutes"}`),
		TTL:     12 * time.Hour,
		Urgency: webpush.UrgencyNormal,
		Topic:   "0123456789abcdef0123456789abcdef",
	})
	if err != nil {
		t.Fatal(err)
	}
	pushes := server.PushesTo("phone")
	if len(pushes) != 1 {
		t.Fatalf("pushes = %+v", pushes)
	}
	push := pushes[0]
	if string(push.Payload) != `{"title":"Dentist in 30 minutes"}` {
		t.Fatalf("decrypted payload = %q", push.Payload)
	}
	if push.TTL != "43200" || push.Urgency != "normal" || push.Topic != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("headers TTL %q Urgency %q Topic %q", push.TTL, push.Urgency, push.Topic)
	}
	if push.Audience != server.URL || push.Subject != "mailto:owner@example.com" {
		t.Fatalf("claims aud %q sub %q", push.Audience, push.Subject)
	}
}

func TestSendReportsTheRefusal(t *testing.T) {
	sender, server := newSender(t)
	phone := server.NewDevice("phone")
	server.Respond("phone", http.StatusGone, http.StatusTooManyRequests, http.StatusForbidden)
	message := webpush.Message{Payload: []byte("{}"), TTL: time.Minute}
	for _, want := range []struct {
		gone, retryable bool
		text            string
	}{
		{true, false, "push service answered 410 Gone: scripted 410"},
		{false, true, "push service answered 429 Too Many Requests"},
		{false, false, "push service answered 403 Forbidden"},
	} {
		err := sender.Send(context.Background(), phone.Subscription, message)
		if err == nil || webpush.IsGone(err) != want.gone || webpush.IsRetryable(err) != want.retryable || !strings.HasPrefix(err.Error(), want.text) {
			t.Errorf("Send = %v (gone %v, retryable %v), want %q", err, webpush.IsGone(err), webpush.IsRetryable(err), want.text)
		}
	}
}

func TestSendRefusesBrokenSubscriptionsForGood(t *testing.T) {
	sender, server := newSender(t)
	phone := server.NewDevice("phone")
	broken := phone.Subscription
	broken.Auth = "c2hvcnQ"
	err := sender.Send(context.Background(), broken, webpush.Message{Payload: []byte("{}")})
	if err == nil || webpush.IsRetryable(err) {
		t.Fatalf("a short auth secret: %v (retryable %v)", err, webpush.IsRetryable(err))
	}
	err = sender.Send(context.Background(), phone.Subscription, webpush.Message{Payload: []byte("{}"), Topic: "not a topic"})
	if err == nil || webpush.IsRetryable(err) {
		t.Fatalf("an invalid topic: %v", err)
	}
	if len(server.Pushes()) != 0 {
		t.Fatalf("pushes sent: %+v", server.Pushes())
	}
}

func TestSendRetriesAnUnreachablePushService(t *testing.T) {
	sender, server := newSender(t)
	phone := server.NewDevice("phone")
	server.Close()
	err := sender.Send(context.Background(), phone.Subscription, webpush.Message{Payload: []byte("{}")})
	if err == nil || !webpush.IsRetryable(err) {
		t.Fatalf("Send to a stopped push service = %v, want a retryable error", err)
	}
	if strings.Contains(err.Error(), "/push/phone") {
		t.Fatalf("the error names the endpoint's path: %v", err)
	}
}

func TestNewSenderNeedsKeysAndASubject(t *testing.T) {
	public, private, _ := webpush.GenerateKeys()
	keys, _ := webpush.ParseKeys(public, private)
	if _, err := webpush.NewSender(nil, "mailto:owner@example.com", nil); err == nil {
		t.Error("a sender without keys")
	}
	if _, err := webpush.NewSender(keys, "owner@example.com", nil); err == nil {
		t.Error("a sender with a subject that is neither mailto: nor https")
	}
}
