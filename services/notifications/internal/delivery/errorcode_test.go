package delivery

import (
	"fmt"
	"strings"
	"testing"

	"github.com/GAMch1k/life-os/pkg/diagnostics"
	"github.com/GAMch1k/life-os/services/notifications/internal/webpush"
)

// A delivery's failure is logged as a classified code, never the channel's
// answer, which can quote the notification (docs/architecture/multi-user.md#logs).
func TestDeliveryFailuresAreLoggedAsCodes(t *testing.T) {
	marker := `{"error":"Dentist with Anna"}`
	for _, tc := range []struct {
		err  error
		want string
	}{
		{&ntfyRefusal{status: 429, text: marker}, "http_429"},
		{Permanent(&ntfyRefusal{status: 403, text: marker}), "http_403"},
		{&webpush.StatusError{StatusCode: 410, Text: marker}, "http_410"},
		{fmt.Errorf("2 of 3 devices failed for now: %s", marker), "go_errors.errorString"},
	} {
		got := diagnostics.ErrorCode(tc.err)
		if got != tc.want || strings.Contains(got, "Anna") {
			t.Errorf("ErrorCode(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
	// The delivery's own record keeps the words, for Status.
	if err := (&ntfyRefusal{status: 403, text: "forbidden"}); err.Error() != "ntfy refused the notification: 403 forbidden" {
		t.Errorf("message = %q", err.Error())
	}
}
