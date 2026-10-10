package banking_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/GAMch1k/life-os/services/banking/internal/banking"
	"github.com/GAMch1k/life-os/services/banking/internal/finance"
	store "github.com/GAMch1k/life-os/services/banking/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/banking/internal/testsupport"
)

func lastImport(t *testing.T, h *testsupport.Harness) finance.Import {
	t.Helper()
	imports := h.Finance.Imports()
	if len(imports) == 0 {
		t.Fatal("Finance received no import")
	}
	return imports[len(imports)-1]
}

func counts(t *testing.T, conn store.BankConnection) banking.Counts {
	t.Helper()
	var c banking.Counts
	if err := json.Unmarshal(conn.LastCounts, &c); err != nil {
		t.Fatalf("last counts %s: %v", conn.LastCounts, err)
	}
	return c
}

func TestConnectChecksTheStateAndRunsTheFirstSync(t *testing.T) {
	h := testsupport.New(t)
	ctx := context.Background()
	state := h.Authorize()

	// The consent asked for: read-only, as long as the bank allows (180
	// days, less a margin), back to the Dashboard's callback.
	auth := h.Bank.AuthRequest(state)
	access := auth["access"].(map[string]any)
	validUntil, _ := time.Parse(time.RFC3339, access["valid_until"].(string))
	if access["balances"] != true || access["transactions"] != true || auth["psu_type"] != "personal" ||
		auth["redirect_url"] != "https://lifeos.example/finance/bank/callback" ||
		validUntil.After(testsupport.Start.Add(180*24*time.Hour)) || validUntil.Before(testsupport.Start.Add(179*24*time.Hour)) {
		t.Fatalf("auth request = %+v", auth)
	}
	// Only the state's hash is stored.
	var stored []byte
	if err := h.Pool.QueryRow(ctx, "SELECT state_hash FROM bank_auth_states").Scan(&stored); err != nil || string(stored) == state || len(stored) != 32 {
		t.Fatalf("stored state %x, %v", stored, err)
	}

	code := h.Bank.Approve(state)
	if _, err := h.Service.Connect(testsupport.Owner(), code, "forged-state", testsupport.PSU()); !errors.Is(err, banking.ErrInvalidState) {
		t.Fatalf("a forged state: %v", err)
	}
	conn, err := h.Service.Connect(testsupport.Owner(), code, state, testsupport.PSU())
	if err != nil {
		t.Fatal(err)
	}
	if conn.Status != banking.StatusConnected || !conn.ConsentValidUntil.Valid || !conn.LastSuccessAt.Valid || conn.ConsecutiveFailures != 0 {
		t.Fatalf("connection = %+v", conn)
	}
	if _, err := h.Service.Connect(testsupport.Owner(), code, state, testsupport.PSU()); !errors.Is(err, banking.ErrInvalidState) {
		t.Fatalf("a used state: %v", err)
	}
	// An expired state.
	late := h.Authorize()
	h.Advance(banking.StateLifetime + time.Second)
	if _, err := h.Service.Connect(testsupport.Owner(), h.Bank.Approve(late), late, testsupport.PSU()); !errors.Is(err, banking.ErrInvalidState) {
		t.Fatalf("an expired state: %v", err)
	}

	// The first sync was owner-present: PSU headers on every account read,
	// no unattended read counted.
	for _, request := range h.Bank.Requests() {
		if strings.HasPrefix(request.Path, "/accounts/") && (request.PSUIP != "203.0.113.7" || request.PSUUserAgent == "") {
			t.Errorf("account read without PSU headers: %+v", request)
		}
	}
	if conn.UnattendedReadsCount != 0 {
		t.Errorf("unattended reads = %d", conn.UnattendedReadsCount)
	}
	if events := h.Activity.OfType(banking.EventConnected); len(events) != 1 || events[0]["actor"].(map[string]any)["kind"] != "user" {
		t.Fatalf("connected events = %+v", events)
	}
	if events := h.Activity.OfType(banking.EventSyncCompleted); len(events) != 1 {
		t.Fatalf("sync completed events = %+v", events)
	}
}

