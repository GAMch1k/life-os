package ics

import "fmt"

// Error is a parse failure whose message is safe to store and show.
type Error struct{ Message string }

func (e *Error) Error() string { return e.Message }

func failure(format string, args ...any) error { return &Error{Message: fmt.Sprintf(format, args...)} }
