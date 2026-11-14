package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/GAMch1k/life-os/pkg/diagnostics"
	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/pkg/identity"
	"github.com/GAMch1k/life-os/services/activity/internal/api/generated"
	store "github.com/GAMch1k/life-os/services/activity/internal/repository/generated"
)

func (f *fakeStore) RecordDiagnosticEvent(_ context.Context, p store.RecordDiagnosticEventParams) error {
	f.diagnostics = append(f.diagnostics, store.DiagnosticEvent{
		ID: int64(len(f.diagnostics) + 1), Service: p.Service, Code: p.Code, Route: p.Route, Status: p.Status,
		SubjectID: p.SubjectID, RequestID: p.RequestID, Count: p.Count, DurationMs: p.DurationMs,
		FirstAt: p.FirstAt, LastAt: p.LastAt, RecordedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	})
	return nil
}

func (f *fakeStore) ListDiagnosticEvents(_ context.Context, p store.ListDiagnosticEventsParams) ([]store.DiagnosticEvent, error) {
	f.listedDiagnostics = p
	rows := slices.Clone(f.diagnostics)
	slices.Reverse(rows)
	return rows[:min(len(rows), int(p.PageLimit))], nil
}

func (f *fakeStore) SummarizeDiagnosticEvents(_ context.Context, since pgtype.Timestamptz) ([]store.SummarizeDiagnosticEventsRow, error) {
	totals := map[[2]string]*store.SummarizeDiagnosticEventsRow{}
	var rows []store.SummarizeDiagnosticEventsRow
	for _, event := range f.diagnostics {
		if event.LastAt.Time.Before(since.Time) {
			continue
		}
		key := [2]string{event.Service, event.Code}
		if totals[key] == nil {
			totals[key] = &store.SummarizeDiagnosticEventsRow{Service: event.Service, Code: event.Code, LastAt: event.LastAt}
		}
		totals[key].Total += int64(event.Count)
	}
	for _, row := range totals {
		rows = append(rows, *row)
	}
	return rows, nil
}

func (f *fakeStore) DeleteDiagnosticEventsBefore(_ context.Context, before pgtype.Timestamptz) (int64, error) {
	f.prunedBefore = append(f.prunedBefore, before.Time)
	kept := f.diagnostics[:0]
	for _, event := range f.diagnostics {
		if !event.LastAt.Time.Before(before.Time) {
			kept = append(kept, event)
		}
	}
	removed := len(f.diagnostics) - len(kept)
	f.diagnostics = kept
	return int64(removed), nil
}

// signer makes identity tokens as the Dashboard's auth check does.
type signer struct {
	private ed25519.PrivateKey
	keys    *identity.KeySet
}

func newSigner(t *testing.T) signer {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	jwks, _ := json.Marshal(map[string]any{"keys": []any{map[string]string{
		"kty": "OKP", "crv": "Ed25519", "kid": "auth-test", "x": base64.RawURLEncoding.EncodeToString(public),
	}}})
	keys, err := identity.ParseKeySet(string(jwks))
	if err != nil {
		t.Fatal(err)
	}
	return signer{private: private, keys: keys}
}

func (s signer) token(role, via string) string { return s.tokenMFA(role, via, false) }

// twoFactor is a browser session that signed in with a second factor (the
// mfa claim, which admin routes need since P21-06).
func (s signer) twoFactor(role string) string { return s.tokenMFA(role, "session", true) }

func (s signer) tokenMFA(role, via string, mfa bool) string {
	encode := func(v any) string {
		raw, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	input := encode(map[string]string{"alg": "EdDSA", "typ": "lifeos-id+jwt", "kid": "auth-test"}) + "." + encode(map[string]any{
		"iss": "lifeos-auth", "aud": "lifeos-services", "sub": "00000000-0000-4000-8000-000000000001",
		"sid": via, "via": via, "role": role, "mfa": mfa, "exp": time.Now().Add(time.Minute).Unix(),
	})
	return input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(s.private, []byte(input)))
}

