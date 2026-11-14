package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/pkg/identity/identitytest"
	"github.com/GAMch1k/life-os/services/backup/internal/backup"
)

// Backups and Back up now are the server's admins'
// (docs/architecture/multi-user.md#roles-and-checks): a member's identity
// token is refused; the owner, an admin, legacy access (the owner, as
// before accounts), and an internal caller without a token are let in.
func TestBackupsAreForAdmins(t *testing.T) {
	signer := identitytest.New(t)
	location, _ := time.LoadLocation("Europe/Berlin")
	app := httpserver.New("backup", httpserver.WithTrustedKeys(signer.Keys), httpserver.WithDiagnostics(nil))
	runner := &fakeRunner{startErr: backup.ErrLocked}
	Register(app, NewHandler(runner, fakeScheduler{}, Settings{
		Schedule: backup.Schedule{Minute: 195, Location: location}, RestoreCheckDay: "sunday",
	}))
	send := func(method, target, token string) int {
		t.Helper()
		request := httptest.NewRequest(method, target, strings.NewReader(""))
		request.Header.Set("X-Life-OS-Actor", "user")
		if token != "" {
			request.Header.Set("X-Life-OS-Identity", token)
		}
		response, err := app.Test(request)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode
	}
	for _, tc := range []struct {
		name, token string
		allowed     bool
	}{
		{"no token (an internal caller)", "", true},
		{"the owner with two-factor", signer.TwoFactor("", "owner"), true},
		{"the owner signed in without two-factor", signer.Token("", "owner", "session"), false},
		{"the owner's paired phone", signer.Token("", "owner", "device"), false},
		{"an admin with two-factor", signer.TwoFactor("5c1d7a8e-0b7f-4d2a-9b1e-2f6a3c4d5e6f", "admin"), true},
		{"legacy access", signer.Token("", "owner", "legacy"), true},
		{"a member", signer.Token("5c1d7a8e-0b7f-4d2a-9b1e-2f6a3c4d5e6f", "member", "session"), false},
	} {
		for _, route := range [][2]string{{http.MethodGet, "/v1/status"}, {http.MethodPost, "/v1/backups"}, {http.MethodGet, "/v1/backups"}} {
			status := send(route[0], route[1], tc.token)
			if refused := status == http.StatusForbidden; refused == tc.allowed {
				t.Errorf("%s, %s %s: %d", tc.name, route[0], route[1], status)
			}
		}
	}
	if status := send(http.MethodGet, "/health", signer.Token("5c1d7a8e-0b7f-4d2a-9b1e-2f6a3c4d5e6f", "member", "session")); status != http.StatusOK {
		t.Errorf("/health with a member's token: %d", status)
	}
}
