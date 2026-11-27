package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/pkg/identity/identitytest"
	"github.com/GAMch1k/life-os/services/notifications/internal/api/generated"
	"github.com/GAMch1k/life-os/services/notifications/internal/email"
)

// fakeMailer is email as the API sees it.
type fakeMailer struct {
	configured bool
	problem    string
	result     error
	state      email.State
	sent       []email.Message
}

func (f *fakeMailer) Configured() bool { return f.configured }
func (f *fakeMailer) Problem() string  { return f.problem }
func (f *fakeMailer) Send(_ context.Context, message email.Message) error {
	f.sent = append(f.sent, message)
	return f.result
}
func (f *fakeMailer) Status(context.Context) (email.State, error) { return f.state, nil }
func (f *fakeMailer) TestMessage(to string) email.Message {
	return email.Message{To: to, Subject: "Life OS can send email", Text: "test", Kind: "test"}
}

const verifyEmail = `{"to":"owner@example.com","subject":"Verify your email address","text":"Open https://lifeos.example.com/verify-email/abc","html":"<p>Open <a href=\"https://lifeos.example.com/verify-email/abc\">the link</a>.</p>","kind":"verify_email"}`

func emailApp(t *testing.T, mailer Mailing) (*httpserver.App, identitytest.Signer) {
	t.Helper()
	signer := identitytest.New(t)
	app := httpserver.New("notifications", httpserver.WithLogger(httpserver.NewJSONLogger(io.Discard, nil)),
		httpserver.WithTrustedKeys(signer.Keys), httpserver.WithDiagnostics(nil))
	handler := NewHandler(nil, nil)
	if mailer != nil {
		handler = handler.WithEmail(mailer)
	}
	Register(app, handler)
	return app, signer
}

func emailRequest(t *testing.T, app *httpserver.App, method, target, body string, headers ...string) (int, errorEnvelope, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("content-type", "application/json")
	req.Header.Set(activity.ActorHeader, "system:dashboard")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := app.Test(req, fiberTestConfig)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	var envelope errorEnvelope
	_ = json.Unmarshal(raw, &envelope)
	return resp.StatusCode, envelope, raw
}

func TestSendEmail(t *testing.T) {
	mailer := &fakeMailer{configured: true}
	app, _ := emailApp(t, mailer)
	status, envelope, _ := emailRequest(t, app, http.MethodPost, "/v1/email/messages", verifyEmail)
	if status != http.StatusNoContent {
		t.Fatalf("%d %+v", status, envelope)
	}
	if len(mailer.sent) != 1 {
		t.Fatalf("sent %d", len(mailer.sent))
	}
	got := mailer.sent[0]
	if got.To != "owner@example.com" || got.Kind != "verify_email" || got.Subject != "Verify your email address" || !strings.Contains(got.HTML, "<a href") {
		t.Fatalf("%+v", got)
	}
}

// Only Life OS itself sends email: every request through the gateway
// carries an identity token, the owner's own included.
func TestEmailWritesAreInternalOnly(t *testing.T) {
	mailer := &fakeMailer{configured: true}
	app, signer := emailApp(t, mailer)
	for name, token := range map[string]string{
		"the owner with two-factor": signer.TwoFactor("", "owner"),
		"legacy access":             signer.Token("", "owner", "legacy"),
		"an API token":              signer.Token("", "owner", "token"),
	} {
		for _, target := range []string{"/v1/email/messages", "/v1/email/test"} {
			body := verifyEmail
			if target == "/v1/email/test" {
				body = `{"to":"owner@example.com"}`
			}
			status, envelope, _ := emailRequest(t, app, http.MethodPost, target, body, "X-Life-OS-Identity", token)
			if status != http.StatusForbidden || envelope.Error.Code != "internal_only" {
				t.Errorf("%s, %s: %d %+v", name, target, status, envelope)
			}
		}
	}
	if len(mailer.sent) != 0 {
		t.Fatalf("sent %d", len(mailer.sent))
	}
	// And a write still names its actor.
	req := httptest.NewRequest(http.MethodPost, "/v1/email/messages", strings.NewReader(verifyEmail))
	resp, err := app.Test(req, fiberTestConfig)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("without an actor: %d", resp.StatusCode)
	}
}

