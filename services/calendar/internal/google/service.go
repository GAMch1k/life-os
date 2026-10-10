package google

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	googleuuid "github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/GAMch1k/life-os/pkg/activity"
	store "github.com/GAMch1k/life-os/services/calendar/internal/repository/generated"
)

// Connection statuses. not_connected has no row.
const (
	StatusNotConnected = "not_connected"
	StatusConnected    = "connected"
	StatusExpired      = "expired"
	StatusError        = "error"
)

// Activity event types.
const (
	EventConnected    = "calendar.google.connected"
	EventDisconnected = "calendar.google.disconnected"
	EventExpired      = "calendar.google.expired"
)

// RefreshMargin is how long before its expiry an access token is replaced,
// so a token never runs out during a request.
const RefreshMargin = 5 * time.Minute

var (
	ErrNotConfigured    = errors.New("Google Calendar is not configured on this server: set LIFE_OS_GOOGLE_CLIENT_ID, LIFE_OS_GOOGLE_CLIENT_SECRET, and LIFE_OS_GOOGLE_TOKEN_KEY in the server's .env and restart Calendar")
	ErrNotConnected     = errors.New("Google Calendar is not connected; connect it first")
	ErrExpired          = errors.New("Google access expired or was revoked; reconnect Google in Calendar")
	ErrInvalidState     = errors.New("this Google sign-in expired or was already used; start again with Connect Google")
	ErrCodeRejected     = errors.New("Google refused the sign-in code; start again with Connect Google")
	ErrScopeMissing     = errors.New("Google did not grant permission to read your calendar list; connect again and allow every permission Life OS asks for")
	ErrNoRefreshToken   = errors.New("Google issued no refresh token; connect again (Life OS asks for offline access)")
	ErrNoPrimary        = errors.New("Google listed no primary calendar, so the account cannot be identified")
	ErrCalendarNotFound = errors.New("Google calendar not found")
)

// Database is what the service needs of the pool: queries and transactions.
type Database interface {
	store.DBTX
	Begin(ctx context.Context) (pgx.Tx, error)
}

// accessToken is the current access token, kept in memory only.
type accessToken struct {
	connection pgtype.UUID
	value      string
	expiresAt  time.Time
}

// usable reports whether the token belongs to connection and stays valid for
// RefreshMargin after now.
func (t accessToken) usable(connection pgtype.UUID, now time.Time) bool {
	return t.value != "" && t.connection == connection && Fresh(t.expiresAt, now)
}

// Fresh reports whether a token expiring at expiresAt can still be used at
// now, keeping RefreshMargin in hand.
func Fresh(expiresAt, now time.Time) bool {
	return now.Add(RefreshMargin).Before(expiresAt)
}

// Service is the Google connection: authorization, token lifecycle, and
// the calendar list. Its zero configuration serves reads and disconnects
// and refuses the rest with ErrNotConfigured.
type Service struct {
	config   Config
	sealer   *Sealer
	api      API
	authURL  string
	db       Database
	queries  *store.Queries
	activity *activity.Emitter
	logger   *slog.Logger
	now      func() time.Time

	mu     sync.Mutex
	cached accessToken

	// calendarAPI is Google's Calendar API address; syncer, when set
	// (NewSyncer), syncs the calendars chosen to sync.
	calendarAPI string
	syncer      *Syncer
}

// NewService connects as config through endpoints, storing in db. A nil
// emitter records no activity.
func NewService(config Config, db Database, endpoints Endpoints, emitter *activity.Emitter, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	service := &Service{
		config: config, api: NewClient(config, endpoints, nil), authURL: endpoints.AuthURL, calendarAPI: endpoints.CalendarAPI,
		db: db, queries: store.New(db), activity: emitter, logger: logger, now: time.Now,
	}
	if config.Configured() {
		sealer, err := NewSealer(config.TokenKey)
		if err != nil {
			logger.Warn("Google Calendar is not configured", "error", err)
			service.config = Config{}
		} else {
			service.sealer = sealer
		}
	}
	return service
}