func TestTheFirstSyncMapsTheBankForFinance(t *testing.T) {
	h := testsupport.New(t)
	conn := h.Connect()
	got := lastImport(t, h)

	if got.ConnectionID != uuid(conn) || got.Provider != "enablebanking" || got.Institution != "Deutsche Bank" ||
		got.Window != (finance.Window{From: "2026-07-12", To: "2026-10-10"}) {
		t.Fatalf("import = %+v", got)
	}
	if len(got.Accounts) != 1 {
		t.Fatalf("accounts = %+v", got.Accounts)
	}
	account := got.Accounts[0]
	if account.Name != "Das Junge Konto" || account.Kind != "bank" || account.Currency != "EUR" || account.IBANMasked != "DE89…3000" ||
		account.BookedBalance == nil || *account.BookedBalance != (finance.Balance{Amount: 100000, Currency: "EUR", Date: "2026-10-09"}) ||
		account.AvailableBalance == nil || *account.AvailableBalance != (finance.Balance{Amount: 98766, Currency: "EUR", Date: "2026-10-10"}) {
		t.Fatalf("account = %+v", account)
	}
	byID := map[string]finance.Transaction{}
	for _, transaction := range got.Transactions {
		byID[transaction.ExternalID] = transaction
	}
	if len(byID) != 4 {
		t.Fatalf("90 days back, the older one left out: %+v", got.Transactions)
	}
	checks := map[string]finance.Transaction{
		"E1": {AccountExternalID: account.ExternalID, ExternalID: "E1", Status: "booked", BookingDate: "2026-10-08", ValueDate: "2026-10-08", Amount: finance.Money{Amount: -1234, Currency: "EUR"}, Counterparty: "REWE Markt", MerchantCategoryCode: "5411"},
		"E2": {AccountExternalID: account.ExternalID, ExternalID: "E2", Status: "booked", BookingDate: "2026-10-01", ValueDate: "2026-10-01", Amount: finance.Money{Amount: 250000, Currency: "EUR"}, Counterparty: "Employer GmbH", Description: "Gehalt Oktober"},
		"P3": {AccountExternalID: account.ExternalID, ExternalID: "P3", Status: "pending", BookingDate: "2026-10-10", Amount: finance.Money{Amount: -499, Currency: "EUR"}, Counterparty: "Bakery"},
	}
	for id, want := range checks {
		if byID[id] != want {
			t.Errorf("%s = %+v, want %+v", id, byID[id], want)
		}
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), testsupport.IBAN) || strings.Contains(string(raw), "Mustermann") {
		t.Fatalf("the import carries the IBAN or the holder's name: %s", raw)
	}
	c := counts(t, conn)
	if c.Fetched != 4 || c.Transactions.Created != 4 || c.Accounts.Created != 1 || conn.SyncCursor.Time.Format(time.DateOnly) != "2026-10-08" {
		t.Fatalf("counts = %+v cursor %v", c, conn.SyncCursor)
	}
	if actors := h.Finance.Actors(); len(actors) != 1 || actors[0] != "system:banking" {
		t.Fatalf("Finance actors = %v", actors)
	}
}

func TestLaterSyncsStartAWeekBeforeTheLastBookedDate(t *testing.T) {
	h := testsupport.New(t)
	conn := h.Connect()
	h.Bank.ResetRequests()
	h.Set(h.Local("2026-10-10", "12:00"))
	h.Service.Tick(context.Background())
	var query string
	for _, request := range h.Bank.Requests() {
		if strings.HasSuffix(request.Path, "/transactions") {
			query = request.Query.Get("date_from") + ".." + request.Query.Get("date_to")
			if request.PSUIP != "" {
				t.Errorf("a scheduled read claims the owner is present: %+v", request)
			}
		}
	}
	if query != "2026-10-01..2026-10-10" {
		t.Fatalf("window = %s", query)
	}
	conn = h.Connection(conn)
	if conn.UnattendedReadsCount != 1 || counts(t, conn).Transactions.Unchanged != 3 {
		t.Fatalf("connection = %+v counts %+v", conn, counts(t, conn))
	}
	// Nothing changed: no activity event for it.
	if events := h.Activity.OfType(banking.EventSyncCompleted); len(events) != 1 {
		t.Fatalf("a sync that changed nothing was recorded: %d events", len(events))
	}
}

