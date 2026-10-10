package enablebanking_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/GAMch1k/life-os/services/banking/internal/enablebanking"
	"github.com/GAMch1k/life-os/services/banking/internal/enablebanking/fakebank"
)

func client(fake *fakebank.Server) *enablebanking.Client {
	return enablebanking.NewClient(fakebank.AppID, fakebank.Key(), fake.URL, nil)
}

func TestParsePrivateKeyReadsPKCS8AndPKCS1(t *testing.T) {
	key := fakebank.Key()
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"pkcs8": pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}),
		"pkcs1": pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}),
	} {
		parsed, err := enablebanking.ParsePrivateKey(data)
		if err != nil || !parsed.Equal(key) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, data := range [][]byte{nil, []byte("not a key"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte{1}})} {
		if _, err := enablebanking.ParsePrivateKey(data); err == nil {
			t.Errorf("ParsePrivateKey(%q) accepted", data)
		}
	}
}

func TestEveryRequestCarriesAValidJWT(t *testing.T) {
	fake := fakebank.New(t)
	aspsp, err := client(fake).FindASPSP(context.Background(), "deutsche bank", "DE")
	if err != nil {
		t.Fatal(err)
	}
	if aspsp.Name != "Deutsche Bank" || aspsp.MaxConsent() != 180*24*time.Hour {
		t.Fatalf("aspsp = %+v", aspsp)
	}
	other, _ := rsa.GenerateKey(rand.Reader, 1024)
	_, err = enablebanking.NewClient(fakebank.AppID, other, fake.URL, nil).FindASPSP(context.Background(), "Deutsche Bank", "DE")
	if apiError := enablebanking.AsAPIError(err); apiError == nil || apiError.Status != http.StatusUnauthorized || !apiError.Unauthorized() {
		t.Fatalf("a foreign key: %v", err)
	}
	if _, err := client(fake).FindASPSP(context.Background(), "Sparkasse", "DE"); !errors.Is(err, enablebanking.ErrUnknownASPSP) {
		t.Fatalf("an unknown bank: %v", err)
	}
}

