package banking

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/banking/internal/enablebanking"
	"github.com/GAMch1k/life-os/services/banking/internal/finance"
	store "github.com/GAMch1k/life-os/services/banking/internal/repository/generated"
)

// Trigger is what started a sync.
type Trigger string

const (
	TriggerConnect   Trigger = "connect"
	TriggerManual    Trigger = "manual"
	TriggerScheduled Trigger = "scheduled"
)

// maxPages bounds the pages read for one account in one sync.
const maxPages = 200

// Counts is a sync's outcome as stored in last_counts: Finance's answer,
// and how many transactions were read and left out.
type Counts struct {
	Accounts struct {
		Created int `json:"created"`
		Updated int `json:"updated"`
	} `json:"accounts"`
	Transactions struct {
		Created        int `json:"created"`
		Updated        int `json:"updated"`
		Unchanged      int `json:"unchanged"`
		RemovedPending int `json:"removedPending"`
		Skipped        int `json:"skipped"`
	} `json:"transactions"`
	Fetched int `json:"fetched"`
	Ignored int `json:"ignored"`
}

// Changed reports whether the sync changed anything in Finance.
func (c Counts) Changed() bool {
	return c.Accounts.Created+c.Transactions.Created+c.Transactions.Updated+c.Transactions.RemovedPending > 0
}

func (c Counts) data() map[string]any {
	return map[string]any{
		"accountsCreated": c.Accounts.Created, "accountsUpdated": c.Accounts.Updated,
		"created": c.Transactions.Created, "updated": c.Transactions.Updated, "unchanged": c.Transactions.Unchanged,
		"removedPending": c.Transactions.RemovedPending, "skipped": c.Transactions.Skipped,
		"fetched": c.Fetched, "ignored": c.Ignored,
	}
}

// runSync syncs one connection; the caller holds syncMu. A scheduled run
// first sends an import still waiting for Finance, without reading the
// bank. A read without the owner present (psu absent) takes one of the
// day's unattended reads, or waits for tomorrow when none is left.
func (s *Service) runSync(ctx context.Context, conn store.BankConnection, trigger Trigger, psu *enablebanking.PSU) (store.BankConnection, error) {
	if trigger == TriggerScheduled && len(conn.PendingImport) > 0 {
		var waiting readResult
		if err := json.Unmarshal(conn.PendingImport, &waiting); err == nil {
			return s.push(ctx, conn, waiting, trigger)
		}
		s.logger.Warn("dropping an unreadable import waiting for Finance", "connection", uuidString(conn.ID))
	}
	if !s.Configured() {
		return conn, ErrNotConfigured
	}
	// Account reads name the accounts' session uids; the session id itself
	// must still open, or the key changed and the session cannot be ended.
	if _, err := s.sealer.Open(conn.SessionCiphertext, conn.SessionNonce); err != nil {
		row, setErr := s.setStatus(ctx, conn, StatusError, "The stored bank session cannot be decrypted with "+EnvTokenKey+" (was the key changed?). Reconnect the bank.")
		if setErr != nil {
			return conn, setErr
		}
		return row, &SyncError{Kind: "refused", Message: *row.LastError, Err: err}
	}
	if !psu.Present() {
		psu = nil
		if ok, err := s.takeUnattendedRead(ctx, conn); err != nil {
			return conn, err
		} else if !ok {
			return conn, ErrDailyLimit
		}
	}
	read, err := s.read(ctx, conn, psu)
	if apiError := enablebanking.AsAPIError(err); apiError != nil && apiError.PSUHeaders() && psu != nil {
		// The bank refused the owner's details: read as an unattended sync
		// rather than not at all.
		s.logger.Warn("the bank refused the owner-present headers; reading without them", "connection", uuidString(conn.ID), "code", apiError.Code)
		if ok, takeErr := s.takeUnattendedRead(ctx, conn); takeErr != nil {
			return conn, takeErr
		} else if ok {
			read, err = s.read(ctx, conn, nil)
		}
	}
	if err != nil {
		return s.bankFailed(ctx, conn, err, trigger)
	}
	return s.push(ctx, conn, read, trigger)
}

