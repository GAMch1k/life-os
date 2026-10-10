package processor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func berlin(t *testing.T) *time.Location {
	t.Helper()
	location, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	return location
}

func TestOwnerTimeZoneFallsBackToUTC(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		invalid    bool
	}{
		{"", "UTC", false},
		{"Europe/Berlin", "Europe/Berlin", false},
		{" Europe/Berlin ", "Europe/Berlin", false},
		{"UTC", "UTC", false},
		{"Mars/Olympus_Mons", "UTC", true},
		{"Local", "UTC", true},
	} {
		location, err := OwnerTimeZone(tc.name)
		if location.String() != tc.want || (err != nil) != tc.invalid {
			t.Errorf("OwnerTimeZone(%q) = %s, %v; want %s (invalid %v)", tc.name, location, err, tc.want, tc.invalid)
		}
	}
}

func TestLoadConfigReadsTheOwnerTimeZone(t *testing.T) {
	t.Setenv("LIFE_OS_TIMEZONE", "Europe/Berlin")
	cfg, err := LoadConfig()
	if err != nil || cfg.TimeZone.String() != "Europe/Berlin" || len(cfg.Warnings) != 0 {
		t.Fatalf("cfg = %+v, err = %v", cfg, err)
	}

	t.Setenv("LIFE_OS_TIMEZONE", "")
	if cfg, err = LoadConfig(); err != nil || cfg.TimeZone != time.UTC || len(cfg.Warnings) != 0 {
		t.Fatalf("unset: cfg = %+v, err = %v", cfg, err)
	}

	// A typo must not stop capture processing; it is logged and UTC is used.
	t.Setenv("LIFE_OS_TIMEZONE", "Europe/Berlinn")
	cfg, err = LoadConfig()
	if err != nil || cfg.TimeZone != time.UTC || len(cfg.Warnings) != 1 || !strings.Contains(cfg.Warnings[0], "Europe/Berlinn") {
		t.Fatalf("invalid: cfg = %+v, err = %v", cfg, err)
	}
}

func TestTimeContextStatesTheOwnersLocalDateTimeAndWeekday(t *testing.T) {
	// The BUG-12 evidence: 23:41 UTC on Friday is already Saturday in Berlin.
	now := time.Date(2026, 10, 9, 23, 41, 0, 0, time.UTC)
	got := timeContext(now, time.Time{}, berlin(t))
	for _, want := range []string{
		"The owner's timezone is Europe/Berlin; it is now Saturday 2026-10-10 01:41 (+02:00) there.",
		"The coming days are Sunday 2026-10-11 (+02:00), Monday 2026-10-12 (+02:00),",
		"Friday 2026-10-16 (+02:00), Saturday 2026-10-17 (+02:00).",
		"tomorrow at 10:00 is 2026-10-11T10:00:00+02:00",
		"a weekday (\"on Friday\") means its next occurrence that is not already past",
		"RFC3339 with the owner's UTC offset",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("time context lacks %q:\n%s", want, got)
		}
	}

	// The week ahead crosses the end of summer time on 2026-10-25.
	got = timeContext(time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC), time.Time{}, berlin(t))
	if !strings.Contains(got, "it is now Thursday 2026-10-22 10:00 (+02:00)") || !strings.Contains(got, "Saturday 2026-10-24 (+02:00), Sunday 2026-10-25 (+01:00)") {
		t.Errorf("DST week:\n%s", got)
	}

	// Without a configured zone the prompt still says which zone it uses.
	if got := timeContext(now, time.Time{}, nil); !strings.Contains(got, "The owner's timezone is UTC; it is now Friday 2026-10-09 23:41 (+00:00) there.") {
		t.Errorf("UTC:\n%s", got)
	}
}

