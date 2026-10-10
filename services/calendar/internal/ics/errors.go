// Package ics parses iCalendar documents and normalizes their events into
// the items Calendar stores. Calendar no longer downloads feeds: the
// calendar-feeds integration service does (P11-08) and syncs their events
// through the integration API. Calendar keeps the parser for its in-process
// Google adapter, which expands recurrence rules with it, and for the item
// type its reconciliation works on.
package ics

import "fmt"

// Error is a failure whose message is safe to store and show.
type Error struct{ Message string }

func (e *Error) Error() string { return e.Message }

func failure(format string, args ...any) error { return &Error{Message: fmt.Sprintf(format, args...)} }
