package banking_test

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/GAMch1k/life-os/services/banking/internal/banking"
	"github.com/GAMch1k/life-os/services/banking/internal/finance"
	"github.com/GAMch1k/life-os/services/banking/internal/testsupport"
)

// The harness's today is 2026-10-10: the first sync reads from 2026-07-12,
// so the history read asks for 2024-10-10 (730 days) to 2026-07-11.
const (
	historyFrom = "2024-10-10"
	historyTo   = "2026-07-11"
)

// withOlderHistory adds booked transactions older than 90 days to the test
// account (besides its OLD one of 2026-06-01): within two years, a card
// payment among them, one older than two years, and an old pending one.
func withOlderHistory(h *testsupport.Harness) {
	h.Bank.Accounts[0].Transactions = append(h.Bank.Accounts[0].Transactions,
		testsupport.Transaction("H1", "BOOK", "DBIT", "15.00", "2025-01-15", map[string]any{"creditor": map[string]any{"name": "Stadtwerke Muster"}}),
		testsupport.Transaction("H2", "BOOK", "CRDT", "2400.00", "2025-06-30", map[string]any{"debtor": map[string]any{"name": "Muster Arbeitgeber GmbH"}}),
		testsupport.Transaction("H3", "BOOK", "DBIT", "9.99", "2024-11-02", map[string]any{"creditor": map[string]any{"name": "ABRECHNUNG KARTE"},
			"remittance_information": []string{"KIOSK MUSTER//Darmstadt/DE 01-11-2024T09:15:00 Kartennr. 4999XXXXXXXX0042"}}),
		testsupport.Transaction("ANCIENT", "BOOK", "DBIT", "1.00", "2024-10-01", nil),
		testsupport.Transaction("OLDHOLD", "PDNG", "DBIT", "3.00", "2025-02-01", nil),
	)
}

// historyRequests are the transaction reads of the history read, as
// "strategy:date_from..date_to".
func historyRequests(h *testsupport.Harness) []string {
	var seen []string
	for _, request := range h.Bank.Requests() {
		if h.Bank.IsHistoryRead(request) {
			seen = append(seen, request.Query.Get("strategy")+":"+request.Query.Get("date_from")+".."+request.Query.Get("date_to"))
		}
	}
	return seen
}

func externalIDs(transactions []finance.Transaction) []string {
	ids := make([]string, 0, len(transactions))
	for _, transaction := range transactions {
		ids = append(ids, transaction.ExternalID)
	}
	slices.Sort(ids)
	return ids
}

func TestTheHistoryReadLoadsOlderTransactionsAfterConnecting(t *testing.T) {
	h := testsupport.New(t, testsupport.WithHistory)
	h.Bank.HistoryAfterLogin = 5 * time.Minute
	withOlderHistory(h)
	conn := h.Connect()
	// The owner does not wait for it: Connect answers with it running and
	// the 90 days already in Finance.
	if conn.HistoryStatus == nil || *conn.HistoryStatus != banking.HistoryRunning || conn.EarliestBookingDate.Time.Format(time.DateOnly) != "2026-08-01" {
		t.Fatalf("connect answered %+v", conn)
	}
	h.Service.WaitHistory()
	conn = h.Connection(conn)
	if *conn.HistoryStatus != banking.HistoryCompleted || conn.HistoryError != nil || !conn.HistoryFinishedAt.Valid || *conn.HistoryTransactions != 4 ||
		conn.EarliestBookingDate.Time.Format(time.DateOnly) != "2024-11-02" {
		t.Fatalf("after the history read: %+v", conn)
	}
	// The connection's own state is the first sync's.
	if conn.Status != banking.StatusConnected || conn.ConsecutiveFailures != 0 || conn.SyncCursor.Time.Format(time.DateOnly) != "2026-10-08" ||
		counts(t, conn).Fetched != 4 || conn.UnattendedReadsCount != 0 {
		t.Fatalf("the history read changed the connection: %+v counts %+v", conn, counts(t, conn))
	}
	// Owner-present (the callback's headers), for the history only.
	for _, request := range h.Bank.Requests() {
		if strings.HasPrefix(request.Path, "/accounts/") && (request.PSUIP != "203.0.113.7" || request.PSUUserAgent == "") {
			t.Errorf("a read without the owner's headers: %+v", request)
		}
	}
	if got := historyRequests(h); strings.Join(got, ",") != ":"+historyFrom+".."+historyTo {
		t.Fatalf("history requests = %v", got)
	}
	imports := h.Finance.Imports()
	if len(imports) != 2 {
		t.Fatalf("%d imports", len(imports))
	}
	history := imports[1]
	if history.Window != (finance.Window{From: "2024-11-02", To: historyTo}) ||
		strings.Join(externalIDs(history.Transactions), ",") != "H1,H2,H3,OLD" {
		t.Fatalf("history import = %+v", history)
	}
	for _, transaction := range history.Transactions {
		if transaction.ExternalID == "H3" && (transaction.Counterparty != "KIOSK MUSTER" || transaction.Description != "Card payment · Darmstadt, DE · 2024-11-01 09:15:00") {
			t.Fatalf("the old card payment = %+v", transaction)
		}
	}
	// Balances stay the first sync's; the pending one of today stays.
	if history.Accounts[0].BookedBalance != nil || history.Accounts[0].AvailableBalance != nil {
		t.Fatalf("the history import carries balances: %+v", history.Accounts[0])
	}
	if _, ok := h.Finance.Known(uuid(conn), "P3"); !ok {
		t.Fatal("the history import removed today's pending transaction")
	}
}

