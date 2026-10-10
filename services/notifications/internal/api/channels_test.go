package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/GAMch1k/life-os/services/notifications/internal/api/generated"
)

func TestChannelState(t *testing.T) {
	earlier := time.Date(2026, 10, 10, 8, 0, 0, 0, time.UTC)
	later := earlier.Add(time.Minute)
	for _, tc := range []struct {
		name             string
		success, failure *time.Time
		want             generated.ChannelState
	}{
		{"never used", nil, nil, generated.Idle},
		{"only successes", &later, nil, generated.Ok},
		{"only failures", nil, &earlier, generated.Failing},
		{"failure after success", &earlier, &later, generated.Failing},
		{"success after failure", &later, &earlier, generated.Ok},
	} {
		if got := channelState(tc.success, tc.failure); got != tc.want {
			t.Errorf("%s: state = %s, want %s", tc.name, got, tc.want)
		}
	}
}

func TestNoConfiguredChannelNeedsNoDatabase(t *testing.T) {
	app := newTestApp(NewHandler(nil, nil))
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/v1/channels", nil), fiberTestConfig)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(raw) != `{"items":[]}` {
		t.Fatalf("GET /v1/channels = %d %s", resp.StatusCode, raw)
	}
}
