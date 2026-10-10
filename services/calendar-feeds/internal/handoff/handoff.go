// Package handoff moves the ICS feeds Calendar refreshed itself into
// calendar-feeds, once, at the release that switches Calendar's feed
// refresher off (P11-08). It reads the feeds' configuration and refresh state
// from Calendar's legacy columns (calendar_sources, read-only, in a
// read-only transaction) and adds each feed here under its Calendar source's
// id, which is also its externalId in Calendar's integration API, so the feed
// keeps syncing the same source and events: nothing is imported twice.
//
// It is idempotent: a feed already handed over stays as it is, so running it
// again changes nothing. It reports counts only; a feed URL can carry
// personal data (the ZAW feed names a street) and is never printed.
package handoff

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	store "github.com/GAMch1k/life-os/services/calendar-feeds/internal/repository/generated"
)

// Integration is the integration Calendar's backfill (migration 000014)
// gave every ICS source.
const Integration = "calendar-feeds"

// Result counts what the handoff found and did.
type Result struct {
	// Found: ICS feeds in Calendar (sources of this integration with a feed
	// URL).
	Found int
	// HandedOver: feeds added here now.
	HandedOver int
	// AlreadyPresent: feeds handed over by an earlier run.
	AlreadyPresent int
	// InProcess: feeds Calendar's migrations have not handed over yet (still
	// refreshed by Calendar itself); calendar-feeds cannot sync them until
	// Calendar's handover migration has run.
	InProcess int
	// Presets: feeds with a preset (ZAW).
	Presets int
	// Replaced: preset feeds this service had added before the handoff,
	// never registered in Calendar, replaced by the handed-over ones.
	Replaced int
}

func (r Result) String() string {
	return fmt.Sprintf("calendar feeds found in Calendar: %d; handed over now: %d; already handed over: %d; presets: %d; replaced unregistered presets: %d; still refreshed by Calendar (run its migrations first): %d",
		r.Found, r.HandedOver, r.AlreadyPresent, r.Presets, r.Replaced, r.InProcess)
}

// legacyFeed is one ICS feed as Calendar stored it.
type legacyFeed struct {
	id                           pgtype.UUID
	name, url                    string
	enabled, inProcess           bool
	include, exclude             []string
	preset                       *string
	presetConfig                 []byte
	nextRefreshAt, lastAttemptAt pgtype.Timestamptz
	lastSuccessAt                pgtype.Timestamptz
	lastOutcome, lastError       *string
	lastCounts                   []byte
	consecutiveFailures          int32
	etag, lastModified           *string
	createdAt, updatedAt         pgtype.Timestamptz
}

// legacyQuery reads Calendar's ICS feeds: the sources of this integration
// that still have their feed URL (the legacy columns stay until P11-09).
// Sources this service registered itself have none.
const legacyQuery = `
SELECT id, name, url, enabled, in_process, filter_include, filter_exclude, preset, preset_config,
       next_refresh_at, last_attempt_at, last_success_at, last_outcome, last_error, last_counts,
       consecutive_failures, etag, last_modified, created_at, updated_at
FROM calendar_sources
WHERE integration = $1 AND url IS NOT NULL AND url <> ''
ORDER BY created_at, id`

// readLegacy reads the feeds in a read-only transaction.
func readLegacy(ctx context.Context, calendar *pgxpool.Pool) ([]legacyFeed, error) {
	tx, err := calendar.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, fmt.Errorf("open a read-only transaction on Calendar's database: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	rows, err := tx.Query(ctx, legacyQuery, Integration)
	if err != nil {
		return nil, fmt.Errorf("read Calendar's feeds (does calendar_db have migration 000014?): %w", err)
	}
	defer rows.Close()
	var feeds []legacyFeed
	for rows.Next() {
		var feed legacyFeed
		if err := rows.Scan(&feed.id, &feed.name, &feed.url, &feed.enabled, &feed.inProcess, &feed.include, &feed.exclude,
			&feed.preset, &feed.presetConfig, &feed.nextRefreshAt, &feed.lastAttemptAt, &feed.lastSuccessAt, &feed.lastOutcome,
			&feed.lastError, &feed.lastCounts, &feed.consecutiveFailures, &feed.etag, &feed.lastModified, &feed.createdAt, &feed.updatedAt); err != nil {
			return nil, fmt.Errorf("read Calendar's feeds: %w", err)
		}
		feeds = append(feeds, feed)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read Calendar's feeds: %w", err)
	}
	return feeds, tx.Commit(ctx)
}

// Run hands Calendar's feeds over to the feeds database: one transaction
// writes them all, so a failed run leaves nothing half done.
func Run(ctx context.Context, calendar, feeds *pgxpool.Pool) (Result, error) {
	legacy, err := readLegacy(ctx, calendar)
	if err != nil {
		return Result{}, err
	}
	result := Result{Found: len(legacy)}
	tx, err := feeds.Begin(ctx)
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	queries := store.New(tx)
	for _, feed := range legacy {
		if feed.inProcess {
			result.InProcess++
		}
		if feed.preset != nil {
			result.Presets++
			replaced, err := queries.DeleteUnregisteredPresetFeed(ctx, store.DeleteUnregisteredPresetFeedParams{Preset: *feed.preset, ID: feed.id})
			if err != nil {
				return result, fmt.Errorf("hand over a preset feed: %w", err)
			}
			result.Replaced += int(replaced)
		}
		include, exclude := feed.include, feed.exclude
		if include == nil {
			include = []string{}
		}
		if exclude == nil {
			exclude = []string{}
		}
		added, err := queries.HandOverFeed(ctx, store.HandOverFeedParams{
			ID: feed.id, Name: feed.name, Url: feed.url, Enabled: feed.enabled, FilterInclude: include, FilterExclude: exclude,
			Preset: feed.preset, PresetConfig: feed.presetConfig, NextRefreshAt: feed.nextRefreshAt, LastAttemptAt: feed.lastAttemptAt,
			LastSuccessAt: feed.lastSuccessAt, LastOutcome: feed.lastOutcome, LastError: feed.lastError, LastCounts: feed.lastCounts,
			ConsecutiveFailures: feed.consecutiveFailures, Etag: feed.etag, LastModified: feed.lastModified,
			CreatedAt: feed.createdAt, UpdatedAt: feed.updatedAt,
		})
		var unique interface{ SQLState() string }
		if errors.As(err, &unique) && unique.SQLState() == "23505" {
			// Another feed here already carries this preset and is registered
			// in Calendar: never two ZAW feeds.
			return result, fmt.Errorf("a %q feed registered in Calendar already exists here; remove it before handing Calendar's over", deref(feed.preset))
		}
		if err != nil {
			return result, fmt.Errorf("hand over a feed: %w", err)
		}
		if added == 1 {
			result.HandedOver++
		} else {
			result.AlreadyPresent++
		}
	}
	return result, tx.Commit(ctx)
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
