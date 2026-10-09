package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	store "github.com/GAMch1k/life-os/services/inbox/internal/repository/generated"
)

func TestScheduledRetriesAreSelectedOnlyWhenDue(t *testing.T) {
	queries, _ := migratedQueries(t)
	ctx := context.Background()
	capture := func(content string) store.InboxItem {
		item, err := queries.CreateInboxItem(ctx, store.CreateInboxItemParams{Content: content, Source: "manual", SourceMetadata: []byte(`{}`), MediaReferences: []byte(`[]`)})
		if err != nil {
			t.Fatal(err)
		}
		return item
	}
	fresh := capture("never failed")
	later := capture("usage limited until later")
	due := capture("backoff already elapsed")
	gaveUp := capture("operator attention")

	reason := func(value string) *string { return &value }
	at := func(offset time.Duration) pgtype.Timestamptz {
		return pgtype.Timestamptz{Time: time.Now().Add(offset), Valid: true}
	}
	if _, err := queries.ScheduleInboxItemRetry(ctx, store.ScheduleInboxItemRetryParams{ID: later.ID, NextRetryAt: at(time.Hour), RetryReason: reason("usage_limited")}); err != nil {
		t.Fatal(err)
	}
	if _, err := queries.ScheduleInboxItemRetry(ctx, store.ScheduleInboxItemRetryParams{ID: due.ID, NextRetryAt: at(-time.Second), RetryReason: reason("backoff")}); err != nil {
		t.Fatal(err)
	}
	if _, err := queries.SetInboxItemState(ctx, store.SetInboxItemStateParams{ID: gaveUp.ID, State: "failed"}); err != nil {
		t.Fatal(err)
	}

	ids := func(items []store.InboxItem) map[[16]byte]bool {
		set := map[[16]byte]bool{}
		for _, item := range items {
			set[item.ID.Bytes] = true
		}
		return set
	}
	raw, yes := "raw", true
	eligible, err := queries.ListInboxItems(ctx, store.ListInboxItemsParams{StateFilter: &raw, DueFilter: &yes, PageLimit: 50})
	if err != nil {
		t.Fatal(err)
	}
	got := ids(eligible)
	if len(eligible) != 2 || !got[fresh.ID.Bytes] || !got[due.ID.Bytes] {
		t.Fatalf("eligible = %d items, want the fresh and the due one", len(eligible))
	}
	allRaw, err := queries.ListInboxItems(ctx, store.ListInboxItemsParams{StateFilter: &raw, PageLimit: 50})
	if err != nil || len(allRaw) != 3 {
		t.Fatalf("raw without due filter = %d, err = %v", len(allRaw), err)
	}
	unresolved, err := queries.ListInboxItems(ctx, store.ListInboxItemsParams{UnresolvedFilter: &yes, PageLimit: 50})
	if err != nil || !ids(unresolved)[gaveUp.ID.Bytes] {
		t.Fatalf("a failed item must stay visible as unresolved (err = %v)", err)
	}

	backlog, err := queries.GetInboxBacklog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if backlog.FailedCount != 1 || !backlog.NextRetryAt.Valid || time.Until(backlog.NextRetryAt.Time) < 50*time.Minute || backlog.NextRetryReason != "usage_limited" {
		t.Fatalf("backlog = %+v", backlog)
	}

	// Starting an attempt clears the schedule; it survives everything else.
	started, err := queries.SetInboxItemState(ctx, store.SetInboxItemStateParams{ID: later.ID, State: "processing"})
	if err != nil || started.NextRetryAt.Valid || started.RetryReason != nil {
		t.Fatalf("started = %+v, err = %v", started, err)
	}
}