func TestTheHistoryReadTakesWhatTheBankHasWhenItRefusesThePeriod(t *testing.T) {
	h := testsupport.New(t, testsupport.WithHistory)
	h.Bank.EarliestDay = "2025-03-01"
	withOlderHistory(h)
	conn := h.Connect()
	h.Service.WaitHistory()
	conn = h.Connection(conn)
	if got := historyRequests(h); strings.Join(got, ",") != ":"+historyFrom+".."+historyTo+",longest:"+historyFrom+".." {
		t.Fatalf("history requests = %v", got)
	}
	imports := h.Finance.Imports()
	// "longest" sends up to today; only what is older than the normal
	// sync's window is imported, from the earliest day the bank returned.
	if len(imports) != 2 || imports[1].Window != (finance.Window{From: "2025-06-30", To: historyTo}) ||
		strings.Join(externalIDs(imports[1].Transactions), ",") != "H2,OLD" {
		t.Fatalf("imports = %+v", imports)
	}
	if *conn.HistoryStatus != banking.HistoryCompleted || conn.EarliestBookingDate.Time.Format(time.DateOnly) != "2025-06-30" {
		t.Fatalf("connection = %+v", conn)
	}
}

func TestAFailedHistoryReadLeavesTheConnectionAndItsNinetyDays(t *testing.T) {
	t.Run("the bank fails", func(t *testing.T) {
		h := testsupport.New(t, testsupport.WithHistory)
		withOlderHistory(h)
		h.Bank.FailIf(h.Bank.IsHistoryRead, http.StatusInternalServerError, "ASPSP_ERROR", 1)
		conn := h.Connect()
		h.Service.WaitHistory()
		conn = h.Connection(conn)
		if *conn.HistoryStatus != banking.HistoryFailed || conn.HistoryError == nil || !strings.Contains(*conn.HistoryError, "ASPSP_ERROR") {
			t.Fatalf("history = %+v", conn)
		}
		if conn.Status != banking.StatusConnected || conn.ConsecutiveFailures != 0 || conn.LastError != nil || !conn.NextSyncAt.Valid ||
			conn.UnattendedReadsCount != 0 || conn.EarliestBookingDate.Time.Format(time.DateOnly) != "2026-08-01" {
			t.Fatalf("the failure touched the connection: %+v", conn)
		}
		if imports := h.Finance.Imports(); len(imports) != 1 || len(imports[0].Transactions) != 4 {
			t.Fatalf("imports = %+v", imports)
		}
		if events := h.Activity.OfType(banking.EventSyncFailed); len(events) != 0 {
			t.Fatalf("a sync failure was recorded: %+v", events)
		}
		// The next scheduled sync runs as usual.
		h.Set(h.Local("2026-10-10", "12:00"))
		h.Service.Tick(context.Background())
		if conn = h.Connection(conn); conn.ConsecutiveFailures != 0 || conn.UnattendedReadsCount != 1 {
			t.Fatalf("the next sync: %+v", conn)
		}
	})
	t.Run("the bank refuses the owner's headers", func(t *testing.T) {
		h := testsupport.New(t, testsupport.WithHistory)
		withOlderHistory(h)
		h.Bank.FailIf(h.Bank.IsHistoryRead, http.StatusBadRequest, "PSU_HEADER_INVALID", 1)
		conn := h.Connect()
		h.Service.WaitHistory()
		// Never read again without the owner: that would take an
		// unattended read.
		if conn = h.Connection(conn); *conn.HistoryStatus != banking.HistoryFailed || conn.UnattendedReadsCount != 0 || len(historyRequests(h)) != 1 {
			t.Fatalf("connection = %+v requests %v", conn, historyRequests(h))
		}
	})
	t.Run("Finance refuses it", func(t *testing.T) {
		h := testsupport.New(t, testsupport.WithHistory)
		withOlderHistory(h)
		h.Finance.FailImport(func(i finance.Import) bool { return i.Window.To == historyTo }, http.StatusBadRequest, "invalid_request")
		conn := h.Connect()
		h.Service.WaitHistory()
		conn = h.Connection(conn)
		if *conn.HistoryStatus != banking.HistoryFailed || !strings.Contains(*conn.HistoryError, "400 invalid_request") || !strings.Contains(*conn.HistoryError, "0 of 1 parts") ||
			conn.Status != banking.StatusConnected || conn.LastError != nil || len(conn.PendingImport) != 0 {
			t.Fatalf("connection = %+v", conn)
		}
	})
	t.Run("Finance does not answer once", func(t *testing.T) {
		h := testsupport.New(t, testsupport.WithHistory)
		withOlderHistory(h)
		h.Finance.FailImport(func(i finance.Import) bool { return i.Window.To == historyTo }, http.StatusServiceUnavailable, "unavailable")
		conn := h.Connect()
		h.Service.WaitHistory()
		if conn = h.Connection(conn); *conn.HistoryStatus != banking.HistoryCompleted || *conn.HistoryTransactions != 4 {
			t.Fatalf("connection = %+v", conn)
		}
	})
}

