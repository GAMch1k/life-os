// Package testsupport wires the banking service to fakes for integration
// tests: a migrated private schema (testdb), the fake Enable Banking API, a
// fake Finance, recorders standing in for Notifications and Activity, a
// captured log, and a controllable clock. Tests skip without
// BANKING_TEST_DATABASE_URL.
package testsupport

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/notify"
	"github.com/GAMch1k/life-os/services/banking/internal/banking"
	"github.com/GAMch1k/life-os/services/banking/internal/enablebanking"
	"github.com/GAMch1k/life-os/services/banking/internal/enablebanking/fakebank"
	"github.com/GAMch1k/life-os/services/banking/internal/finance"
	"github.com/GAMch1k/life-os/services/banking/internal/finance/fakefinance"
	store "github.com/GAMch1k/life-os/services/banking/internal/repository/generated"
	"github.com/GAMch1k/life-os/services/banking/internal/testdb"
)

// IBAN is the test account's IBAN; it must never leave Banking unmasked.
const IBAN = "DE89370400440532013000"

// TokenKey seals sessions in tests.
var TokenKey = bytes.Repeat([]byte{9}, banking.TokenKeySize)

// Recorder stands in for an HTTP service and keeps the JSON bodies posted
// to it. Fail makes the next requests answer 503.
type Recorder struct {
	*httptest.Server
	mu     sync.Mutex
	bodies []map[string]any
	fail   int
}

// NewRecorder starts a recorder.
func NewRecorder(t testing.TB) *Recorder {
	r := &Recorder{}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.fail > 0 {
			r.fail--
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		raw, _ := io.ReadAll(req.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		if body == nil {
			body = map[string]any{}
		}
		body["_actor"] = req.Header.Get(activity.ActorHeader)
		r.bodies = append(r.bodies, body)
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(r.Close)
	return r
}

// Fail makes the next times requests fail.
func (r *Recorder) Fail(times int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fail = times
}

// Bodies returns the recorded bodies.
func (r *Recorder) Bodies() []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.bodies)
}

// OfType returns the recorded activity events of eventType.
func (r *Recorder) OfType(eventType string) []map[string]any {
	var matched []map[string]any
	for _, body := range r.Bodies() {
		if body["type"] == eventType {
			matched = append(matched, body)
		}
	}
	return matched
}

// Raw is everything recorded, as JSON.
func (r *Recorder) Raw() string {
	raw, _ := json.Marshal(r.Bodies())
	return string(raw)
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Harness is the service with its fakes.
type Harness struct {
	T             testing.TB
	Pool          *pgxpool.Pool
	Bank          *fakebank.Server
	Finance       *fakefinance.Server
	Notifications *Recorder
	Activity      *Recorder
	Service       *banking.Service
	Location      *time.Location
	Logger        *slog.Logger

	logs  *lockedBuffer
	mu    sync.Mutex
	clock time.Time
}

// Start is the harness's initial time: Saturday 10 Oct 2026, 10:00 in
// Berlin (08:00 UTC).
var Start = time.Date(2026, 10, 10, 8, 0, 0, 0, time.UTC)

// New builds a configured service with one Deutsche Bank account holding
// balances and a few transactions. The history read after connecting is off
// unless configure turns it on (WithHistory).
func New(t testing.TB, configure ...func(*banking.Options)) *Harness {
	t.Helper()
	pool := testdb.Open(t)
	location, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	h := &Harness{T: t, Pool: pool, Location: location, clock: Start, logs: &lockedBuffer{}}
	h.Bank = fakebank.New(t)
	h.Bank.Now = h.Now
	h.Bank.Accounts = []fakebank.Account{DefaultAccount()}
	h.Finance = fakefinance.New(t)
	h.Notifications = NewRecorder(t)
	h.Activity = NewRecorder(t)
	// Without the wall-clock time: TestSecretsNeverLeave looks for amounts
	// such as "12.34", which a time like 13:57:12.34… also holds.
	h.Logger = slog.New(slog.NewJSONHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug, ReplaceAttr: dropTime}))
	minutes, _ := banking.ParseSyncTimes(banking.DefaultSyncTimes)
	settings := banking.Settings{AppID: fakebank.AppID, Key: fakebank.Key(), TokenKey: TokenKey, RedirectURI: "https://lifeos.example/finance/bank/callback", APIURL: h.Bank.URL}
	opts := banking.Options{Schedule: banking.Schedule{Minutes: minutes, Location: location}, HistoryDays: -1, HistoryRetryWait: 10 * time.Millisecond}
	for _, change := range configure {
		change(&opts)
	}
	h.Service = banking.New(settings, "", pool, finance.New(h.Finance.URL), notify.New(h.Notifications.URL, "banking"),
		activity.NewEmitter(h.Activity.URL, "banking", h.Logger), opts, h.Logger).WithClock(h.Now)
	return h
}

// WithHistory turns the history read after connecting on, 730 days back.
func WithHistory(opts *banking.Options) {
	opts.HistoryDays = banking.DefaultHistoryDays
}