func diagnosticsApp(t *testing.T, fake *fakeStore) (*httpserver.App, signer) {
	t.Helper()
	keys := newSigner(t)
	app := httpserver.New("activity", httpserver.WithLogger(httpserver.NewJSONLogger(io.Discard, nil)),
		httpserver.WithTrustedKeys(keys.keys), httpserver.WithDiagnostics(nil))
	generated.RegisterHandlers(app, NewHandler(fake))
	return app, keys
}

func call(t *testing.T, app *httpserver.App, method, path, token, body string) (int, string) {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("content-type", "application/json")
	if token != "" {
		request.Header.Set(identity.Header, token)
	}
	response, err := app.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(response.Body)
	return response.StatusCode, string(raw)
}

const validDiagnostics = `{"events":[{"service":"tasks","code":"service_unavailable","route":"/v1/tasks/:taskId","status":503,"requestId":"5f1f3b5e-7a2c-4b6d-9e8f-0a1b2c3d4e5f","count":3,"durationMs":40,"firstAt":"2026-11-14T12:00:00Z","lastAt":"2026-11-14T12:00:30Z"}]}`

// A scenario with made-up content (a title, an amount, a note, a search)
// planted in every field a diagnostic has: the ones that could carry it are
// refused, and nothing of it is stored or read back, as Banking's
// TestSecretsNeverLeave does for its secrets.
func TestDiagnosticsNeverCarryContent(t *testing.T) {
	fake := &fakeStore{events: map[[16]byte]store.ActivityEvent{}}
	app, _ := diagnosticsApp(t, fake)
	markers := []string{"Zebra-Title-7Q", "4711.93", "Secret Note Kx", "search-term-Pz"}

	if status, body := call(t, app, "POST", "/v1/diagnostic-events", "", validDiagnostics); status != 202 || !strings.Contains(body, `"recorded":1`) {
		t.Fatalf("a valid batch: %d %s", status, body)
	}
	planted := map[string]string{
		"a title as the service": strings.Replace(validDiagnostics, `"service":"tasks"`, `"service":"Zebra-Title-7Q"`, 1),
		"a code off the list":    strings.Replace(validDiagnostics, `"code":"service_unavailable"`, `"code":"Zebra-Title-7Q"`, 1),
		"a note in the route":    strings.Replace(validDiagnostics, `"/v1/tasks/:taskId"`, `"/v1/tasks/Secret Note Kx"`, 1),
		"an amount as the route": strings.Replace(validDiagnostics, `"/v1/tasks/:taskId"`, `"4711.93"`, 1),
		"a search as request id": strings.Replace(validDiagnostics, `"5f1f3b5e-7a2c-4b6d-9e8f-0a1b2c3d4e5f"`, `"search-term-Pz"`, 1),
		"a search as subject id": strings.Replace(validDiagnostics, `"count":3`, `"subjectId":"search-term-Pz","count":3`, 1),
	}
	for name, body := range planted {
		if status, response := call(t, app, "POST", "/v1/diagnostic-events", "", body); status != 400 {
			t.Errorf("%s: %d %s", name, status, response)
		}
	}
	// A field the contract does not have is never stored.
	extra := strings.Replace(validDiagnostics, `"count":3`, `"message":"Secret Note Kx","title":"Zebra-Title-7Q","amount":4711.93,"count":3`, 1)
	if status, body := call(t, app, "POST", "/v1/diagnostic-events", "", extra); status != 202 {
		t.Fatalf("extra fields: %d %s", status, body)
	}

	_, list := call(t, app, "GET", "/v1/diagnostic-events", "", "")
	_, summary := call(t, app, "GET", "/v1/diagnostic-events/summary?hours=2160", "", "")
	stored, _ := json.Marshal(fake.diagnostics)
	for _, marker := range markers {
		for name, text := range map[string]string{"stored": string(stored), "list": list, "summary": summary} {
			if strings.Contains(text, marker) {
				t.Errorf("%q reached the %s diagnostics: %s", marker, name, text)
			}
		}
	}
	if len(fake.diagnostics) != 2 || !strings.Contains(list, `"route":"/v1/tasks/:taskId"`) {
		t.Fatalf("stored %d, list %s", len(fake.diagnostics), list)
	}
}

