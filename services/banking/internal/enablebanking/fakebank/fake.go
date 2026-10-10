// Package fakebank is an in-memory Enable Banking API for tests. It checks
// every request's JWT against a test key, serves one bank with its accounts,
// balances, and transactions (paged), and can fail requests on demand.
// It never talks to the real API.
package fakebank

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// AppID is the application id the fake expects as the JWT's kid.
const AppID = "11111111-2222-3333-4444-555555555555"

var (
	keyOnce sync.Once
	testKey *rsa.PrivateKey
)

// Key is the test application key, shared by all tests (generated once).
func Key() *rsa.PrivateKey {
	keyOnce.Do(func() {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		testKey = key
	})
	return testKey
}

// Account is one account the fake bank holds.
type Account struct {
	IBAN            string
	Product         string
	HolderName      string
	Currency        string
	CashAccountType string
	Balances        []map[string]any
	Transactions    []map[string]any
}

// Request is a request the fake received.
type Request struct {
	Method string
	Path   string
	Query  url.Values
	// PSUIP and PSUUserAgent are the PSU headers sent with it.
	PSUIP        string
	PSUUserAgent string
	Body         map[string]any
}

type failure struct {
	method, prefix string
	status         int
	code           string
	times          int
}

type session struct {
	id         string
	status     string
	validUntil time.Time
	uids       map[string]int // uid -> account index
}

// Server is the fake API.
type Server struct {
	*httptest.Server
	t testing.TB

	mu sync.Mutex
	// ASPSP is the bank's name, country, and maximum consent (seconds).
	ASPSPName   string
	Country     string
	MaxConsent  int64
	Accounts    []Account
	PageSize    int
	EarliestDay string // date_from before it is WRONG_TRANSACTIONS_PERIOD (default strategy)
	Now         func() time.Time

	requests []Request
	auths    map[string]map[string]any // state -> /auth body
	codes    map[string]string         // code -> state
	sessions map[string]*session
	failures []*failure
	seq      int
}

// New starts a fake with Deutsche Bank (DE, 180 days) and no accounts.
func New(t testing.TB) *Server {
	s := &Server{
		t: t, ASPSPName: "Deutsche Bank", Country: "DE", MaxConsent: 15552000, Now: time.Now,
		auths: map[string]map[string]any{}, codes: map[string]string{}, sessions: map[string]*session{},
	}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

// Fail makes the next times requests whose path starts with prefix (and
// method matches, "" for any) answer status with the API error code.
func (s *Server) Fail(method, prefix string, status int, code string, times int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures = append(s.failures, &failure{method: method, prefix: prefix, status: status, code: code, times: times})
}

// Requests returns the requests received so far.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.requests)
}

// AccountReads counts the balance and transaction requests received.
func (s *Server) AccountReads() int {
	count := 0
	for _, request := range s.Requests() {
		if strings.HasPrefix(request.Path, "/accounts/") {
			count++
		}
	}
	return count
}

// ResetRequests forgets the requests received so far.
func (s *Server) ResetRequests() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = nil
}

// AuthRequest is the /auth body sent with state, or nil.
func (s *Server) AuthRequest(state string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.auths[state]
}

// Approve is the owner giving consent at the bank for the authorization
// started with state: it returns the code the bank sends back.
func (s *Server) Approve(state string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.auths[state]; !ok {
		s.t.Fatalf("fakebank: no authorization was started with state %q", state)
	}
	s.seq++
	code := fmt.Sprintf("code-%d", s.seq)
	s.codes[code] = state
	return code
}

// SetSessionStatus changes a session's status (EXPIRED, REVOKED, ...): its
// account reads then fail like the real API's.
func (s *Server) SetSessionStatus(sessionID, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if session, ok := s.sessions[sessionID]; ok {
		session.status = status
	}
}

// SessionIDs lists the sessions created, oldest first.
func (s *Server) SessionIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.sessions))
	for id := range s.sessions {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// SessionStatus is a session's status ("" when unknown).
func (s *Server) SessionStatus(sessionID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if session, ok := s.sessions[sessionID]; ok {
		return session.status
	}
	return ""
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	recorded := Request{Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(),
		PSUIP: r.Header.Get("Psu-Ip-Address"), PSUUserAgent: r.Header.Get("Psu-User-Agent")}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&recorded.Body)
	}
	s.requests = append(s.requests, recorded)

	if err := s.verifyJWT(r.Header.Get("Authorization")); err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED_ACCESS", err.Error())
		return
	}
	for _, f := range s.failures {
		if f.times > 0 && (f.method == "" || f.method == r.Method) && strings.HasPrefix(r.URL.Path, f.prefix) {
			f.times--
			writeError(w, f.status, f.code, "Injected failure")
			return
		}
	}
	path := r.URL.Path
	switch {
	case r.Method == http.MethodGet && path == "/aspsps":
		s.listASPSPs(w, r)
	case r.Method == http.MethodPost && path == "/auth":
		s.startAuth(w, recorded.Body)
	case r.Method == http.MethodPost && path == "/sessions":
		s.createSession(w, recorded.Body)
	case strings.HasPrefix(path, "/sessions/"):
		s.sessionByID(w, r.Method, strings.TrimPrefix(path, "/sessions/"))
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/accounts/"):
		s.account(w, r, strings.Split(strings.TrimPrefix(path, "/accounts/"), "/"))
	default:
		writeError(w, http.StatusNotFound, "", "Not found")
	}
}

