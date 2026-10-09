package processor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"
)

// Usage is the Codex account's usage as reported by the Codex CLI itself
// through its app-server protocol (`account/read` and
// `account/rateLimits/read`, see `codex app-server generate-json-schema`).
// The protocol is marked experimental by Codex; when a read fails, the usage
// is reported as unavailable rather than estimated.
type Usage struct {
	CheckedAt time.Time `json:"checkedAt"`
	// AuthMode is the account type, for example "chatgpt" (subscription
	// sign-in) or "apiKey".
	AuthMode string       `json:"authMode,omitempty"`
	PlanType string       `json:"planType,omitempty"`
	Limits   []UsageLimit `json:"limits"`
	// ResetCreditsAvailable counts one-off "full reset" grants on the account.
	ResetCreditsAvailable *int `json:"resetCreditsAvailable,omitempty"`
}

// UsageLimit is one metered bucket, for example "codex".
type UsageLimit struct {
	ID string `json:"id"`
	// Name and Model are the backend's labels for the bucket, when given.
	Name string `json:"name,omitempty"`
	// Model is the bucket's normal model slug, when given.
	Model string `json:"model,omitempty"`
	// Reached is set when the backend reports this limit as exhausted, for
	// example "rate_limit_reached".
	Reached string        `json:"reached,omitempty"`
	Windows []UsageWindow `json:"windows"`
	// HasCredits reports purchased credits for this bucket, when known.
	HasCredits *bool `json:"hasCredits,omitempty"`
}

type UsageWindow struct {
	UsedPercent   float64    `json:"usedPercent"`
	WindowMinutes *int       `json:"windowMinutes,omitempty"`
	ResetsAt      *time.Time `json:"resetsAt,omitempty"`
}

// Usage starts `codex app-server` over stdio, reads the account and its rate
// limits, and stops it. It uses no model quota.
func (c CodexCLI) Usage(ctx context.Context) (Usage, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := c.command(ctx, "app-server")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return Usage{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Usage{}, err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return Usage{}, fmt.Errorf("start Codex app-server: %w", err)
	}
	defer func() {
		_ = stdin.Close()
		killGroup(cmd)
		_ = cmd.Wait()
	}()
	usage, err := readUsage(stdout, stdin, time.Now().UTC())
	if err != nil && ctx.Err() != nil {
		return Usage{}, fmt.Errorf("read Codex usage: %w", ctx.Err())
	}
	return usage, err
}