func TestReconnectingReadsTheHistoryAgain(t *testing.T) {
	h := testsupport.New(t, testsupport.WithHistory)
	h.Bank.HistoryAfterLogin = 5 * time.Minute
	conn := h.Connect()
	h.Service.WaitHistory()
	first := h.Connection(conn)
	if *first.HistoryStatus != banking.HistoryCompleted || *first.HistoryTransactions != 1 {
		t.Fatalf("first history read: %+v", first)
	}
	// A day later, the bank shares older history only after a new login:
	// the reconnect's.
	h.Advance(24 * time.Hour)
	withOlderHistory(h)
	h.Bank.ResetRequests()
	again := h.Connect()
	if again.ID != conn.ID || *again.HistoryStatus != banking.HistoryRunning || again.HistoryStartedAt.Time.Equal(first.HistoryStartedAt.Time) {
		t.Fatalf("reconnected = %+v", again)
	}
	h.Service.WaitHistory()
	again = h.Connection(again)
	// One day later, the window moved by a day.
	if got := historyRequests(h); strings.Join(got, ",") != ":2024-10-11..2026-07-12" {
		t.Fatalf("history requests = %v", got)
	}
	if *again.HistoryStatus != banking.HistoryCompleted || *again.HistoryTransactions != 4 || again.EarliestBookingDate.Time.Format(time.DateOnly) != "2024-11-02" ||
		again.UnattendedReadsCount != 0 {
		t.Fatalf("after reconnecting: %+v", again)
	}
}

