// Package banking is the bank integration: it connects the owner's bank
// accounts through Enable Banking (read-only consent), keeps the sealed
// provider session, syncs on a schedule within the banks' daily limits,
// reminds the owner before the consent ends, and pushes accounts and
// transactions into Finance through Finance's bank import endpoint.
//
// The session id, the application key and its tokens, and IBANs are never
// logged, returned, or put in activity events.
package banking

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/notify"
	"github.com/GAMch1k/life-os/services/banking/internal/enablebanking"
	"github.com/GAMch1k/life-os/services/banking/internal/finance"
	store "github.com/GAMch1k/life-os/services/banking/internal/repository/generated"
)

// Connection statuses.
const (
	StatusPending      = "pending"
	StatusConnected    = "connected"
	StatusExpired      = "expired"
	StatusError        = "error"
	StatusDisconnected = "disconnected"
)

// The bank connected by default.
const (
	DefaultInstitution = "Deutsche Bank"
	DefaultCountry     = "DE"
)

// Activity event types; their data never holds transaction details.
const (
	EventConnected       = "banking.connected"
	EventDisconnected    = "banking.disconnected"
	EventSyncCompleted   = "banking.sync.completed"
	EventSyncFailed      = "banking.sync.failed"
	EventConsentExpiring = "banking.consent.expiring"
	EventConsentExpired  = "banking.consent.expired"
)

// SubjectType is the activity subject of a connection.
const SubjectType = "bank_connection"

var (
	ErrNotConfigured  = errors.New("bank connections are not configured on this server: set " + EnvAppID + ", " + EnvKeyFile + ", and " + EnvTokenKey + " in the server's .env and restart banking")
	ErrNotFound       = errors.New("bank connection not found")
	ErrInactive       = errors.New("this bank connection is not active; reconnect the bank first")
	ErrInvalidState   = errors.New("this bank sign-in expired or was already used; start again with Connect")
	ErrCodeRejected   = errors.New("the bank did not accept the sign-in; start again with Connect")
	ErrNoAccounts     = errors.New("the consent covers no account; connect again and choose at least one account")
	ErrUnknownBank    = errors.New("Enable Banking does not offer this bank for personal accounts")
	ErrSyncInProgress = errors.New("a sync of this connection is already running")
	ErrDailyLimit     = errors.New("no more bank reads without you are allowed today; the next scheduled sync is tomorrow")
)

// ThrottledError means the owner asked for a sync too soon after the last.
type ThrottledError struct {
	RetryAfter time.Duration
}

func (e *ThrottledError) Error() string {
	return fmt.Sprintf("synced moments ago; sync again in %d minutes", int(e.RetryAfter.Round(time.Minute)/time.Minute)+1)
}

// SyncError is a sync that failed; Kind says where: "bank" (the bank or
// provider, retried), "expired" (the consent ended: reconnect), "refused"
// (the bank needs the owner: reconnect), "finance" (Finance unavailable:
// the sync is kept and sent again), or "finance_refused".
type SyncError struct {
	Kind    string
	Message string
	Err     error
}

func (e *SyncError) Error() string { return e.Message }
func (e *SyncError) Unwrap() error { return e.Err }

