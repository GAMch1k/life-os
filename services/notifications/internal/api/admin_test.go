package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/pkg/identity/identitytest"
)

// The delivery channels' health is Status's details, so the server's
// admins' (docs/architecture/multi-user.md#roles-and-checks).
func TestChannelsAreForAdmins(t *testing.T) {
	signer := identitytest.New(t)
	app := httpserver.New("notifications", httpserver.WithTrustedKeys(signer.Keys), httpserver.WithDiagnostics(nil))
	Register(app, NewHandler(nil, nil))
	member := "5c1d7a8e-0b7f-4d2a-9b1e-2f6a3c4d5e6f"
	for _, tc := range []struct {
		name, token string
		status      int
	}{
		{"the Dashboard (no token)", "", http.StatusOK},
		{"legacy access", signer.Token("", "owner", "legacy"), http.StatusOK},
		{"an admin with two-factor", signer.TwoFactor(member, "admin"), http.StatusOK},
		{"an admin signed in without two-factor", signer.Token(member, "admin", "session"), http.StatusForbidden},
		{"the owner's paired phone", signer.Token("", "owner", "device"), http.StatusForbidden},
		{"the owner's API token", signer.Token("", "owner", "token"), http.StatusForbidden},
		{"a member", signer.Token(member, "member", "session"), http.StatusForbidden},
	} {
		request := httptest.NewRequest(http.MethodGet, "/v1/channels", nil)
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
