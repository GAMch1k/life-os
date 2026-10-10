package google

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Endpoints are Google's OAuth and Calendar API addresses; tests point them
// at a fake.
type Endpoints struct {
	AuthURL     string
	TokenURL    string
	RevokeURL   string
	CalendarAPI string
}

// GoogleEndpoints are the real ones.
var GoogleEndpoints = Endpoints{
	AuthURL:     "https://accounts.google.com/o/oauth2/v2/auth",
	TokenURL:    "https://oauth2.googleapis.com/token",
	RevokeURL:   "https://oauth2.googleapis.com/revoke",
	CalendarAPI: "https://www.googleapis.com/calendar/v3",
}

// Token is what the token endpoint returns. RefreshToken is empty when
// Google issues none (a refresh usually keeps the old one). ExpiresIn is the
// access token's lifetime from when it was requested.
type Token struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    time.Duration
	Scopes       []string
}

// defaultLifetime is assumed when Google does not say how long an access
// token lasts (it says; this is a safe fallback).
const defaultLifetime = time.Hour

// CalendarEntry is one calendar of the account's calendar list.
type CalendarEntry struct {
	ID         string
	Summary    string
	Color      string
	AccessRole string
	Primary    bool
}

// API is Google as Calendar uses it.
type API interface {
	// Exchange trades an authorization code (and its PKCE verifier) for tokens.
	Exchange(ctx context.Context, code, verifier string) (Token, error)
	// Refresh gets a new access token from a refresh token.
	Refresh(ctx context.Context, refreshToken string) (Token, error)
	// Revoke withdraws the authorization a token belongs to.
	Revoke(ctx context.Context, token string) error
	// CalendarList reads every calendar in the account's list.
	CalendarList(ctx context.Context, accessToken string) ([]CalendarEntry, error)
	// Events reads one page of a calendar's events (see Client.Events).
	Events(ctx context.Context, accessToken, calendarID string, query EventsQuery) (EventsPage, error)
}

// OAuthError is a refusal from Google's OAuth endpoints, such as
// invalid_grant (the refresh token expired or was revoked) or
// invalid_client (the client id or secret is wrong).
type OAuthError struct {
	Status      int
	Code        string
	Description string
}

func (e *OAuthError) Error() string {
	message := "Google refused the request (" + e.Code
	if e.Description != "" {
		message += ": " + e.Description
	}
	return message + ")"
}

// APIError is a refusal from the Calendar API.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("Google Calendar refused the request (HTTP %d)", e.Status)
	}
	return fmt.Sprintf("Google Calendar refused the request (HTTP %d: %s)", e.Status, e.Message)
}

// UnavailableError means Google could not be reached or failed on its side;
// trying again later may succeed.
type UnavailableError struct {
	Err error
}

func (e *UnavailableError) Error() string { return "Google is unavailable: " + e.Err.Error() }
func (e *UnavailableError) Unwrap() error { return e.Err }

// IsInvalidGrant reports whether Google refused a grant: a refresh token
// that expired or was revoked, or a code that expired or was used.
func IsInvalidGrant(err error) bool {
	var oauth *OAuthError
	return errors.As(err, &oauth) && oauth.Code == "invalid_grant"
}

// maxResponseBytes bounds what Calendar reads of one Google response.
const maxResponseBytes = 4 << 20

// Client implements API over net/http.
type Client struct {
	config    Config
	endpoints Endpoints
	http      *http.Client
}

// NewClient talks to endpoints as the client in config. A nil httpClient
// uses one with a 15 s timeout.
func NewClient(config Config, endpoints Endpoints, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &Client{config: config, endpoints: endpoints, http: httpClient}
}

// AuthURL is the consent page URL (see Config.AuthURL).
func (c *Client) AuthURL(state, challenge, loginHint string) string {
	return c.config.AuthURL(c.endpoints.AuthURL, state, challenge, loginHint)
}

func (c *Client) Exchange(ctx context.Context, code, verifier string) (Token, error) {
	return c.token(ctx, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {verifier},
		"redirect_uri":  {c.config.RedirectURI},
	})
}

func (c *Client) Refresh(ctx context.Context, refreshToken string) (Token, error) {
	return c.token(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refreshToken}})
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
	// Error fields, set on a refusal.
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

