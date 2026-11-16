// Package finance pushes bank syncs into the Finance service through its
// bank import endpoints (services/finance/api/openapi.yaml: importBankSync
// and removeBankConnection), the only way Banking touches Finance. The
// types mirror that contract; Banking never imports Finance's code or reads
// its database.
package finance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/GAMch1k/life-os/pkg/activity"
)

// Actor is who Banking's imports are attributed to.
const Actor = "system:banking"

// Money is an amount in minor units (cents); negative is money out.
type Money struct {
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
}

// Balance is a balance the bank reported for a day.
type Balance struct {
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
	Date     string `json:"date"`
}

// Account is one account of the sync.
type Account struct {
	ExternalID       string   `json:"externalId"`
	Name             string   `json:"name"`
	Kind             string   `json:"kind"`
	Currency         string   `json:"currency"`
	IBANMasked       string   `json:"ibanMasked,omitempty"`
	BookedBalance    *Balance `json:"bookedBalance,omitempty"`
	AvailableBalance *Balance `json:"availableBalance,omitempty"`
}

// Transaction is one transaction of the sync.
type Transaction struct {
	AccountExternalID    string `json:"accountExternalId"`
	ExternalID           string `json:"externalId"`
	Status               string `json:"status"`
	BookingDate          string `json:"bookingDate"`
	ValueDate            string `json:"valueDate,omitempty"`
	Amount               Money  `json:"amount"`
	Counterparty         string `json:"counterparty,omitempty"`
	Description          string `json:"description,omitempty"`
	MerchantCategoryCode string `json:"merchantCategoryCode,omitempty"`
	BankReference        string `json:"bankReference,omitempty"`
}

// Window is the booking dates the sync covers (inclusive).
type Window struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Import is one bank sync (BankImportRequest).
type Import struct {
	ConnectionID string        `json:"connectionId"`
	Provider     string        `json:"provider"`
	Institution  string        `json:"institution"`
	Window       Window        `json:"window"`
	Accounts     []Account     `json:"accounts"`
	Transactions []Transaction `json:"transactions"`
}

// Result is Finance's answer (BankImportResult).
type Result struct {
	AccountIDs []struct {
		ExternalID string `json:"externalId"`
		ID         string `json:"id"`
	} `json:"accountIds"`
	Accounts struct {
		Created int `json:"created"`
		Updated int `json:"updated"`
	} `json:"accounts"`
	Transactions struct {
		Created        int `json:"created"`
		Updated        int `json:"updated"`
		Unchanged      int `json:"unchanged"`
		RemovedPending int `json:"removedPending"`
		Skipped        int `json:"skipped"`
	} `json:"transactions"`
}

// StatusError is an answer other than success.
type StatusError struct {
	Status  int
	Code    string
	Message string
}

// HTTPStatus and ErrorCode are what a log line keeps of Finance's answer
// (pkg/diagnostics.ErrorCode), never its message.
func (e *StatusError) HTTPStatus() int   { return e.Status }
func (e *StatusError) ErrorCode() string { return e.Code }

func (e *StatusError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("Finance returned %d %s: %s", e.Status, e.Code, e.Message)
	}
	return fmt.Sprintf("Finance returned %d", e.Status)
}

// Permanent means sending the same import again cannot succeed (Finance
// refused it, such as 400 currency_mismatch); only 5xx, 429, and no answer
// are worth retrying.
func (e *StatusError) Permanent() bool {
	return e.Status >= 400 && e.Status < 500 && e.Status != http.StatusTooManyRequests && e.Status != http.StatusRequestTimeout
}

// IsPermanent reports whether err is an import Finance refused for good.
func IsPermanent(err error) bool {
	var status *StatusError
	return errors.As(err, &status) && status.Permanent()
}

// ErrNotConfigured means FINANCE_SERVICE_URL is not set.
var ErrNotConfigured = errors.New("Finance is not configured (FINANCE_SERVICE_URL)")

// Client calls Finance at URL.
type Client struct {
	URL  string
	HTTP *http.Client
}

// New returns a client for the Finance service at baseURL.
func New(baseURL string) *Client {
	return &Client{URL: strings.TrimRight(strings.TrimSpace(baseURL), "/"), HTTP: &http.Client{Timeout: 60 * time.Second}}
}

// Import sends one sync. Finance applies it in one transaction.
func (c *Client) Import(ctx context.Context, request Import) (Result, error) {
	var result Result
	err := c.do(ctx, http.MethodPost, "/v1/imports/bank", request, &result)
	return result, err
}

// RemoveConnection archives the connection's accounts, keeping their
// transactions. Idempotent.
func (c *Client) RemoveConnection(ctx context.Context, connectionID string) error {
	return c.do(ctx, http.MethodDelete, "/v1/imports/bank/"+url.PathEscape(connectionID), nil, nil)
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	if c == nil || c.URL == "" {
		return ErrNotConfigured
	}
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.URL+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Accept", "application/json")
	trace := activity.TraceFrom(ctx)
	trace.Actor = activity.Actor{}
	trace.Apply(request.Header)
	request.Header.Set(activity.ActorHeader, Actor)
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		var urlError *url.Error
		if errors.As(err, &urlError) {
			err = urlError.Err
		}
		return fmt.Errorf("Finance could not be reached: %w", err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode < 200 || response.StatusCode > 299 {
		status := &StatusError{Status: response.StatusCode}
		var envelope struct {
			Error struct{ Code, Message string } `json:"error"`
		}
		if json.Unmarshal(raw, &envelope) == nil {
			status.Code, status.Message = envelope.Error.Code, envelope.Error.Message
		}
		return status
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("Finance returned an unreadable answer: %w", err)
	}
	return nil
}