func TestDiagnosticsAreForSignedInAdmins(t *testing.T) {
	fake := &fakeStore{events: map[[16]byte]store.ActivityEvent{}}
	app, keys := diagnosticsApp(t, fake)
	for _, tc := range []struct {
		name, token string
		status      int
		code        string
	}{
		{"an internal caller (the Dashboard after its own check)", "", 200, ""},
		{"an admin who signed in with two-factor", keys.twoFactor("admin"), 200, ""},
		{"the owner who signed in with two-factor", keys.twoFactor("owner"), 200, ""},
		{"an admin who signed in without two-factor", keys.token("admin", "session"), 403, "second_factor_required"},
		{"the owner's paired phone", keys.token("owner", "device"), 403, "admin_only"},
		{"the owner's API token", keys.token("owner", "token"), 403, "admin_only"},
		{"a member", keys.twoFactor("member"), 403, "admin_only"},
		{"legacy access", keys.token("owner", "legacy"), 401, "sign_in_required"},
	} {
		for _, path := range []string{"/v1/diagnostic-events", "/v1/diagnostic-events/summary"} {
			status, body := call(t, app, "GET", path, tc.token, "")
			if status != tc.status || (tc.code != "" && !strings.Contains(body, `"code":"`+tc.code+`"`)) {
				t.Errorf("%s, %s: %d %s", tc.name, path, status, body)
			}
		}
	}
	// A person never records diagnostics, not even an admin.
	if status, body := call(t, app, "POST", "/v1/diagnostic-events", keys.token("owner", "session"), validDiagnostics); status != 403 {
		t.Errorf("a person recording: %d %s", status, body)
	}
}

func TestDiagnosticsArePrunedAfter90Days(t *testing.T) {
	fake := &fakeStore{events: map[[16]byte]store.ActivityEvent{}}
	handler := NewHandler(fake)
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	handler.now = func() time.Time { return now }
	app := httpserver.New("activity", httpserver.WithLogger(httpserver.NewJSONLogger(io.Discard, nil)), httpserver.WithDiagnostics(nil))
	generated.RegisterHandlers(app, handler)
	old := strings.NewReplacer("2026-11-14T12:00:00Z", "2026-08-01T12:00:00Z", "2026-11-14T12:00:30Z", "2026-08-01T12:00:30Z").Replace(validDiagnostics)
	fake.diagnostics = nil
	_ = fake.RecordDiagnosticEvent(context.Background(), mustParams(t, old))
	if status, body := call(t, app, "POST", "/v1/diagnostic-events", "", validDiagnostics); status != 202 {
		t.Fatalf("%d %s", status, body)
	}
	if len(fake.prunedBefore) != 1 || !fake.prunedBefore[0].Equal(now.Add(-DiagnosticsRetention)) || len(fake.diagnostics) != 1 {
		t.Fatalf("pruned before %v, kept %d", fake.prunedBefore, len(fake.diagnostics))
	}
	// At most once an hour.
	call(t, app, "POST", "/v1/diagnostic-events", "", validDiagnostics)
	if len(fake.prunedBefore) != 1 {
		t.Fatalf("pruned %d times within the hour", len(fake.prunedBefore))
	}
}

