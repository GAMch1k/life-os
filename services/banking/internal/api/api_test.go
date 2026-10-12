package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/services/banking/internal/api"
	"github.com/GAMch1k/life-os/services/banking/internal/banking"
	"github.com/GAMch1k/life-os/services/banking/internal/testsupport"
)

var fiberTestConfig = fiber.TestConfig{Timeout: 20 * time.Second, FailOnTimeout: true}

func newApp(service *banking.Service) *httpserver.App {
	app := httpserver.New("banking", httpserver.WithLogger(httpserver.NewJSONLogger(io.Discard, nil)))
	api.Register(app, api.NewHandler(service, "Europe/Berlin"))
	return app
}

type response struct {
	status int
	header http.Header
	raw    string
	error  struct{ Code, Message string }
}

// owner sends a request like the Dashboard through the gateway: as the
// owner, with the owner's address and browser.
func send(t *testing.T, app *httpserver.App, method, target, body string, headers map[string]string) response {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("content-type", "application/json")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := app.Test(req, fiberTestConfig)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	result := response{status: resp.StatusCode, header: resp.Header, raw: string(raw)}
	var envelope struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	_ = json.Unmarshal(raw, &envelope)
	result.error = envelope.Error
	return result
}

var owner = map[string]string{activity.ActorHeader: "user", "X-Real-IP": "203.0.113.7", "User-Agent": "Mozilla/5.0 Firefox/140.0"}

func TestWritesRequireAnActor(t *testing.T) {
	app := newApp(banking.New(banking.Settings{}, "", nil, nil, nil, nil, banking.Options{}, nil))
	for _, write := range []struct{ method, target string }{
		{http.MethodPost, "/v1/connections/authorize"},
		{http.MethodPost, "/v1/connections"},
		{http.MethodPost, "/v1/connections/7f8d2b1e-1c1a-4b7e-9a51-2f0c6d1e9a10/sync"},
		{http.MethodDelete, "/v1/connections/7f8d2b1e-1c1a-4b7e-9a51-2f0c6d1e9a10"},
	} {
		for _, actor := range []string{"", "owner", "system:"} {
			got := send(t, app, write.method, write.target, `{}`, map[string]string{activity.ActorHeader: actor})
			if got.status != http.StatusBadRequest || got.error.Code != activity.ActorRequiredCode {
				t.Errorf("%s %s as %q = %d %+v", write.method, write.target, actor, got.status, got.error)
			}
		}
	}
}

func TestNotConfiguredIsReportedNotFailed(t *testing.T) {
	problem := "Enable Banking is not configured: LIFE_OS_BANKING_TOKEN_KEY is not set"
	pool := testsupport.New(t).Pool
	app := newApp(banking.New(banking.Settings{}, problem, pool, nil, nil, nil, banking.Options{}, nil))
	got := send(t, app, http.MethodGet, "/v1/connections", "", nil)
	var list struct {
		Configured   bool
		SetupProblem *string
		Items        []any
		Schedule     struct {
			Times            []string
			TimeZone         string
			UnattendedPerDay int
		}
	}
	if err := json.Unmarshal([]byte(got.raw), &list); err != nil || got.status != http.StatusOK || list.Configured ||
		list.SetupProblem == nil || *list.SetupProblem != problem || len(list.Items) != 0 ||
		strings.Join(list.Schedule.Times, ",") != "06:00,12:00,18:00,23:00" || list.Schedule.UnattendedPerDay != 4 {
		t.Fatalf("list = %d %s", got.status, got.raw)
	}
	if got := send(t, app, http.MethodPost, "/v1/connections/authorize", "", owner); got.status != http.StatusServiceUnavailable || got.error.Code != "banking_not_configured" {
		t.Fatalf("authorize = %d %+v", got.status, got.error)
	}
}