// takeUnattendedRead counts one read without the owner today; false means
// the day's reads are used up, and the next sync waits for tomorrow.
func (s *Service) takeUnattendedRead(ctx context.Context, conn store.BankConnection) (bool, error) {
	now := s.now()
	day := s.opts.Schedule.Day(now)
	_, err := s.queries.TakeUnattendedRead(ctx, store.TakeUnattendedReadParams{
		Day: pgtype.Date{Time: day, Valid: true}, ID: conn.ID, Max: int32(s.opts.MaxUnattended),
	})
	if err == nil {
		return true, nil
	}
	if !isNoRows(err) {
		return false, err
	}
	next := s.opts.Schedule.FirstTomorrow(now)
	s.logger.Info("no unattended bank reads left today", "connection", uuidString(conn.ID), "limit", s.opts.MaxUnattended, "next", next)
	if err := s.queries.SetNextSync(ctx, store.SetNextSyncParams{NextSyncAt: timestamptz(next), Now: timestamptz(now), ID: conn.ID}); err != nil {
		return false, err
	}
	return false, nil
}

// window is the booking dates to read: from the last booked date less the
// overlap, never more than InitialDays back (the first sync reads all of
// them), up to today (the provider takes dates in UTC).
func (s *Service) window(conn store.BankConnection) finance.Window {
	today := s.now().UTC()
	earliest := today.AddDate(0, 0, -s.opts.InitialDays)
	from := earliest
	if conn.SyncCursor.Valid {
		if cursor := conn.SyncCursor.Time.AddDate(0, 0, -s.opts.OverlapDays); cursor.After(earliest) {
			from = cursor
		}
		if from.After(today) {
			from = today
		}
	}
	return finance.Window{From: from.Format(time.DateOnly), To: today.Format(time.DateOnly)}
}

// read reads every account's balances and transactions in the window.
func (s *Service) read(ctx context.Context, conn store.BankConnection, psu *enablebanking.PSU) (readResult, error) {
	window := s.window(conn)
	accounts := Accounts(conn)
	data := make([]accountData, 0, len(accounts))
	for _, account := range accounts {
		balances, err := s.bank.Balances(ctx, account.UID, psu)
		if err != nil {
			return readResult{}, err
		}
		transactions, err := s.readTransactions(ctx, account.UID, window, psu)
		if err != nil {
			return readResult{}, err
		}
		data = append(data, accountData{account: account, balances: balances, transactions: transactions})
	}
	return buildImport(uuidString(conn.ID), conn.Institution, window, data), nil
}

// readTransactions reads every page. A bank that does not serve the whole
// window is asked for as much as it has instead.
func (s *Service) readTransactions(ctx context.Context, uid string, window finance.Window, psu *enablebanking.PSU) ([]enablebanking.Transaction, error) {
	query := enablebanking.TransactionsQuery{DateFrom: window.From, DateTo: window.To}
	var all []enablebanking.Transaction
	for page := 0; page < maxPages; page++ {
		result, err := s.bank.Transactions(ctx, uid, query, psu)
		if apiError := enablebanking.AsAPIError(err); apiError != nil && apiError.WrongPeriod() && page == 0 && query.Strategy == "" {
			query.Strategy = "longest"
			page--
			continue
		}
		if err != nil {
			return nil, err
		}
		all = append(all, result.Transactions...)
		if result.ContinuationKey == "" {
			return all, nil
		}
		query.ContinuationKey = result.ContinuationKey
	}
	return nil, errors.New("the bank returned more pages of transactions than expected")
}

