package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const testToken = "tk_abcdefghijklmnopqrstuvwxyz012"

// fakeNtfy records what a channel publishes and answers with a fixed status
// and body.
type fakeNtfy struct {
	mu       sync.Mutex
	requests []recordedPublish
	status   int
	body     string
}

type recordedPublish struct {
	method, path, auth, contentType string
	message                         map[string]any
}

func (f *fakeNtfy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var message map[string]any
	_ = json.Unmarshal(raw, &message)
	f.mu.Lock()
	f.requests = append(f.requests, recordedPublish{
		method: r.Method, path: r.URL.Path, auth: r.Header.Get("Authorization"),
		contentType: r.Header.Get("Content-Type"), message: message,
	})
	status, body := f.status, f.body
	f.mu.Unlock()
	if status == 0 {
		status = http.StatusOK
		body = `{"id":"abc","event":"message"}`
	}
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func (f *fakeNtfy) last(t *testing.T) recordedPublish {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		t.Fatal("nothing was published")
	}
	return f.requests[len(f.requests)-1]
}

func newNtfyHarness(t *testing.T, fake *fakeNtfy) *NtfyChannel {
	t.Helper()
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	channel, err := NewNtfy(NtfyConfig{URL: server.URL, Topic: "lifeos", Token: testToken, LinkBaseURL: "https://lifeos.example.com/"})
	if err != nil {
		t.Fatal(err)
	}
	return channel
}

func sample(importance, category, body, link string) Notification {
	return Notification{
		ID: uuid.New(), Title: "Dentist in 30 minutes", Body: body, Importance: importance,
		Category: category, Source: "calendar", Link: link, CreatedAt: time.Now(), OccurredAt: time.Now(),
	}
}

func TestNtfyPublishesJSONWithTheToken(t *testing.T) {
	fake := &fakeNtfy{}
	channel := newNtfyHarness(t, fake)
	if channel.Name() != "ntfy" {
		t.Fatalf("Name() = %q", channel.Name())
	}
	err := channel.Send(context.Background(), sample("medium", "calendar.reminder", "Dr. Müller <Praxis> & Co.\nBring the card", "/calendar?date=2026-10-10"))
	if err != nil {
		t.Fatal(err)
	}
	got := fake.last(t)
	if got.method != http.MethodPost || got.path != "/" || got.contentType != "application/json" {
		t.Errorf("request = %s %s (%s), want a JSON POST to the server root", got.method, got.path, got.contentType)
	}
	if got.auth != "Bearer "+testToken {
		t.Errorf("Authorization = %q", got.auth)
	}
	want := map[string]any{
		"topic":    "lifeos",
		"title":    "Dentist in 30 minutes",
		"message":  "Dr. Müller <Praxis> & Co.\nBring the card",
		"priority": float64(3),
		"tags":     []any{"calendar"},
		"click":    "https://lifeos.example.com/calendar?date=2026-10-10",
	}
	for key, value := range want {
		if fmt.Sprint(got.message[key]) != fmt.Sprint(value) {
			t.Errorf("%s = %#v, want %#v", key, got.message[key], value)
		}
	}
	if _, ok := got.message["markdown"]; ok {
		t.Errorf("markdown must stay off: %v", got.message)
	}
}

func TestNtfyPriorityFollowsImportance(t *testing.T) {
	fake := &fakeNtfy{}
	channel := newNtfyHarness(t, fake)
	for importance, want := range map[string]float64{"low": 2, "medium": 3, "high": 5, "unknown": 3} {
		if err := channel.Send(context.Background(), sample(importance, "status.service_down", "", "")); err != nil {
			t.Fatal(err)
		}
		if got := fake.last(t).message["priority"]; got != want {
			t.Errorf("%s: priority = %v, want %v", importance, got, want)
		}
	}
}

func TestNtfyTagsFollowTheCategoryDomain(t *testing.T) {
	for category, want := range map[string]string{
		"calendar.reminder":   "[calendar]",
		"inbox.human_review":  "[inbox_tray]",
		"status.service_down": "[warning]",
		"test.manual":         "[test_tube]",
		"food.order":          "[]",
	} {
		if got := fmt.Sprint(NtfyTags(category)); got != want {
			t.Errorf("NtfyTags(%q) = %s, want %s", category, got, want)
		}
	}
	fake := &fakeNtfy{}
	channel := newNtfyHarness(t, fake)
	if err := channel.Send(context.Background(), sample("low", "food.order", "", "")); err != nil {
		t.Fatal(err)
	}
	if _, ok := fake.last(t).message["tags"]; ok {
		t.Error("a category without an emoji sends no tags")
	}
}

func TestNtfyEmptyBodyRepeatsTheTitleAndNoLinkOpensTheList(t *testing.T) {
	fake := &fakeNtfy{}
	channel := newNtfyHarness(t, fake)
	if err := channel.Send(context.Background(), sample("low", "test.manual", "  ", "")); err != nil {
		t.Fatal(err)
	}
	got := fake.last(t).message
	if got["message"] != "Dentist in 30 minutes" {
		t.Errorf("message = %v, want the title", got["message"])
	}
	if got["click"] != "https://lifeos.example.com/notifications" {
		t.Errorf("click = %v, want the notification list", got["click"])
	}
}