// WithClock replaces the clock (tests).
func (s *Service) WithClock(now func() time.Time) *Service {
	s.now = now
	return s
}

// Configured reports whether Google can be connected.
func (s *Service) Configured() bool {
	return s != nil && s.config.Configured() && s.sealer != nil
}

// Connection is the stored connection; found is false when there is none.
func (s *Service) Connection(ctx context.Context) (store.GoogleConnection, bool, error) {
	row, err := s.queries.GetGoogleConnection(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.GoogleConnection{}, false, nil
	}
	if err != nil {
		return store.GoogleConnection{}, false, err
	}
	return row, true, nil
}

// Authorize starts an authorization: it stores a single-use state with its
// PKCE verifier for StateLifetime and returns Google's consent page URL.
func (s *Service) Authorize(ctx context.Context) (string, error) {
	if !s.Configured() {
		return "", ErrNotConfigured
	}
	now := s.now()
	if err := s.queries.DeleteExpiredGoogleOAuthStates(ctx, timestamptz(now)); err != nil {
		return "", err
	}
	state, err := NewState()
	if err != nil {
		return "", err
	}
	verifier, err := NewVerifier()
	if err != nil {
		return "", err
	}
	if err := s.queries.CreateGoogleOAuthState(ctx, store.CreateGoogleOAuthStateParams{
		StateHash: HashState(state), CodeVerifier: verifier, ExpiresAt: timestamptz(now.Add(StateLifetime)),
	}); err != nil {
		return "", err
	}
	// A reconnect preselects the account already connected.
	hint := ""
	if existing, found, err := s.Connection(ctx); err == nil && found {
		hint = existing.AccountEmail
	}
	return s.config.AuthURL(s.authURL, state, Challenge(verifier), hint), nil
}

// Connect completes an authorization with the code and state Google
// returned: it checks the state, exchanges the code, identifies the account
// from its calendar list, and stores the encrypted refresh token and the
// list. Connecting another account replaces the connection.
func (s *Service) Connect(ctx context.Context, code, state string) (store.GoogleConnection, error) {
	if !s.Configured() {
		return store.GoogleConnection{}, ErrNotConfigured
	}
	pending, err := s.queries.ConsumeGoogleOAuthState(ctx, HashState(state))
	if errors.Is(err, pgx.ErrNoRows) {
		return store.GoogleConnection{}, ErrInvalidState
	}
	if err != nil {
		return store.GoogleConnection{}, err
	}
	if !pending.ExpiresAt.Time.After(s.now()) {
		return store.GoogleConnection{}, ErrInvalidState
	}
	started := s.now()
	token, err := s.api.Exchange(ctx, code, pending.CodeVerifier)
	if IsInvalidGrant(err) {
		// The code expired, was used, or is not this client's.
		return store.GoogleConnection{}, fmt.Errorf("%w: %w", ErrCodeRejected, err)
	}
	if err != nil {
		return store.GoogleConnection{}, err // such as invalid_client: the setup is wrong
	}
	scopes := token.Scopes
	if len(scopes) == 0 {
		scopes = Scopes
	}
	switch {
	case token.RefreshToken == "":
		s.revoke(ctx, token.AccessToken)
		return store.GoogleConnection{}, ErrNoRefreshToken
	case !slices.Contains(scopes, ScopeCalendarList):
		s.revoke(ctx, token.RefreshToken)
		return store.GoogleConnection{}, ErrScopeMissing
	}
	entries, err := s.api.CalendarList(ctx, token.AccessToken)
	if err != nil {
		return store.GoogleConnection{}, err
	}
	email := primaryID(entries)
	if email == "" {
		return store.GoogleConnection{}, ErrNoPrimary
	}
	ciphertext, nonce, err := s.sealer.Seal([]byte(token.RefreshToken))
	if err != nil {
		return store.GoogleConnection{}, err
	}
	expiresAt := started.Add(token.ExpiresIn)

	s.mu.Lock()
	var row store.GoogleConnection
	var replaced, unlisted []removedSource
	err = s.inTx(ctx, func(q *store.Queries) error {
		existing, err := q.GetGoogleConnection(ctx)
		found := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if found && !strings.EqualFold(existing.AccountEmail, email) {
			// Another account: its calendar choices (and the events synced
			// from its calendars) do not carry over.
			if replaced, err = linkedSources(ctx, q, existing.ID, nil); err != nil {
				return err
			}
			if _, err := q.DeleteGoogleConnection(ctx, existing.ID); err != nil {
				return err
			}
			found = false
		}
		if found {
			row, err = q.ReconnectGoogleConnection(ctx, store.ReconnectGoogleConnectionParams{
				Scopes: scopes, RefreshTokenCiphertext: ciphertext, RefreshTokenNonce: nonce,
				AccessTokenExpiresAt: timestamptz(expiresAt), ID: existing.ID,
			})
		} else {
			row, err = q.CreateGoogleConnection(ctx, store.CreateGoogleConnectionParams{
				AccountEmail: email, Scopes: scopes, RefreshTokenCiphertext: ciphertext, RefreshTokenNonce: nonce,
				AccessTokenExpiresAt: timestamptz(expiresAt),
			})
		}
		if err != nil {
			return err
		}
		if unlisted, err = syncCalendars(ctx, q, row.ID, entries); err != nil {
			return err
		}
		// A reconnect resumes syncing right away.
		return q.MarkGoogleCalendarsDue(ctx, store.MarkGoogleCalendarsDueParams{ConnectionID: row.ID, Now: timestamptz(s.now())})
	})
	if err == nil {
		s.cached = accessToken{connection: row.ID, value: token.AccessToken, expiresAt: expiresAt}
	}
	s.mu.Unlock()
	if err != nil {
		return store.GoogleConnection{}, err
	}
	s.recordConnected(ctx, row, len(entries))
	s.recordSourcesRemoved(ctx, replaced, removedReplaced)
	s.recordSourcesRemoved(ctx, unlisted, removedUnlisted)
	s.syncer.Wake()
	return row, nil
}