func TestTheConnectFlowOverHTTP(t *testing.T) {
	h := testsupport.New(t)
	app := newApp(h.Service)

	if got := send(t, app, http.MethodPost, "/v1/connections/authorize", `{"country":"DEU"}`, owner); got.status != http.StatusBadRequest {
		t.Fatalf("bad country = %d", got.status)
	}
	if got := send(t, app, http.MethodPost, "/v1/connections/authorize", `{"institution":"Sparkasse"}`, owner); got.status != http.StatusUnprocessableEntity || got.error.Code != "unknown_bank" {
		t.Fatalf("unknown bank = %d %+v", got.status, got.error)
	}
	got := send(t, app, http.MethodPost, "/v1/connections/authorize", `{"institution":"Deutsche Bank","country":"DE"}`, owner)
	var authorization struct{ URL string }
	if err := json.Unmarshal([]byte(got.raw), &authorization); err != nil || got.status != http.StatusOK || authorization.URL == "" {
		t.Fatalf("authorize = %d %s", got.status, got.raw)
	}
	page, _ := url.Parse(authorization.URL)
	state := page.Query().Get("state")
	code := h.Bank.Approve(state)

	if got := send(t, app, http.MethodPost, "/v1/connections", `{"code":"x"}`, owner); got.status != http.StatusBadRequest || got.error.Code != "bad_request" {
		t.Fatalf("no state = %d %+v", got.status, got.error)
	}
	if got := send(t, app, http.MethodPost, "/v1/connections", `{"code":"`+code+`","state":"nope"}`, owner); got.status != http.StatusBadRequest || got.error.Code != "invalid_state" {
		t.Fatalf("wrong state = %d %+v", got.status, got.error)
	}
	got = send(t, app, http.MethodPost, "/v1/connections", `{"code":"`+code+`","state":"`+state+`"}`, owner)
	var conn struct {
		ID                    string
		Status                string
		Accounts              []struct{ ID, Name, Currency, Kind, IbanMasked string }
		LastCounts            *struct{ Fetched int }
		ConsentValidUntil     *time.Time
		NextSyncAt            *time.Time
		UnattendedReadsToday  int
		ManualSyncAvailableAt *time.Time
		WaitingForFinance     bool
	}
	if err := json.Unmarshal([]byte(got.raw), &conn); err != nil || got.status != http.StatusOK {
		t.Fatalf("connect = %d %s", got.status, got.raw)
	}
	if conn.Status != "connected" || len(conn.Accounts) != 1 || conn.Accounts[0].Name != "Das Junge Konto" || conn.Accounts[0].IbanMasked != "DE89…3000" ||
		conn.Accounts[0].Kind != "bank" || conn.LastCounts == nil || conn.LastCounts.Fetched != 4 || conn.ConsentValidUntil == nil ||
		conn.NextSyncAt == nil || conn.UnattendedReadsToday != 0 || conn.WaitingForFinance {
		t.Fatalf("connection = %s", got.raw)
	}
	// The owner's address and browser made the first sync owner-present.
	for _, request := range h.Bank.Requests() {
		if strings.HasPrefix(request.Path, "/accounts/") && request.PSUIP != "203.0.113.7" {
			t.Fatalf("not owner-present: %+v", request)
		}
	}
	assertNoSecrets(t, h, got.raw)

	// Sync now: owner-present, then throttled with Retry-After.
	h.Advance(time.Minute)
	sync := "/v1/connections/" + conn.ID + "/sync"
	if got := send(t, app, http.MethodPost, sync, "", owner); got.status != http.StatusOK {
		t.Fatalf("sync = %d %s", got.status, got.raw)
	}
	got = send(t, app, http.MethodPost, sync, "", owner)
	if got.status != http.StatusTooManyRequests || got.error.Code != "sync_throttled" || got.header.Get("Retry-After") != "600" {
		t.Fatalf("throttled = %d %+v retry %q", got.status, got.error, got.header.Get("Retry-After"))
	}
	list := send(t, app, http.MethodGet, "/v1/connections", "", nil)
	if !strings.Contains(list.raw, `"manualSyncAvailableAt":"`) {
		t.Fatalf("list = %s", list.raw)
	}
	assertNoSecrets(t, h, list.raw)

	// A dev tool is never owner-present, even with the headers.
	h.Advance(11 * time.Minute)
	h.Bank.ResetRequests()
	dev := map[string]string{activity.ActorHeader: "dev:claude-code", "X-Real-IP": "203.0.113.7", "User-Agent": "curl/8"}
	if got := send(t, app, http.MethodPost, sync, "", dev); got.status != http.StatusOK {
		t.Fatalf("dev sync = %d %s", got.status, got.raw)
	}
	for _, request := range h.Bank.Requests() {
		if request.PSUIP != "" {
			t.Fatalf("a dev tool's sync claimed the owner was present: %+v", request)
		}
	}

	// A failing bank answers 502 with a plain message.
	h.Advance(11 * time.Minute)
	h.Bank.Fail("GET", "/accounts/", http.StatusServiceUnavailable, "ASPSP_TIMEOUT", 1)
	if got := send(t, app, http.MethodPost, sync, "", owner); got.status != http.StatusBadGateway || got.error.Code != "bank_unavailable" || !strings.Contains(got.error.Message, "Retrying automatically") {
		t.Fatalf("failing bank = %d %+v", got.status, got.error)
	}

	if got := send(t, app, http.MethodDelete, "/v1/connections/"+conn.ID, "", owner); got.status != http.StatusNoContent {
		t.Fatalf("disconnect = %d %s", got.status, got.raw)
	}
	if got := send(t, app, http.MethodPost, sync, "", owner); got.status != http.StatusConflict || got.error.Code != "connection_inactive" {
		t.Fatalf("sync after disconnect = %d %+v", got.status, got.error)
	}
	if got := send(t, app, http.MethodDelete, "/v1/connections/7f8d2b1e-1c1a-4b7e-9a51-2f0c6d1e9a10", "", owner); got.status != http.StatusNotFound {
		t.Fatalf("unknown = %d", got.status)
	}
	list = send(t, app, http.MethodGet, "/v1/connections", "", nil)
	if !strings.Contains(list.raw, `"status":"disconnected"`) || !strings.Contains(list.raw, `"nextSyncAt":null`) {
		t.Fatalf("list = %s", list.raw)
	}
}