func TestTimeContextResolvesRelativeDatesFromTheCaptureTime(t *testing.T) {
	captured := time.Date(2026, 10, 9, 21, 0, 0, 0, time.UTC) // Friday 23:00 Berlin
	now := captured.Add(11 * time.Hour)                       // Saturday 10:00 Berlin, after a retry
	got := timeContext(now, captured, berlin(t))
	for _, want := range []string{
		"The owner's timezone is Europe/Berlin. The item was captured on Friday 2026-10-09 23:00 (+02:00) there; resolve relative dates from that moment (it is now Saturday 2026-10-10 10:00 (+02:00)).",
		"The coming days are Saturday 2026-10-10 (+02:00),",
		"tomorrow at 10:00 is 2026-10-10T10:00:00+02:00",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("time context lacks %q:\n%s", want, got)
		}
	}

	// Captured at 23:59:30 and planned just after midnight: "tomorrow" is
	// still the day after the capture.
	captured = time.Date(2026, 10, 9, 21, 59, 30, 0, time.UTC)
	got = timeContext(captured.Add(40*time.Second), captured, berlin(t))
	if !strings.Contains(got, "The item was captured on Friday 2026-10-09 23:59 (+02:00) there; resolve relative dates from that moment. ") ||
		strings.Contains(got, "it is now") ||
		!strings.Contains(got, "tomorrow at 10:00 is 2026-10-10T10:00:00+02:00") {
		t.Errorf("capture just before midnight:\n%s", got)
	}
}

func TestPlanPromptAndCodexEnvironmentCarryTheOwnersTimeZone(t *testing.T) {
	dir := t.TempDir()
	promptFile, envFile := filepath.Join(dir, "prompt"), filepath.Join(dir, "env")
	t.Setenv("TZ", "UTC")
	codex := fakeCodex(t, `env > `+envFile+`
printf '{}' > "$out"`)
	// Record the last argument (the prompt) with a wrapper.
	wrapper := filepath.Join(dir, "codex-prompt")
	script := "#!/bin/sh\nfor last in \"$@\"; do :; done\nprintf '%s' \"$last\" > " + promptFile + "\nexec " + codex + " \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 9, 23, 41, 0, 0, time.UTC)
	cli := CodexCLI{Command: wrapper, WorkDir: dir, TimeZone: berlin(t), Now: func() time.Time { return now }}
	if _, err := cli.Plan(context.Background(), `{"content":"Remind me to call mom tomorrow at 10."}`); err != nil {
		t.Fatal(err)
	}
	prompt, _ := os.ReadFile(promptFile)
	if !strings.HasPrefix(string(prompt), "Classify this Inbox item.") ||
		!strings.Contains(string(prompt), "Never propose calendar update or deletion. The owner's timezone is Europe/Berlin; it is now Saturday 2026-10-10 01:41 (+02:00) there.") ||
		!strings.HasSuffix(string(prompt), `Inbox item: {"content":"Remind me to call mom tomorrow at 10."}`) {
		t.Fatalf("prompt = %s", prompt)
	}
	env, _ := os.ReadFile(envFile)
	if !strings.Contains(string(env), "TZ=Europe/Berlin\n") || strings.Contains(string(env), "TZ=UTC") {
		t.Fatalf("Codex env lacks the owner's TZ:\n%s", env)
	}
}

func TestPlanningUsesTheCapturesTimeZoneElseTheConfiguredOne(t *testing.T) {
	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	configured := CodexCLI{TimeZone: berlin(t)}
	for _, tc := range []struct {
		name string
		ctx  context.Context
		want *time.Location
	}{
		{"the capturing browser's zone wins", WithCaptureTimeZone(context.Background(), "America/New_York"), newYork},
		{"an unknown zone falls back", WithCaptureTimeZone(context.Background(), "Mars/Olympus_Mons"), configured.TimeZone},
		{"Local is not a zone", WithCaptureTimeZone(context.Background(), "Local"), configured.TimeZone},
		{"an empty zone falls back", WithCaptureTimeZone(context.Background(), ""), configured.TimeZone},
		{"no zone falls back", context.Background(), configured.TimeZone},
	} {
		if got := configured.location(tc.ctx); got.String() != tc.want.String() {
			t.Errorf("%s: location = %s, want %s", tc.name, got, tc.want)
		}
	}
	// Without a configured zone either, timeContext and TZ fall back to UTC.
	if got := (CodexCLI{}).location(context.Background()); got != nil {
		t.Errorf("unconfigured location = %s, want nil (UTC)", got)
	}
}