func TestAtMostFourUnattendedReadsADay(t *testing.T) {
	h := testsupport.New(t)
	conn := h.Connect()
	ctx := context.Background()
	for _, at := range []string{"12:00", "18:00", "23:00"} {
		h.Set(h.Local("2026-10-10", at))
		h.Service.Tick(ctx)
	}
	conn = h.Connection(conn)
	if conn.UnattendedReadsCount != 3 {
		t.Fatalf("reads = %d", conn.UnattendedReadsCount)
	}
	// A dev tool's "sync now" (no owner present) is the fourth; a fifth is
	// refused without reading the bank.
	h.Set(h.Local("2026-10-10", "23:20"))
	if _, err := h.Service.SyncNow(context.Background(), conn.ID, nil); err != nil {
		t.Fatal(err)
	}
	reads := h.Bank.AccountReads()
	h.Set(h.Local("2026-10-10", "23:40"))
	if _, err := h.Service.SyncNow(context.Background(), conn.ID, nil); !errors.Is(err, banking.ErrDailyLimit) {
		t.Fatalf("a fifth read: %v", err)
	}
	if h.Bank.AccountReads() != reads {
		t.Fatal("the bank was read beyond the limit")
	}
	conn = h.Connection(conn)
	if !conn.NextSyncAt.Time.Equal(h.Local("2026-10-11", "06:00")) {
		t.Fatalf("next sync = %s", conn.NextSyncAt.Time.In(h.Location))
	}
	// The owner present still syncs.
	h.Set(h.Local("2026-10-10", "23:55"))
	if _, err := h.Service.SyncNow(testsupport.Owner(), conn.ID, testsupport.PSU()); err != nil {
		t.Fatalf("an owner-present sync after the limit: %v", err)
	}
	// A new day starts over.
	h.Set(h.Local("2026-10-11", "06:00"))
	h.Service.Tick(ctx)
	if conn = h.Connection(conn); conn.UnattendedReadsCount != 1 || conn.UnattendedReadsDate.Time.Format(time.DateOnly) != "2026-10-11" {
		t.Fatalf("the next day: %d reads on %v", conn.UnattendedReadsCount, conn.UnattendedReadsDate)
	}
}

func TestSyncNowIsOwnerPresentAndThrottled(t *testing.T) {
	h := testsupport.New(t)
	conn := h.Connect()
	h.Bank.ResetRequests()
	h.Advance(time.Minute)
	_, err := h.Service.SyncNow(testsupport.Owner(), conn.ID, testsupport.PSU())
	var throttled *banking.ThrottledError
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range h.Bank.Requests() {
		if request.PSUIP != "203.0.113.7" || request.PSUUserAgent != "Mozilla/5.0 Firefox/140.0" {
			t.Errorf("read without the owner's headers: %+v", request)
		}
	}
	h.Advance(9 * time.Minute)
	if _, err := h.Service.SyncNow(testsupport.Owner(), conn.ID, testsupport.PSU()); !errors.As(err, &throttled) || throttled.RetryAfter != time.Minute {
		t.Fatalf("a second sync within 10 minutes: %v", err)
	}
	h.Advance(time.Minute)
	if _, err := h.Service.SyncNow(testsupport.Owner(), conn.ID, testsupport.PSU()); err != nil {
		t.Fatalf("after 10 minutes: %v", err)
	}
	if conn = h.Connection(conn); conn.UnattendedReadsCount != 0 {
		t.Fatalf("owner-present syncs were counted: %d", conn.UnattendedReadsCount)
	}

	// The bank refuses the owner's headers: read without them (counted).
	h.Advance(10 * time.Minute)
	h.Bank.Fail("GET", "/accounts/", http.StatusBadRequest, "PSU_HEADER_INVALID", 1)
	if _, err := h.Service.SyncNow(testsupport.Owner(), conn.ID, testsupport.PSU()); err != nil {
		t.Fatalf("PSU headers refused: %v", err)
	}
	if conn = h.Connection(conn); conn.UnattendedReadsCount != 1 {
		t.Fatalf("the fallback read was not counted: %d", conn.UnattendedReadsCount)
	}
	if _, err := h.Service.SyncNow(testsupport.Owner(), testsupport.Missing(), testsupport.PSU()); !errors.Is(err, banking.ErrNotFound) {
		t.Fatalf("an unknown connection: %v", err)
	}
}