func TestSendEmailValidates(t *testing.T) {
	mailer := &fakeMailer{configured: true}
	app, _ := emailApp(t, mailer)
	for name, body := range map[string]string{
		"not JSON":                 `to=owner`,
		"no address":               `{"subject":"s","text":"t","kind":"k"}`,
		"a name with the address":  `{"to":"Owner <owner@example.com>","subject":"s","text":"t","kind":"k"}`,
		"two addresses":            `{"to":"a@example.com, b@example.com","subject":"s","text":"t","kind":"k"}`,
		"a header in the address":  `{"to":"a@example.com\r\nBcc: b@example.com","subject":"s","text":"t","kind":"k"}`,
		"a subject on two lines":   `{"to":"owner@example.com","subject":"a\nBcc: b@example.com","text":"t","kind":"k"}`,
		"no text":                  `{"to":"owner@example.com","subject":"s","text":" ","kind":"k"}`,
		"a bad kind":               `{"to":"owner@example.com","subject":"s","text":"t","kind":"Not A Code"}`,
		"a tracking pixel":         `{"to":"owner@example.com","subject":"s","text":"t","kind":"k","html":"<img src=\"https://t.example.com/p.gif\">"}`,
		"a script":                 `{"to":"owner@example.com","subject":"s","text":"t","kind":"k","html":"<script>x()</script>"}`,
		"a remote background":      `{"to":"owner@example.com","subject":"s","text":"t","kind":"k","html":"<p style=\"background:url(https://t.example.com/p)\">x</p>"}`,
		"an event handler":         `{"to":"owner@example.com","subject":"s","text":"t","kind":"k","html":"<p onclick=\"x()\">x</p>"}`,
		"a test without a address": `{}`,
	} {
		target := "/v1/email/messages"
		if name == "a test without a address" {
			target = "/v1/email/test"
		}
		status, envelope, _ := emailRequest(t, app, http.MethodPost, target, body)
		if status != http.StatusBadRequest || envelope.Error.Code != "bad_request" {
			t.Errorf("%s: %d %+v", name, status, envelope)
		}
	}
	if len(mailer.sent) != 0 {
		t.Fatalf("sent %d", len(mailer.sent))
	}
}

func TestSendEmailOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mailer Mailing
		status int
		code   string
	}{
		{"email is off", nil, http.StatusConflict, "email_not_configured"},
		{"settings that cannot work", &fakeMailer{problem: "LIFE_OS_SMTP_TLS must be tls, starttls, or none"}, http.StatusConflict, "email_not_configured"},
		{"too many", &fakeMailer{configured: true, result: email.ErrRateLimited}, http.StatusTooManyRequests, "rate_limited"},
		{"the provider refused the password", &fakeMailer{configured: true, result: &email.Failure{Code: email.CodeAuth, Message: "The mail server refused the user name or password (SMTP 535)."}}, http.StatusBadGateway, "email_auth_failed"},
		{"the recipient was refused", &fakeMailer{configured: true, result: &email.Failure{Code: email.CodeRecipient, Message: "refused", Recipient: true}}, http.StatusBadGateway, "email_recipient_refused"},
	} {
		app, _ := emailApp(t, tc.mailer)
		for _, request := range []struct{ target, body string }{
			{"/v1/email/messages", verifyEmail},
			{"/v1/email/test", `{"to":"owner@example.com"}`},
		} {
			status, envelope, _ := emailRequest(t, app, http.MethodPost, request.target, request.body)
			if status != tc.status || envelope.Error.Code != tc.code {
				t.Errorf("%s, %s: %d %+v, want %d %s", tc.name, request.target, status, envelope, tc.status, tc.code)
			}
		}
	}
	app, _ := emailApp(t, &fakeMailer{problem: "LIFE_OS_SMTP_TLS must be tls, starttls, or none"})
	_, envelope, _ := emailRequest(t, app, http.MethodPost, "/v1/email/test", `{"to":"owner@example.com"}`)
	if !strings.Contains(envelope.Error.Message, "LIFE_OS_SMTP_TLS") {
		t.Errorf("the problem is not named: %s", envelope.Error.Message)
	}
}

func TestEmailStatus(t *testing.T) {
	since := time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)
	mailer := &fakeMailer{configured: true, state: email.State{
		Configured: true, State: "failing", FailingSince: &since, LastFailureAt: &since,
		LastErrorCode: email.CodeAuth, LastError: "The mail server refused the user name or password.",
	}}
	app, signer := emailApp(t, mailer)
	status, _, raw := emailRequest(t, app, http.MethodGet, "/v1/email/status", "")
	if status != http.StatusOK {
		t.Fatalf("%d %s", status, raw)
	}
	var got generated.EmailStatus
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !got.Configured || got.State != generated.EmailStateFailing || got.FailingSince == nil || !got.FailingSince.Equal(since) ||
		got.LastErrorCode == nil || *got.LastErrorCode != "email_auth_failed" || got.Problem != nil {
		t.Fatalf("%+v", got)
	}
	// Status's detail: the server's admins' through the gateway.
	member := "5c1d7a8e-0b7f-4d2a-9b1e-2f6a3c4d5e6f"
	for name, tc := range map[string]struct {
		token  string
		status int
	}{
		"an admin with two-factor": {signer.TwoFactor(member, "admin"), http.StatusOK},
		"legacy access":            {signer.Token("", "owner", "legacy"), http.StatusOK},
		"a member":                 {signer.Token(member, "member", "session"), http.StatusForbidden},
		"the owner's API token":    {signer.Token("", "owner", "token"), http.StatusForbidden},
	} {
		status, _, _ := emailRequest(t, app, http.MethodGet, "/v1/email/status", "", "X-Life-OS-Identity", tc.token)
		if status != tc.status {
			t.Errorf("%s: %d, want %d", name, status, tc.status)
		}
	}
	// Off: no mailer at all.
	off, _ := emailApp(t, nil)
	_, _, raw = emailRequest(t, off, http.MethodGet, "/v1/email/status", "")
	if err := json.Unmarshal(raw, &got); err != nil || got.Configured || got.State != generated.EmailStateOff {
		t.Fatalf("off: %s", raw)
	}
}
