package api

import (
	"strings"
	"testing"
	"time"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/notifications/internal/api/generated"
)

func valid() generated.CreateNotificationRequest {
	return generated.CreateNotificationRequest{
		Title:      "Dentist in 30 minutes",
		Importance: generated.Medium,
		Category:   "calendar.reminder",
		DedupeKey:  "calendar.reminder:abc:2026-10-10T09:00:00Z:30",
	}
}

func ptr[T any](value T) *T { return &value }

func TestNewNotificationDefaults(t *testing.T) {
	now := time.Date(2026, 10, 10, 8, 30, 0, 0, time.FixedZone("CEST", 2*3600))
	params, err := newNotification(valid(), activity.System("calendar"), now)
	if err != nil {
		t.Fatal(err)
	}
	if params.Title != "Dentist in 30 minutes" || params.Body != "" || params.Importance != "medium" ||
		params.Category != "calendar.reminder" || params.Source != "calendar" || params.Link != nil {
		t.Fatalf("params = %+v", params)
	}
	if !params.OccurredAt.Time.Equal(now) || params.OccurredAt.Time.Location() != time.UTC {
		t.Fatalf("occurredAt = %v, want %v in UTC", params.OccurredAt.Time, now)
	}
}

func TestNewNotificationKeepsGivenFields(t *testing.T) {
	body := valid()
	body.Title = "  Inbox needs you  "
	body.Body = ptr("Two Tasks fit \"buy milk\".\r\nWhich one?\tPick.\n")
	body.Source = ptr("inbox")
	body.Link = ptr("/inbox?item=1#review")
	occurred := time.Date(2026, 10, 9, 22, 0, 0, 0, time.UTC)
	body.OccurredAt = &occurred
	params, err := newNotification(body, activity.User(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if params.Title != "Inbox needs you" || params.Body != "Two Tasks fit \"buy milk\".\nWhich one?\tPick." ||
		params.Source != "inbox" || *params.Link != "/inbox?item=1#review" || !params.OccurredAt.Time.Equal(occurred) {
		t.Fatalf("params = %+v", params)
	}
}

func TestNewNotificationRejectsInvalidInput(t *testing.T) {
	tests := map[string]struct {
		change func(*generated.CreateNotificationRequest)
		want   string
	}{
		"blank title":          {func(b *generated.CreateNotificationRequest) { b.Title = "   " }, "title is required"},
		"long title":           {func(b *generated.CreateNotificationRequest) { b.Title = strings.Repeat("ä", 201) }, "at most 200"},
		"multi-line title":     {func(b *generated.CreateNotificationRequest) { b.Title = "one\ntwo" }, "one line"},
		"long body":            {func(b *generated.CreateNotificationRequest) { b.Body = ptr(strings.Repeat("x", 2001)) }, "at most 2000"},
		"control in body":      {func(b *generated.CreateNotificationRequest) { b.Body = ptr("bell\a") }, "plain text"},
		"importance":           {func(b *generated.CreateNotificationRequest) { b.Importance = "urgent" }, "importance"},
		"single-part category": {func(b *generated.CreateNotificationRequest) { b.Category = "calendar" }, "category"},
		"upper-case category":  {func(b *generated.CreateNotificationRequest) { b.Category = "Calendar.reminder" }, "category"},
		"long category":        {func(b *generated.CreateNotificationRequest) { b.Category = "a." + strings.Repeat("b", 99) }, "category"},
		"source":               {func(b *generated.CreateNotificationRequest) { b.Source = ptr("Calendar Service") }, "source"},
		"empty source":         {func(b *generated.CreateNotificationRequest) { b.Source = ptr("") }, "source"},
		"absolute link":        {func(b *generated.CreateNotificationRequest) { b.Link = ptr("https://evil.example/") }, "link"},
		"protocol-relative":    {func(b *generated.CreateNotificationRequest) { b.Link = ptr("//evil.example/x") }, "link"},
		"backslash link":       {func(b *generated.CreateNotificationRequest) { b.Link = ptr("/\\evil.example") }, "link"},
		"javascript link":      {func(b *generated.CreateNotificationRequest) { b.Link = ptr("javascript:alert(1)") }, "link"},
		"link with space":      {func(b *generated.CreateNotificationRequest) { b.Link = ptr("/inbox item") }, "link"},
		"empty link":           {func(b *generated.CreateNotificationRequest) { b.Link = ptr("") }, "link"},
		"long link":            {func(b *generated.CreateNotificationRequest) { b.Link = ptr("/" + strings.Repeat("a", 500)) }, "link"},
		"missing dedupe key":   {func(b *generated.CreateNotificationRequest) { b.DedupeKey = "" }, "dedupeKey is required"},
		"padded dedupe key":    {func(b *generated.CreateNotificationRequest) { b.DedupeKey = " key" }, "spaces"},
		"long dedupe key":      {func(b *generated.CreateNotificationRequest) { b.DedupeKey = strings.Repeat("k", 201) }, "at most 200"},
		"control dedupe key":   {func(b *generated.CreateNotificationRequest) { b.DedupeKey = "a\x00b" }, "control"},
		"zero occurredAt":      {func(b *generated.CreateNotificationRequest) { b.OccurredAt = &time.Time{} }, "occurredAt"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			body := valid()
			test.change(&body)
			_, err := newNotification(body, activity.User(), time.Now())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want one containing %q", err, test.want)
			}
		})
	}
}

func TestNewNotificationAcceptsBoundaryValues(t *testing.T) {
	body := valid()
	body.Title = strings.Repeat("ä", 200)
	body.Body = ptr(strings.Repeat("ö", 2000))
	body.DedupeKey = strings.Repeat("k", 200)
	body.Link = ptr("/")
	body.Category = "inbox.human_review.v2"
	if _, err := newNotification(body, activity.User(), time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestSourceOfActor(t *testing.T) {
	tests := []struct {
		actor activity.Actor
		want  string
	}{
		{activity.User(), "dashboard"},
		{activity.System("calendar"), "calendar"},
		{activity.System("voice-worker"), "voice-worker"},
		{activity.Agent("codex-inbox-processor"), "codex-inbox-processor"},
		{activity.Agent("Inbox_Processor"), "inbox-processor"},
		{activity.System("..."), "system"},
		{activity.Dev("claude-code"), "dev"},
		{activity.Actor{}, "unknown"},
	}
	for _, test := range tests {
		if got := sourceOf(test.actor); got != test.want {
			t.Errorf("sourceOf(%+v) = %q, want %q", test.actor, got, test.want)
		}
	}
}
