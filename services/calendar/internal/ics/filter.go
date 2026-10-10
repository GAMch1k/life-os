package ics

import "strings"

// Filter selects which feed events a source imports. A term matches an item
// when it equals one of the item's CATEGORIES or occurs in its SUMMARY, both
// compared case-insensitively. With Include terms, only items matching one
// of them are kept; then items matching an Exclude term are dropped. An
// empty filter keeps everything.
type Filter struct {
	Include []string
	Exclude []string
}

// Keep reports whether the filter keeps item.
func (f Filter) Keep(item Item) bool {
	if len(f.Include) > 0 && !matchesAny(f.Include, item) {
		return false
	}
	return !matchesAny(f.Exclude, item)
}

// Apply returns the items the filter keeps.
func (f Filter) Apply(items []Item) []Item {
	kept := make([]Item, 0, len(items))
	for _, item := range items {
		if f.Keep(item) {
			kept = append(kept, item)
		}
	}
	return kept
}

func matchesAny(terms []string, item Item) bool {
	summary := strings.ToLower(item.Title)
	for _, term := range terms {
		term = strings.TrimSpace(term)
		if term == "" {
			continue
		}
		if strings.Contains(summary, strings.ToLower(term)) {
			return true
		}
		for _, category := range item.Categories {
			if strings.EqualFold(category, term) {
				return true
			}
		}
	}
	return false
}

// StripLocationSuffix removes a trailing " - <location>" from the item's
// title when it repeats the item's own location, as MyMüll waste-collection
// feeds do ("Bioabfall - <town>, <street>" at "<town>, <street>"). The
// location stays in Location; any other title is left as it is.
func StripLocationSuffix(item Item) Item {
	if item.Location == nil {
		return item
	}
	location := strings.TrimSpace(*item.Location)
	if location == "" {
		return item
	}
	if title, found := strings.CutSuffix(strings.TrimSpace(item.Title), " - "+location); found && strings.TrimSpace(title) != "" {
		item.Title = strings.TrimSpace(title)
	}
	return item
}
