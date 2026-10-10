package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/GAMch1k/life-os/services/notifications/internal/api/generated"
)

func (c apiClient) channels() []generated.Channel {
	c.t.Helper()
	status, raw := c.do(http.MethodGet, "/v1/channels", nil)
	if status != http.StatusOK {
		c.t.Fatalf("GET /v1/channels = %d %s", status, raw)
	}
	return decode[generated.ChannelList](c.t, raw).Items
}

// setDelivery puts one delivery into a state, as the worker would have left
// it ago before now.
func (c apiClient) setDelivery(notification generated.Notification, channel, status, lastError string, ago time.Duration) {
	c.t.Helper()
	var err error
	at := time.Now().Add(-ago)
	switch status {
	case "sent":
		_, err = c.pool.Exec(context.Background(), `UPDATE deliveries SET status = 'sent', attempts = 1, sent_at = $3,
			next_attempt_at = NULL, updated_at = $3 WHERE notification_id = $1 AND channel = $2`, notification.Id, channel, at)
	case "retrying":
		_, err = c.pool.Exec(context.Background(), `UPDATE deliveries SET status = 'retrying', attempts = 1, last_error = $4,
			next_attempt_at = now() + INTERVAL '1 minute', updated_at = $3 WHERE notification_id = $1 AND channel = $2`, notification.Id, channel, at, lastError)
	default:
		_, err = c.pool.Exec(context.Background(), `UPDATE deliveries SET status = $4, attempts = 1, last_error = $5,
			next_attempt_at = NULL, updated_at = $3 WHERE notification_id = $1 AND channel = $2`, notification.Id, channel, at, status, lastError)
	}
	if err != nil {
		c.t.Fatal(err)
	}
}

func TestChannelsReportDeliveryHealth(t *testing.T) {
	c := newAPIClient(t, "ntfy", "log")
	items := c.channels()
	if len(items) != 2 || items[0].Name != "ntfy" || items[1].Name != "log" {
		t.Fatalf("channels = %+v, want ntfy then log", items)
	}
	for _, item := range items {
		if item.State != generated.Idle || item.LastSuccessAt != nil || item.LastFailureAt != nil || item.LastError != nil ||
			item.RecentFailures != 0 || item.Retrying != 0 || item.Pending != 0 {
			t.Errorf("unused channel = %+v, want idle", item)
		}
	}

	first := c.create(notification("First", "low", "test.manual", "test.manual:health-1"), http.StatusCreated)
	second := c.create(notification("Second", "high", "status.service_down", "test.manual:health-2"), http.StatusCreated)
	c.create(notification("Third", "medium", "inbox.human_review", "test.manual:health-3"), http.StatusCreated)
	// ntfy: sent two hours ago, then a failure an hour ago that is being
	// retried; the third is still pending.
	c.setDelivery(first, "ntfy", "sent", "", 2*time.Hour)
	c.setDelivery(second, "ntfy", "retrying", "ntfy is unreachable: dial tcp: lookup ntfy", time.Hour)
	// log: a failure 30 hours ago, outside the recent window, then a success.
	c.setDelivery(second, "log", "failed", "an old failure", 30*time.Hour)
	c.setDelivery(first, "log", "sent", "", 10*time.Minute)

	items = c.channels()
	ntfy, logChannel := items[0], items[1]
	if ntfy.State != generated.Failing || ntfy.LastError == nil || *ntfy.LastError != "ntfy is unreachable: dial tcp: lookup ntfy" ||
		ntfy.RecentFailures != 1 || ntfy.Retrying != 1 || ntfy.Pending != 1 {
		t.Errorf("ntfy = %+v, want failing with one retrying, one pending", ntfy)
	}
	if ntfy.LastSuccessAt == nil || ntfy.LastFailureAt == nil || !ntfy.LastFailureAt.After(*ntfy.LastSuccessAt) {
		t.Errorf("ntfy times = %v, %v, want a failure after the success", ntfy.LastSuccessAt, ntfy.LastFailureAt)
	}
	if logChannel.State != generated.Ok || logChannel.RecentFailures != 0 || logChannel.Retrying != 0 || logChannel.Pending != 1 ||
		logChannel.LastError == nil || *logChannel.LastError != "an old failure" {
		t.Errorf("log = %+v, want ok with its old failure outside the recent window", logChannel)
	}

	// A later success clears the failing state.
	c.setDelivery(second, "ntfy", "sent", "", time.Minute)
	if got := c.channels()[0]; got.State != generated.Ok || got.Retrying != 0 {
		t.Errorf("ntfy after a success = %+v, want ok", got)
	}
}

func TestNoChannelsListsNone(t *testing.T) {
	c := newAPIClient(t)
	if items := c.channels(); len(items) != 0 {
		t.Fatalf("channels = %+v, want none", items)
	}
}
