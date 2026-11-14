package feeds

import (
	"fmt"
	"strings"
	"testing"

	"github.com/GAMch1k/life-os/services/calendar-feeds/internal/calendar"
)

// The log says what Calendar answered as its status and code, never its
// message, which can quote an event (docs/architecture/multi-user.md#logs).
func TestCalendarCodeCarriesNoMessage(t *testing.T) {
	marker := "Restmüll at Anna's"
	for _, tc := range []struct {
		err  error
		want string
	}{
		{&calendar.StatusError{Status: 400, Code: "invalid_item", Message: marker}, "calendar_http_400_invalid_item"},
		{fmt.Errorf("register: %w", &calendar.StatusError{Status: 409, Code: marker}), "calendar_http_409_other"},
		{calendar.ErrNotConfigured, "calendar_not_configured"},
		{fmt.Errorf("dial: %s", marker), "calendar_unreachable"},
	} {
		if got := calendarCode(tc.err); got != tc.want || strings.Contains(got, "Anna") {
			t.Errorf("calendarCode(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}
