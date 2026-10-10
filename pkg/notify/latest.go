package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Sent is a notification as the Notifications service stored it.
type Sent struct {
	Title      string     `json:"title"`
	Importance Importance `json:"importance"`
	Category   string     `json:"category"`
	Source     string     `json:"source"`
	DedupeKey  string     `json:"dedupeKey"`
	OccurredAt time.Time  `json:"occurredAt"`
	CreatedAt  time.Time  `json:"createdAt"`
}

// Latest returns the newest notification this client's service sent in
// category, or nil when it has sent none there. category is matched like the
// list filter: the category itself and every category below it
// (status.processing also matches status.processing.*). A producer that keeps
// its state in memory reads its own history with it after a restart, for
// example to tell whether an episode it notified has ended. A disabled Client
// returns nil, nil. Latest retries like Send.
func (c *Client) Latest(ctx context.Context, category string) (*Sent, error) {
	if !c.Enabled() {
		return nil, nil
	}
	query := url.Values{"category": {category}, "source": {sourceName(c.Service)}, "limit": {"1"}}
	target := c.URL + "/v1/notifications?" + query.Encode()

	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var (
		sent *Sent
		err  error
	)
	for _, delay := range retryDelays {
		if delay > 0 {
			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("read latest %s notification: %w", category, errors.Join(err, ctx.Err()))
			case <-time.After(delay):
			}
		}
		sent, err = c.latest(ctx, target)
		var status *StatusError
		if err == nil || (errors.As(err, &status) && !status.Temporary()) {
			break
		}
	}
	if err != nil {
		return nil, fmt.Errorf("read latest %s notification: %w", category, err)
	}
	return sent, nil
}

func (c *Client) latest(ctx context.Context, target string) (*Sent, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("accept", "application/json")
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		status := &StatusError{Status: response.StatusCode}
		var envelope struct {
			Error struct{ Code, Message string } `json:"error"`
		}
		if json.Unmarshal(raw, &envelope) == nil {
			status.Code, status.Message = envelope.Error.Code, envelope.Error.Message
		}
		return nil, status
	}
	var list struct {
		Items []Sent `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("decode notifications list: %w", err)
	}
	if len(list.Items) == 0 {
		return nil, nil
	}
	return &list.Items[0], nil
}

// sourceName is the source Notifications derives from the actor
// system:<service>: the name in lowercase with '_' as '-'.
func sourceName(service string) string {
	return strings.Trim(strings.ReplaceAll(strings.ToLower(service), "_", "-"), ".-")
}
