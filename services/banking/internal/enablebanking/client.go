// Package enablebanking is a small client for the Enable Banking API
// (https://enablebanking.com/docs/api/reference/), a licensed PSD2 provider.
// Life OS uses its account information service only: starting a consent,
// creating and ending the session, and reading balances and transactions.
// It never asks for payments.
//
// Every request is signed with a short-lived RS256 JWT made from the
// application's private key. The session id, the JWT, the key, and account
// numbers never appear in errors this package returns.
package enablebanking

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// DefaultBaseURL is Enable Banking's API.
const DefaultBaseURL = "https://api.enablebanking.com"

// TokenLifetime is how long one JWT is valid (the API allows up to 24 h).
const TokenLifetime = time.Hour

// tokenMargin is how long before its expiry a JWT is replaced.
const tokenMargin = 5 * time.Minute

// MaxConsent is the longest consent PSD2 allows.
const MaxConsent = 180 * 24 * time.Hour

// ParsePrivateKey reads the application's RSA private key from PEM (PKCS #8,
// as Enable Banking's control panel generates it, or PKCS #1).
func ParsePrivateKey(data []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("the key file holds no PEM block")
	}
	switch block.Type {
	case "RSA PRIVATE KEY":
		return x509.ParsePKCS1PrivateKey(block.Bytes)
	case "PRIVATE KEY":
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		key, ok := parsed.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("the key file holds no RSA key")
		}
		return key, nil
	}
	return nil, fmt.Errorf("the key file holds a %q block, not a private key", block.Type)
}

// Client calls the API as one application.
type Client struct {
	appID   string
	key     *rsa.PrivateKey
	baseURL string
	http    *http.Client
	now     func() time.Time

	mu       sync.Mutex
	token    string
	tokenExp time.Time
}

// NewClient signs as appID with key. An empty baseURL is DefaultBaseURL; a
// nil httpClient gets a 60 s timeout.
func NewClient(appID string, key *rsa.PrivateKey, baseURL string, httpClient *http.Client) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 60 * time.Second}
	}
	return &Client{appID: appID, key: key, baseURL: strings.TrimRight(baseURL, "/"), http: httpClient, now: time.Now}
}

// WithClock replaces the clock (tests).
func (c *Client) WithClock(now func() time.Time) *Client {
	c.now = now
	return c
}

// jwt returns a valid token, signing a new one when the last expires soon.
func (c *Client) jwt() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if c.token != "" && now.Add(tokenMargin).Before(c.tokenExp) {
		return c.token, nil
	}
	token, err := SignJWT(c.appID, c.key, now, TokenLifetime)
	if err != nil {
		return "", err
	}
	c.token, c.tokenExp = token, now.Add(TokenLifetime)
	return token, nil
}