func assertNoSecrets(t *testing.T, h *testsupport.Harness, body string) {
	t.Helper()
	for _, secret := range append(h.Bank.SessionIDs(), testsupport.IBAN, "0532013000", "uid", "aaaaaaaa-0000") {
		if strings.Contains(body, secret) {
			t.Errorf("response contains %q: %s", secret, body)
		}
	}
}

func TestConsentFailuresOverHTTP(t *testing.T) {
	h := testsupport.New(t)
	app := newApp(h.Service)

	if got := send(t, app, http.MethodPost, "/v1/connections/consent-failure", `{"error":"server_error"}`, nil); got.status != http.StatusBadRequest || got.error.Code != activity.ActorRequiredCode {
		t.Fatalf("no actor = %d %+v", got.status, got.error)
	}
	for _, body := range []string{`{}`, `{"error":"  "}`, `not json`, `{"error":"` + strings.Repeat("e", 101) + `"}`, `{"error":"x","errorDescription":"` + strings.Repeat("d", 2001) + `"}`} {
		if got := send(t, app, http.MethodPost, "/v1/connections/consent-failure", body, owner); got.status != http.StatusBadRequest || got.error.Code != "bad_request" {
			t.Fatalf("%s = %d %+v", body, got.status, got.error)
		}
	}
	list := send(t, app, http.MethodGet, "/v1/connections", "", nil)
	if !strings.Contains(list.raw, `"lastConsentFailure":null`) {
		t.Fatalf("list = %s", list.raw)
	}

	state := h.Authorize()
	// A code sent along is ignored and never stored.
	body := `{"state":"` + state + `","error":"server_error","errorDescription":"Internal error at the bank","code":"secret-code"}`
	if got := send(t, app, http.MethodPost, "/v1/connections/consent-failure", body, owner); got.status != http.StatusNoContent {
		t.Fatalf("record = %d %s", got.status, got.raw)
	}
	list = send(t, app, http.MethodGet, "/v1/connections", "", nil)
	var parsed struct {
		LastConsentFailure *struct {
			At           time.Time
			Error        string
			Description  *string
			Institution  *string
			StateMatched bool
		}
	}
	if err := json.Unmarshal([]byte(list.raw), &parsed); err != nil || parsed.LastConsentFailure == nil {
		t.Fatalf("list = %s", list.raw)
	}
	failure := parsed.LastConsentFailure
	if !failure.At.Equal(testsupport.Start) || failure.Error != "server_error" || failure.Description == nil || *failure.Description != "Internal error at the bank" ||
		failure.Institution == nil || *failure.Institution != "Deutsche Bank" || !failure.StateMatched {
		t.Fatalf("failure = %+v", failure)
	}
	if strings.Contains(list.raw, state) {
		t.Fatal("the state is listed")
	}
	var stored string
	if err := h.Pool.QueryRow(context.Background(), "SELECT row_to_json(f)::text FROM bank_consent_failures f").Scan(&stored); err != nil ||
		strings.Contains(stored, "secret-code") || strings.Contains(stored, state) {
		t.Fatalf("stored = %s, %v", stored, err)
	}
	if strings.Contains(h.Logs(), "secret-code") || strings.Contains(h.Logs(), state) {
		t.Fatal("the code or state was logged")
	}
}

