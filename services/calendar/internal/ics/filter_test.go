package ics

import (
	"os"
	"testing"
)

// MyMüll summaries repeat the pickup address, already the location, after
// the waste type; only that repetition is removed.
func TestStripLocationSuffix(t *testing.T) {
	body, err := os.ReadFile("testdata/zaw-sample.ics")
	if err != nil {
		t.Fatal(err)
	}
	types := map[string]bool{
		"Restmüll Container wöchentlich": true, "Papier Tonnen und Container": true, "Schadstoffmobil": true,
		"Bioabfall": true, "Restmüll Tonnen und Container 14-täglich": true, "Gelber Sack": true,
	}
	for _, item := range parse(t, body).Items {
		stripped := StripLocationSuffix(item)
		if !types[stripped.Title] || stripped.Location == nil || *stripped.Location != "Musterstadt, Beispielweg" {
			t.Errorf("%q at %v became %q", item.Title, item.Location, stripped.Title)
		}
	}

	location := func(value string) *string { return &value }
	for _, tc := range []struct {
		title    string
		location *string
		want     string
	}{
		{"Bioabfall - Musterstadt, Beispielweg", location(" Musterstadt, Beispielweg "), "Bioabfall"},
		{"Bioabfall - Musterstadt, Beispielweg ", location("Musterstadt, Beispielweg"), "Bioabfall"},
		// Not the event's own location, or no location: unchanged.
		{"Bioabfall - Musterstadt, Nebenweg", location("Musterstadt, Beispielweg"), "Bioabfall - Musterstadt, Nebenweg"},
		{"Bioabfall - Musterstadt, Beispielweg", nil, "Bioabfall - Musterstadt, Beispielweg"},
		{"Team - Room 4", location(""), "Team - Room 4"},
		// The location elsewhere in the title, or nothing left: unchanged.
		{"Musterstadt, Beispielweg - Bioabfall", location("Musterstadt, Beispielweg"), "Musterstadt, Beispielweg - Bioabfall"},
		{" - Musterstadt, Beispielweg", location("Musterstadt, Beispielweg"), " - Musterstadt, Beispielweg"},
		{"Bioabfall-Musterstadt, Beispielweg", location("Musterstadt, Beispielweg"), "Bioabfall-Musterstadt, Beispielweg"},
	} {
		got := StripLocationSuffix(Item{Title: tc.title, Location: tc.location})
		if got.Title != tc.want || got.Location != tc.location {
			t.Errorf("StripLocationSuffix(%q) = %q, want %q", tc.title, got.Title, tc.want)
		}
	}
}