// bankFailed records a failed read: an ended consent expires the
// connection; a bank that needs the owner puts it in error; anything else
// is retried with backoff (a refusal for too many reads after a long
// pause). An import still waiting for Finance is kept.
func (s *Service) bankFailed(ctx context.Context, conn store.BankConnection, err error, trigger Trigger) (store.BankConnection, error) {
	apiError := enablebanking.AsAPIError(err)
	switch {
	case apiError != nil && apiError.SessionEnded():
		row, expireErr := s.expire(ctx, conn, "ended_at_bank")
		if expireErr != nil {
			return conn, expireErr
		}
		return row, &SyncError{Kind: "expired", Message: fmt.Sprintf("%s access ended (the consent expired or was revoked at the bank). Reconnect the bank.", conn.Institution), Err: err}
	case apiError != nil && apiError.ActionRequired():
		message := fmt.Sprintf("%s needs you to confirm access again. Reconnect the bank.", conn.Institution)
		row, setErr := s.setStatus(ctx, conn, StatusError, message)
		if setErr != nil {
			return conn, setErr
		}
		s.syncFailedEvent(ctx, row, trigger, message, apiError.Code)
		return row, &SyncError{Kind: "refused", Message: message, Err: err}
	}
	now := s.now()
	failures := int(conn.ConsecutiveFailures) + 1
	next := s.opts.Schedule.bankRetry(now, failures)
	message := fmt.Sprintf("%s could not be read: %s. Retrying automatically.", conn.Institution, describe(err))
	if apiError != nil && apiError.RateLimited() {
		next = s.opts.Schedule.rateLimitRetry(now)
		message = fmt.Sprintf("%s refused more reads for now (too many today). The next sync waits a few hours.", conn.Institution)
	}
	if apiError != nil && apiError.Unauthorized() {
		message = fmt.Sprintf("Enable Banking refused this server's application (%s): check %s and the key file. Retrying at the next sync time.", apiError.Code, EnvAppID)
		next = s.opts.Schedule.Next(now)
	}
	row, recordErr := s.queries.RecordSyncFailure(ctx, store.RecordSyncFailureParams{
		Now: timestamptz(now), NextSyncAt: timestamptz(next), LastError: &message, PendingImport: conn.PendingImport, ID: conn.ID,
	})
	if recordErr != nil {
		return conn, recordErr
	}
	code := ""
	if apiError != nil {
		code = apiError.Code
	}
	s.logger.Warn("bank sync failed", "connection", uuidString(conn.ID), "trigger", trigger, "failures", row.ConsecutiveFailures, "next", next, "error", err)
	if row.ConsecutiveFailures == 1 {
		s.syncFailedEvent(ctx, row, trigger, message, code)
	}
	return row, &SyncError{Kind: "bank", Message: message, Err: err}
}

// describe says what went wrong in a read, without identifiers.
func describe(err error) string {
	if apiError := enablebanking.AsAPIError(err); apiError != nil {
		text := fmt.Sprintf("Enable Banking answered %d", apiError.Status)
		if apiError.Code != "" {
			text += " " + apiError.Code
		}
		return text
	}
	var transport *enablebanking.TransportError
	if errors.As(err, &transport) {
		if errors.Is(err, context.DeadlineExceeded) {
			return "the bank took too long to answer"
		}
		return "Enable Banking could not be reached"
	}
	return "an unexpected error"
}