// Database is what the service needs of the pool.
type Database interface {
	store.DBTX
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Options shape syncing.
type Options struct {
	Schedule Schedule
	// MaxUnattended is the most reads without the owner a day (1-4).
	MaxUnattended int
	// ManualInterval is the least time between two syncs the owner asks for.
	ManualInterval time.Duration
	// InitialDays is how far back the first sync reads (and the earliest any
	// sync reads); OverlapDays is how far before the last booked date a
	// later sync starts.
	InitialDays int
	OverlapDays int
	// RequestTimeout bounds a sync made while the owner waits; SyncTimeout a
	// scheduled one.
	RequestTimeout time.Duration
	SyncTimeout    time.Duration
}

func (o Options) withDefaults() Options {
	if len(o.Schedule.Minutes) == 0 {
		o.Schedule.Minutes, _ = ParseSyncTimes(DefaultSyncTimes)
	}
	if o.MaxUnattended <= 0 || o.MaxUnattended > MaxUnattendedPerDay {
		o.MaxUnattended = MaxUnattendedPerDay
	}
	if o.ManualInterval <= 0 {
		o.ManualInterval = 10 * time.Minute
	}
	if o.InitialDays <= 0 {
		o.InitialDays = 90
	}
	if o.OverlapDays < 0 {
		o.OverlapDays = 0
	} else if o.OverlapDays == 0 {
		o.OverlapDays = 7
	}
	if o.RequestTimeout <= 0 {
		o.RequestTimeout = 45 * time.Second
	}
	if o.SyncTimeout <= 0 {
		o.SyncTimeout = 5 * time.Minute
	}
	return o
}

// Service is the bank integration.
type Service struct {
	settings     Settings
	setupProblem string
	bank         *enablebanking.Client
	sealer       *Sealer
	finance      *finance.Client
	notify       *notify.Client
	activity     *activity.Emitter
	db           Database
	queries      *store.Queries
	opts         Options
	logger       *slog.Logger
	now          func() time.Time

	// syncMu lets one sync, connect, or disconnect run at a time.
	syncMu sync.Mutex
}

// New builds the service. settings may be unconfigured (setupProblem
// explains why, from LoadSettings); the service then lists connections,
// sends consent reminders, disconnects, and refuses the rest.
func New(settings Settings, setupProblem string, db Database, fin *finance.Client, notifier *notify.Client, emitter *activity.Emitter, opts Options, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	s := &Service{
		settings: settings, setupProblem: setupProblem, finance: fin, notify: notifier, activity: emitter,
		db: db, queries: store.New(db), opts: opts.withDefaults(), logger: logger, now: time.Now,
	}
	if settings.Configured() {
		sealer, err := NewSealer(settings.TokenKey)
		if err != nil {
			s.settings, s.setupProblem = Settings{}, "Enable Banking is not configured: "+err.Error()
		} else {
			s.sealer = sealer
			s.bank = enablebanking.NewClient(settings.AppID, settings.Key, settings.APIURL, nil)
		}
	}
	return s
}

// WithClock replaces the clock (tests).
func (s *Service) WithClock(now func() time.Time) *Service {
	s.now = now
	if s.bank != nil {
		s.bank.WithClock(now)
	}
	return s
}

// Configured reports whether a bank can be connected and synced.
func (s *Service) Configured() bool {
	return s != nil && s.settings.Configured() && s.sealer != nil && s.bank != nil
}

// SetupProblem is what is missing or wrong in the setup ("" when none).
func (s *Service) SetupProblem() string { return s.setupProblem }

// Options are the effective sync options.
func (s *Service) Options() Options { return s.opts }

// Now is the service's clock.
func (s *Service) Now() time.Time { return s.now() }

// Connections lists every connection, live ones first.
func (s *Service) Connections(ctx context.Context) ([]store.BankConnection, error) {
	return s.queries.ListConnections(ctx)
}

// Authorize starts a consent for institution in country (Deutsche Bank, DE
// by default): it stores a single-use state for StateLifetime and returns
// the bank's consent page. A live connection of that bank is renewed by it.
func (s *Service) Authorize(ctx context.Context, institution, country string) (string, error) {
	if !s.Configured() {
		return "", ErrNotConfigured
	}
	institution, country = strings.TrimSpace(institution), strings.ToUpper(strings.TrimSpace(country))
	if institution == "" {
		institution = DefaultInstitution
	}
	if country == "" {
		country = DefaultCountry
	}
	aspsp, err := s.bank.FindASPSP(ctx, institution, country)
	if errors.Is(err, enablebanking.ErrUnknownASPSP) {
		return "", ErrUnknownBank
	}
	if err != nil {
		return "", &SyncError{Kind: "bank", Message: "Enable Banking could not be reached; try again in a moment", Err: err}
	}
	now := s.now()
	if err := s.queries.DeleteExpiredAuthStates(ctx, timestamptz(now)); err != nil {
		return "", err
	}
	state, err := NewState()
	if err != nil {
		return "", err
	}
	var connectionID pgtype.UUID
	if live, err := s.queries.GetLiveConnection(ctx, store.GetLiveConnectionParams{Provider: ProviderEnableBanking, Institution: aspsp.Name, Country: country}); err == nil {
		connectionID = live.ID
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	if err := s.queries.CreateAuthState(ctx, store.CreateAuthStateParams{
		StateHash: HashState(state), Institution: aspsp.Name, Country: country, ConnectionID: connectionID,
		ExpiresAt: timestamptz(now.Add(StateLifetime)),
	}); err != nil {
		return "", err
	}
	// As long as the bank allows (at most 180 days), less a margin so the
	// provider never sees a date beyond its maximum.
	validUntil := now.Add(aspsp.MaxConsent() - 10*time.Minute)
	url, err := s.bank.StartAuth(ctx, enablebanking.AuthRequest{
		ValidUntil: validUntil, ASPSPName: aspsp.Name, Country: country, State: state, RedirectURL: s.settings.RedirectURI,
	})
	if err != nil {
		return "", &SyncError{Kind: "bank", Message: "Enable Banking could not start the consent; try again in a moment", Err: err}
	}
	return url, nil
}

// Connect completes a consent with the code and state the bank returned:
// it checks the state, creates the provider session, stores it sealed with
// the accounts, and runs the first sync while the owner waits (psu marks
// it owner-present). Connecting a bank with a live connection renews it,
// keeping its id (and so its Finance accounts).
func (s *Service) Connect(ctx context.Context, code, state string, psu *enablebanking.PSU) (store.BankConnection, error) {
	if !s.Configured() {
		return store.BankConnection{}, ErrNotConfigured
	}
	pending, err := s.queries.ConsumeAuthState(ctx, HashState(state))
	if errors.Is(err, pgx.ErrNoRows) {
		return store.BankConnection{}, ErrInvalidState
	}
	if err != nil {
		return store.BankConnection{}, err
	}
	if !pending.ExpiresAt.Time.After(s.now()) {
		return store.BankConnection{}, ErrInvalidState
	}
	session, err := s.bank.CreateSession(ctx, code)
	if apiError := enablebanking.AsAPIError(err); apiError != nil && apiError.CodeRejected() {
		return store.BankConnection{}, ErrCodeRejected
	}
	if err != nil {
		return store.BankConnection{}, &SyncError{Kind: "bank", Message: "Enable Banking could not complete the connection; start again in a moment", Err: err}
	}
	accounts := storedAccounts(pending.Institution, session.Accounts)
	if len(accounts) == 0 {
		if err := s.bank.DeleteSession(ctx, session.ID, psu); err != nil {
			s.logger.Warn("ending a session without accounts failed", "error", err)
		}
		return store.BankConnection{}, ErrNoAccounts
	}
	rawAccounts, err := json.Marshal(accounts)
	if err != nil {
		return store.BankConnection{}, err
	}
	ciphertext, nonce, err := s.sealer.Seal([]byte(session.ID))
	if err != nil {
		return store.BankConnection{}, err
	}
	consent := pgtype.Timestamptz{}
	if !session.Access.ValidUntil.IsZero() {
		consent = timestamptz(session.Access.ValidUntil)
	}

	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	now := s.now()
	var row store.BankConnection
	var previousSession []byte
	var previousNonce []byte
	renewed := false
	err = s.inTx(ctx, func(q *store.Queries) error {
		existing, err := q.GetLiveConnection(ctx, store.GetLiveConnectionParams{Provider: ProviderEnableBanking, Institution: pending.Institution, Country: pending.Country})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err == nil {
			renewed, previousSession, previousNonce = true, existing.SessionCiphertext, existing.SessionNonce
			row, err = q.ReconnectConnection(ctx, store.ReconnectConnectionParams{
				ConsentValidUntil: consent, SessionCiphertext: ciphertext, SessionNonce: nonce, Accounts: rawAccounts,
				Now: timestamptz(now), ID: existing.ID,
			})
			return err
		}
		row, err = q.CreateConnection(ctx, store.CreateConnectionParams{
			Provider: ProviderEnableBanking, Institution: pending.Institution, Country: pending.Country,
			ConsentValidUntil: consent, SessionCiphertext: ciphertext, SessionNonce: nonce, Accounts: rawAccounts,
			Now: timestamptz(now),
		})
		return err
	})
	if err != nil {
		return store.BankConnection{}, err
	}
	// A renewal replaces a session that may still be valid: end it, so no
	// consent is left behind.
	if renewed && previousSession != nil {
		if old, err := s.sealer.Open(previousSession, previousNonce); err == nil && string(old) != session.ID {
			if err := s.bank.DeleteSession(ctx, string(old), psu); err != nil && !sessionGone(err) {
				s.logger.Warn("ending the replaced bank session failed", "connection", uuidString(row.ID), "error", err)
			}
		}
	}
	trace := activity.TraceFrom(ctx)
	summary := fmt.Sprintf("Connected %s (%d account%s)", row.Institution, len(accounts), pluralS(len(accounts)))
	if renewed {
		summary = fmt.Sprintf("Reconnected %s (%d account%s)", row.Institution, len(accounts), pluralS(len(accounts)))
	}
	s.emit(ctx, activity.Event{
		ID: activity.EventID(EventConnected, uuidString(row.ID), now.UTC().Format(time.RFC3339Nano)), Type: EventConnected,
		Subject: subject(row), Actor: trace.Actor, CorrelationID: trace.CorrelationID, CausationID: trace.CausationID,
		Outcome: activity.Succeeded, Summary: summary,
		Data: map[string]any{"institution": row.Institution, "accounts": len(accounts), "consentValidUntil": optionalTime(row.ConsentValidUntil), "reconnected": renewed},
	})
	s.logger.Info("bank connected", "connection", uuidString(row.ID), "institution", row.Institution, "accounts", len(accounts), "reconnected", renewed)

	// The first sync, while the owner waits; a failure leaves the
	// connection for the scheduler to retry.
	syncCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.opts.RequestTimeout)
	defer cancel()
	synced, err := s.runSync(syncCtx, row, TriggerConnect, psu)
	if err != nil {
		s.logger.Warn("the first sync failed", "connection", uuidString(row.ID), "error", err)
		if current, getErr := s.queries.GetConnection(ctx, row.ID); getErr == nil {
			return current, nil
		}
		return row, nil
	}
	return synced, nil
}

// SyncNow syncs a connection the owner asked for. With psu (the owner's
// address and browser) the read is owner-present and does not count
// against the daily limit; at most one every ManualInterval.
func (s *Service) SyncNow(ctx context.Context, id pgtype.UUID, psu *enablebanking.PSU) (store.BankConnection, error) {
	if !s.Configured() {
		return store.BankConnection{}, ErrNotConfigured
	}
	conn, err := s.queries.GetConnection(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.BankConnection{}, ErrNotFound
	}
	if err != nil {
		return store.BankConnection{}, err
	}
	if !active(conn) {
		return store.BankConnection{}, ErrInactive
	}
	if !s.syncMu.TryLock() {
		return store.BankConnection{}, ErrSyncInProgress
	}
	defer s.syncMu.Unlock()
	now := s.now()
	if _, err := s.queries.ClaimManualSync(ctx, store.ClaimManualSyncParams{
		Now: timestamptz(now), ID: id, NotBefore: timestamptz(now.Add(-s.opts.ManualInterval)),
	}); errors.Is(err, pgx.ErrNoRows) {
		wait := s.opts.ManualInterval
		if conn.LastManualSyncAt.Valid {
			wait = conn.LastManualSyncAt.Time.Add(s.opts.ManualInterval).Sub(now)
		}
		return store.BankConnection{}, &ThrottledError{RetryAfter: max(wait, time.Second)}
	} else if err != nil {
		return store.BankConnection{}, err
	}
	// Re-read under the lock: a sync may have just finished.
	if conn, err = s.queries.GetConnection(ctx, id); err != nil {
		return store.BankConnection{}, err
	}
	syncCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.opts.RequestTimeout)
	defer cancel()
	return s.runSync(syncCtx, conn, TriggerManual, psu)
}

