package email

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	store "github.com/GAMch1k/life-os/services/notifications/internal/repository/generated"
)

// memoryStore is email_status in memory.
type memoryStore struct {
	row   store.GetEmailStatusRow
	saved bool
}

func (m *memoryStore) GetEmailStatus(context.Context) (store.GetEmailStatusRow, error) {
	if !m.saved {
		return store.GetEmailStatusRow{}, pgx.ErrNoRows
	}
	return m.row, nil
}

func (m *memoryStore) NoteEmailSent(_ context.Context, at pgtype.Timestamptz) error {
	m.saved = true
	m.row.LastSuccessAt = at
	m.row.FailingSince = pgtype.Timestamptz{}
	return nil
}

func (m *memoryStore) NoteEmailFailed(_ context.Context, arg store.NoteEmailFailedParams) error {
	m.saved = true
	m.row.LastFailureAt = arg.At
	if !m.row.FailingSince.Valid {
		m.row.FailingSince = arg.At
	}
	code := arg.Code
	m.row.LastErrorCode = &code
	return nil
}

// scriptedSender answers each send with the next error (nil: sent).
type scriptedSender struct {
	results []error
	sent    []Message
}

func (s *scriptedSender) Send(_ context.Context, message Message) error {
	s.sent = append(s.sent, message)
	if len(s.results) == 0 {
		return nil
	}
	err := s.results[0]
	s.results = s.results[1:]
	return err
}

var working = Settings{Host: "smtp.example.com", Username: "apikey", Password: "hunter2-hunter2", From: "lifeos@example.com", LinkBaseURL: "https://lifeos.example.com"}

func TestMailerOffAndInvalid(t *testing.T) {
	off := NewMailer(Settings{}, nil, &memoryStore{}, discard())
	if off.Configured() {
		t.Fatal("configured without a host")
	}
	if state, _ := off.Status(context.Background()); state.State != "off" || state.Configured {
		t.Fatalf("%+v", state)
	}
	if err := off.Send(context.Background(), message()); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("send while off: %v", err)
	}
	bad := NewMailer(Settings{Host: "smtp.example.com", TLS: "ssl", From: "x@example.com"}, nil, &memoryStore{}, discard())
	state, _ := bad.Status(context.Background())
	if bad.Configured() || state.State != "invalid" || !strings.Contains(state.Problem, "LIFE_OS_SMTP_TLS") {
		t.Fatalf("%+v", state)
	}
}

func TestMailerStatusFollowsTheSends(t *testing.T) {
	var logs bytes.Buffer
	st := &memoryStore{}
	sender := &scriptedSender{results: []error{
		nil,
		&Failure{Code: CodeAuth, Message: "refused"},
		&Failure{Code: CodeRecipient, Message: "refused", Recipient: true},
		nil,
	}}
	mailer := NewMailer(working, sender, st, slog.New(slog.NewJSONHandler(&logs, nil)))
	ctx := context.Background()
	status := func() State {
		t.Helper()
		state, err := mailer.Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return state
	}
	if got := status(); got.State != "idle" || !got.Configured {
		t.Fatalf("before any send: %+v", got)
	}
	if err := mailer.Send(ctx, message()); err != nil {
		t.Fatal(err)
	}
	if got := status(); got.State != "ok" || got.LastSuccessAt == nil {
		t.Fatalf("after a send: %+v", got)
	}
	if err := mailer.Send(ctx, message()); err == nil {
		t.Fatal("a failure went unreported")
	}
	failing := status()
	if failing.State != "failing" || failing.FailingSince == nil || failing.LastErrorCode != CodeAuth || failing.LastError == "" {
		t.Fatalf("after a failure: %+v", failing)
	}
	// A refused recipient says nothing about the server.
	if err := mailer.Send(ctx, message()); err == nil {
		t.Fatal("a refused recipient went unreported")
	}
	if got := status(); got.State != "failing" || !got.FailingSince.Equal(*failing.FailingSince) {
		t.Fatalf("after a refused recipient: %+v", got)
	}
	if err := mailer.Send(ctx, message()); err != nil {
		t.Fatal(err)
	}
	if got := status(); got.State != "ok" || got.FailingSince != nil {
		t.Fatalf("after it works again: %+v", got)
	}
	for _, secret := range []string{"owner@example.com", "hunter2-hunter2", "Verify your email", "https://lifeos.example.com/verify-email"} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("the log shows %q:\n%s", secret, logs.String())
		}
	}
	if !strings.Contains(logs.String(), `"kind":"verify_email"`) || !strings.Contains(logs.String(), `"code":"email_auth_failed"`) {
		t.Errorf("the log lacks the kind or the code:\n%s", logs.String())
	}
}

func TestMailerRateLimits(t *testing.T) {
	sender := &scriptedSender{}
	mailer := NewMailer(working, sender, &memoryStore{}, discard())
	now := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	mailer.now = func() time.Time { return now }
	ctx := context.Background()
	for i := 0; i < MaxPerMinute; i++ {
		if err := mailer.Send(ctx, message()); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	if err := mailer.Send(ctx, message()); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("send past the minute's limit: %v", err)
	}
	// Spread over the hour, the hour's limit holds.
	for minute := 1; len(sender.sent) < MaxPerHour; minute++ {
		now = now.Add(time.Minute)
		for i := 0; i < MaxPerMinute && len(sender.sent) < MaxPerHour; i++ {
			if err := mailer.Send(ctx, message()); err != nil {
				t.Fatalf("minute %d: %v", minute, err)
			}
		}
	}
	now = now.Add(time.Minute)
	if err := mailer.Send(ctx, message()); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("send past the hour's limit: %v", err)
	}
	now = now.Add(time.Hour)
	if err := mailer.Send(ctx, message()); err != nil {
		t.Fatalf("an hour later: %v", err)
	}
}

func TestTestMessage(t *testing.T) {
	mailer := NewMailer(working, &scriptedSender{}, &memoryStore{}, discard())
	msg := mailer.TestMessage("owner@example.com")
	if msg.To != "owner@example.com" || msg.Kind != "test" || !strings.Contains(msg.Text, "https://lifeos.example.com") {
		t.Fatalf("%+v", msg)
	}
	if strings.Contains(msg.HTML, "<img") || strings.Contains(msg.HTML, "<script") {
		t.Fatalf("the test email loads something: %s", msg.HTML)
	}
}

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
