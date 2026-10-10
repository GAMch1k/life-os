package feeds

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/GAMch1k/life-os/services/calendar-feeds/internal/ics"
	store "github.com/GAMch1k/life-os/services/calendar-feeds/internal/repository/generated"
)

func zawFixture(t *testing.T) []ics.Item {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "ics", "testdata", "zaw-sample.ics"))
	if err != nil {
		t.Fatal(err)
	}
	feed, err := ics.Parse(body, ics.Options{Owner: berlin, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	return feed.Items
}

// The ZAW preset keeps exactly the four bin types, titled by type alone, and
// reminds the evening before at 19:00.
func TestZAWPresetSelectsTheFourBinTypes(t *testing.T) {
	preset := ZAWPreset("https://feeds.example.test/abholtermine.ics?area=<area>")
	config := preset.Config
	if preset.Key != PresetZAW || config.Name != "ZAW" || config.URL != "https://feeds.example.test/abholtermine.ics?area=<area>" || len(config.Exclude) != 0 {
		t.Fatalf("preset = %+v", preset)
	}
	var reminders []map[string]any
	if err := json.Unmarshal(config.DefaultReminders, &reminders); err != nil || len(reminders) != 1 ||
		reminders[0]["kind"] != "localTime" || reminders[0]["daysBefore"] != 1.0 || reminders[0]["localTime"] != "19:00" {
		t.Fatalf("default reminders = %s (%v)", config.DefaultReminders, err)
	}
	// Each call is its own configuration.
	config.Include[0] = "changed"
	if ZAWPreset("").Config.Include[0] != "Bioabfall" {
		t.Fatal("ZAWPreset shares its type list")
	}

	zaw := PresetZAW
	kept := (ics.Filter{Include: ZAWPreset("").Config.Include}).Apply(presetItems(&zaw, zawFixture(t)))
	counts := map[string]int{}
	for _, item := range kept {
		counts[item.Title]++
		if *item.Location != "Musterstadt, Beispielweg" {
			t.Errorf("%q lost its location: %v", item.Title, item.Location)
		}
	}
	want := map[string]int{"Bioabfall": 6, "Gelber Sack": 6, "Papier Tonnen und Container": 3, "Restmüll Tonnen und Container 14-täglich": 6}
	if len(kept) != 21 || len(counts) != len(want) {
		t.Fatalf("kept %d: %v", len(kept), counts)
	}
	for title, count := range want {
		if counts[title] != count {
			t.Errorf("%s: %d pickups, want %d", title, counts[title], count)
		}
	}
}

// The title clean-up belongs to the ZAW preset only, and runs before the
// filter, so a street whose name contains a waste type does not bring back
// an excluded type.
func TestPresetItemsCleanUpOnlyZAWBeforeFiltering(t *testing.T) {
	plain := zawFixture(t)
	if got := presetItems(nil, slices.Clone(plain)); got[0].Title != "Restmüll Container wöchentlich - Musterstadt, Beispielweg" {
		t.Fatalf("a feed without a preset was changed: %q", got[0].Title)
	}
	other := "other"
	if got := presetItems(&other, slices.Clone(plain)); got[0].Title != plain[0].Title {
		t.Fatalf("another preset was changed: %q", got[0].Title)
	}

	location := "Musterstadt, Papiermühle"
	container := ics.Item{ExternalUID: "c", Title: "Restmüll Container wöchentlich - " + location, Location: &location}
	paper := ics.Item{ExternalUID: "p", Title: "Papier Tonnen und Container - " + location, Location: &location}
	filter := ics.Filter{Include: ZAWPreset("").Config.Include}
	if raw := filter.Apply([]ics.Item{container, paper}); len(raw) != 2 {
		t.Fatalf("without the clean-up the street matches: %d kept", len(raw))
	}
	zaw := PresetZAW
	kept := filter.Apply(presetItems(&zaw, []ics.Item{container, paper}))
	if len(kept) != 1 || kept[0].ExternalUID != "p" || kept[0].Title != "Papier Tonnen und Container" {
		t.Fatalf("kept = %+v", kept)
	}
}

func TestSameJSON(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{`[{"kind":"localTime","daysBefore":1,"localTime":"19:00"}]`, `[{"localTime": "19:00", "kind": "localTime", "daysBefore": 1}]`, true},
		{`[{"kind":"localTime","daysBefore":1,"localTime":"19:00"}]`, `[{"kind":"localTime","daysBefore":2,"localTime":"19:00"}]`, false},
		{``, `null`, true},
		{``, `[]`, false},
		{`{"include":["a"]}`, `{"include":["a"],"exclude":[]}`, false},
		{`{`, `{`, false},
	} {
		if got := sameJSON([]byte(tc.a), []byte(tc.b)); got != tc.want {
			t.Errorf("sameJSON(%s, %s) = %v", tc.a, tc.b, got)
		}
	}
}

// Only the ZAW feed's source says its locations are private (the owner's
// street); another feed leaves Calendar's setting alone.
func TestOnlyTheZAWFeedRegistersPrivateLocations(t *testing.T) {
	preset := PresetZAW
	config, _ := json.Marshal(ZAWPreset("https://feeds.example.test/zaw.ics").Config)
	zaw := Registration(store.Feed{Name: "ZAW", Preset: &preset, PresetConfig: config})
	if zaw.Provider != ProviderZAW || zaw.PrivateLocation == nil || !*zaw.PrivateLocation {
		t.Fatalf("ZAW registration = %+v", zaw)
	}
	other := Registration(store.Feed{Name: "Holidays"})
	if other.Provider != ProviderICS || other.PrivateLocation != nil {
		t.Fatalf("other registration = %+v", other)
	}
	encoded, _ := json.Marshal(other)
	if strings.Contains(string(encoded), "privateLocation") {
		t.Fatalf("another feed sends privateLocation: %s", encoded)
	}
}
