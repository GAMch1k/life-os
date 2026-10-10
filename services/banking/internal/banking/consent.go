package banking

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/notify"
	store "github.com/GAMch1k/life-os/services/banking/internal/repository/generated"
)

// ConsentCategory is the notification category of consent reminders.
const ConsentCategory = "banking.consent"

// Consent notices, in the order they are sent.
const (
	notice7d      = "7d"
	notice1d      = "1d"
	noticeExpired = "expired"
)

var noticeRank = map[string]int{notice7d: 1, notice1d: 2, noticeExpired: 3}

// checkConsent reminds the owner 7 days and 1 day before a live
// connection's consent ends, expires it when it has ended, and makes sure
// an expired connection's notice was sent. Notices that fail to send are
// tried again at the next check (their dedupe keys prevent doubles).
func (s *Service) checkConsent(ctx context.Context, conn store.BankConnection) store.BankConnection {
	now := s.now()
	switch {
	case active(conn) && conn.ConsentValidUntil.Valid:
		left := conn.ConsentValidUntil.Time.Sub(now)
		switch {
		case left <= 0:
			row, err := s.expire(ctx, conn, "consent_ended")
			if err != nil {
				s.logger.Warn("expiring the bank connection failed", "connection", uuidString(conn.ID), "error", err)
				return conn
			}
			return row
		case left <= 24*time.Hour:
			s.remind(ctx, conn, notice1d)
		case left <= 7*24*time.Hour:
			s.remind(ctx, conn, notice7d)
		}
	case conn.Status == StatusExpired:
		s.remind(ctx, conn, noticeExpired)
	}
	return conn
}

// expire marks a connection expired (reason "consent_ended": its consent
// ran out; "ended_at_bank": the provider says the session ended) and tells
// the owner.
func (s *Service) expire(ctx context.Context, conn store.BankConnection, reason string) (store.BankConnection, error) {
	message := fmt.Sprintf("%s access expired on %s. Reconnect the bank to keep syncing.", conn.Institution, s.formatDay(conn.ConsentValidUntil.Time))
	if reason != "consent_ended" || !conn.ConsentValidUntil.Valid {
		message = fmt.Sprintf("%s access ended: the consent expired or was revoked at the bank. Reconnect the bank to keep syncing.", conn.Institution)
	}
	row, err := s.setStatus(ctx, conn, StatusExpired, message)
	if err != nil {
		return conn, err
	}
	if row.Status != StatusExpired {
		return row, nil
	}
	s.logger.Info("bank consent ended", "connection", uuidString(conn.ID), "reason", reason)
	trace := activity.TraceFrom(ctx)
	s.emit(ctx, activity.Event{
		ID:   activity.EventID(EventConsentExpired, uuidString(conn.ID), consentKey(conn)),
		Type: EventConsentExpired, Subject: subject(row), Actor: trace.Actor, CorrelationID: trace.CorrelationID,
		Outcome: activity.Failed, Summary: conn.Institution + " access expired",
		Data: map[string]any{"institution": conn.Institution, "reason": reason, "consentValidUntil": optionalTime(conn.ConsentValidUntil)},
	})
	s.remind(ctx, row, noticeExpired)
	return row, nil
}

// remind sends a consent notice unless it, or a later one, was already sent
// for this consent.
func (s *Service) remind(ctx context.Context, conn store.BankConnection, notice string) {
	sent := ""
	if conn.ConsentNotice != nil && sameInstant(conn.ConsentNoticeFor, conn.ConsentValidUntil) {
		sent = *conn.ConsentNotice
	}
	if noticeRank[sent] >= noticeRank[notice] {
		return
	}
	until := conn.ConsentValidUntil.Time
	var title, body string
	switch notice {
	case notice7d:
		title = fmt.Sprintf("%s access expires on %s", conn.Institution, s.formatDay(until))
		body = fmt.Sprintf("Reconnect %s in Finance before then to keep your transactions syncing. Your accounts and history stay as they are.", conn.Institution)
	case notice1d:
		title = fmt.Sprintf("%s access expires within a day", conn.Institution)
		body = fmt.Sprintf("It ends on %s. Reconnect %s in Finance to keep your transactions syncing.", s.formatTime(until), conn.Institution)
	default:
		title = fmt.Sprintf("%s access expired", conn.Institution)
		body = fmt.Sprintf("Transactions no longer sync. Reconnect %s in Finance; your accounts and history stay.", conn.Institution)
	}
	err := s.notify.Send(ctx, notify.Notification{
		Title: title, Body: body, Importance: notify.Medium, Category: ConsentCategory, Link: "/finance",
		DedupeKey: fmt.Sprintf("%s:%s:%s:%s", ConsentCategory, uuidString(conn.ID), consentKey(conn), notice),
	})
	if err != nil {
		s.logger.Warn("the bank consent notice was not sent; retrying later", "connection", uuidString(conn.ID), "notice", notice, "error", err)
		return
	}
	if err := s.queries.SetConsentNotice(ctx, store.SetConsentNoticeParams{Notice: &notice, NoticeFor: conn.ConsentValidUntil, Now: timestamptz(s.now()), ID: conn.ID}); err != nil {
		s.logger.Warn("recording the bank consent notice failed", "connection", uuidString(conn.ID), "error", err)
	}
	if notice != noticeExpired {
		trace := activity.TraceFrom(ctx)
		s.emit(ctx, activity.Event{
			ID:   activity.EventID(EventConsentExpiring, uuidString(conn.ID), consentKey(conn), notice),
			Type: EventConsentExpiring, Subject: subject(conn), Actor: trace.Actor, CorrelationID: trace.CorrelationID,
			Outcome: activity.Pending, Summary: fmt.Sprintf("%s access expires on %s", conn.Institution, s.formatDay(until)),
			Data: map[string]any{"institution": conn.Institution, "notice": notice, "consentValidUntil": optionalTime(conn.ConsentValidUntil)},
		})
	}
}

// consentKey names the consent a notice is about.
func consentKey(conn store.BankConnection) string {
	if !conn.ConsentValidUntil.Valid {
		return "none"
	}
	return conn.ConsentValidUntil.Time.UTC().Format(time.RFC3339)
}

// formatDay is a date such as "Sat 17 Oct 2026" in the owner's timezone.
func (s *Service) formatDay(t time.Time) string {
	return t.In(s.opts.Schedule.location()).Format("Mon 2 Jan 2006")
}

// formatTime is a time such as "Sat 17 Oct 2026, 14:05 CEST".
func (s *Service) formatTime(t time.Time) string {
	return t.In(s.opts.Schedule.location()).Format("Mon 2 Jan 2006, 15:04 MST")
}

// sameInstant reports whether two optional times are both unset or equal.
func sameInstant(a, b pgtype.Timestamptz) bool {
	return a.Valid == b.Valid && (!a.Valid || a.Time.Equal(b.Time))
}

func isNoRows(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}
