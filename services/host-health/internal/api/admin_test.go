package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/pkg/identity/identitytest"
	"github.com/GAMch1k/life-os/services/host-health/internal/health"
)

// The host's memory, disks, and containers are the server's admins'
// (docs/architecture/multi-user.md#roles-and-checks): a member's identity
// token is refused; the owner, an admin, legacy access (the owner, as
// before accounts), and the Dashboard without a token are let in.
func TestHostHealthIsForAdmins(t *testing.T) {
	signer := identitytest.New(t)
	fixture := health.Fixture{Scenario: health.FixtureHealthy}
	sampler := health.NewSampler(health.Options{Mode: "fixture", Host: fixture, Docker: fixture, Usage: fixture, Thresholds: health.DefaultThresholds(), Now: func() time.Time { return now }})
	sampler.SampleOnce(context.Background())
	app := httpserver.New("host-health", httpserver.WithTrustedKeys(signer.Keys), httpserver.WithDiagnostics(nil))
	handler := NewHandler(sampler, time.Minute)
	handler.now = func() time.Time { return now }
	Register(app, handler)
	member := "5c1d7a8e-0b7f-4d2a-9b1e-2f6a3c4d5e6f"
	for _, tc := range []struct {
		name, token string
		status      int
	}{
		{"no token (the Dashboard)", "", http.StatusOK},
		{"the owner with two-factor", signer.TwoFactor("", "owner"), http.StatusOK},
		{"an admin with two-factor", signer.TwoFactor(member, "admin"), http.StatusOK},
		{"an admin signed in without two-factor", signer.Token(member, "admin", "session"), http.StatusForbidden},
		{"the owner's paired phone", signer.Token("", "owner", "device"), http.StatusForbidden},
		{"the owner's API token", signer.Token("", "owner", "token"), http.StatusForbidden},
		{"legacy access", signer.Token("", "owner", "legacy"), http.StatusOK},
		{"a member", signer.Token(member, "member", "session"), http.StatusForbidden},
	} {
		for _, target := range []string{"/v1/status", "/v1/history?hours=24"} {
			request := httptest.NewRequest(http.MethodGet, target, nil)
			if tc.token != "" {
				request.Header.Set("X-Life-OS-Identity", tc.token)
			}
			response, err := app.Test(request)
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != tc.status {
				t.Errorf("%s, %s: %d, want %d", tc.name, target, response.StatusCode, tc.status)
			}
		}
	}
}