func TestConsentRemindersAndExpiry(t *testing.T) {
	h := testsupport.New(t)
	conn := h.Connect()
	ctx := context.Background()
	until := conn.ConsentValidUntil.Time
	h.Set(until.Add(-8 * 24 * time.Hour))
	h.Service.Tick(ctx)
	if len(h.Notifications.Bodies()) != 0 {
		t.Fatal("reminded 8 days ahead")
	}
	h.Set(until.Add(-7*24*time.Hour + time.Hour))
	h.Service.Tick(ctx)
	h.Service.Tick(ctx)
	notes := h.Notifications.Bodies()
	if len(notes) != 1 || notes[0]["category"] != "banking.consent" || notes[0]["importance"] != "medium" || notes[0]["link"] != "/finance" ||
		!strings.HasPrefix(notes[0]["title"].(string), "Deutsche Bank access expires on ") || notes[0]["_actor"] != "system:banking" {
		t.Fatalf("7-day reminder = %+v", notes)
	}
	if events := h.Activity.OfType(banking.EventConsentExpiring); len(events) != 1 {
		t.Fatalf("expiring events = %+v", events)
	}
	// The notice failed to send: tried again at the next check.
	h.Set(until.Add(-20 * time.Hour))
	h.Notifications.Fail(3)
	h.Service.Tick(ctx)
	h.Service.Tick(ctx)
	if notes = h.Notifications.Bodies(); len(notes) != 2 || notes[1]["title"] != "Deutsche Bank access expires within a day" {
		t.Fatalf("1-day reminder = %+v", notes)
	}
	h.Set(until.Add(time.Minute))
	reads := h.Bank.AccountReads()
	h.Service.Tick(ctx)
	h.Service.Tick(ctx)
	conn = h.Connection(conn)
	if conn.Status != banking.StatusExpired || conn.NextSyncAt.Valid || conn.LastError == nil {
		t.Fatalf("after the consent: %+v", conn)
	}
	if notes = h.Notifications.Bodies(); len(notes) != 3 || notes[2]["title"] != "Deutsche Bank access expired" {
		t.Fatalf("expiry notice = %+v", notes)
	}
	if events := h.Activity.OfType(banking.EventConsentExpired); len(events) != 1 {
		t.Fatalf("expired events = %+v", events)
	}
	if h.Bank.AccountReads() != reads {
		t.Fatal("an expired connection was read")
	}
	if _, err := h.Service.SyncNow(testsupport.Owner(), conn.ID, testsupport.PSU()); !errors.Is(err, banking.ErrInactive) {
		t.Fatalf("sync now when expired: %v", err)
	}

	// Reconnecting keeps the connection (and so the Finance accounts).
	old := h.Bank.SessionIDs()
	again := h.Connect()
	if again.ID != conn.ID || again.Status != banking.StatusConnected || again.ConsentNotice != nil {
		t.Fatalf("reconnected = %+v", again)
	}
	imports := h.Finance.Imports()
	if imports[len(imports)-1].Accounts[0].ExternalID != imports[0].Accounts[0].ExternalID || imports[len(imports)-1].ConnectionID != uuid(conn) {
		t.Fatal("the reconnect changed the Finance account ids")
	}
	if len(old) != 1 || h.Bank.SessionStatus(old[0]) != "CLOSED" {
		t.Fatalf("the replaced session was not ended: %v %s", old, h.Bank.SessionStatus(old[0]))
	}
}