type rpcMessage struct {
	ID     *int            `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

// readUsage speaks the app-server JSON-RPC exchange: initialize, then read the
// account (id 2) and its rate limits (id 3). Notifications are ignored.
func readUsage(from io.Reader, to io.Writer, now time.Time) (Usage, error) {
	send := func(message map[string]any) error {
		message["jsonrpc"] = "2.0"
		raw, err := json.Marshal(message)
		if err != nil {
			return err
		}
		_, err = to.Write(append(raw, '\n'))
		return err
	}
	if err := send(map[string]any{"id": 1, "method": "initialize", "params": map[string]any{"clientInfo": map[string]string{"name": "life-os-inbox-processor", "version": "0.1.0"}}}); err != nil {
		return Usage{}, err
	}
	var account, limits json.RawMessage
	scanner := bufio.NewScanner(from)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var message rpcMessage
		if json.Unmarshal(scanner.Bytes(), &message) != nil || message.ID == nil {
			continue
		}
		if len(message.Error) > 0 && string(message.Error) != "null" {
			return Usage{}, fmt.Errorf("Codex app-server request %d failed: %s", *message.ID, message.Error)
		}
		switch *message.ID {
		case 1:
			for _, next := range []map[string]any{
				{"method": "initialized"},
				{"id": 2, "method": "account/read", "params": map[string]any{}},
				{"id": 3, "method": "account/rateLimits/read"},
			} {
				if err := send(next); err != nil {
					return Usage{}, err
				}
			}
		case 2:
			account = message.Result
		case 3:
			limits = message.Result
		}
		if account != nil && limits != nil {
			return parseUsage(account, limits, now)
		}
	}
	if err := scanner.Err(); err != nil {
		return Usage{}, err
	}
	return Usage{}, errors.New("Codex app-server closed before reporting usage")
}

type rateLimitWindow struct {
	UsedPercent        float64 `json:"usedPercent"`
	WindowDurationMins *int    `json:"windowDurationMins"`
	ResetsAt           *int64  `json:"resetsAt"`
}

type rateLimitSnapshot struct {
	LimitID         string           `json:"limitId"`
	LimitName       string           `json:"limitName"`
	NormalModelSlug string           `json:"normalModelSlug"`
	Primary         *rateLimitWindow `json:"primary"`
	Secondary       *rateLimitWindow `json:"secondary"`
	Credits         *struct {
		HasCredits bool `json:"hasCredits"`
	} `json:"credits"`
	PlanType             string `json:"planType"`
	RateLimitReachedType string `json:"rateLimitReachedType"`
}

func parseUsage(accountRaw, limitsRaw json.RawMessage, now time.Time) (Usage, error) {
	var account struct {
		Account *struct {
			Type     string `json:"type"`
			PlanType string `json:"planType"`
		} `json:"account"`
	}
	var limits struct {
		RateLimits            *rateLimitSnapshot           `json:"rateLimits"`
		RateLimitsByLimitID   map[string]rateLimitSnapshot `json:"rateLimitsByLimitId"`
		RateLimitResetCredits *struct {
			AvailableCount int `json:"availableCount"`
		} `json:"rateLimitResetCredits"`
	}
	if err := json.Unmarshal(accountRaw, &account); err != nil {
		return Usage{}, fmt.Errorf("decode Codex account: %w", err)
	}
	if err := json.Unmarshal(limitsRaw, &limits); err != nil {
		return Usage{}, fmt.Errorf("decode Codex rate limits: %w", err)
	}
	usage := Usage{CheckedAt: now, Limits: []UsageLimit{}}
	if account.Account != nil {
		usage.AuthMode, usage.PlanType = account.Account.Type, account.Account.PlanType
	}
	if limits.RateLimitResetCredits != nil {
		count := limits.RateLimitResetCredits.AvailableCount
		usage.ResetCreditsAvailable = &count
	}
	snapshots := limits.RateLimitsByLimitID
	if len(snapshots) == 0 && limits.RateLimits != nil {
		snapshots = map[string]rateLimitSnapshot{limits.RateLimits.LimitID: *limits.RateLimits}
	}
	for id, snapshot := range snapshots {
		if usage.PlanType == "" {
			usage.PlanType = snapshot.PlanType
		}
		limit := UsageLimit{ID: id, Name: snapshot.LimitName, Model: snapshot.NormalModelSlug, Reached: snapshot.RateLimitReachedType, Windows: []UsageWindow{}}
		if snapshot.Credits != nil {
			hasCredits := snapshot.Credits.HasCredits
			limit.HasCredits = &hasCredits
		}
		for _, window := range []*rateLimitWindow{snapshot.Primary, snapshot.Secondary} {
			if window == nil {
				continue
			}
			parsed := UsageWindow{UsedPercent: window.UsedPercent, WindowMinutes: window.WindowDurationMins}
			if window.ResetsAt != nil {
				at := time.Unix(*window.ResetsAt, 0).UTC()
				parsed.ResetsAt = &at
			}
			limit.Windows = append(limit.Windows, parsed)
		}
		usage.Limits = append(usage.Limits, limit)
	}
	// The main "codex" bucket first, then the rest by id.
	sort.Slice(usage.Limits, func(i, j int) bool {
		if (usage.Limits[i].ID == "codex") != (usage.Limits[j].ID == "codex") {
			return usage.Limits[i].ID == "codex"
		}
		return usage.Limits[i].ID < usage.Limits[j].ID
	})
	return usage, nil
}