// Disconnect revokes the authorization at Google (best effort) and deletes
// the connection with its calendar list. It reports whether there was one.
// Without a configuration the token cannot be decrypted, so it is only
// forgotten; the owner removes Life OS in their Google Account.
func (s *Service) Disconnect(ctx context.Context) (bool, error) {
	row, revoked, removed, found, err := s.disconnect(ctx)
	if err != nil || !found {
		return false, err
	}
	s.recordDisconnected(ctx, row, revoked)
	s.recordSourcesRemoved(ctx, removed, removedDisconnected)
	return true, nil
}

// disconnect deletes the connection; its calendars, their sources, and the
// events synced from them go with it (ON DELETE CASCADE). removed are those
// sources, as they were.
func (s *Service) disconnect(ctx context.Context) (row store.GoogleConnection, revoked bool, removed []removedSource, found bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, found, err = s.Connection(ctx)
	if err != nil || !found {
		return row, false, nil, false, err
	}
	if removed, err = linkedSources(ctx, s.queries, row.ID, nil); err != nil {
		return row, false, nil, false, err
	}
	if s.Configured() {
		refresh, openErr := s.sealer.Open(row.RefreshTokenCiphertext, row.RefreshTokenNonce)
		if openErr == nil {
			revoked = s.revoke(ctx, string(refresh))
		} else {
			s.logger.Warn("Google token not revoked: it cannot be decrypted", "error", openErr)
		}
	}
	if _, err = s.queries.DeleteGoogleConnection(ctx, row.ID); err != nil {
		return row, false, nil, false, err
	}
	s.cached = accessToken{}
	return row, revoked, removed, true, nil
}

// revoke asks Google to withdraw the authorization of token and reports
// whether it did; a failure is logged (without the token) and ignored.
func (s *Service) revoke(ctx context.Context, token string) bool {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := s.api.Revoke(ctx, token); err != nil {
		s.logger.Warn("Google authorization not revoked", "error", err)
		return false
	}
	return true
}

