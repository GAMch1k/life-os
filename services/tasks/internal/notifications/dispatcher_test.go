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
	"github.com/GAMch1k/life-os/services/tasks/internal/api"
	"github.com/GAMch1k/life-os/services/tasks/internal/api/generated"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestNotificationsAreOffWithoutTheNotificationsService(t *testing.T) {
	d := New(nil, notify.New("", "tasks"), nil, nil, Config{}, quiet)
	if d.Enabled() {
		t.Fatal("notifications without NOTIFICATIONS_SERVICE_URL must be off")
	}
	// Run returns at once instead of polling.
	d.Run(context.Background())
	if d.interval.Seconds() != 30 || d.grace.Hours() != 2 || d.lateAfter.Minutes() != 1 || d.schedule != DefaultSchedule() || d.owner != time.UTC {
		t.Fatalf("defaults: interval %s, grace %s, late after %s, schedule %+v", d.interval, d.grace, d.lateAfter, d.schedule)
	}
}

func TestStatusReportsTheSchedule(t *testing.T) {
	app := httpserver.New("tasks", httpserver.WithLogger(httpserver.NewJSONLogger(io.Discard, nil)))
	api.Register(app, api.NewHandler(nil, nil))
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/v1/notifications/status", nil))
	if err != nil {
		t.Fatal(err)
	}
	var status generated.TaskNotificationStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil || resp.StatusCode != http.StatusOK || status.Enabled || status.HandledThrough != nil {
		t.Fatalf("without notifications: %d %+v %v", resp.StatusCode, status, err)
	}

	schedule := DefaultSchedule()
	schedule.Summary = Clock{9, 0}
	d := New(nil, notify.New("http://notifications:8080", "tasks"), NewCalendar("http://calendar:8080", berlin), nil, Config{Owner: berlin, Schedule: schedule}, quiet)
	got, err := d.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := generated.TaskNotificationStatus{
		Enabled: true, CalendarConfigured: true, IntervalSeconds: 30, GraceMinutes: 120, TimeZone: "Europe/Berlin",
		SummaryTime: "09:00", MorningTime: "09:00", EveningTime: "19:00", TimedLeadMinutes: 30, MergedMorning: true,
	}
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