func TestAConsentEndedAtTheBankExpiresTheConnection(t *testing.T) {
	h := testsupport.New(t)
	conn := h.Connect()
	h.Bank.SetSessionStatus(h.Bank.SessionIDs()[0], "REVOKED")
	h.Set(h.Local("2026-10-10", "12:00"))
	h.Service.Tick(context.Background())
	conn = h.Connection(conn)
	if conn.Status != banking.StatusExpired || !strings.Contains(*conn.LastError, "revoked at the bank") {
		t.Fatalf("connection = %+v", conn)
	}
	if notes := h.Notifications.Bodies(); len(notes) != 1 || notes[0]["title"] != "Deutsche Bank access expired" {
		t.Fatalf("notices = %+v", notes)
	}
}

func TestReconnectingWhileConnectedEndsTheOldSession(t *testing.T) {
	h := testsupport.New(t)
	first := h.Connect()
	second := h.Connect()
	sessions := h.Bank.SessionIDs()
	if second.ID != first.ID || len(sessions) != 2 || h.Bank.SessionStatus(sessions[0]) != "CLOSED" || h.Bank.SessionStatus(sessions[1]) != "AUTHORIZED" {
		t.Fatalf("sessions %v: %s %s", sessions, h.Bank.SessionStatus(sessions[0]), h.Bank.SessionStatus(sessions[1]))
	}
	var count int
	_ = h.Pool.QueryRow(context.Background(), "SELECT count(*) FROM bank_connections").Scan(&count)
	if count != 1 {
		t.Fatalf("%d connections", count)
	}
}

func TestDisconnectEndsTheSessionAndArchivesInFinance(t *testing.T) {
	h := testsupport.New(t)
	conn := h.Connect()
	ctx := context.Background()
	h.Finance.Fail(http.MethodDelete, http.StatusServiceUnavailable, "unavailable")
	if err := h.Service.Disconnect(testsupport.Owner(), conn.ID, testsupport.PSU()); err != nil {
		t.Fatal(err)
	}
	if status := h.Bank.SessionStatus(h.Bank.SessionIDs()[0]); status != "CLOSED" {
		t.Fatalf("session = %s", status)
	}
	conn = h.Connection(conn)
	if conn.Status != banking.StatusDisconnected || conn.SessionCiphertext != nil || conn.NextSyncAt.Valid || !conn.ArchivePending {
		t.Fatalf("disconnected = %+v", conn)
	}
	if len(h.Finance.Removed()) != 0 {
		t.Fatal("Finance was unavailable")
	}
	h.Service.Tick(ctx)
	if removed := h.Finance.Removed(); len(removed) != 1 || removed[0] != uuid(conn) {
		t.Fatalf("removed = %v", removed)
	}
	if conn = h.Connection(conn); conn.ArchivePending {
		t.Fatal("the archive is still pending")
	}
	if events := h.Activity.OfType(banking.EventDisconnected); len(events) != 1 || events[0]["data"].(map[string]any)["endedAtProvider"] != true {
		t.Fatalf("disconnected events = %+v", events)
	}
	if err := h.Service.Disconnect(testsupport.Owner(), conn.ID, testsupport.PSU()); err != nil {
		t.Fatalf("disconnecting again: %v", err)
	}
	if _, err := h.Service.SyncNow(testsupport.Owner(), conn.ID, testsupport.PSU()); !errors.Is(err, banking.ErrInactive) {
		t.Fatalf("sync after disconnect: %v", err)
	}
	// Connecting again is a new connection: the archived accounts keep
	// their history.
	again := h.Connect()
	if again.ID == conn.ID {
		t.Fatal("a disconnected connection was reused")
	}
	if err := h.Service.Disconnect(testsupport.Owner(), testsupport.Missing(), nil); !errors.Is(err, banking.ErrNotFound) {
		t.Fatalf("an unknown connection: %v", err)
	}
}