// AccessToken returns a usable access token, refreshing it from the stored
// refresh token when it is missing or about to expire. A refresh token that
// Google refuses (invalid_grant) marks the connection expired: ErrExpired
// until the owner reconnects.
func (s *Service) AccessToken(ctx context.Context) (string, error) {
	if !s.Configured() {
		return "", ErrNotConfigured
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	row, found, err := s.Connection(ctx)
	if err != nil {
		return "", err
	}
	if !found {
		return "", ErrNotConnected
	}
	if row.Status == StatusExpired {
		return "", ErrExpired
	}
	now := s.now()
	if s.cached.usable(row.ID, now) {
		return s.cached.value, nil
	}
	refresh, err := s.sealer.Open(row.RefreshTokenCiphertext, row.RefreshTokenNonce)
	if err != nil {
		s.setStatus(ctx, row, StatusError, "The stored Google token cannot be decrypted with LIFE_OS_GOOGLE_TOKEN_KEY (was the key changed?); reconnect Google in Calendar.")
		return "", err
	}
	token, err := s.api.Refresh(ctx, string(refresh))
	var oauth *OAuthError
	switch {
	case IsInvalidGrant(err):
		s.setStatus(ctx, row, StatusExpired, fmt.Sprintf("%s: reconnect Google in Calendar.", err.Error()))
		return "", fmt.Errorf("%w: %w", ErrExpired, err)
	case errors.As(err, &oauth):
		s.setStatus(ctx, row, StatusError, err.Error()+".")
		return "", err
	case err != nil:
		return "", err // unreachable or failing: the status stays
	}
	expiresAt := now.Add(token.ExpiresIn)
	params := store.RecordGoogleTokenRefreshParams{AccessTokenExpiresAt: timestamptz(expiresAt), ID: row.ID}
	if token.RefreshToken != "" && token.RefreshToken != string(refresh) {
		// Google rotated the refresh token: keep the new one.
		if params.RefreshTokenCiphertext, params.RefreshTokenNonce, err = s.sealer.Seal([]byte(token.RefreshToken)); err != nil {
			return "", err
		}
	}
	if _, err := s.queries.RecordGoogleTokenRefresh(ctx, params); err != nil {
		return "", err
	}
	s.cached = accessToken{connection: row.ID, value: token.AccessToken, expiresAt: expiresAt}
	return token.AccessToken, nil
}

// forget drops the access token in memory, so the next use refreshes it.
func (s *Service) forget() {
	s.mu.Lock()
	s.cached = accessToken{}
	s.mu.Unlock()
}

// setStatus records why the connection is unusable; turning expired is
// recorded in Activity.
func (s *Service) setStatus(ctx context.Context, row store.GoogleConnection, status, message string) {
	s.cached = accessToken{}
	if _, err := s.queries.SetGoogleConnectionStatus(ctx, store.SetGoogleConnectionStatusParams{
		Status: status, LastError: &message, ID: row.ID,
	}); err != nil {
		s.logger.Warn("Google connection status not recorded", "status", status, "error", err)
		return
	}
	if status == StatusExpired && row.Status != StatusExpired {
		s.recordExpired(ctx, row, message)
	}
}

// Calendars is the stored calendar list; empty without a connection.
func (s *Service) Calendars(ctx context.Context) ([]store.GoogleCalendar, error) {
	row, found, err := s.Connection(ctx)
	if err != nil || !found {
		return []store.GoogleCalendar{}, err
	}
	return s.queries.ListGoogleCalendars(ctx, row.ID)
}

// RefreshCalendars reads the calendar list from Google again: new calendars
// are added (a new primary one with sync on, others off), known ones keep
// the owner's choice, and calendars no longer listed are dropped.
func (s *Service) RefreshCalendars(ctx context.Context) ([]store.GoogleCalendar, error) {
	entries, err := s.calendarList(ctx)
	if err != nil {
		return nil, err
	}
	if primaryID(entries) == "" {
		// Every account has a primary calendar: a list without one is a
		// fault at Google, and applying it would drop every calendar and
		// the events synced from them.
		return nil, ErrNoPrimary
	}
	var calendars []store.GoogleCalendar
	var unlisted []removedSource
	err = s.inTx(ctx, func(q *store.Queries) error {
		row, err := q.GetGoogleConnection(ctx)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotConnected // disconnected meanwhile
		}
		if err != nil {
			return err
		}
		if unlisted, err = syncCalendars(ctx, q, row.ID, entries); err != nil {
			return err
		}
		calendars, err = q.ListGoogleCalendars(ctx, row.ID)
		return err
	})
	if err == nil {
		s.recordSourcesRemoved(ctx, unlisted, removedUnlisted)
	}
	return calendars, err
}