func TestPlanPromptAndCodexEnvironmentUseTheCapturesTimeZone(t *testing.T) {
	dir := t.TempDir()
	promptFile, envFile := filepath.Join(dir, "prompt"), filepath.Join(dir, "env")
	codex := fakeCodex(t, `env > `+envFile+`
printf '{}' > "$out"`)
	wrapper := filepath.Join(dir, "codex-prompt")
	script := "#!/bin/sh\nfor last in \"$@\"; do :; done\nprintf '%s' \"$last\" > " + promptFile + "\nexec " + codex + " \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	captured := time.Date(2026, 10, 9, 23, 41, 0, 0, time.UTC)
	cli := CodexCLI{Command: wrapper, WorkDir: dir, TimeZone: berlin(t), Now: func() time.Time { return captured.Add(time.Minute) }}
	ctx := WithCaptureTimeZone(WithCapturedAt(context.Background(), captured), "America/New_York")
	if _, err := cli.Plan(ctx, `{"content":"Remind me to call mom tomorrow at 10."}`); err != nil {
		t.Fatal(err)
	}
	prompt, _ := os.ReadFile(promptFile)
	// 23:41 UTC on Friday is still Friday evening in New York.
	for _, want := range []string{
		"The owner's timezone is America/New_York. The item was captured on Friday 2026-10-09 19:41 (-04:00) there;",
		"tomorrow at 10:00 is 2026-10-10T10:00:00-04:00",
	} {
		if !strings.Contains(string(prompt), want) {
			t.Errorf("prompt lacks %q:\n%s", want, prompt)
		}
	}
	env, _ := os.ReadFile(envFile)
	if !strings.Contains(string(env), "TZ=America/New_York\n") {
		t.Fatalf("Codex env lacks the capture's TZ:\n%s", env)
	}
}

type capturePlanner struct {
	capturedAt []time.Time
	timeZones  []string
}

func (p *capturePlanner) Plan(ctx context.Context, _ string) (string, error) {
	p.capturedAt = append(p.capturedAt, capturedAtFrom(ctx))
	zone, _ := ctx.Value(captureTimeZoneKey{}).(string)
	p.timeZones = append(p.timeZones, zone)
	return `{"actions":[],"ambiguous":true,"confidence":0.1,"reason":"unclear"}`, nil
}

func TestRunnerPlansWithTheItemsCaptureTime(t *testing.T) {
	captured := time.Date(2026, 10, 9, 21, 0, 0, 0, time.UTC)
	tools := &corpusTools{item: inboxItem{ID: "item-1", Content: "call mom tomorrow at 10", Source: "web", CreatedAt: captured}}
	planner := &capturePlanner{}
	if _, err := (Runner{Planner: planner, Tools: tools, Model: "gpt-5.6-luna"}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(planner.capturedAt) != 1 || !planner.capturedAt[0].Equal(captured) {
		t.Fatalf("planned with capture times %v, want %s", planner.capturedAt, captured)
	}
	if planner.timeZones[0] != "" {
		t.Fatalf("a capture without metadata planned with zone %q", planner.timeZones[0])
	}
}

func TestRunnerPlansInTheCapturingBrowsersTimeZone(t *testing.T) {
	tools := &corpusTools{item: inboxItem{ID: "item-1", Content: "call mom tomorrow at 10", Source: "manual", SourceMetadata: map[string]any{"timeZone": "America/New_York"}, CreatedAt: time.Now()}}
	planner := &capturePlanner{}
	if _, err := (Runner{Planner: planner, Tools: tools, Model: "gpt-5.6-luna"}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(planner.timeZones) != 1 || planner.timeZones[0] != "America/New_York" {
		t.Fatalf("planned with zones %v, want America/New_York", planner.timeZones)
	}
}

func TestStatusReportsTheTimeZone(t *testing.T) {
	m := NewMonitor("gpt-5.6-luna")
	m.SetTimeZone("Europe/Berlin")
	raw, _ := json.Marshal(m.Snapshot())
	if !strings.Contains(string(raw), `"timeZone":"Europe/Berlin"`) {
		t.Fatalf("status = %s", raw)
	}
}