func TestTheHistoryGoesToFinanceInPartsThatNeverOverlap(t *testing.T) {
	h := testsupport.New(t, testsupport.WithHistory, func(opts *banking.Options) { opts.HistoryChunk = 2 })
	h.Bank.Accounts[0].Transactions = append(h.Bank.Accounts[0].Transactions,
		testsupport.Transaction("A", "BOOK", "DBIT", "1.00", "2024-12-01", nil),
		testsupport.Transaction("B", "BOOK", "DBIT", "2.00", "2025-01-10", nil),
		testsupport.Transaction("C", "BOOK", "DBIT", "3.00", "2025-01-10", nil),
		testsupport.Transaction("D", "BOOK", "DBIT", "4.00", "2025-03-05", nil),
		testsupport.Transaction("E", "BOOK", "DBIT", "5.00", "2025-05-20", nil),
	)
	conn := h.Connect()
	h.Service.WaitHistory()
	conn = h.Connection(conn)
	imports := h.Finance.Imports()[1:]
	var got []string
	for _, part := range imports {
		got = append(got, part.Window.From+".."+part.Window.To+"="+strings.Join(externalIDs(part.Transactions), "+"))
	}
	// Newest first, a day never split, each window up to the day before
	// the next newer part's.
	want := []string{"2026-06-01..2026-07-11=OLD", "2025-03-05..2026-05-31=D+E", "2025-01-10..2025-03-04=B+C", "2024-12-01..2025-01-09=A"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("parts = %v", got)
	}
	if *conn.HistoryStatus != banking.HistoryCompleted || *conn.HistoryTransactions != 6 || conn.EarliestBookingDate.Time.Format(time.DateOnly) != "2024-12-01" {
		t.Fatalf("connection = %+v", conn)
	}
	if _, ok := h.Finance.Known(uuid(conn), "P3"); !ok {
		t.Fatal("a history part removed today's pending transaction")
	}

	// A part Finance refuses stops the read; the newer parts stay, so the
	// dates held stay contiguous back from today.
	h2 := testsupport.New(t, testsupport.WithHistory, func(opts *banking.Options) { opts.HistoryChunk = 2 })
	h2.Bank.Accounts = h.Bank.Accounts
	h2.Finance.FailImport(func(i finance.Import) bool { return i.Window.From == "2025-01-10" }, http.StatusBadRequest, "invalid_request")
	conn = h2.Connect()
	h2.Service.WaitHistory()
	if conn = h2.Connection(conn); *conn.HistoryStatus != banking.HistoryFailed || *conn.HistoryTransactions != 3 ||
		!strings.Contains(*conn.HistoryError, "2 of 4 parts") || conn.EarliestBookingDate.Time.Format(time.DateOnly) != "2025-03-05" {
		t.Fatalf("after a refused part: %+v", conn)
	}
}

func TestNoHistoryReadWithoutTheOwner(t *testing.T) {
	h := testsupport.New(t, testsupport.WithHistory)
	state := h.Authorize()
	conn, err := h.Service.Connect(testsupport.Owner(), h.Bank.Approve(state), state, nil)
	if err != nil {
		t.Fatal(err)
	}
	h.Service.WaitHistory()
	if conn = h.Connection(conn); conn.HistoryStatus != nil || len(h.Finance.Imports()) != 1 || len(historyRequests(h)) != 0 {
		t.Fatalf("a history read ran without the owner: %+v", conn)
	}
}

func TestARestartFailsAnInterruptedHistoryRead(t *testing.T) {
	h := testsupport.New(t, testsupport.WithHistory)
	conn := h.Connect()
	h.Service.WaitHistory()
	if _, err := h.Pool.Exec(context.Background(), "UPDATE bank_connections SET history_status = 'running', history_finished_at = NULL, history_started_at = $1",
		testsupport.Start.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	// Run marks it first, then stops with its context.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	h.Service.Run(ctx, time.Minute)
	if conn = h.Connection(conn); *conn.HistoryStatus != banking.HistoryFailed || !strings.Contains(*conn.HistoryError, "Interrupted") || !conn.HistoryFinishedAt.Valid {
		t.Fatalf("connection = %+v", conn)
	}
}