// calendarList reads the list with a usable access token, refreshing it
// once if Google no longer accepts it.
func (s *Service) calendarList(ctx context.Context) ([]CalendarEntry, error) {
	for attempt := 0; ; attempt++ {
		token, err := s.AccessToken(ctx)
		if err != nil {
			return nil, err
		}
		entries, err := s.api.CalendarList(ctx, token)
		if attempt == 0 && isUnauthorized(err) {
			s.forget()
			continue
		}
		return entries, err
	}
}

// SetCalendarSync records whether a calendar syncs. Switching it on makes
// it due for a full sync now. Switching it off removes what was synced from
// it: its source and the source's events (they stay in Google), and the
// stored Google events, recorded in Activity.
func (s *Service) SetCalendarSync(ctx context.Context, id googleuuid.UUID, enabled bool) (store.GoogleCalendar, error) {
	var row store.GoogleCalendar
	var removed []removedSource
	switched := false
	err := s.inTx(ctx, func(q *store.Queries) error {
		current, err := q.LockGoogleCalendar(ctx, pgtype.UUID{Bytes: id, Valid: true})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrCalendarNotFound
		}
		if err != nil {
			return err
		}
		switched = current.SyncEnabled != enabled
		if row, err = q.SetGoogleCalendarSync(ctx, store.SetGoogleCalendarSyncParams{SyncEnabled: enabled, ID: current.ID, Now: timestamptz(s.now())}); err != nil {
			return err
		}
		if enabled || !switched {
			return nil
		}
		if removed, err = linkedSources(ctx, q, current.ConnectionID, []pgtype.UUID{current.ID}); err != nil {
			return err
		}
		for _, source := range removed {
			if err := q.DeleteGoogleSource(ctx, source.id); err != nil {
				return err
			}
		}
		return q.DeleteGoogleEvents(ctx, current.ID)
	})
	if err != nil {
		return store.GoogleCalendar{}, err
	}
	s.recordSourcesRemoved(ctx, removed, removedSyncOff)
	if enabled && switched {
		s.syncer.Wake()
	}
	return row, nil
}

// SyncCalendar syncs a calendar now (full: every event listed again) and
// returns it with its sync state.
func (s *Service) SyncCalendar(ctx context.Context, id googleuuid.UUID, full bool) (store.GoogleCalendar, error) {
	if !s.Configured() {
		return store.GoogleCalendar{}, ErrNotConfigured
	}
	if s.syncer == nil {
		return store.GoogleCalendar{}, errors.New("Google Calendar sync is not running")
	}
	connection, found, err := s.Connection(ctx)
	switch {
	case err != nil:
		return store.GoogleCalendar{}, err
	case !found:
		return store.GoogleCalendar{}, ErrNotConnected
	case connection.Status == StatusExpired:
		return store.GoogleCalendar{}, ErrExpired
	}
	return s.syncer.Sync(ctx, id, SyncTrigger{Manual: true, Full: full, Trace: activity.TraceFrom(ctx)})
}