func TestTheTokenIsReusedUntilShortlyBeforeItExpires(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	fake := fakebank.New(t)
	fake.Now = func() time.Time { return now }
	c := client(fake).WithClock(func() time.Time { return now })
	ctx := context.Background()
	if _, err := c.FindASPSP(ctx, "Deutsche Bank", "DE"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(54 * time.Minute)
	if _, err := c.FindASPSP(ctx, "Deutsche Bank", "DE"); err != nil {
		t.Fatalf("a token one hour old is replaced in time: %v", err)
	}
}

func TestMaxConsentNeverExceedsPSD2(t *testing.T) {
	if got := (enablebanking.ASPSP{MaximumConsentValidity: 400 * 86400}).MaxConsent(); got != enablebanking.MaxConsent {
		t.Errorf("400 days -> %v", got)
	}
	if got := (enablebanking.ASPSP{MaximumConsentValidity: 90 * 86400}).MaxConsent(); got != 90*24*time.Hour {
		t.Errorf("90 days -> %v", got)
	}
}

func TestPSUHeadersAreSentOnlyWhenTheOwnerIsPresent(t *testing.T) {
	fake := fakebank.New(t)
	fake.Accounts = []fakebank.Account{{IBAN: "DE89370400440532013000", Currency: "EUR", CashAccountType: "CACC"}}
	c := client(fake)
	ctx := context.Background()
	if _, err := c.StartAuth(ctx, enablebanking.AuthRequest{ValidUntil: time.Now().Add(24 * time.Hour), ASPSPName: "Deutsche Bank", Country: "DE", State: "s1", RedirectURL: "https://lifeos.example/finance/bank/callback"}); err != nil {
		t.Fatal(err)
	}
	session, err := c.CreateSession(ctx, fake.Approve("s1"))
	if err != nil {
		t.Fatal(err)
	}
	uid := session.Accounts[0].UID
	if _, err := c.Balances(ctx, uid, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Balances(ctx, uid, &enablebanking.PSU{IPAddress: "203.0.113.7"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Transactions(ctx, uid, enablebanking.TransactionsQuery{DateFrom: "2026-10-01"}, &enablebanking.PSU{IPAddress: "203.0.113.7", UserAgent: "Firefox"}); err != nil {
		t.Fatal(err)
	}
	requests := fake.Requests()
	reads := requests[len(requests)-3:]
	if reads[0].PSUIP != "" || reads[1].PSUIP != "" || reads[1].PSUUserAgent != "" {
		t.Errorf("incomplete PSU details must not be sent: %+v", reads[:2])
	}
	if reads[2].PSUIP != "203.0.113.7" || reads[2].PSUUserAgent != "Firefox" {
		t.Errorf("PSU headers = %+v", reads[2])
	}
}

func TestErrorsAreClassifiedAndNameNoIdentifiers(t *testing.T) {
	fake := fakebank.New(t)
	c := client(fake)
	ctx := context.Background()
	const session = "0f0e0d0c-0b0a-4000-8000-000000000001"
	for _, tc := range []struct {
		status int
		code   string
		check  func(*enablebanking.APIError) bool
	}{
		{http.StatusUnauthorized, "EXPIRED_SESSION", (*enablebanking.APIError).SessionEnded},
		{http.StatusForbidden, "REVOKED_SESSION", (*enablebanking.APIError).SessionEnded},
		{http.StatusTooManyRequests, "ASPSP_RATE_LIMIT_EXCEEDED", (*enablebanking.APIError).RateLimited},
		{http.StatusInternalServerError, "ASPSP_ERROR", (*enablebanking.APIError).Temporary},
		{http.StatusBadRequest, "PSU_HEADER_NOT_PROVIDED", (*enablebanking.APIError).PSUHeaders},
		{http.StatusUnprocessableEntity, "WRONG_TRANSACTIONS_PERIOD", (*enablebanking.APIError).WrongPeriod},
		{http.StatusBadRequest, "ASPSP_PSU_ACTION_REQUIRED", (*enablebanking.APIError).ActionRequired},
		{http.StatusForbidden, "ACCESS_DENIED", (*enablebanking.APIError).Unauthorized},
	} {
		fake.Fail("", "/sessions/", tc.status, tc.code, 1)
		_, err := c.SessionStatus(ctx, session)
		apiError := enablebanking.AsAPIError(err)
		if apiError == nil || apiError.Status != tc.status || apiError.Code != tc.code || !tc.check(apiError) {
			t.Errorf("%d %s: %v", tc.status, tc.code, err)
			continue
		}
		if strings.Contains(err.Error(), session) {
			t.Errorf("the error names the session: %v", err)
		}
	}
	fake.Fail("", "/sessions/", http.StatusServiceUnavailable, "", 1)
	if apiError := enablebanking.AsAPIError(func() error { _, err := c.SessionStatus(ctx, session); return err }()); apiError == nil || !apiError.Temporary() || apiError.SessionEnded() {
		t.Errorf("a 503 is temporary: %+v", apiError)
	}

	// No answer at all: the error never repeats the URL.
	fake.Close()
	_, err := c.SessionStatus(ctx, session)
	var transport *enablebanking.TransportError
	if !errors.As(err, &transport) || strings.Contains(err.Error(), session) || strings.Contains(err.Error(), "/sessions") {
		t.Fatalf("transport error = %v", err)
	}
}

func TestRedactRemovesIdentifiersAndIBANs(t *testing.T) {
	got := enablebanking.Redact("Session 0f0e0d0c-0b0a-4000-8000-000000000001 for DE89 3704 0044 0532 0130 00 and  DE89370400440532013000 failed")
	if got != "Session [id] for [account] and [account] failed" {
		t.Fatalf("Redact = %q", got)
	}
}