// push sends a read to Finance. Accepted, the connection is up to date and
// its cursor moves on. Finance unavailable: the read is kept and sent again
// shortly, without reading the bank. Refused (400): it is dropped and the
// next sync reads again.
func (s *Service) push(ctx context.Context, conn store.BankConnection, read readResult, trigger Trigger) (store.BankConnection, error) {
	result, err := s.finance.Import(ctx, read.Import)
	now := s.now()
	if err != nil {
		failures := int(conn.ConsecutiveFailures) + 1
		var keep []byte
		var next time.Time
		var message, kind string
		if finance.IsPermanent(err) {
			kind, next = "finance_refused", s.opts.Schedule.Next(now)
			message = fmt.Sprintf("Finance refused the sync (%s). It is read again at the next sync time.", financeCode(err))
		} else {
			kind, next = "finance", financeRetry(now, failures)
			message = "Finance could not be reached; the sync is kept and sent again shortly."
			if keep, err = json.Marshal(read); err != nil {
				return conn, err
			}
		}
		row, recordErr := s.queries.RecordSyncFailure(ctx, store.RecordSyncFailureParams{
			Now: timestamptz(now), NextSyncAt: timestamptz(next), LastError: &message, PendingImport: keep, ID: conn.ID,
		})
		if recordErr != nil {
			return conn, recordErr
		}
		s.logger.Warn("pushing the bank sync to Finance failed", "connection", uuidString(conn.ID), "trigger", trigger, "kept", keep != nil, "failures", row.ConsecutiveFailures, "error", err)
		if row.ConsecutiveFailures == 1 {
			s.syncFailedEvent(ctx, row, trigger, message, financeCode(err))
		}
		return row, &SyncError{Kind: kind, Message: message, Err: err}
	}
	var counts Counts
	counts.Accounts.Created, counts.Accounts.Updated = result.Accounts.Created, result.Accounts.Updated
	counts.Transactions.Created, counts.Transactions.Updated = result.Transactions.Created, result.Transactions.Updated
	counts.Transactions.Unchanged, counts.Transactions.RemovedPending = result.Transactions.Unchanged, result.Transactions.RemovedPending
	counts.Transactions.Skipped = result.Transactions.Skipped
	counts.Fetched, counts.Ignored = read.Fetched, read.Ignored
	rawCounts, err := json.Marshal(counts)
	if err != nil {
		return conn, err
	}
	cursor := conn.SyncCursor
	if read.Cursor != "" {
		if day, err := time.Parse(time.DateOnly, read.Cursor); err == nil && (!cursor.Valid || day.After(cursor.Time)) {
			cursor = pgtype.Date{Time: day, Valid: true}
		}
	}
	row, err := s.queries.RecordSyncSuccess(ctx, store.RecordSyncSuccessParams{
		Now: timestamptz(now), NextSyncAt: timestamptz(s.opts.Schedule.Next(now)), LastCounts: rawCounts, SyncCursor: cursor, ID: conn.ID,
	})
	if err != nil {
		return conn, err
	}
	s.logger.Info("bank synced", "connection", uuidString(conn.ID), "trigger", trigger, "fetched", counts.Fetched,
		"created", counts.Transactions.Created, "updated", counts.Transactions.Updated, "removedPending", counts.Transactions.RemovedPending)
	if counts.Changed() || conn.ConsecutiveFailures > 0 {
		trace := activity.TraceFrom(ctx)
		data := counts.data()
		data["trigger"] = string(trigger)
		data["window"] = map[string]string{"from": read.Import.Window.From, "to": read.Import.Window.To}
		if conn.ConsecutiveFailures > 0 {
			data["afterFailures"] = conn.ConsecutiveFailures
		}
		s.emit(ctx, activity.Event{
			ID: activity.EventID(EventSyncCompleted, uuidString(conn.ID), now.UTC().Format(time.RFC3339Nano)), Type: EventSyncCompleted,
			Subject: subject(row), Actor: trace.Actor, CorrelationID: trace.CorrelationID, CausationID: trace.CausationID,
			Outcome: activity.Succeeded, Summary: syncSummary(conn.Institution, counts), Data: data,
		})
	}
	return row, nil
}

func syncSummary(institution string, counts Counts) string {
	created, updated := counts.Transactions.Created, counts.Transactions.Updated
	switch {
	case created > 0 && updated > 0:
		return fmt.Sprintf("Synced %s: %d new and %d updated transaction%s", institution, created, updated, pluralS(updated))
	case created > 0:
		return fmt.Sprintf("Synced %s: %d new transaction%s", institution, created, pluralS(created))
	case updated > 0:
		return fmt.Sprintf("Synced %s: %d updated transaction%s", institution, updated, pluralS(updated))
	}
	return "Synced " + institution
}

func financeCode(err error) string {
	var status *finance.StatusError
	if errors.As(err, &status) {
		if status.Code != "" {
			return fmt.Sprintf("%d %s", status.Status, status.Code)
		}
		return fmt.Sprint(status.Status)
	}
	return "unreachable"
}

// syncFailedEvent records the first failure of a streak.
func (s *Service) syncFailedEvent(ctx context.Context, conn store.BankConnection, trigger Trigger, message, code string) {
	trace := activity.TraceFrom(ctx)
	s.emit(ctx, activity.Event{
		ID:   activity.EventID(EventSyncFailed, uuidString(conn.ID), conn.LastSyncAt.Time.UTC().Format(time.RFC3339Nano)),
		Type: EventSyncFailed, Subject: subject(conn), Actor: trace.Actor, CorrelationID: trace.CorrelationID, CausationID: trace.CausationID,
		Outcome: activity.Failed, Summary: "Syncing " + conn.Institution + " failed",
		Data: map[string]any{"trigger": string(trigger), "error": message, "code": code},
	})
}

// setStatus moves a connection to expired or error; it is not synced again
// until reconnected.
func (s *Service) setStatus(ctx context.Context, conn store.BankConnection, status, message string) (store.BankConnection, error) {
	row, err := s.queries.SetConnectionStatus(ctx, store.SetConnectionStatusParams{Status: status, LastError: &message, Now: timestamptz(s.now()), ID: conn.ID})
	if isNoRows(err) {
		return conn, nil
	}
	return row, err
}
