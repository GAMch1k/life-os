package banking

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/GAMch1k/life-os/services/banking/internal/enablebanking"
	"github.com/GAMch1k/life-os/services/banking/internal/finance"
	store "github.com/GAMch1k/life-os/services/banking/internal/repository/generated"
)

// The history read (P13-07). PSD2 lets a bank serve transactions older than
// 90 days only with fresh strong customer authentication, so banks share
// them only shortly after the owner logged in at the bank. Right after a
// connect or reconnect, Banking reads up to HistoryDays back in the
// background, owner-present with the callback's PSU headers (never one of
// the day's unattended reads), and imports what is older than the normal
// sync's window into Finance. Its outcome is recorded on the connection; a
// failure never touches the connection's status or its normal syncs.

// DefaultHistoryDays is how far back the history read asks by default; a
// bank that refuses that period is asked for as much as it has.
const DefaultHistoryDays = 730

// MaxHistoryDays is the most BANKING_HISTORY_DAYS allows.
const MaxHistoryDays = 3650

// History read outcomes (bank_connections.history_status).
const (
	HistoryRunning   = "running"
	HistoryCompleted = "completed"
	HistoryFailed    = "failed"
)

// historyAttempts is how often a part of the history is sent to Finance
// while Finance does not answer.
const historyAttempts = 3

// historyWindow is the booking dates the history read asks for: from
// HistoryDays back to the day before the normal sync's window (InitialDays
// back) starts, in UTC like the provider's dates.
func (s *Service) historyWindow() finance.Window {
	today := s.now().UTC()
	return finance.Window{
		From: today.AddDate(0, 0, -s.opts.HistoryDays).Format(time.DateOnly),
		To:   today.AddDate(0, 0, -s.opts.InitialDays-1).Format(time.DateOnly),
	}
}

// startHistoryRead starts the history read of a connection the owner just
// consented to and returns the connection marked as reading. It needs the
// owner present (psu); without, or when the history read is off, conn is
// returned as it is. A history read already under way (an earlier consent's)
// is replaced: its result is no longer recorded.
func (s *Service) startHistoryRead(ctx context.Context, conn store.BankConnection, psu *enablebanking.PSU) store.BankConnection {
	if s.opts.HistoryDays <= s.opts.InitialDays {
		return conn
	}
	if !psu.Present() {
		s.logger.Info("no history read: the owner is not present, and it must not take an unattended read", "connection", uuidString(conn.ID))
		return conn
	}
	row, err := s.queries.StartHistoryRead(ctx, store.StartHistoryReadParams{Now: timestamptz(s.now()), ID: conn.ID})
	if err != nil {
		if !isNoRows(err) {
			s.logger.Warn("starting the history read failed", "connection", uuidString(conn.ID), "error", err)
		}
		return conn
	}
	s.history.Add(1)
	go func() {
		defer s.history.Done()
		readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.opts.HistoryTimeout)
		defer cancel()
		s.readHistory(readCtx, row, psu)
	}()
	return row
}

// WaitHistory waits for the history reads under way (tests).
func (s *Service) WaitHistory() {
	s.history.Wait()
}

// readHistory reads every account's booked transactions in the history
// window (as much as the bank has when it refuses the period) and imports
// them into Finance in parts.
func (s *Service) readHistory(ctx context.Context, conn store.BankConnection, psu *enablebanking.PSU) {
	id := uuidString(conn.ID)
	window := s.historyWindow()
	accounts := Accounts(conn)
	data := make([]accountData, 0, len(accounts))
	fetched := 0
	for _, account := range accounts {
		transactions, err := s.readTransactions(ctx, account.UID, window, psu)
		if err != nil {
			s.logger.Warn("the bank history read failed", "connection", id, "error", err)
			s.finishHistory(ctx, conn, HistoryFailed, fmt.Sprintf("%s did not share older transactions: %s.", conn.Institution, describe(err)))
			return
		}
		fetched += len(transactions)
		// Only booked transactions before the normal sync's window: that
		// window, pending ones included, stays the normal syncs' (a bank
		// asked for "longest" sends up to today).
		older := make([]enablebanking.Transaction, 0, len(transactions))
		for _, transaction := range transactions {
			day := firstDate(transaction.BookingDate, transaction.TransactionDate, transaction.ValueDate)
			if strings.EqualFold(transaction.Status, "BOOK") && day != "" && day <= window.To {
				older = append(older, transaction)
			}
		}
		data = append(data, accountData{account: account, transactions: older})
	}
	read := buildImport(id, conn.Institution, window, data)
	parts := historyParts(read.Import, s.opts.HistoryChunk)
	imported := 0
	for index, part := range parts {
		// A reconnect or disconnect meanwhile ends this read: a newer one
		// runs after a reconnect, and a disconnected bank's accounts are
		// archived.
		current, err := s.queries.GetConnection(ctx, conn.ID)
		if err != nil || !active(current) || !sameInstant(current.HistoryStartedAt, conn.HistoryStartedAt) {
			s.logger.Info("the history read stopped: the connection changed", "connection", id)
			s.finishHistory(ctx, conn, HistoryFailed, "Stopped: the bank connection changed while older history was loading.")
			return
		}
		if err := s.pushHistory(ctx, id, part); err != nil {
			s.logger.Warn("Finance did not take the bank history", "connection", id, "part", index+1, "parts", len(parts), "error", err)
			s.finishHistory(ctx, conn, HistoryFailed, fmt.Sprintf("Finance did not take the older transactions (%s); %d of %d parts were imported.", financeCode(err), index, len(parts)))
			return
		}
		imported += len(part.Transactions)
		if err := s.queries.RecordHistoryProgress(ctx, store.RecordHistoryProgressParams{
			Earliest: dateOf(part.Window.From), Started: conn.HistoryStartedAt, Imported: int32(len(part.Transactions)),
			Now: timestamptz(s.now()), ID: conn.ID,
		}); err != nil {
			s.logger.Warn("recording the history read's progress failed", "connection", id, "error", err)
		}
	}
	s.logger.Info("bank history read", "connection", id, "fetched", fetched, "imported", imported, "parts", len(parts),
		"from", window.From, "to", window.To)
	s.finishHistory(ctx, conn, HistoryCompleted, "")
}

