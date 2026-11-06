package config

import (
	"strings"
	"testing"
)

func TestGoogleSettings(t *testing.T) {
	t.Parallel()

	base := map[string]string{"DATABASE_URL": "postgres://google_calendar_user:secret@localhost/google_calendar_db"}
	cfg, err := LoadFrom(mapLookup(base))
	if err != nil {
		t.Fatal(err)
	}
	unset, err := cfg.Google()
	if err != nil || unset.Configured() || cfg.GoogleRedirectURI != "" {
		t.Fatalf("unset: %+v, %v (redirect %q)", unset, err, cfg.GoogleRedirectURI)
	}

	// A partial or broken configuration does not stop the service: it is
	// reported and Google stays unconfigured.
	partial := map[string]string{"LIFE_OS_GOOGLE_CLIENT_ID": "id.apps.googleusercontent.com", "LIFE_OS_GOOGLE_TOKEN_KEY": "short"}
	for key, value := range base {
		partial[key] = value
	}
	cfg, err = LoadFrom(mapLookup(partial))
	if err != nil {
		t.Fatalf("a partial Google configuration failed the load: %v", err)
	}
	if got, err := cfg.Google(); err == nil || got.Configured() || !strings.Contains(err.Error(), "LIFE_OS_GOOGLE_CLIENT_SECRET") {
		t.Fatalf("partial: %+v, %v", got, err)
	}

	full := map[string]string{
		"LIFE_OS_GOOGLE_CLIENT_ID": "id.apps.googleusercontent.com", "LIFE_OS_GOOGLE_CLIENT_SECRET": "secret",
		"LIFE_OS_GOOGLE_TOKEN_KEY":    "KioqKioqKioqKioqKioqKioqKioqKioqKioqKioqKio=",
		"LIFE_OS_GOOGLE_REDIRECT_URI": "http://localhost:3000/calendar/google/callback",
	}
	for key, value := range base {
		full[key] = value
	}
	cfg, err = LoadFrom(mapLookup(full))
	if err != nil {
		t.Fatal(err)
	}
	got, err := cfg.Google()
	if err != nil || !got.Configured() || got.RedirectURI != "http://localhost:3000/calendar/google/callback" || len(got.TokenKey) != 32 {
		t.Fatalf("full: %+v, %v", got, err)
	}
}
