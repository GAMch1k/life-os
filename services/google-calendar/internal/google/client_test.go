package google_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/GAMch1k/life-os/services/google-calendar/internal/google"
	"github.com/GAMch1k/life-os/services/google-calendar/internal/google/fakegoogle"
)

func fakeClient(t *testing.T, calendars ...fakegoogle.Calendar) (*google.Client, *fakegoogle.Server) {
	t.Helper()
	fake := fakegoogle.New(t, calendars...)
	config := google.Config{ClientID: fakegoogle.ClientID, ClientSecret: fakegoogle.ClientSecret, RedirectURI: google.DefaultRedirectURI, TokenKey: make([]byte, 32)}
	return google.NewClient(config, fake.Endpoints(), nil), fake
}

func challengeOf(t *testing.T, raw string) string {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.Query().Get("code_challenge")
}

func TestClientExchangesRefreshesListsAndRevokes(t *testing.T) {
	client, fake := fakeClient(t,
		fakegoogle.Calendar{ID: "owner@example.com", Summary: "Owner", Color: "#9fe1e7", AccessRole: "owner", Primary: true},
		fakegoogle.Calendar{ID: "family@group.calendar.google.com", Summary: "Family", AccessRole: "writer"},
		fakegoogle.Calendar{ID: "de.german#holiday@group.v.calendar.google.com", Summary: "Holidays in Germany", AccessRole: "reader"},
	)
	ctx := context.Background()
	verifier, _ := google.NewVerifier()
	code := fake.IssueCode(challengeOf(t, client.AuthURL("state", google.Challenge(verifier), "")))

	// A wrong verifier is refused: the code is bound to the challenge.
	if _, err := client.Exchange(ctx, code, "wrong-verifier-wrong-verifier-wrong-verifier"); !google.IsInvalidGrant(err) {
		t.Fatalf("wrong verifier: %v", err)
	}
	code = fake.IssueCode(google.Challenge(verifier))
	token, err := client.Exchange(ctx, code, verifier)
	if err != nil || token.AccessToken == "" || token.RefreshToken == "" || token.ExpiresIn != time.Hour || len(token.Scopes) != 3 {
		t.Fatalf("exchange = %+v, %v", token, err)
	}

	// Three calendars over two pages.
	entries, err := client.CalendarList(ctx, token.AccessToken)
	if err != nil || len(entries) != 3 || !entries[0].Primary || entries[0].ID != "owner@example.com" || entries[0].Color != "#9fe1e7" ||
		entries[2].ID != "de.german#holiday@group.v.calendar.google.com" || entries[1].AccessRole != "writer" {
		t.Fatalf("calendar list = %+v, %v", entries, err)
	}
	if _, _, lists := fake.Counts(); lists != 2 {
		t.Fatalf("pages read = %d, want 2", lists)
	}
	var apiErr *google.APIError
	if _, err := client.CalendarList(ctx, "not-a-token"); !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized {
		t.Fatalf("bad access token: %v", err)
	}

	refreshed, err := client.Refresh(ctx, token.RefreshToken)
	if err != nil || refreshed.AccessToken == "" || refreshed.AccessToken == token.AccessToken || refreshed.RefreshToken != "" {
		t.Fatalf("refresh = %+v, %v", refreshed, err)
	}

	if err := client.Revoke(ctx, token.RefreshToken); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	// Revoking again, or a token Google no longer knows, is not a failure.
	if err := client.Revoke(ctx, token.RefreshToken); err != nil {
		t.Fatalf("revoke again: %v", err)
	}
	_, err = client.Refresh(ctx, token.RefreshToken)
	if !google.IsInvalidGrant(err) {
		t.Fatalf("refresh after revoke: %v", err)
	}
	for _, secret := range append(fake.Tokens(), fakegoogle.ClientSecret) {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error %q contains a secret", err)
		}
	}
}

func TestClientClassifiesRefusalsAndOutages(t *testing.T) {
	client, fake := fakeClient(t)
	ctx := context.Background()

	fake.Lock()
	fake.TokenStatus = http.StatusServiceUnavailable
	fake.Unlock()
	var unavailable *google.UnavailableError
	if _, err := client.Refresh(ctx, "anything"); !errors.As(err, &unavailable) || google.IsInvalidGrant(err) {
		t.Fatalf("503: %v", err)
	}

	fake.Lock()
	fake.TokenStatus = 0
	fake.Unlock()
	wrong := google.NewClient(google.Config{ClientID: "other", ClientSecret: "nope", RedirectURI: google.DefaultRedirectURI}, fake.Endpoints(), nil)
	var oauth *google.OAuthError
	if _, err := wrong.Refresh(ctx, "anything"); !errors.As(err, &oauth) || oauth.Code != "invalid_client" || google.IsInvalidGrant(err) {
		t.Fatalf("invalid client: %v", err)
	}

	fake.Close()
	if _, err := client.Refresh(ctx, "anything"); !errors.As(err, &unavailable) || strings.Contains(err.Error(), fake.URL) {
		t.Fatalf("unreachable: %v", err)
	}
}