func (s *Server) verifyJWT(header string) error {
	token, ok := strings.CutPrefix(header, "Bearer ")
	if !ok {
		return fmt.Errorf("no bearer token")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return fmt.Errorf("malformed token")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&Key().PublicKey, crypto.SHA256, digest[:], signature); err != nil {
		return fmt.Errorf("bad signature")
	}
	var head struct{ Typ, Alg, Kid string }
	var claims struct {
		Iss string `json:"iss"`
		Aud string `json:"aud"`
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
	}
	if err := decodeSegment(parts[0], &head); err != nil {
		return err
	}
	if err := decodeSegment(parts[1], &claims); err != nil {
		return err
	}
	now := s.Now().Unix()
	switch {
	case head.Typ != "JWT" || head.Alg != "RS256" || head.Kid != AppID:
		return fmt.Errorf("wrong token header")
	case claims.Iss != "enablebanking.com" || claims.Aud != "api.enablebanking.com":
		return fmt.Errorf("wrong token claims")
	case claims.Exp <= now || claims.Iat > now+60 || claims.Exp-claims.Iat > 86400:
		return fmt.Errorf("token expired or too long")
	}
	return nil
}

func decodeSegment(segment string, out any) error {
	raw, err := base64.RawURLEncoding.DecodeString(segment)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func (s *Server) listASPSPs(w http.ResponseWriter, r *http.Request) {
	items := []map[string]any{}
	if r.URL.Query().Get("country") == s.Country {
		items = append(items, map[string]any{
			"name": s.ASPSPName, "country": s.Country, "maximum_consent_validity": s.MaxConsent,
			"psu_types": []string{"personal"}, "required_psu_headers": []string{},
		})
	}
	writeJSON(w, map[string]any{"aspsps": items})
}

func (s *Server) startAuth(w http.ResponseWriter, body map[string]any) {
	state, _ := body["state"].(string)
	aspsp, _ := body["aspsp"].(map[string]any)
	access, _ := body["access"].(map[string]any)
	validUntil, err := time.Parse(time.RFC3339, fmt.Sprint(access["valid_until"]))
	switch {
	case state == "" || aspsp["name"] != s.ASPSPName || aspsp["country"] != s.Country:
		writeError(w, http.StatusBadRequest, "WRONG_ASPSP_PROVIDED", "Wrong ASPSP name provided")
		return
	case err != nil || validUntil.After(s.Now().Add(time.Duration(s.MaxConsent)*time.Second)):
		writeError(w, http.StatusBadRequest, "WRONG_REQUEST_PARAMETERS", "valid_until is too far in the future")
		return
	case access["balances"] != nil || access["transactions"] != nil || access["accounts"] != nil:
		// Like Deutsche Bank: a consent naming what to access, without
		// accounts, fails (the real bank redirects with server_error).
		writeError(w, http.StatusBadRequest, "WRONG_REQUEST_PARAMETERS", "Detailed consent is not supported")
		return
	case body["psu_type"] != "personal" || body["redirect_url"] == "":
		writeError(w, http.StatusBadRequest, "WRONG_REQUEST_PARAMETERS", "Wrong request parameters provided")
		return
	}
	s.auths[state] = body
	writeJSON(w, map[string]any{"url": "https://bank.example/consent?state=" + url.QueryEscape(state), "authorization_id": "auth-1"})
}

func (s *Server) createSession(w http.ResponseWriter, body map[string]any) {
	code, _ := body["code"].(string)
	state, ok := s.codes[code]
	if !ok {
		writeError(w, http.StatusBadRequest, "WRONG_AUTHORIZATION_CODE", "Wrong authorization code provided")
		return
	}
	delete(s.codes, code)
	access, _ := s.auths[state]["access"].(map[string]any)
	validUntil, _ := time.Parse(time.RFC3339, fmt.Sprint(access["valid_until"]))
	s.seq++
	sess := &session{id: fmt.Sprintf("00000000-0000-4000-8000-%012d", s.seq), status: "AUTHORIZED", validUntil: validUntil, uids: map[string]int{}}
	accounts := []map[string]any{}
	for index, account := range s.Accounts {
		s.seq++
		uid := fmt.Sprintf("aaaaaaaa-0000-4000-8000-%012d", s.seq)
		sess.uids[uid] = index
		accounts = append(accounts, map[string]any{
			"uid": uid, "account_id": map[string]any{"iban": account.IBAN}, "name": account.HolderName,
			"product": account.Product, "currency": account.Currency, "cash_account_type": account.CashAccountType,
			"identification_hash": "hash-" + account.IBAN, "identification_hashes": []string{"hash-" + account.IBAN},
		})
	}
	s.sessions[sess.id] = sess
	writeJSON(w, map[string]any{
		"session_id": sess.id, "accounts": accounts, "psu_type": "personal",
		"aspsp":  map[string]any{"name": s.ASPSPName, "country": s.Country},
		"access": map[string]any{"valid_until": validUntil.Format(time.RFC3339)},
	})
}

func (s *Server) sessionByID(w http.ResponseWriter, method, id string) {
	sess, ok := s.sessions[id]
	if !ok {
		writeError(w, http.StatusNotFound, "SESSION_DOES_NOT_EXIST", "No session found matching provided id")
		return
	}
	switch method {
	case http.MethodGet:
		writeJSON(w, map[string]any{"status": sess.status, "access": map[string]any{"valid_until": sess.validUntil.Format(time.RFC3339)}})
	case http.MethodDelete:
		if sess.status != "AUTHORIZED" {
			writeError(w, http.StatusBadRequest, sessionError(sess.status), "Session is not active")
			return
		}
		sess.status = "CLOSED"
		writeJSON(w, map[string]any{"message": "OK"})
	default:
		writeError(w, http.StatusMethodNotAllowed, "", "Method not allowed")
	}
}

func sessionError(status string) string {
	switch status {
	case "EXPIRED":
		return "EXPIRED_SESSION"
	case "REVOKED":
		return "REVOKED_SESSION"
	}
	return "CLOSED_SESSION"
}

func (s *Server) account(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) != 2 {
		writeError(w, http.StatusNotFound, "", "Not found")
		return
	}
	var sess *session
	index := -1
	for _, candidate := range s.sessions {
		if i, ok := candidate.uids[parts[0]]; ok {
			sess, index = candidate, i
		}
	}
	if sess == nil {
		writeError(w, http.StatusNotFound, "ACCOUNT_DOES_NOT_EXIST", "No account found matching provided id")
		return
	}
	if sess.status != "AUTHORIZED" {
		writeError(w, http.StatusUnauthorized, sessionError(sess.status), "Session is not authorized")
		return
	}
	account := s.Accounts[index]
	switch parts[1] {
	case "balances":
		writeJSON(w, map[string]any{"balances": account.Balances})
	case "transactions":
		s.transactions(w, r.URL.Query(), account)
	default:
		writeError(w, http.StatusNotFound, "", "Not found")
	}
}