// Disconnect removes a connection: it ends the provider session (which
// closes the bank consent where it can), wipes it, stops syncing, and asks
// Finance to archive the connection's accounts (retried by the scheduler
// when Finance is unavailable). Disconnecting again is harmless.
func (s *Service) Disconnect(ctx context.Context, id pgtype.UUID, psu *enablebanking.PSU) error {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	conn, err := s.queries.GetConnection(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if conn.Status != StatusDisconnected {
		revoked := false
		switch {
		case conn.SessionCiphertext == nil:
		case !s.Configured():
			s.logger.Warn("banking is not configured, so the bank session was not ended at the provider; it ends with the consent", "connection", uuidString(id))
		default:
			sessionID, err := s.sealer.Open(conn.SessionCiphertext, conn.SessionNonce)
			if err == nil {
				err = s.bank.DeleteSession(ctx, string(sessionID), psu)
			}
			revoked = err == nil || sessionGone(err)
			if !revoked {
				s.logger.Warn("ending the bank session at the provider failed; it ends with the consent", "connection", uuidString(id), "error", err)
			}
		}
		now := s.now()
		if conn, err = s.queries.DisconnectConnection(ctx, store.DisconnectConnectionParams{Now: timestamptz(now), ID: id}); err != nil {
			return err
		}
		trace := activity.TraceFrom(ctx)
		s.emit(ctx, activity.Event{
			ID: activity.EventID(EventDisconnected, uuidString(id)), Type: EventDisconnected, Subject: subject(conn),
			Actor: trace.Actor, CorrelationID: trace.CorrelationID, CausationID: trace.CausationID, Outcome: activity.Succeeded,
			Summary: "Disconnected " + conn.Institution,
			Data:    map[string]any{"institution": conn.Institution, "endedAtProvider": revoked},
		})
		s.logger.Info("bank disconnected", "connection", uuidString(id), "endedAtProvider", revoked)
	}
	if conn.ArchivePending {
		s.archive(ctx, conn)
	}
	return nil
}

// archive asks Finance to archive a disconnected connection's accounts.
func (s *Service) archive(ctx context.Context, conn store.BankConnection) {
	if err := s.finance.RemoveConnection(ctx, uuidString(conn.ID)); err != nil {
		s.logger.Warn("Finance did not archive the disconnected bank's accounts yet; retrying later", "connection", uuidString(conn.ID), "error", err)
		return
	}
	if err := s.queries.ClearArchivePending(ctx, store.ClearArchivePendingParams{Now: timestamptz(s.now()), ID: conn.ID}); err != nil {
		s.logger.Warn("recording the Finance archive failed", "connection", uuidString(conn.ID), "error", err)
	}
}

// Accounts reads a connection's stored accounts.
func Accounts(conn store.BankConnection) []StoredAccount {
	var accounts []StoredAccount
	if err := json.Unmarshal(conn.Accounts, &accounts); err != nil {
		return []StoredAccount{}
	}
	return accounts
}

// active reports whether a connection is synced.
func active(conn store.BankConnection) bool {
	return conn.Status == StatusPending || conn.Status == StatusConnected
}

// sessionGone reports whether the provider no longer has the session, so
// there is nothing to end.
func sessionGone(err error) bool {
	apiError := enablebanking.AsAPIError(err)
	return apiError != nil && (apiError.SessionEnded() || apiError.Status == 404)
}

func (s *Service) inTx(ctx context.Context, fn func(*store.Queries) error) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(s.queries.WithTx(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) emit(ctx context.Context, event activity.Event) {
	s.activity.Emit(ctx, event)
}

func subject(conn store.BankConnection) activity.Subject {
	return activity.Subject{Type: SubjectType, ID: uuidString(conn.ID)}
}

func timestamptz(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}

func optionalTime(t pgtype.Timestamptz) any {
	if !t.Valid {
		return nil
	}
	return t.Time.UTC().Format(time.RFC3339)
}

func uuidString(id pgtype.UUID) string {
	if !id.Valid {
		return ""
	}
	b := id.Bytes
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func pluralS(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}