func TestTheHistoryReadIsListed(t *testing.T) {
	h := testsupport.New(t, testsupport.WithHistory)
	app := newApp(h.Service)
	type listed struct {
		Schedule struct{ HistoryDays int }
		Items    []struct {
			TransactionsSince *string
			HistoryRead       *struct {
				Status       string
				StartedAt    time.Time
				FinishedAt   *time.Time
				Transactions int
				Error        *string
			}
		}
	}
	list := func() listed {
		t.Helper()
		got := send(t, app, http.MethodGet, "/v1/connections", "", nil)
		var parsed listed
		if err := json.Unmarshal([]byte(got.raw), &parsed); err != nil || got.status != http.StatusOK {
			t.Fatalf("list = %d %s", got.status, got.raw)
		}
		return parsed
	}
	if got := list(); got.Schedule.HistoryDays != 730 || len(got.Items) != 0 {
		t.Fatalf("before connecting: %+v", got)
	}
	state := h.Authorize()
	got := send(t, app, http.MethodPost, "/v1/connections", `{"code":"`+h.Bank.Approve(state)+`","state":"`+state+`"}`, owner)
	if got.status != http.StatusOK || !strings.Contains(got.raw, `"status":"running"`) || !strings.Contains(got.raw, `"transactionsSince":"2026-08-01"`) {
		t.Fatalf("connect = %d %s", got.status, got.raw)
	}
	h.Service.WaitHistory()
	item := list().Items[0]
	if item.TransactionsSince == nil || *item.TransactionsSince != "2026-06-01" || item.HistoryRead == nil || item.HistoryRead.Status != "completed" ||
		!item.HistoryRead.StartedAt.Equal(testsupport.Start) || item.HistoryRead.FinishedAt == nil || item.HistoryRead.Transactions != 1 || item.HistoryRead.Error != nil {
		t.Fatalf("listed = %+v %+v", item, item.HistoryRead)
	}
	off := send(t, newApp(testsupport.New(t).Service), http.MethodGet, "/v1/connections", "", nil)
	if !strings.Contains(off.raw, `"historyDays":0`) {
		t.Fatalf("history off = %s", off.raw)
	}
}
