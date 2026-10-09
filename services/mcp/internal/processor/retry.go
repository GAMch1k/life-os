package processor

import (
	"context"
	"math"
	"math/rand/v2"
	"strings"
	"time"
)

// Failure kinds decide how a processing failure is retried.
type failureKind int

const (
	// failureCodex: the Codex CLI run itself failed (usage limit, sign-in,
	// transport). Recovery is expected; a usage limit has a known reset time.
	failureCodex failureKind = iota
	// failureDependency: an MCP tool or owning service was unavailable.
	failureDependency
	// failureModelOutput: Codex answered, but with an unusable plan or an
	// action the owning service rejected. Retrying may help a few times.
	failureModelOutput
)

// Retry reasons stored with a scheduled retry.
const (
	reasonUsageLimited = "usage_limited"
	reasonBackoff      = "backoff"
)

// maxModelOutputAttempts is how many attempts an item gets when Codex keeps
// producing unusable output before it needs operator attention.
const maxModelOutputAttempts = 5

// RetryPolicy schedules technical retries: at the provider's own recovery time
// when it reports one, otherwise with capped exponential backoff and jitter.
type RetryPolicy struct {
	// Base is the first backoff delay; each earlier attempt doubles it.
	Base time.Duration
	// Max caps a backoff delay.
	Max time.Duration
	// RecoveryAt returns when the Codex usage limit resets, if Codex reports it.
	RecoveryAt func(context.Context) (time.Time, bool)
	Now        func() time.Time
	// Jitter returns a value in [0, 1).
	Jitter func() float64
}

func (p RetryPolicy) withDefaults() RetryPolicy {
	if p.Base <= 0 {
		p.Base = 30 * time.Second
	}
	if p.Max <= 0 {
		p.Max = time.Hour
	}
	if p.Now == nil {
		p.Now = time.Now
	}
	if p.Jitter == nil {
		p.Jitter = rand.Float64
	}
	return p
}

// next returns when an item may be retried and why. previous is how many
// attempts the item had before this one.
func (p RetryPolicy) next(ctx context.Context, kind failureKind, previous int, cause string) (time.Time, string) {
	p = p.withDefaults()
	now := p.Now()
	if kind == failureCodex && classifyCodexError(cause) == CodexUsageLimited && p.RecoveryAt != nil {
		if at, ok := p.RecoveryAt(ctx); ok && at.After(now) {
			// Spread retries over a minute after the reset.
			return at.Add(time.Duration(p.Jitter() * float64(time.Minute))), reasonUsageLimited
		}
	}
	return now.Add(p.backoff(previous)), reasonBackoff
}

// backoff doubles from Base per previous attempt, caps at Max, and applies
// ±20% jitter.
func (p RetryPolicy) backoff(previous int) time.Duration {
	exponent := math.Min(float64(previous), 30)
	delay := math.Min(float64(p.Base)*math.Pow(2, exponent), float64(p.Max))
	return time.Duration(delay * (0.8 + 0.4*p.Jitter()))
}

// classifyActionError separates an owning service rejecting the action (model
// output) from it being unreachable (dependency).
func classifyActionError(err error) failureKind {
	if text := err.Error(); strings.Contains(text, " rejected") || strings.Contains(text, "returned 4") {
		return failureModelOutput
	}
	return failureDependency
}

// UsageRecovery returns the reset time of the exhausted Codex usage limit: the
// latest reset among buckets reported as reached or fully used.
func (u Usage) UsageRecovery() (time.Time, bool) {
	var latest time.Time
	for _, limit := range u.Limits {
		for _, window := range limit.Windows {
			if window.ResetsAt != nil && (limit.Reached != "" || window.UsedPercent >= 100) && window.ResetsAt.After(latest) {
				latest = *window.ResetsAt
			}
		}
	}
	return latest, !latest.IsZero()
}
