package delivery

import "time"

// Delivery states, as stored and returned by the API.
const (
	StatusPending  = "pending"
	StatusRetrying = "retrying"
	StatusSent     = "sent"
	StatusFailed   = "failed"
	StatusSkipped  = "skipped"
)

// Policy decides when a failed attempt is tried again and when a delivery
// gives up.
type Policy struct {
	// BaseDelay is the wait after the first failed attempt; each further
	// failure doubles it, up to MaxDelay.
	BaseDelay time.Duration
	MaxDelay  time.Duration
	// MaxAge bounds how long after its notification was created a delivery
	// is still tried. Later, it fails instead of arriving stale.
	MaxAge time.Duration
}

// Backoff is the wait after failed attempt number attempt (1-based):
// BaseDelay × 2^(attempt-1), capped at MaxDelay.
func (p Policy) Backoff(attempt int32) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := p.BaseDelay
	for i := int32(1); i < attempt; i++ {
		delay *= 2
		if delay >= p.MaxDelay || delay <= 0 {
			return p.MaxDelay
		}
	}
	return min(delay, p.MaxDelay)
}

// Expired reports whether a notification created at createdAt is too old
// to deliver at now.
func (p Policy) Expired(createdAt, now time.Time) bool {
	return now.Sub(createdAt) > p.MaxAge
}

// Decision is what happens to a delivery after a failed attempt.
type Decision struct {
	Status        string // StatusRetrying, StatusFailed, or StatusSkipped
	NextAttemptAt time.Time
}

// AfterFailure decides the outcome of failed attempt number attempt of a
// notification created at createdAt: a Skip skips it, a Permanent error or a
// retry that would fall beyond MaxAge fails it, and anything else retries
// after Backoff.
func (p Policy) AfterFailure(attempt int32, createdAt, now time.Time, err error) Decision {
	switch {
	case IsSkip(err):
		return Decision{Status: StatusSkipped}
	case IsPermanent(err):
		return Decision{Status: StatusFailed}
	}
	next := now.Add(p.Backoff(attempt))
	if p.Expired(createdAt, next) {
		return Decision{Status: StatusFailed}
	}
	return Decision{Status: StatusRetrying, NextAttemptAt: next}
}