// Old diagnostics also go on a timer, while no service sends any.
func TestDiagnosticsArePrunedDailyWithoutNewOnes(t *testing.T) {
	fake := &fakeStore{events: map[[16]byte]store.ActivityEvent{}}
	signal := &pruneSignal{fakeStore: fake, pruned: make(chan struct{}, 1)}
	handler := NewHandler(signal)
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	handler.now = func() time.Time { return now }
	old := strings.NewReplacer("2026-11-14T12:00:00Z", "2026-08-01T12:00:00Z", "2026-11-14T12:00:30Z", "2026-08-01T12:00:30Z").Replace(validDiagnostics)
	_ = fake.RecordDiagnosticEvent(context.Background(), mustParams(t, old))
	_ = fake.RecordDiagnosticEvent(context.Background(), mustParams(t, validDiagnostics))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		handler.RunDiagnosticsPruner(ctx, time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)))
		close(done)
	}()
	select {
	case <-signal.pruned:
	case <-time.After(5 * time.Second):
		t.Fatal("the pruner did not run at start")
	}
	cancel()
	<-done
	if !fake.prunedBefore[0].Equal(now.Add(-DiagnosticsRetention)) || len(fake.diagnostics) != 1 {
		t.Fatalf("pruned before %v, kept %d", fake.prunedBefore, len(fake.diagnostics))
	}
}

// pruneSignal says when the pruner ran, for a test in another goroutine.
type pruneSignal struct {
	*fakeStore
	pruned chan struct{}
}

func (p *pruneSignal) DeleteDiagnosticEventsBefore(ctx context.Context, before pgtype.Timestamptz) (int64, error) {
	removed, err := p.fakeStore.DeleteDiagnosticEventsBefore(ctx, before)
	select {
	case p.pruned <- struct{}{}:
	default:
	}
	return removed, err
}

func mustParams(t *testing.T, body string) store.RecordDiagnosticEventParams {
	t.Helper()
	var batch generated.DiagnosticBatch
	if err := json.Unmarshal([]byte(body), &batch); err != nil {
		t.Fatal(err)
	}
	params, err := diagnosticParams(batch.Events[0])
	if err != nil {
		t.Fatal(err)
	}
	return params
}

// The contract's codes are pkg/diagnostics' list, so a service never sends
// a code this service refuses.
func TestDiagnosticCodesMatchTheSharedList(t *testing.T) {
	var contract []string
	for _, code := range diagnostics.Codes {
		if !generated.DiagnosticCode(code).Valid() {
			t.Errorf("%s is in pkg/diagnostics but not in the contract", code)
		}
		contract = append(contract, string(code))
	}
	for _, code := range []generated.DiagnosticCode{generated.InternalError, generated.Panic, generated.NotReady, generated.ServerError} {
		if !slices.Contains(contract, string(code)) {
			t.Errorf("%s is in the contract but not in pkg/diagnostics", code)
		}
	}
	spec, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	_, schema, _ := strings.Cut(string(spec), "    DiagnosticCode:\n")
	_, enum, _ := strings.Cut(schema, "enum: [")
	enum, _, _ = strings.Cut(enum, "]")
	listed := strings.Split(enum, ", ")
	if !slices.Equal(listed, contract) {
		t.Errorf("the contract lists %v, pkg/diagnostics %v", listed, contract)
	}
}

func TestActorUserIDRoundTrips(t *testing.T) {
	app := newApp(&fakeStore{events: map[[16]byte]store.ActivityEvent{}})
	body := strings.Replace(validEvent, `"actor":{"kind":"user"}`, `"actor":{"kind":"dev","name":"claude-code"},"actorUserId":"00000000-0000-4000-8000-000000000001"`, 1)
	status, response := post(t, app, body)
	if status != 201 || !strings.Contains(response, `"actorUserId":"00000000-0000-4000-8000-000000000001"`) {
		t.Fatalf("%d %s", status, response)
	}
	status, response = post(t, app, strings.Replace(validEvent, "7b0b8c5e", "8b0b8c5e", 1))
	if status != 201 || strings.Contains(response, "actorUserId") {
		t.Fatalf("an event without a person: %d %s", status, response)
	}
}