func TestBankFailuresBackOffAndLoseNothing(t *testing.T) {
	h := testsupport.New(t)
	conn := h.Connect()
	ctx := context.Background()
	cursor := conn.SyncCursor

	h.Set(h.Local("2026-10-10", "12:00"))
	h.Bank.Fail("GET", "/accounts/", http.StatusInternalServerError, "ASPSP_ERROR", 2)
	h.Service.Tick(ctx)
	conn = h.Connection(conn)
	if conn.ConsecutiveFailures != 1 || conn.SyncCursor != cursor || !conn.NextSyncAt.Time.Equal(h.Local("2026-10-10", "12:15")) ||
		conn.Status != banking.StatusConnected || !strings.Contains(*conn.LastError, "ASPSP_ERROR") {
		t.Fatalf("after a failure: %+v next %s", conn, conn.NextSyncAt.Time.In(h.Location))
	}
	h.Set(h.Local("2026-10-10", "12:15"))
	h.Service.Tick(ctx)
	if conn = h.Connection(conn); conn.ConsecutiveFailures != 2 || !conn.NextSyncAt.Time.Equal(h.Local("2026-10-10", "12:45")) {
		t.Fatalf("after two failures: %d next %s", conn.ConsecutiveFailures, conn.NextSyncAt.Time.In(h.Location))
	}
	if events := h.Activity.OfType(banking.EventSyncFailed); len(events) != 1 {
		t.Fatalf("failed events once per streak: %d", len(events))
	}
	h.Set(h.Local("2026-10-10", "12:45"))
	h.Service.Tick(ctx)
	conn = h.Connection(conn)
	if conn.ConsecutiveFailures != 0 || conn.LastError != nil {
		t.Fatalf("after recovering: %+v", conn)
	}
	completed := h.Activity.OfType(banking.EventSyncCompleted)
	if len(completed) != 2 || completed[1]["data"].(map[string]any)["afterFailures"] != float64(2) {
		t.Fatalf("recovery events = %+v", completed)
	}

	// Rate limited: leave the bank alone for hours.
	h.Set(h.Local("2026-10-10", "18:00"))
	h.Bank.Fail("GET", "/accounts/", http.StatusTooManyRequests, "ASPSP_RATE_LIMIT_EXCEEDED", 1)
	h.Service.Tick(ctx)
	if conn = h.Connection(conn); !conn.NextSyncAt.Time.Equal(h.Local("2026-10-11", "06:00")) {
		t.Fatalf("after a rate limit: next %s", conn.NextSyncAt.Time.In(h.Location))
	}
}

func TestFinanceFailuresKeepTheImportWithoutReadingTheBankAgain(t *testing.T) {
	h := testsupport.New(t)
	ctx := context.Background()
	h.Finance.Fail(http.MethodPost, http.StatusServiceUnavailable, "unavailable")
	conn := h.Connect()
	if conn.Status != banking.StatusPending || len(conn.PendingImport) == 0 || conn.ConsecutiveFailures != 1 || conn.SyncCursor.Valid {
		t.Fatalf("after Finance failed the first sync: %+v", conn)
	}
	if strings.Contains(string(conn.PendingImport), testsupport.IBAN) {
		t.Fatal("the waiting import holds the IBAN")
	}
	reads := h.Bank.AccountReads()
	h.Advance(time.Minute)
	h.Service.Tick(ctx)
	conn = h.Connection(conn)
	if conn.Status != banking.StatusConnected || len(conn.PendingImport) != 0 || conn.SyncCursor.Time.Format(time.DateOnly) != "2026-10-08" {
		t.Fatalf("after resending: %+v", conn)
	}
	if h.Bank.AccountReads() != reads || conn.UnattendedReadsCount != 0 {
		t.Fatal("resending read the bank again")
	}
	if len(h.Finance.Imports()) != 1 || counts(t, conn).Fetched != 4 {
		t.Fatalf("imports %d counts %+v", len(h.Finance.Imports()), counts(t, conn))
	}

	// Finance refuses an import: it is dropped, not retried in a loop.
	h.Set(h.Local("2026-10-10", "12:00"))
	h.Finance.Fail(http.MethodPost, http.StatusBadRequest, "currency_mismatch")
	h.Service.Tick(ctx)
	conn = h.Connection(conn)
	if len(conn.PendingImport) != 0 || !conn.NextSyncAt.Time.Equal(h.Local("2026-10-10", "18:00")) || !strings.Contains(*conn.LastError, "currency_mismatch") {
		t.Fatalf("after a refusal: %+v", conn)
	}
}