// pushHistory sends one part of the history to Finance, again after a pause
// while Finance does not answer (a refusal is final).
func (s *Service) pushHistory(ctx context.Context, id string, part finance.Import) error {
	wait := s.opts.HistoryRetryWait
	for attempt := 1; ; attempt++ {
		_, err := s.finance.Import(ctx, part)
		if err == nil || finance.IsPermanent(err) || attempt == historyAttempts {
			return err
		}
		s.logger.Info("Finance did not answer the history import; sending it again", "connection", id, "attempt", attempt, "error", err)
		select {
		case <-ctx.Done():
			return err
		case <-time.After(wait):
		}
		wait *= 3
	}
}

// finishHistory records the history read's outcome, unless a newer one
// replaced it. It still records when the read ran out of time.
func (s *Service) finishHistory(ctx context.Context, conn store.BankConnection, status, message string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	var text *string
	if message != "" {
		text = &message
	}
	_, err := s.queries.FinishHistoryRead(ctx, store.FinishHistoryReadParams{
		HistoryStatus: &status, Now: timestamptz(s.now()), HistoryError: text, ID: conn.ID, Started: conn.HistoryStartedAt,
	})
	if err != nil && !isNoRows(err) {
		s.logger.Warn("recording the history read's outcome failed", "connection", uuidString(conn.ID), "error", err)
	}
}

// failInterruptedHistory marks history reads a stopped process left running
// as failed; none runs before the service does.
func (s *Service) failInterruptedHistory(ctx context.Context) {
	message := "Interrupted: banking restarted while older history was loading. Reconnect the bank to load it."
	now := s.now()
	if _, err := s.queries.FailInterruptedHistoryReads(ctx, store.FailInterruptedHistoryReadsParams{
		Now: timestamptz(now), HistoryError: &message, Before: timestamptz(now),
	}); err != nil {
		s.logger.Warn("marking interrupted history reads failed", "error", err)
	}
}

// historyParts splits a history import into imports of about size
// transactions each, newest first, so that the dates Finance holds stay
// contiguous back from today when a later part fails. A day is never split.
//
// Finance removes the connection's pending transactions booked inside an
// import's window that the import does not carry, so the windows are
// chosen to never remove anything wrongly: they neither overlap nor reach
// into the normal sync's window (where pending transactions live). Each
// part's window runs from its earliest booking date to the day before the
// next newer part's earliest, the newest part's up to the end of the
// history window, and the oldest starts at the earliest date the bank
// actually returned, not at the date asked for (a bank asked for "longest"
// may have served less).
func historyParts(read finance.Import, size int) []finance.Import {
	transactions := slices.Clone(read.Transactions)
	if len(transactions) == 0 {
		return nil
	}
	slices.SortStableFunc(transactions, func(a, b finance.Transaction) int { return strings.Compare(a.BookingDate, b.BookingDate) })
	var groups [][]finance.Transaction
	for start := 0; start < len(transactions); {
		end := start
		for end < len(transactions) {
			next := end
			for next < len(transactions) && transactions[next].BookingDate == transactions[end].BookingDate {
				next++
			}
			if end > start && next-start > size {
				break
			}
			end = next
		}
		groups = append(groups, transactions[start:end])
		start = end
	}
	parts := make([]finance.Import, 0, len(groups))
	for index, group := range groups {
		to := read.Window.To
		if index+1 < len(groups) {
			to = dayBefore(groups[index+1][0].BookingDate)
		}
		parts = append(parts, finance.Import{
			ConnectionID: read.ConnectionID, Provider: read.Provider, Institution: read.Institution,
			Window:   finance.Window{From: group[0].BookingDate, To: to},
			Accounts: read.Accounts, Transactions: group,
		})
	}
	slices.Reverse(parts)
	return parts
}

func dayBefore(day string) string {
	parsed, err := time.Parse(time.DateOnly, day)
	if err != nil {
		return day
	}
	return parsed.AddDate(0, 0, -1).Format(time.DateOnly)
}
