// Package monitor watches the Dashboard's runtime status and tells the owner,
// through the Notifications service, when a check breaks or recovers.
//
// The rules that decide whether something is ok, a warning, or a problem live
// in the Dashboard (apps/dashboard/src/lib/runtime-status.ts) and are served
// at GET /api/status. The monitor only compares those states over time; it
// never re-implements them.
package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// State is a check's state as the Dashboard reports it.
type State string

const (
	OK      State = "ok"
	Warning State = "warning"
	Problem State = "problem"
)

// parseState maps a reported state to one the monitor acts on. Anything the
// Dashboard may add later that is neither a warning nor a problem (such as a
// neutral state) needs no attention, like ok.
func parseState(value string) State {
	switch State(value) {
	case Problem, Warning:
		return State(value)
	}
	return OK
}

// Check is one check of the runtime status (StatusCheck in
// apps/dashboard/src/lib/runtime-status.ts); facts and parts are not needed.
type Check struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	State   string `json:"state"`
	Summary string `json:"summary"`
	Action  string `json:"action"`
}

// Status is the runtime status (RuntimeStatus) served at GET /api/status.
type Status struct {
	CheckedAt time.Time `json:"checkedAt"`
	Checks    []Check   `json:"checks"`
}

// StatusSource reads the current runtime status.
type StatusSource interface {
	Fetch(ctx context.Context) (Status, error)
}

// StatusClient reads the runtime status from the Dashboard.
type StatusClient struct {
	URL  string
	HTTP *http.Client
	// Timeout bounds one read; zero means 10 s.
	Timeout time.Duration
}

// DefaultStatusTimeout bounds one read of the runtime status by default.
const DefaultStatusTimeout = 10 * time.Second

// Fetch reads and validates the runtime status. Any failure (no answer, an
// error status, a payload that is not a runtime status) is an error: the
// status is unreachable.
func (c *StatusClient) Fetch(ctx context.Context) (Status, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultStatusTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL, nil)
	if err != nil {
		return Status{}, err
	}
	request.Header.Set("accept", "application/json")
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return Status{}, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return Status{}, err
	}
	if response.StatusCode != http.StatusOK {
		return Status{}, fmt.Errorf("status endpoint returned %d", response.StatusCode)
	}
	var status Status
	if err := json.Unmarshal(raw, &status); err != nil {
		return Status{}, fmt.Errorf("status endpoint returned no runtime status: %w", err)
	}
	if len(status.Checks) == 0 {
		return Status{}, errors.New("status endpoint returned no checks")
	}
	for _, check := range status.Checks {
		if strings.TrimSpace(check.ID) == "" || strings.TrimSpace(check.Title) == "" {
			return Status{}, errors.New("status endpoint returned a check without an id or title")
		}
	}
	return status, nil
}