// DefaultAccount is the test account: Das Junge Konto, EUR, with closing
// booked and interim available balances and transactions from 1 Aug to
// 10 Oct 2026 (one pending), plus one older than 90 days.
func DefaultAccount() fakebank.Account {
	return fakebank.Account{
		IBAN: IBAN, Product: "Das Junge Konto", HolderName: "Max Mustermann", Currency: "EUR", CashAccountType: "CACC",
		Balances: []map[string]any{
			{"balance_type": "CLBD", "balance_amount": map[string]any{"amount": "1000.00", "currency": "EUR"}, "reference_date": "2026-10-09"},
			{"balance_type": "ITAV", "balance_amount": map[string]any{"amount": "987.66", "currency": "EUR"}, "reference_date": "2026-10-10"},
			{"balance_type": "XPCD", "balance_amount": map[string]any{"amount": "982.67", "currency": "EUR"}, "reference_date": "2026-10-10"},
		},
		Transactions: []map[string]any{
			Transaction("E0", "BOOK", "DBIT", "49.90", "2026-08-01", map[string]any{"creditor": map[string]any{"name": "Stadtwerke"}}),
			Transaction("E2", "BOOK", "CRDT", "2500.00", "2026-10-01", map[string]any{"debtor": map[string]any{"name": "Employer GmbH"}, "remittance_information": []string{"Gehalt", "Oktober"}}),
			Transaction("E1", "BOOK", "DBIT", "12.34", "2026-10-08", map[string]any{"creditor": map[string]any{"name": "REWE Markt"}, "debtor": map[string]any{"name": "Max Mustermann"}, "merchant_category_code": "5411", "debtor_account": map[string]any{"iban": IBAN}}),
			Transaction("P3", "PDNG", "DBIT", "4.99", "", map[string]any{"transaction_date": "2026-10-10", "creditor": map[string]any{"name": "Bakery"}}),
			Transaction("OLD", "BOOK", "DBIT", "1.00", "2026-06-01", nil),
		},
	}
}

// Transaction is one provider transaction; an empty bookingDate leaves it
// out.
func Transaction(ref, status, indicator, amount, bookingDate string, extra map[string]any) map[string]any {
	transaction := map[string]any{
		"entry_reference": ref, "transaction_id": "tid-" + ref, "status": status, "credit_debit_indicator": indicator,
		"transaction_amount": map[string]any{"amount": amount, "currency": "EUR"},
	}
	if bookingDate != "" {
		transaction["booking_date"] = bookingDate
		transaction["value_date"] = bookingDate
	}
	for key, value := range extra {
		transaction[key] = value
	}
	return transaction
}

// Now is the harness clock.
func (h *Harness) Now() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.clock
}

// Set moves the clock to t.
func (h *Harness) Set(t time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clock = t
}

// Advance moves the clock on by d.
func (h *Harness) Advance(d time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clock = h.clock.Add(d)
}

// Local is a Berlin wall-clock time on day (YYYY-MM-DD) at hh:mm.
func (h *Harness) Local(day, clock string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", day+" "+clock, h.Location)
	if err != nil {
		h.T.Fatal(err)
	}
	return t
}

// Logs is everything the service logged.
func (h *Harness) Logs() string { return h.logs.String() }

// dropTime leaves the time out of the harness's log records.
func dropTime(groups []string, a slog.Attr) slog.Attr {
	if len(groups) == 0 && a.Key == slog.TimeKey {
		return slog.Attr{}
	}
	return a
}

// Owner is a request context made by the owner.
func Owner() context.Context {
	return activity.WithTrace(context.Background(), activity.Trace{Actor: activity.User(), CorrelationID: "corr-owner"})
}

// PSU is the owner's browser.
func PSU() *enablebanking.PSU {
	return &enablebanking.PSU{IPAddress: "203.0.113.7", UserAgent: "Mozilla/5.0 Firefox/140.0", AcceptLanguage: "de-DE"}
}

// Authorize starts a consent and returns its state.
func (h *Harness) Authorize() string {
	h.T.Helper()
	page, err := h.Service.Authorize(Owner(), "", "")
	if err != nil {
		h.T.Fatal(err)
	}
	parsed, err := url.Parse(page)
	if err != nil {
		h.T.Fatal(err)
	}
	return parsed.Query().Get("state")
}

// Connect runs the whole consent as the owner and returns the connection
// as Connect answered it; a history read it started may still run
// (Service.WaitHistory).
func (h *Harness) Connect() store.BankConnection {
	h.T.Helper()
	state := h.Authorize()
	conn, err := h.Service.Connect(Owner(), h.Bank.Approve(state), state, PSU())
	if err != nil {
		h.T.Fatal(err)
	}
	h.T.Cleanup(h.Service.WaitHistory)
	return conn
}

// Connection reads a connection.
func (h *Harness) Connection(conn store.BankConnection) store.BankConnection {
	h.T.Helper()
	row, err := store.New(h.Pool).GetConnection(context.Background(), conn.ID)
	if err != nil {
		h.T.Fatal(err)
	}
	return row
}

// Missing is a connection id that does not exist.
func Missing() pgtype.UUID {
	return pgtype.UUID{Bytes: [16]byte{0xde, 0xad}, Valid: true}
}
