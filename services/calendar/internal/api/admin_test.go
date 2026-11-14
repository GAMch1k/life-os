package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/pkg/identity/identitytest"
)

// Calendar's reminder delivery health is Status's details, so the server's
// admins' (docs/architecture/multi-user.md#roles-and-checks).
func TestReminderStatusIsForAdmins(t *testing.T) {
	signer := identitytest.New(t)
	app := httpserver.New("calendar", httpserver.WithTrustedKeys(signer.Keys), httpserver.WithDiagnostics(nil))
	Register(app, NewHandler(nil, "UTC"))
	member := "5c1d7a8e-0b7f-4d2a-9b1e-2f6a3c4d5e6f"
	for _, tc := range []struct {
		name, token string
		status      int
	}{
		{"the Dashboard (no token)", "", http.StatusOK},
		{"legacy access", signer.Token("", "owner", "legacy"), http.StatusOK},
		{"an admin", signer.Token(member, "admin", "session"), http.StatusOK},
		{"a member", signer.Token(member, "member", "session"), http.StatusForbidden},
	} {
		request := httptest.NewRequest(http.MethodGet, "/v1/reminders/status", nil)
		if tc.token != "" {
			request.Header.Set("X-Life-OS-Identity", tc.token)
		}
		response, err := app.Test(request)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != tc.status {
			t.Errorf("%s: %d, want %d", tc.name, response.StatusCode, tc.status)
		}
	}
}