func TestNtfyTruncatesLongMessagesOnACharacterBoundary(t *testing.T) {
	fake := &fakeNtfy{}
	channel := newNtfyHarness(t, fake)
	body := strings.Repeat("Жёлтый ", 400) // 2800 characters, 5200 bytes
	if err := channel.Send(context.Background(), sample("low", "test.manual", body, "")); err != nil {
		t.Fatal(err)
	}
	message, _ := fake.last(t).message["message"].(string)
	if len(message) > ntfyMessageLimit || !utf8.ValidString(message) || !strings.HasSuffix(message, "…") {
		t.Errorf("message has %d bytes (valid UTF-8: %v), want at most %d ending in an ellipsis", len(message), utf8.ValidString(message), ntfyMessageLimit)
	}
	if short := truncateBytes("short", 10); short != "short" {
		t.Errorf("truncateBytes(short) = %q", short)
	}
}

func TestNtfyClassifiesRefusals(t *testing.T) {
	for _, tc := range []struct {
		status    int
		body      string
		permanent bool
		want      string
	}{
		{http.StatusBadRequest, `{"code":40007,"http":400,"error":"invalid priority"}`, true, "400 invalid priority (ntfy code 40007)"},
		{http.StatusUnauthorized, `{"code":40101,"http":401,"error":"unauthorized"}`, true, "401 unauthorized"},
		{http.StatusForbidden, `{"code":40301,"http":403,"error":"forbidden"}`, true, "403 forbidden"},
		{http.StatusNotFound, `page not found`, true, "404 page not found"},
		{http.StatusRequestEntityTooLarge, ``, true, "413 Request Entity Too Large"},
		{http.StatusFound, ``, true, "302"},
		{http.StatusTooManyRequests, `{"code":42901,"http":429,"error":"limit reached: too many requests"}`, false, "429 limit reached"},
		{http.StatusRequestTimeout, ``, false, "408"},
		{http.StatusInternalServerError, `{"code":50001,"http":500,"error":"internal server error"}`, false, "500 internal server error"},
		{http.StatusBadGateway, `bad gateway`, false, "502 bad gateway"},
	} {
		channel := newNtfyHarness(t, &fakeNtfy{status: tc.status, body: tc.body})
		err := channel.Send(context.Background(), sample("high", "status.service_down", "", ""))
		if err == nil {
			t.Fatalf("%d: Send() = nil", tc.status)
		}
		if IsPermanent(err) != tc.permanent {
			t.Errorf("%d: permanent = %v, want %v (%v)", tc.status, IsPermanent(err), tc.permanent, err)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%d: error %q does not contain %q", tc.status, err, tc.want)
		}
		if strings.Contains(err.Error(), testToken) {
			t.Errorf("%d: the error shows the token", tc.status)
		}
	}
}

func TestNtfyUnreachableIsRetried(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	url := server.URL
	server.Close()
	channel, err := NewNtfy(NtfyConfig{URL: url, Topic: "lifeos", Token: testToken, LinkBaseURL: "https://lifeos.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	err = channel.Send(context.Background(), sample("high", "status.service_down", "", ""))
	if err == nil || IsPermanent(err) || strings.Contains(err.Error(), testToken) {
		t.Fatalf("Send() to a stopped server = %v, want a retryable error without the token", err)
	}
}

func TestNtfyHonoursTheDeadline(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) })
	channel, err := NewNtfy(NtfyConfig{URL: server.URL, Topic: "lifeos", Token: testToken, LinkBaseURL: "https://lifeos.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err = channel.Send(ctx, sample("low", "test.manual", "", ""))
	if err == nil || IsPermanent(err) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Send() past its deadline = %v, want a retryable deadline error", err)
	}
}

func TestNewNtfyRequiresItsConfiguration(t *testing.T) {
	_, err := NewNtfy(NtfyConfig{URL: "ntfy:80", Topic: "life os", LinkBaseURL: "/relative"})
	if err == nil {
		t.Fatal("NewNtfy() = nil error")
	}
	for _, want := range []string{"NOTIFICATIONS_NTFY_URL", "NOTIFICATIONS_NTFY_TOPIC", "NOTIFICATIONS_NTFY_TOKEN", "NOTIFICATIONS_LINK_BASE_URL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
	if _, err := Build([]string{"ntfy"}, Settings{}); err == nil || !strings.Contains(err.Error(), `channel "ntfy"`) {
		t.Errorf("Build(ntfy) without settings = %v, want a configuration error", err)
	}
	channel, err := Build([]string{"ntfy"}, Settings{Ntfy: NtfyConfig{URL: "http://ntfy:80", Topic: "lifeos", Token: testToken, LinkBaseURL: "https://lifeos.example.com"}})
	if err != nil || len(channel) != 1 || channel[0].Name() != "ntfy" {
		t.Fatalf("Build(ntfy) = %v, %v", channel, err)
	}
	if shown := fmt.Sprint(channel[0]); strings.Contains(shown, testToken) {
		t.Errorf("the channel prints its token: %s", shown)
	}
}
