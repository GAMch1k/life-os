package banking

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/banking/internal/enablebanking"
	store "github.com/GAMch1k/life-os/services/banking/internal/repository/generated"
)

// EventConsentFailed records a consent the bank or provider did not
// complete.
const EventConsentFailed = "banking.consent.failed"

// How a failed consent's state matched a pending authorization.
const (
	StateMatched = "matched"
	StateExpired = "expired"
	StateUnknown = "unknown"
)

// codeForm is a provider's error code as the log keeps it: as sent when it
// has a code's form (access_denied), else "other", since the callback's
// error parameter is whatever the redirect carried.
func codeForm(code string) string {
	if code == "" || len(code) > 64 || strings.Trim(strings.ToLower(code), "abcdefghijklmnopqrstuvwxyz0123456789_.-") != "" {
		return "other"
	}
	return code
}

// keptConsentFailures bounds the failures kept (the newest).
const keptConsentFailures = 100

// RecordConsentFailure keeps a consent that came back from the bank with an
// error instead of a code: when, the provider's error and description
// (cleaned, identifiers removed, at most 300 characters), and the bank and
// connection of the authorization its state belonged to. The state is used
// up like a completed one; an unknown or expired state is recorded as such.
// The state itself is never stored or logged, and no code is accepted.
func (s *Service) RecordConsentFailure(ctx context.Context, state, code, description string) (store.BankConsentFailure, error) {
	code = clip(code, 100)
	description = clip(enablebanking.Redact(description), 300)
	now := s.now()
	params := store.CreateConsentFailureParams{OccurredAt: timestamptz(now), Error: code, StateMatch: StateUnknown}
	if description != "" {
		params.Description = &description
	}
	if state != "" {
		pending, err := s.queries.ConsumeAuthState(ctx, HashState(state))
		switch {
		case err == nil:
			params.Institution, params.Country, params.ConnectionID = &pending.Institution, &pending.Country, pending.ConnectionID
			params.StateMatch = StateExpired
			if pending.ExpiresAt.Time.After(now) {
				params.StateMatch = StateMatched
			}
		case !isNoRows(err):
			return store.BankConsentFailure{}, err
		}
	}
	row, err := s.queries.CreateConsentFailure(ctx, params)
	if err != nil {
		return store.BankConsentFailure{}, err
	}
	if err := s.queries.PruneConsentFailures(ctx, keptConsentFailures); err != nil {
		s.logger.Warn("pruning old consent failures failed", "error", err)
	}
	// Ids and codes only: the bank's description is its own free text, and
	// which bank someone uses is theirs (docs/architecture/multi-user.md#logs).
	s.logger.Warn("bank consent not completed", "failure", uuidString(row.ID), "error", codeForm(code), "state", row.StateMatch)

	subject := activity.Subject{Type: "bank_consent", ID: uuidString(row.ID)}
	if row.ConnectionID.Valid {
		subject = activity.Subject{Type: SubjectType, ID: uuidString(row.ConnectionID)}
	}
	data := map[string]any{"error": code, "description": nil}
	if description != "" {
		data["description"] = description
	}
	trace := activity.TraceFrom(ctx)
	s.emit(ctx, activity.Event{
		ID: activity.EventID(EventConsentFailed, uuidString(row.ID)), Type: EventConsentFailed, Subject: subject,
		Actor: trace.Actor, CorrelationID: trace.CorrelationID, CausationID: trace.CausationID, Outcome: activity.Failed,
		Summary: fmt.Sprintf("Bank consent failed: %s", code), Data: data,
	})
	return row, nil
}

// LatestConsentFailure is the newest failure no connection has succeeded
// after, or nil.
func (s *Service) LatestConsentFailure(ctx context.Context) (*store.BankConsentFailure, error) {
	row, err := s.queries.LatestConsentFailure(ctx)
	if isNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// clearConsentFailures marks every failure so far as resolved: a
// connection succeeded.
func (s *Service) clearConsentFailures(ctx context.Context, now time.Time) {
	if err := s.queries.ClearConsentFailures(ctx, timestamptz(now)); err != nil {
		s.logger.Warn("clearing the failed consents failed", "error", err)
	}
}
