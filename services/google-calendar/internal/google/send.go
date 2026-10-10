package google

import (
	"bytes"
	"encoding/json"
	"sort"
	"time"

	"github.com/GAMch1k/life-os/services/google-calendar/internal/calendar"
	"github.com/GAMch1k/life-os/services/google-calendar/internal/sources"
)

// What a calendar's derived items become in Calendar: SourceEvents, sent in
// deltas of what changed since Calendar was last told (google_sent_events),
// a few hundred at a time.

// maxBatchItems bounds the items of one sync request to Calendar.
const maxBatchItems = 500

// sourceEvent is item as Calendar's integration API takes it. reason, when
// not empty, makes the event read-only in Life OS.
func sourceEvent(item sources.Item, reason string) calendar.SourceEvent {
	out := calendar.SourceEvent{
		ExternalUID: item.ExternalUID, Title: item.Title, Description: item.Description, Location: item.Location,
		AllDay: item.AllDay, TimeZone: item.TimeZone, ReadOnlyReason: reason,
	}
	if item.AllDay {
		out.StartDate, out.EndDate = item.StartDate.Format(time.DateOnly), item.EndDate.Format(time.DateOnly)
		out.TimeZone = nil
	} else {
		start, end := item.StartAt.UTC(), item.EndAt.UTC()
		out.StartAt, out.EndAt = &start, &end
	}
	if item.Cancelled {
		out.Status = "cancelled"
	}
	if rule := item.Recurrence; rule != nil {
		recurrence := &calendar.Recurrence{Frequency: rule.Frequency, Interval: max(rule.Interval, 1), ByWeekday: rule.ByWeekday}
		if rule.Count > 0 {
			count := rule.Count
			recurrence.Count = &count
		}
		if rule.Until != nil {
			until := rule.Until.UTC()
			recurrence.Until = &until
		}
		out.Recurrence = recurrence
		keys := make([]string, 0, len(item.Exceptions))
		for key := range item.Exceptions {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			exception := item.Exceptions[key]
			sent := calendar.SourceException{RecurrenceID: key, Cancelled: exception.Cancelled}
			if !exception.Changes.Empty() {
				if raw, err := exception.Changes.JSON(); err == nil {
					sent.Changes = raw
				}
			}
			if !sent.Cancelled && len(sent.Changes) == 0 {
				continue
			}
			out.Exceptions = append(out.Exceptions, sent)
		}
	}
	if item.Reminders != nil {
		reminders := &calendar.SourceReminders{UseDefault: item.Reminders.UseDefault, Reminders: []calendar.Reminder{}}
		for _, reminder := range item.Reminders.Reminders {
			reminders.Reminders = append(reminders.Reminders, calendar.Reminder(reminder))
		}
		out.Reminders = reminders
	}
	if item.Priority != nil {
		switch *item.Priority {
		case "low", "medium", "high":
			out.Priority = *item.Priority
		case "":
			out.Priority = "default"
		}
	}
	return out
}

// delta is what one sync tells Calendar: the new and changed events, and
// the uids of those Google no longer has.
type delta struct {
	items   []calendar.SourceEvent
	deleted []string
	// sent are the events Calendar holds after the delta, by uid (the JSON
	// recorded in google_sent_events).
	sent map[string]json.RawMessage
}

// compare is the delta from what Calendar was last told (previous, by uid;
// a nil item is unknown and sent again) to events. rekeys give a new
// series its placeholder uid (previousExternalUid).
func compare(previous map[string][]byte, events []calendar.SourceEvent, rekeys map[string]string) (delta, error) {
	out := delta{sent: map[string]json.RawMessage{}}
	seen := map[string]bool{}
	for _, event := range events {
		encoded, err := json.Marshal(event)
		if err != nil {
			return delta{}, err
		}
		seen[event.ExternalUID] = true
		out.sent[event.ExternalUID] = encoded
		before, known := previous[event.ExternalUID]
		if known && before != nil && sameJSON(before, encoded) {
			continue
		}
		if placeholder, ok := rekeys[event.ExternalUID]; ok && !known {
			event.PreviousExternalUID = placeholder
		}
		out.items = append(out.items, event)
	}
	for uid := range previous {
		if !seen[uid] {
			out.deleted = append(out.deleted, uid)
		}
	}
	sort.Strings(out.deleted)
	return out, nil
}

// batches splits a delta's items into requests of at most maxBatchItems;
// the deletions go with the last one. An empty delta is one empty batch.
func (d delta) batches() []calendar.SyncRequest {
	var out []calendar.SyncRequest
	for start := 0; start < len(d.items) || start == 0; start += maxBatchItems {
		end := min(start+maxBatchItems, len(d.items))
		out = append(out, calendar.SyncRequest{Mode: "delta", Items: d.items[start:end]})
		if end >= len(d.items) {
			break
		}
	}
	out[len(out)-1].Deleted = d.deleted
	return out
}

func sameJSON(a, b []byte) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	if bytes.Equal(a, b) {
		return true
	}
	var left, right any
	if json.Unmarshal(a, &left) != nil || json.Unmarshal(b, &right) != nil {
		return false
	}
	l, _ := json.Marshal(left)
	r, _ := json.Marshal(right)
	return string(l) == string(r)
}
