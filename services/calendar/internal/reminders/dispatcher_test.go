package reminders

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/pkg/notify"
	"github.com/GAMch1k/life-os/services/calendar/internal/api"
	"github.com/GAMch1k/life-os/services/calendar/internal/api/generated"
)

func TestDeliveryIsOffWithoutNotifications(t *testing.T) {
	d := New(nil, nil, notify.New("", "calendar"), nil, Config{}, quiet)
	if d.Enabled() {
		t.Fatal("delivery without NOTIFICATIONS_SERVICE_URL must be off")
	}
	// Run returns at once instead of polling.
	d.Run(context.Background())
	if d.interval.Seconds() != 30 || d.grace.Hours() != 2 || d.lateAfter.Minutes() != 1 {
		t.Fatalf("defaults: interval %s, grace %s, late after %s", d.interval, d.grace, d.lateAfter)
	}
}

func TestStatusReadsAsOffWithoutDelivery(t *testing.T) {
	app := httpserver.New("calendar", httpserver.WithLogger(httpserver.NewJSONLogger(io.Discard, nil)))
	api.Register(app, api.NewHandler(nil, ""))
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/v1/reminders/status", nil))
	if err != nil {
		t.Fatal(err)
	}
	var status generated.ReminderDeliveryStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil || resp.StatusCode != http.StatusOK || status.Enabled || status.HandledThrough != nil {
		t.Fatalf("status = %d %+v %v", resp.StatusCode, status, err)
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