func (c *Client) token(ctx context.Context, form url.Values) (Token, error) {
	form.Set("client_id", c.config.ClientID)
	form.Set("client_secret", c.config.ClientSecret)
	response, err := c.postForm(ctx, c.endpoints.TokenURL, form)
	if err != nil {
		return Token{}, err
	}
	defer response.Body.Close()
	var body tokenResponse
	decodeErr := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(&body)
	if response.StatusCode >= 500 {
		return Token{}, &UnavailableError{Err: fmt.Errorf("token endpoint returned HTTP %d", response.StatusCode)}
	}
	if response.StatusCode != http.StatusOK || body.Error != "" {
		if body.Error == "" {
			body.Error = fmt.Sprintf("http_%d", response.StatusCode)
		}
		return Token{}, &OAuthError{Status: response.StatusCode, Code: body.Error, Description: clip(body.ErrorDescription, 200)}
	}
	if decodeErr != nil || body.AccessToken == "" {
		return Token{}, &UnavailableError{Err: errors.New("token endpoint returned no access token")}
	}
	token := Token{AccessToken: body.AccessToken, RefreshToken: body.RefreshToken, Scopes: strings.Fields(body.Scope), ExpiresIn: defaultLifetime}
	if body.ExpiresIn > 0 {
		token.ExpiresIn = time.Duration(body.ExpiresIn) * time.Second
	}
	return token, nil
}

// Revoke succeeds also when Google no longer knows the token (already
// revoked or expired).
func (c *Client) Revoke(ctx context.Context, token string) error {
	response, err := c.postForm(ctx, c.endpoints.RevokeURL, url.Values{"token": {token}})
	if err != nil {
		return err
	}
	defer response.Body.Close()
	var body tokenResponse
	_ = json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(&body)
	switch {
	case response.StatusCode == http.StatusOK:
		return nil
	case response.StatusCode == http.StatusBadRequest && (body.Error == "invalid_token" || body.Error == "invalid_grant"):
		return nil
	case response.StatusCode >= 500:
		return &UnavailableError{Err: fmt.Errorf("revoke endpoint returned HTTP %d", response.StatusCode)}
	default:
		return &OAuthError{Status: response.StatusCode, Code: firstNonEmpty(body.Error, fmt.Sprintf("http_%d", response.StatusCode)), Description: clip(body.ErrorDescription, 200)}
	}
}

func (c *Client) postForm(ctx context.Context, endpoint string, form url.Values) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return nil, &UnavailableError{Err: transportError(err)}
	}
	return response, nil
}

type calendarListResponse struct {
	Items []struct {
		ID              string `json:"id"`
		Summary         string `json:"summary"`
		SummaryOverride string `json:"summaryOverride"`
		BackgroundColor string `json:"backgroundColor"`
		AccessRole      string `json:"accessRole"`
		Primary         bool   `json:"primary"`
		Deleted         bool   `json:"deleted"`
	} `json:"items"`
	NextPageToken string `json:"nextPageToken"`
}

type apiErrorResponse struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

// maxCalendarPages bounds the pages read of one calendar list.
const maxCalendarPages = 20

func (c *Client) CalendarList(ctx context.Context, accessToken string) ([]CalendarEntry, error) {
	var entries []CalendarEntry
	pageToken := ""
	for range maxCalendarPages {
		query := url.Values{"maxResults": {"250"}}
		if pageToken != "" {
			query.Set("pageToken", pageToken)
		}
		var page calendarListResponse
		if err := c.getJSON(ctx, c.endpoints.CalendarAPI+"/users/me/calendarList?"+query.Encode(), accessToken, &page); err != nil {
			return nil, err
		}
		for _, item := range page.Items {
			if item.Deleted || item.ID == "" {
				continue
			}
			summary := firstNonEmpty(item.SummaryOverride, item.Summary, item.ID)
			entries = append(entries, CalendarEntry{
				ID: item.ID, Summary: summary, Color: item.BackgroundColor, AccessRole: item.AccessRole, Primary: item.Primary,
			})
		}
		if page.NextPageToken == "" {
			return entries, nil
		}
		pageToken = page.NextPageToken
	}
	return nil, &UnavailableError{Err: errors.New("the calendar list has too many pages")}
}

func (c *Client) getJSON(ctx context.Context, target, accessToken string, out any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("Accept", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return &UnavailableError{Err: transportError(err)}
	}
	defer response.Body.Close()
	reader := io.LimitReader(response.Body, maxResponseBytes)
	if response.StatusCode >= 500 {
		return &UnavailableError{Err: fmt.Errorf("Calendar API returned HTTP %d", response.StatusCode)}
	}
	if response.StatusCode != http.StatusOK {
		var body apiErrorResponse
		_ = json.NewDecoder(reader).Decode(&body)
		return &APIError{Status: response.StatusCode, Message: clip(body.Error.Message, 200)}
	}
	if err := json.NewDecoder(reader).Decode(out); err != nil {
		return &UnavailableError{Err: errors.New("Calendar API returned an unreadable response")}
	}
	return nil
}

// transportError drops the request URL from a transport failure (it may
// carry a page token) and keeps the reason.
func transportError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err
	}
	return err
}

func clip(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	if runes := []rune(text); len(runes) > limit {
		return string(runes[:limit-1]) + "…"
	}
	return text
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