// Sources are the ids of the synced calendars' sources, by calendar.
func (s *Service) Sources(ctx context.Context) (map[[16]byte]pgtype.UUID, error) {
	out := map[[16]byte]pgtype.UUID{}
	row, found, err := s.Connection(ctx)
	if err != nil || !found {
		return out, err
	}
	rows, err := s.queries.ListGoogleSourceIDs(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	for _, source := range rows {
		out[source.GoogleCalendarID.Bytes] = source.ID
	}
	return out, nil
}

func (s *Service) inTx(ctx context.Context, work func(*store.Queries) error) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := work(s.queries.WithTx(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// syncCalendars makes the stored list match Google's. Calendars Google no
// longer lists are dropped with their sources and events; removed are those
// sources, as they were.
func syncCalendars(ctx context.Context, q *store.Queries, connection pgtype.UUID, entries []CalendarEntry) ([]removedSource, error) {
	listed := make([]string, 0, len(entries))
	for _, entry := range entries {
		var color *string
		if entry.Color != "" {
			color = &entry.Color
		}
		if err := q.UpsertGoogleCalendar(ctx, store.UpsertGoogleCalendarParams{
			ConnectionID: connection, GoogleCalendarID: entry.ID, Summary: entry.Summary, Color: color,
			AccessRole: accessRole(entry.AccessRole), IsPrimary: entry.Primary, InitialSyncEnabled: entry.Primary,
		}); err != nil {
			return nil, err
		}
		listed = append(listed, entry.ID)
	}
	gone, err := q.ListGoogleCalendarsNotListed(ctx, store.ListGoogleCalendarsNotListedParams{ConnectionID: connection, Listed: listed})
	if err != nil || len(gone) == 0 {
		return nil, err
	}
	removed, err := linkedSources(ctx, q, connection, gone)
	if err != nil {
		return nil, err
	}
	_, err = q.DeleteGoogleCalendarsNotListed(ctx, store.DeleteGoogleCalendarsNotListedParams{ConnectionID: connection, Listed: listed})
	return removed, err
}

// removedSource is a synced calendar's source that went away, with the
// number of events it held.
type removedSource struct {
	id     pgtype.UUID
	name   string
	events int
}

// linkedSources are the sources of the connection's synced calendars
// (narrowed to calendars, when given).
func linkedSources(ctx context.Context, q *store.Queries, connection pgtype.UUID, calendars []pgtype.UUID) ([]removedSource, error) {
	rows, err := q.ListGoogleSources(ctx, store.ListGoogleSourcesParams{ConnectionID: connection, CalendarIds: calendars})
	if err != nil {
		return nil, err
	}
	out := make([]removedSource, 0, len(rows))
	for _, row := range rows {
		out = append(out, removedSource{id: row.ID, name: row.Name, events: int(row.Events)})
	}
	return out, nil
}

// accessRole is Google's role, or reader for one Calendar does not know.
func accessRole(role string) string {
	switch role {
	case "owner", "writer", "reader", "freeBusyReader":
		return role
	}
	return "reader"
}

// primaryID is the primary calendar's id: the account's email address.
func primaryID(entries []CalendarEntry) string {
	for _, entry := range entries {
		if entry.Primary {
			return entry.ID
		}
	}
	return ""
}

func timestamptz(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}

// connectionSubject is the Activity subject of the connection.
func connectionSubject(row store.GoogleConnection) activity.Subject {
	return activity.Subject{Type: "google_connection", ID: googleuuid.UUID(row.ID.Bytes).String()}
}

func (s *Service) recordConnected(ctx context.Context, row store.GoogleConnection, calendars int) {
	trace := activity.TraceFrom(ctx)
	s.activity.Emit(ctx, activity.Event{
		ID:            activity.EventID(EventConnected, googleuuid.UUID(row.ID.Bytes).String(), row.ConnectedAt.Time.UTC().Format(time.RFC3339Nano)),
		Type:          EventConnected,
		Subject:       connectionSubject(row),
		CorrelationID: trace.CorrelationID,
		CausationID:   trace.CausationID,
		Actor:         trace.Actor,
		Outcome:       activity.Succeeded,
		Summary:       activity.Preview(fmt.Sprintf("Google Calendar connected: %s (%d %s)", row.AccountEmail, calendars, plural(calendars, "calendar", "calendars")), 500),
		Data:          map[string]any{"accountEmail": row.AccountEmail, "calendars": calendars, "scopes": row.Scopes, "missingScopes": MissingScopes(row.Scopes)},
	})
}

func (s *Service) recordDisconnected(ctx context.Context, row store.GoogleConnection, revoked bool) {
	trace := activity.TraceFrom(ctx)
	summary := "Google Calendar disconnected: " + row.AccountEmail
	if !revoked {
		summary += " (not revoked at Google; remove Life OS in your Google Account's third-party access)"
	}
	s.activity.Emit(ctx, activity.Event{
		ID:            activity.EventID(EventDisconnected, googleuuid.UUID(row.ID.Bytes).String()),
		Type:          EventDisconnected,
		Subject:       connectionSubject(row),
		CorrelationID: trace.CorrelationID,
		CausationID:   trace.CausationID,
		Actor:         trace.Actor,
		Outcome:       activity.Succeeded,
		Summary:       activity.Preview(summary, 500),
		Data:          map[string]any{"accountEmail": row.AccountEmail, "revokedAtGoogle": revoked},
	})
}

// recordExpired is Calendar's own finding, whoever's request found it.
func (s *Service) recordExpired(ctx context.Context, row store.GoogleConnection, message string) {
	trace := activity.TraceFrom(ctx)
	s.activity.Emit(ctx, activity.Event{
		ID:            activity.EventID(EventExpired, googleuuid.UUID(row.ID.Bytes).String(), row.ConnectedAt.Time.UTC().Format(time.RFC3339Nano)),
		Type:          EventExpired,
		Subject:       connectionSubject(row),
		CorrelationID: trace.CorrelationID,
		CausationID:   trace.CausationID,
		Outcome:       activity.Failed,
		Summary:       activity.Preview(fmt.Sprintf("Google Calendar access for %s expired or was revoked; reconnect Google in Calendar", row.AccountEmail), 500),
		Data:          map[string]any{"accountEmail": row.AccountEmail, "error": message},
	})
}

// Why a synced calendar's source was removed.
const (
	removedSyncOff      = "sync_disabled"
	removedDisconnected = "disconnected"
	removedUnlisted     = "calendar_unlisted"
	removedReplaced     = "account_replaced"
)

// recordSourcesRemoved records, per source, that a synced calendar's events
// left Life OS (calendar.source.removed, attributed to the request's actor).
func (s *Service) recordSourcesRemoved(ctx context.Context, removed []removedSource, reason string) {
	trace := activity.TraceFrom(ctx)
	for _, source := range removed {
		id := googleuuid.UUID(source.id.Bytes).String()
		events := fmt.Sprintf("%d %s", source.events, plural(source.events, "event", "events"))
		var summary string
		switch reason {
		case removedSyncOff:
			summary = fmt.Sprintf("%s no longer syncs from Google: %s removed from Life OS", source.name, events)
		case removedDisconnected:
			summary = fmt.Sprintf("Google Calendar disconnected: %s's %s removed from Life OS", source.name, events)
		case removedUnlisted:
			summary = fmt.Sprintf("Google no longer lists %s: its %s removed from Life OS", source.name, events)
		default:
			summary = fmt.Sprintf("Another Google account was connected: %s's %s removed from Life OS", source.name, events)
		}
		s.activity.Emit(ctx, activity.Event{
			ID:            activity.EventID(EventSourceRemoved, id),
			Type:          EventSourceRemoved,
			Subject:       activity.Subject{Type: "calendar_source", ID: id},
			CorrelationID: trace.CorrelationID,
			CausationID:   trace.CausationID,
			Actor:         trace.Actor,
			Outcome:       activity.Succeeded,
			Summary:       activity.Preview(summary, 500),
			Data:          map[string]any{"sourceId": id, "sourceName": source.name, "sourceKind": "google", "reason": reason, "removedEvents": source.events},
		})
	}
}
