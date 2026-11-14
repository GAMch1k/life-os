package google

import (
	"fmt"
	"strings"
	"testing"

	"github.com/GAMch1k/life-os/services/google-calendar/internal/calendar"
)

// What the log says went wrong is a code, never Google's or Calendar's own
// words, which can quote an event, nor a Google calendar's id, which is
// often an email address (docs/architecture/multi-user.md#logs).
func TestLogsCarryCodesNotMessages(t *testing.T) {
	marker := "Dentist with Anna at 4711.93"
	for _, tc := range []struct {
		err  error
		want string
	}{
		{&APIError{Status: 400, Message: "Invalid value for " + marker}, "google_http_400"},
		{&OAuthError{Status: 400, Code: "invalid_grant", Description: marker}, "google_oauth_invalid_grant"},
		{&OAuthError{Status: 400, Code: marker}, "google_oauth_other"},
		{&UnavailableError{Err: fmt.Errorf("dial %s", marker)}, "google_unavailable"},
		{&calendar.StatusError{Status: 400, Code: "invalid_item", Message: "items[0] " + marker}, "calendar_http_400_invalid_item"},
		{fmt.Errorf("export: %w", &calendar.StatusError{Status: 409, Code: marker}), "calendar_http_409_other"},
		{ErrExpired, "google_expired"},
		{fmt.Errorf("something about %s", marker), "failed"},
	} {
		got := logCode(tc.err)
		if got != tc.want || strings.Contains(got, "Anna") {
			t.Errorf("logCode(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
	if id := idHash("anna@example.com"); len(id) != 12 || strings.Contains(id, "anna") {
		t.Errorf("idHash = %q", id)
	}
}
