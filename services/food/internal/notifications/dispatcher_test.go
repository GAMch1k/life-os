package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/pkg/notify"
	"github.com/GAMch1k/life-os/services/food/internal/api"
	"github.com/GAMch1k/life-os/services/food/internal/api/generated"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestRemindersAreOffWithoutTheNotificationsService(t *testing.T) {
	d := New(nil, notify.New("", "food"), nil, Config{}, quiet)
	if d.Enabled() {
		t.Fatal("reminders without NOTIFICATIONS_SERVICE_URL must be off")
	}
	// Run returns at once instead of polling.
	d.Run(context.Background())
	if d.interval.Seconds() != 30 || d.grace.Hours() != 2 || d.lateAfter.Minutes() != 1 || d.expiry != (Clock{8, 0}) || d.owner != time.UTC {
		t.Fatalf("defaults: interval %s, grace %s, late after %s, expiry %s", d.interval, d.grace, d.lateAfter, d.expiry)
	}
}

func TestStatusReportsTheSchedule(t *testing.T) {
	app := httpserver.New("food", httpserver.WithLogger(httpserver.NewJSONLogger(io.Discard, nil)))
	api.Register(app, api.NewHandler(nil, api.Options{}))
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/v1/notifications/status", nil))
	if err != nil {
		t.Fatal(err)
	}
	var status generated.FoodNotificationStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil || resp.StatusCode != http.StatusOK || status.Enabled || status.HandledThrough != nil {
		t.Fatalf("without reminders: %d %+v %v", resp.StatusCode, status, err)
	}

	d := New(nil, notify.New("http://notifications:8080", "food"), nil, Config{Owner: berlin, ExpiryTime: Clock{7, 30}, Interval: 20 * time.Second, Grace: time.Hour}, quiet)
	got, err := d.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := generated.FoodNotificationStatus{Enabled: true, IntervalSeconds: 20, GraceMinutes: 60, TimeZone: "Europe/Berlin", ExpiryTime: "07:30"}
	if got != want {
		t.Fatalf("status = %+v\nwant %+v", got, want)
	}
}

func TestPermanentFailures(t *testing.T) {
	for err, want := range map[error]bool{
		fmt.Errorf("%w: title is required", notify.ErrInvalid):                          true,
		fmt.Errorf("send: %w", &notify.StatusError{Status: http.StatusBadRequest}):      true,
		fmt.Errorf("send: %w", &notify.StatusError{Status: http.StatusTooManyRequests}): false,
		fmt.Errorf("send: %w", &notify.StatusError{Status: http.StatusBadGateway}):      false,
		errors.New("connection refused"):                                                false,
		context.DeadlineExceeded:                                                        false,
	} {
		if got := permanent(err); got != want {
			t.Errorf("permanent(%v) = %v, want %v", err, got, want)
		}
	}
}
