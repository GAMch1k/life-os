package email

import (
	"context"
	"testing"
	"time"

	store "github.com/GAMch1k/life-os/services/notifications/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/notifications/internal/testdb"
)

// email_status on PostgreSQL: one row, failing since the first failure
// after the latest success, and a refused recipient leaves it alone.
func TestStatusInTheDatabase(t *testing.T) {
	pool := testdb.Open(t)
	sender := &scriptedSender{results: []error{
		&Failure{Code: CodeConnect, Message: "x"},
		&Failure{Code: CodeAuth, Message: "x"},
		&Failure{Code: CodeRecipient, Message: "x", Recipient: true},
		nil,
	}}
	mailer := NewMailer(working, sender, store.New(pool), discard())
	clock := time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)
	mailer.now = func() time.Time { return clock }
	ctx := context.Background()

	state, err := mailer.Status(ctx)
	if err != nil || state.State != "idle" {
		t.Fatalf("before any send: %+v %v", state, err)
	}
	_ = mailer.Send(ctx, message())
	first := clock
	clock = clock.Add(time.Minute)
	_ = mailer.Send(ctx, message())
	clock = clock.Add(time.Minute)
	_ = mailer.Send(ctx, message())
	state, err = mailer.Status(ctx)
	if err != nil || state.State != "failing" || !state.FailingSince.Equal(first) || state.LastErrorCode != CodeAuth ||
		!state.LastFailureAt.Equal(first.Add(time.Minute)) {
		t.Fatalf("failing: %+v %v", state, err)
	}
	clock = clock.Add(time.Minute)
	if err := mailer.Send(ctx, message()); err != nil {
		t.Fatal(err)
	}
	state, err = mailer.Status(ctx)
	if err != nil || state.State != "ok" || state.FailingSince != nil || !state.LastSuccessAt.Equal(clock) {
		t.Fatalf("works again: %+v %v", state, err)
	}
	var rows int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM email_status").Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("rows %d %v", rows, err)
	}
}