func TestPagesAndPeriodsAreHandled(t *testing.T) {
	h := testsupport.New(t)
	h.Bank.PageSize = 1
	h.Bank.EarliestDay = "2026-09-01"
	conn := h.Connect()
	if c := counts(t, conn); c.Fetched != 3 {
		t.Fatalf("the bank serves from 1 Sep only, in pages of one: %+v", c)
	}
	var strategies []string
	for _, request := range h.Bank.Requests() {
		if strings.HasSuffix(request.Path, "/transactions") {
			strategies = append(strategies, request.Query.Get("strategy")+":"+request.Query.Get("continuation_key"))
		}
	}
	if strings.Join(strategies, ",") != ":,longest:,longest:page-1,longest:page-2" {
		t.Fatalf("requests = %v", strategies)
	}
}

func TestAnUnreadableSessionPutsTheConnectionInError(t *testing.T) {
	h := testsupport.New(t)
	conn := h.Connect()
	if _, err := h.Pool.Exec(context.Background(), "UPDATE bank_connections SET session_nonce = '\\x00'"); err != nil {
		t.Fatal(err)
	}
	h.Set(h.Local("2026-10-10", "12:00"))
	h.Service.Tick(context.Background())
	if conn = h.Connection(conn); conn.Status != banking.StatusError || !strings.Contains(*conn.LastError, banking.EnvTokenKey) {
		t.Fatalf("connection = %+v", conn)
	}
}

func TestSecretsNeverLeave(t *testing.T) {
	h := testsupport.New(t)
	conn := h.Connect()
	h.Set(h.Local("2026-10-10", "12:00"))
	h.Bank.Fail("GET", "/accounts/", http.StatusInternalServerError, "ASPSP_ERROR", 1)
	h.Service.Tick(context.Background())
	if err := h.Service.Disconnect(testsupport.Owner(), conn.ID, nil); err != nil {
		t.Fatal(err)
	}
	session := h.Bank.SessionIDs()[0]
	everything := h.Logs() + h.Activity.Raw() + h.Notifications.Raw()
	for _, secret := range []string{session, testsupport.IBAN, "0532013000", "Bearer", "eyJ", "PRIVATE KEY"} {
		if strings.Contains(everything, secret) {
			t.Errorf("%q leaked into logs, activity, or notifications", secret)
		}
	}
	// Activity and logs never carry transaction details.
	for _, detail := range []string{"REWE", "Employer", "Gehalt", "Bakery", "12.34", "-1234", "250000"} {
		if strings.Contains(h.Logs()+h.Activity.Raw(), detail) {
			t.Errorf("transaction detail %q leaked into logs or activity", detail)
		}
	}
	for _, account := range banking.Accounts(h.Connection(conn)) {
		if strings.Contains(account.Key+account.Name+account.IBANMasked, "0532013000") {
			t.Errorf("stored account holds the IBAN: %+v", account)
		}
	}
}

func uuid(conn store.BankConnection) string {
	b := conn.ID.Bytes
	return strings.ToLower(strings.Join([]string{hex(b[0:4]), hex(b[4:6]), hex(b[6:8]), hex(b[8:10]), hex(b[10:16])}, "-"))
}

func hex(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, digits[c>>4], digits[c&15])
	}
	return string(out)
}