// SignJWT is the API's authorization token: RS256, kid = the application id,
// issued by enablebanking.com for api.enablebanking.com, valid for lifetime.
func SignJWT(appID string, key *rsa.PrivateKey, now time.Time, lifetime time.Duration) (string, error) {
	header, err := json.Marshal(map[string]string{"typ": "JWT", "alg": "RS256", "kid": appID})
	if err != nil {
		return "", err
	}
	claims, err := json.Marshal(map[string]any{
		"iss": "enablebanking.com", "aud": "api.enablebanking.com",
		"iat": now.Unix(), "exp": now.Add(lifetime).Unix(),
	})
	if err != nil {
		return "", err
	}
	signing := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signing))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("sign the Enable Banking token: %w", err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

// PSU describes the owner while they are using Life OS. Sent with an account
// read, it tells the bank the owner is present, so the read does not count
// against the few unattended reads a bank allows per day.
type PSU struct {
	IPAddress      string
	UserAgent      string
	AcceptLanguage string
}

// Present reports whether the PSU headers can be sent: the bank needs at
// least the address and the user agent.
func (p *PSU) Present() bool {
	return p != nil && p.IPAddress != "" && p.UserAgent != ""
}

func (p *PSU) apply(header http.Header) {
	if !p.Present() {
		return
	}
	header.Set("Psu-Ip-Address", p.IPAddress)
	header.Set("Psu-User-Agent", p.UserAgent)
	if p.AcceptLanguage != "" {
		header.Set("Psu-Accept-language", p.AcceptLanguage)
	}
}

// ASPSP is a bank as the API lists it.
type ASPSP struct {
	Name    string `json:"name"`
	Country string `json:"country"`
	// MaximumConsentValidity is the longest consent the bank grants, in
	// seconds.
	MaximumConsentValidity int64    `json:"maximum_consent_validity"`
	PSUTypes               []string `json:"psu_types"`
	RequiredPSUHeaders     []string `json:"required_psu_headers"`
}

// MaxConsent is the longest consent to ask this bank for: its own maximum,
// never more than PSD2's 180 days.
func (a ASPSP) MaxConsent() time.Duration {
	if a.MaximumConsentValidity <= 0 {
		return MaxConsent
	}
	return min(time.Duration(a.MaximumConsentValidity)*time.Second, MaxConsent)
}

// ErrUnknownASPSP means the API does not list the bank for personal
// accounts in that country.
var ErrUnknownASPSP = errors.New("Enable Banking does not offer this bank for personal accounts")

// FindASPSP looks the bank up by name (ignoring case) among the country's
// banks for personal accounts.
func (c *Client) FindASPSP(ctx context.Context, name, country string) (ASPSP, error) {
	query := url.Values{"country": {country}, "psu_type": {"personal"}}
	var list struct {
		ASPSPs []ASPSP `json:"aspsps"`
	}
	if err := c.do(ctx, "list banks", http.MethodGet, "/aspsps?"+query.Encode(), nil, nil, &list); err != nil {
		return ASPSP{}, err
	}
	for _, aspsp := range list.ASPSPs {
		if strings.EqualFold(aspsp.Name, name) && strings.EqualFold(aspsp.Country, country) {
			return aspsp, nil
		}
	}
	return ASPSP{}, ErrUnknownASPSP
}

// AuthRequest starts a consent.
type AuthRequest struct {
	ValidUntil  time.Time
	ASPSPName   string
	Country     string
	State       string
	RedirectURL string
}

// StartAuth asks for an account information consent (balances and
// transactions) and returns the bank's page where the owner gives it.
func (c *Client) StartAuth(ctx context.Context, request AuthRequest) (string, error) {
	body := map[string]any{
		// Only valid_until: the bank's full account information consent.
		// Naming balances and transactions without accounts asks a Berlin
		// Group bank to let the owner pick accounts, and Deutsche Bank
		// answered that with server_error (2026-10-10); the plain consent
		// is the one its read test granted.
		"access":       map[string]any{"valid_until": request.ValidUntil.UTC().Format(time.RFC3339)},
		"aspsp":        map[string]string{"name": request.ASPSPName, "country": request.Country},
		"state":        request.State,
		"redirect_url": request.RedirectURL,
		"psu_type":     "personal",
	}
	var response struct {
		URL string `json:"url"`
	}
	if err := c.do(ctx, "start authorization", http.MethodPost, "/auth", body, nil, &response); err != nil {
		return "", err
	}
	if response.URL == "" {
		return "", errors.New("Enable Banking returned no authorization page")
	}
	return response.URL, nil
}

// AccountIdentification is an account number.
type AccountIdentification struct {
	IBAN  string `json:"iban"`
	Other *struct {
		Identification string `json:"identification"`
		SchemeName     string `json:"scheme_name"`
	} `json:"other"`
}

// Account is one consented account.
type Account struct {
	// UID identifies the account in this session only.
	UID       string                 `json:"uid"`
	AccountID *AccountIdentification `json:"account_id"`
	// Name is the account holder's name, not the account's.
	Name                 string   `json:"name"`
	Details              string   `json:"details"`
	Product              string   `json:"product"`
	Currency             string   `json:"currency"`
	CashAccountType      string   `json:"cash_account_type"`
	Usage                string   `json:"usage"`
	IdentificationHash   string   `json:"identification_hash"`
	IdentificationHashes []string `json:"identification_hashes"`
}

// IBAN is the account's IBAN, or "".
func (a Account) IBAN() string {
	if a.AccountID == nil {
		return ""
	}
	return a.AccountID.IBAN
}

// Access is what the owner consented to.
type Access struct {
	ValidUntil time.Time `json:"valid_until"`
}

// Session is an authorized consent. ID is the credential for reading the
// accounts: keep it secret.
type Session struct {
	ID       string    `json:"session_id"`
	Accounts []Account `json:"accounts"`
	Access   Access    `json:"access"`
}

// CreateSession completes a consent with the code the bank returned.
func (c *Client) CreateSession(ctx context.Context, code string) (Session, error) {
	var session Session
	if err := c.do(ctx, "create session", http.MethodPost, "/sessions", map[string]string{"code": code}, nil, &session); err != nil {
		return Session{}, err
	}
	if session.ID == "" {
		return Session{}, errors.New("Enable Banking returned no session")
	}
	return session, nil
}

// SessionStatus is the session's state at the provider, such as AUTHORIZED,
// EXPIRED, or REVOKED.
func (c *Client) SessionStatus(ctx context.Context, sessionID string) (string, error) {
	var response struct {
		Status string `json:"status"`
	}
	if err := c.do(ctx, "read session", http.MethodGet, "/sessions/"+url.PathEscape(sessionID), nil, nil, &response); err != nil {
		return "", err
	}
	return response.Status, nil
}

// DeleteSession ends the session; the provider closes the bank consent
// where it can.
func (c *Client) DeleteSession(ctx context.Context, sessionID string, psu *PSU) error {
	return c.do(ctx, "delete session", http.MethodDelete, "/sessions/"+url.PathEscape(sessionID), nil, psu, nil)
}

// Amount is a decimal amount as the API sends it.
type Amount struct {
	Currency string `json:"currency"`
	Amount   string `json:"amount"`
}

// Balance is one of an account's balances, such as CLBD (closing booked)
// or ITAV (interim available).
type Balance struct {
	Name               string `json:"name"`
	Amount             Amount `json:"balance_amount"`
	Type               string `json:"balance_type"`
	ReferenceDate      string `json:"reference_date"`
	LastChangeDateTime string `json:"last_change_date_time"`
}

// Balances reads an account's balances.
func (c *Client) Balances(ctx context.Context, accountUID string, psu *PSU) ([]Balance, error) {
	var response struct {
		Balances []Balance `json:"balances"`
	}
	if err := c.do(ctx, "read balances", http.MethodGet, "/accounts/"+url.PathEscape(accountUID)+"/balances", nil, psu, &response); err != nil {
		return nil, err
	}
	return response.Balances, nil
}

// Party is a creditor or debtor.
type Party struct {
	Name string `json:"name"`
}

// Remittance is the remittance information: a list of strings (one string
// is accepted too).
type Remittance []string

func (r *Remittance) UnmarshalJSON(data []byte) error {
	var list []string
	if err := json.Unmarshal(data, &list); err == nil {
		*r = list
		return nil
	}
	var one string
	if err := json.Unmarshal(data, &one); err != nil {
		return err
	}
	*r = Remittance{one}
	return nil
}

// Transaction is one transaction as the API sends it.
type Transaction struct {
	EntryReference       string     `json:"entry_reference"`
	TransactionID        string     `json:"transaction_id"`
	MerchantCategoryCode string     `json:"merchant_category_code"`
	Amount               Amount     `json:"transaction_amount"`
	Creditor             *Party     `json:"creditor"`
	Debtor               *Party     `json:"debtor"`
	CreditDebitIndicator string     `json:"credit_debit_indicator"`
	Status               string     `json:"status"`
	BookingDate          string     `json:"booking_date"`
	ValueDate            string     `json:"value_date"`
	TransactionDate      string     `json:"transaction_date"`
	ReferenceNumber      string     `json:"reference_number"`
	Remittance           Remittance `json:"remittance_information"`
	Note                 string     `json:"note"`
}

// TransactionsQuery selects transactions by booking date (inclusive, UTC).
type TransactionsQuery struct {
	DateFrom string
	DateTo   string
	// Strategy is "" (the API's default) or "longest", which returns as
	// much history as the bank allows from DateFrom and ignores DateTo.
	Strategy        string
	ContinuationKey string
}

// TransactionsPage is one page; ContinuationKey is "" on the last.
type TransactionsPage struct {
	Transactions    []Transaction `json:"transactions"`
	ContinuationKey string        `json:"continuation_key"`
}

// Transactions reads one page of an account's transactions.
func (c *Client) Transactions(ctx context.Context, accountUID string, query TransactionsQuery, psu *PSU) (TransactionsPage, error) {
	values := url.Values{}
	if query.DateFrom != "" {
		values.Set("date_from", query.DateFrom)
	}
	if query.DateTo != "" && query.Strategy != "longest" {
		values.Set("date_to", query.DateTo)
	}
	if query.Strategy != "" {
		values.Set("strategy", query.Strategy)
	}
	if query.ContinuationKey != "" {
		values.Set("continuation_key", query.ContinuationKey)
	}
	path := "/accounts/" + url.PathEscape(accountUID) + "/transactions"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var page TransactionsPage
	if err := c.do(ctx, "read transactions", http.MethodGet, path, nil, psu, &page); err != nil {
		return TransactionsPage{}, err
	}
	return page, nil
}

// maxResponse bounds a response body.
const maxResponse = 32 << 20

func (c *Client) do(ctx context.Context, op, method, path string, body any, psu *PSU, out any) error {
	token, err := c.jwt()
	if err != nil {
		return err
	}
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return &TransportError{Op: op, Err: errors.New("invalid request")}
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	psu.apply(request.Header)
	response, err := c.http.Do(request)
	if err != nil {
		// The URL names the session or account: keep only the cause.
		var urlError *url.Error
		if errors.As(err, &urlError) {
			err = urlError.Err
		}
		return &TransportError{Op: op, Err: err}
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponse))
	if err != nil {
		return &TransportError{Op: op, Err: err}
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return newAPIError(op, response.StatusCode, raw)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return &TransportError{Op: op, Err: fmt.Errorf("unreadable response: %w", err)}
	}
	return nil
}
