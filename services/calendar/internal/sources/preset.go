package sources

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/GAMch1k/life-os/services/calendar/internal/ics"
	store "github.com/GAMch1k/life-os/services/calendar/internal/repository/generated"
)

// PresetZAW is the key of the ZAW waste-collection source.
const PresetZAW = "zaw"

// maxPresetURL mirrors the API's limit on a source URL.
const maxPresetURL = 2048

// zawTypes are the waste types the ZAW source imports, as named in the feed's
// summaries: bins only, without the weekly residual-waste collection for
// large containers ("Restmüll Container wöchentlich") and the hazardous-waste
// truck ("Schadstoffmobil"). The owner can change them on the source.
var zawTypes = []string{"Bioabfall", "Gelber Sack", "Papier", "Restmüll Tonnen und Container 14-täglich"}

// zawReminders: the evening before a pickup, at 19:00 owner-local.
const zawReminders = `[{"kind":"localTime","daysBefore":1,"localTime":"19:00"}]`

// Preset is a source Calendar keeps configured from its own environment
// rather than through the API. Its source is found by Key, whatever its name
// or URL, so restarts never add a second one.
type Preset struct {
	Key    string
	Config PresetConfig
}

// PresetConfig is what a preset sets on its source. The configuration last
// applied is stored with the source (preset_config), so a restart changes
// only what the preset changed since and leaves edits made through the API.
type PresetConfig struct {
	Name             string          `json:"name"`
	URL              string          `json:"url"`
	Include          []string        `json:"include"`
	Exclude          []string        `json:"exclude"`
	DefaultReminders json.RawMessage `json:"defaultReminders"`
}

// ZAWPreset is the ZAW waste-collection source: the owner's personal
// MyMüll iCal feed at url (it names the street, so it comes from the
// environment and is never logged), the four bin types, and a reminder the
// evening before each pickup.
func ZAWPreset(url string) Preset {
	return Preset{Key: PresetZAW, Config: PresetConfig{
		Name: "ZAW", URL: url, Include: slices.Clone(zawTypes), Exclude: []string{},
		DefaultReminders: json.RawMessage(zawReminders),
	}}
}

// presetItems applies a preset source's feed-specific clean-up before the
// filter, so filter terms match the event type alone.
func presetItems(preset *string, items []ics.Item) []ics.Item {
	if preset == nil || *preset != PresetZAW {
		return items
	}
	for index := range items {
		// "Bioabfall - <town>, <street>" becomes "Bioabfall".
		items[index] = ics.StripLocationSuffix(items[index])
	}
	return items
}

// Ensure outcomes.
const (
	EnsureCreated   = "created"
	EnsureAdopted   = "adopted"
	EnsureUpdated   = "updated"
	EnsureUnchanged = "unchanged"
)

// EnsureResult is what EnsurePreset did.
type EnsureResult struct {
	Action string
	Source store.CalendarSource
	// Due: the source needs a refresh now (it is new, or has a new URL or
	// filter); wake the scheduler.
	Due bool
}

