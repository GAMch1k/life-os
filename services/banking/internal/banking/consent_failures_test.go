package banking_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/GAMch1k/life-os/services/banking/internal/banking"
	"github.com/GAMch1k/life-os/services/banking/internal/testsupport"
)

func TestAConsentTheBankDidNotCompleteIsKeptAndShownUntilAConnectionSucceeds(t *testing.T) {
	h := testsupport.New(t)
	ctx := context.Background()
	if latest, err := h.Service.LatestConsentFailure(ctx); err != nil || latest != nil {
		t.Fatalf("no failure yet: %+v, %v", latest, err)
	}

	state := h.Authorize()
	long := "Deutsche Bank  rejected the request for 0f0e0d0c-0b0a-4000-8000-000000000001 " + strings.Repeat("x", 400)
	failure, err := h.Service.RecordConsentFailure(testsupport.Owner(), state, " server_error ", long)
	if err != nil {
		t.Fatal(err)
	}
	if failure.Error != "server_error" || failure.StateMatch != banking.StateMatched || failure.Institution == nil || *failure.Institution != "Deutsche Bank" ||
		failure.Description == nil || len([]rune(*failure.Description)) != 300 || strings.Contains(*failure.Description, "0f0e0d0c") ||
		!strings.HasPrefix(*failure.Description, "Deutsche Bank rejected the request for [id] ") || !failure.OccurredAt.Time.Equal(testsupport.Start) {
		t.Fatalf("failure = %+v %q", failure, *failure.Description)
	}
	// The state is used up: the bank cannot complete it later.
	if _, err := h.Service.Connect(testsupport.Owner(), h.Bank.Approve(state), state, testsupport.PSU()); !errors.Is(err, banking.ErrInvalidState) {
		t.Fatalf("a failed consent's state was still usable: %v", err)
	}

	// An expired and an unknown state are recorded as such.
	late := h.Authorize()
	h.Advance(banking.StateLifetime + time.Minute)
	expired, err := h.Service.RecordConsentFailure(testsupport.Owner(), late, "access_denied", "")
	if err != nil || expired.StateMatch != banking.StateExpired || expired.Institution == nil || expired.Description != nil {
		t.Fatalf("expired = %+v, %v", expired, err)
	}
	h.Advance(time.Minute)
	unknown, err := h.Service.RecordConsentFailure(testsupport.Owner(), "", "server_error", "Internal error")
	if err != nil || unknown.StateMatch != banking.StateUnknown || unknown.Institution != nil {
		t.Fatalf("unknown = %+v, %v", unknown, err)
	}
	latest, err := h.Service.LatestConsentFailure(ctx)
	if err != nil || latest == nil || latest.ID != unknown.ID {
		t.Fatalf("latest = %+v, %v", latest, err)
	}

	// Activity: error and description only.
	events := h.Activity.OfType(banking.EventConsentFailed)
	if len(events) != 3 {
		t.Fatalf("events = %+v", events)
	}
	data := events[0]["data"].(map[string]any)
	if len(data) != 2 || data["error"] != "server_error" || !strings.HasPrefix(data["description"].(string), "Deutsche Bank rejected") ||
		events[0]["actor"].(map[string]any)["kind"] != "user" || events[0]["outcome"] != "failed" {
		t.Fatalf("event = %+v", events[0])
	}
	if strings.Contains(h.Activity.Raw()+h.Logs(), state) || strings.Contains(h.Logs(), late) {
		t.Fatal("a state leaked into activity or logs")
	}
	// The log has ids and codes: not the bank's words, nor which bank.
	for _, text := range []string{"Deutsche Bank", "rejected the request", "Internal error"} {
		if strings.Contains(h.Logs(), text) {
			t.Errorf("%q reached the log", text)
		}
	}

	// A connection that succeeds clears them; they stay in the table.
	h.Connect()
	if latest, err := h.Service.LatestConsentFailure(ctx); err != nil || latest != nil {
		t.Fatalf("after connecting: %+v, %v", latest, err)
	}
	var kept, cleared int
	if err := h.Pool.QueryRow(ctx, "SELECT count(*), count(cleared_at) FROM bank_consent_failures").Scan(&kept, &cleared); err != nil || kept != 3 || cleared != 3 {
		t.Fatalf("kept %d cleared %d: %v", kept, cleared, err)
	}
}

func TestAFailedReconnectNamesTheConnectionAndOnlyTheNewestFailuresAreKept(t *testing.T) {
	h := testsupport.New(t)
	ctx := context.Background()
	conn := h.Connect()
	state := h.Authorize()
	failure, err := h.Service.RecordConsentFailure(testsupport.Owner(), state, "server_error", "")
	if err != nil || failure.ConnectionID != conn.ID {
		t.Fatalf("a reconnect's failure = %+v, %v", failure, err)
	}
	if events := h.Activity.OfType(banking.EventConsentFailed); len(events) != 1 || events[0]["subject"].(map[string]any)["type"] != banking.SubjectType {
		t.Fatalf("events = %+v", events)
	}
	// The failed reconnect leaves the connection as it was.
	if again := h.Connection(conn); again.Status != banking.StatusConnected {
		t.Fatalf("connection = %+v", again)
	}
	for i := 0; i < 105; i++ {
		h.Advance(time.Second)
		if _, err := h.Service.RecordConsentFailure(testsupport.Owner(), "", "server_error", ""); err != nil {
			t.Fatal(err)
		}
	}
	var kept int
	if err := h.Pool.QueryRow(ctx, "SELECT count(*) FROM bank_consent_failures").Scan(&kept); err != nil || kept != 100 {
		t.Fatalf("kept %d: %v", kept, err)
	}
}