func (s *Server) transactions(w http.ResponseWriter, query url.Values, account Account) {
	from, to, strategy := query.Get("date_from"), query.Get("date_to"), query.Get("strategy")
	if strategy == "longest" {
		to = ""
		if s.EarliestDay != "" && from < s.EarliestDay {
			from = s.EarliestDay
		}
	} else if s.EarliestDay != "" && from != "" && from < s.EarliestDay {
		writeError(w, http.StatusUnprocessableEntity, "WRONG_TRANSACTIONS_PERIOD", "Wrong transactions period requested")
		return
	}
	matched := []map[string]any{}
	for _, transaction := range account.Transactions {
		day, _ := transaction["booking_date"].(string)
		if day == "" {
			day, _ = transaction["transaction_date"].(string)
		}
		if (from == "" || day >= from) && (to == "" || day <= to) {
			matched = append(matched, transaction)
		}
	}
	start := 0
	if key := query.Get("continuation_key"); key != "" {
		page, err := strconv.Atoi(strings.TrimPrefix(key, "page-"))
		if err != nil {
			writeError(w, http.StatusBadRequest, "WRONG_CONTINUATION_KEY", "Wrong continuation key provided")
			return
		}
		start = page * s.PageSize
	}
	end := len(matched)
	next := any(nil)
	if s.PageSize > 0 && start+s.PageSize < len(matched) {
		end = start + s.PageSize
		next = fmt.Sprintf("page-%d", end/s.PageSize)
	}
	if start > len(matched) {
		start = len(matched)
	}
	writeJSON(w, map[string]any{"transactions": matched[start:end], "continuation_key": next})
}

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	body := map[string]any{"code": status, "message": message}
	if code != "" {
		body["error"] = code
	}
	_ = json.NewEncoder(w).Encode(body)
}