// EnsurePreset makes sure exactly one source carries preset.Key, configured
// as preset.Config:
//
//   - without one, a source added through the API for the same feed URL is
//     taken over, else a new enabled source is added, due for its first
//     refresh right away;
//   - an existing one gets each setting the preset changed since it was last
//     applied (all of them when taken over); a setting the preset did not
//     change keeps any edit made through the API, and enabled is never
//     touched. A new URL or filter drops the validators and makes the source
//     due right away, as a PATCH would.
//
// It runs in one transaction under an advisory lock, so concurrent starts
// neither duplicate nor half-apply the preset. Errors never contain the URL.
func (r *Refresher) EnsurePreset(ctx context.Context, preset Preset) (EnsureResult, error) {
	config := preset.Config
	config.Include, config.Exclude = orEmpty(config.Include), orEmpty(config.Exclude)
	if len(config.URL) > maxPresetURL {
		return EnsureResult{}, fmt.Errorf("the %s feed URL must be at most %d characters", preset.Key, maxPresetURL)
	}
	url, err := r.fetcher.NormalizeURL(config.URL)
	if err != nil {
		return EnsureResult{}, fmt.Errorf("the %s feed URL is not usable: %w", preset.Key, err)
	}
	config.URL = url
	if config.Name == "" || utf8.RuneCountInString(config.Name) > 100 {
		return EnsureResult{}, fmt.Errorf("the %s preset needs a name of 1 to 100 characters", preset.Key)
	}
	applied, err := json.Marshal(config)
	if err != nil {
		return EnsureResult{}, err
	}
	reminders := nullJSON(config.DefaultReminders)
	now := r.now().UTC()

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return EnsureResult{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	queries := r.queries.WithTx(tx)
	if err := queries.LockPreset(ctx, preset.Key); err != nil {
		return EnsureResult{}, err
	}
	action := EnsureUpdated
	var last *PresetConfig
	current, err := queries.LockPresetSource(ctx, preset.Key)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		action = EnsureAdopted
		current, err = queries.LockAdoptableSource(ctx, config.URL)
		if errors.Is(err, pgx.ErrNoRows) {
			created, err := queries.CreatePresetSource(ctx, store.CreatePresetSourceParams{
				Kind: "ics", Name: config.Name, Url: config.URL, FilterInclude: config.Include, FilterExclude: config.Exclude,
				DefaultReminders: reminders, NextRefreshAt: at(now), Preset: preset.Key, PresetConfig: applied,
			})
			if err != nil {
				return EnsureResult{}, err
			}
			return EnsureResult{Action: EnsureCreated, Source: created, Due: true}, tx.Commit(ctx)
		}
		if err != nil {
			return EnsureResult{}, err
		}
	case err != nil:
		return EnsureResult{}, err
	default:
		var stored PresetConfig
		if json.Unmarshal(current.PresetConfig, &stored) == nil {
			last = &stored
		}
	}

	// Each setting the preset changed since it was last applied (every
	// setting when there is no record) replaces the source's.
	next := PresetConfig{Name: current.Name, URL: current.Url, Include: orEmpty(current.FilterInclude), Exclude: orEmpty(current.FilterExclude), DefaultReminders: current.DefaultReminders}
	if last == nil || last.Name != config.Name {
		next.Name = config.Name
	}
	if last == nil || last.URL != config.URL {
		next.URL = config.URL
	}
	if last == nil || !slices.Equal(last.Include, config.Include) || !slices.Equal(last.Exclude, config.Exclude) {
		next.Include, next.Exclude = config.Include, config.Exclude
	}
	if last == nil || !sameJSON(last.DefaultReminders, config.DefaultReminders) {
		next.DefaultReminders = reminders
	}
	reconfigured := next.URL != current.Url || !slices.Equal(next.Include, current.FilterInclude) || !slices.Equal(next.Exclude, current.FilterExclude)
	changed := action == EnsureAdopted || reconfigured || next.Name != current.Name ||
		!sameJSON(next.DefaultReminders, current.DefaultReminders) || !sameJSON(current.PresetConfig, applied)
	if !changed {
		return EnsureResult{Action: EnsureUnchanged, Source: current}, tx.Commit(ctx)
	}
	params := store.ApplySourcePresetParams{
		ID: current.ID, Name: next.Name, Url: next.URL, FilterInclude: next.Include, FilterExclude: next.Exclude,
		DefaultReminders: nullJSON(next.DefaultReminders), Preset: preset.Key, PresetConfig: applied, ResetValidators: reconfigured,
	}
	due := reconfigured && current.Enabled
	if due {
		params.NextRefreshAt = at(now)
	}
	updated, err := queries.ApplySourcePreset(ctx, params)
	if err != nil {
		return EnsureResult{}, err
	}
	return EnsureResult{Action: action, Source: updated, Due: due}, tx.Commit(ctx)
}

// PresetSource returns the source carrying the preset key, if any.
func (r *Refresher) PresetSource(ctx context.Context, key string) (store.CalendarSource, bool, error) {
	source, err := r.queries.GetPresetSource(ctx, key)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.CalendarSource{}, false, nil
	}
	return source, err == nil, err
}

// sameJSON compares two JSON documents by value (JSONB reorders keys);
// absent and null are the same.
func sameJSON(a, b []byte) bool {
	var left, right any
	if len(bytes.TrimSpace(a)) > 0 && json.Unmarshal(a, &left) != nil {
		return false
	}
	if len(bytes.TrimSpace(b)) > 0 && json.Unmarshal(b, &right) != nil {
		return false
	}
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return bytes.Equal(leftJSON, rightJSON)
}

// nullJSON is raw, or SQL NULL for absent or JSON null.
func nullJSON(raw []byte) []byte {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	return raw
}

func orEmpty(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
