package processor

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Config keeps model selection operational rather than embedded in Inbox logic.
type Config struct {
	Model        string
	CodexCommand string
	InboxURL     string
	MCPURL       string
	WorkDir      string
	PollInterval time.Duration
	HealthPort   string
	// CodexTimeout bounds one planning run.
	CodexTimeout time.Duration
	// MaxConcurrency limits simultaneous Codex planning runs.
	MaxConcurrency int
	// StaleAfter is the heartbeat age after which an unfinished attempt is
	// considered abandoned and reconciled.
	StaleAfter time.Duration
	// ReasoningEffort is passed to Codex as model_reasoning_effort when set;
	// empty keeps the Codex configuration's default.
	ReasoningEffort string
	// TimeZone is the owner's timezone (LIFE_OS_TIMEZONE, an IANA name such
	// as Europe/Berlin). Relative dates and clock times in captures are
	// interpreted in it. Unset or unknown means UTC.
	TimeZone *time.Location
	// IdeasAt is the minute of the owner's day the daily recipe ideas run is
	// due (LIFE_OS_IDEAS_TIME, HH:MM, default 16:00); -1 turns it off
	// ("off"). The owner can still ask for ideas.
	IdeasAt int
	// IdeasWebSearch is Codex's web_search mode for the ideas job alone
	// (LIFE_OS_IDEAS_WEB_SEARCH: disabled, the default, cached, or live).
	// Capture planning never searches.
	IdeasWebSearch string
	// Warnings are non-fatal configuration problems to log at startup.
	Warnings []string
}

// Profile names the model and reasoning effort, for example
// "gpt-6-luna/low", as recorded on processing attempts.
func (c Config) Profile() string {
	if c.ReasoningEffort == "" {
		return c.Model
	}
	return c.Model + "/" + c.ReasoningEffort
}

func LoadConfig() (Config, error) {
	pollMillis, err := strconv.Atoi(env("LIFE_OS_PROCESSOR_POLL_MILLIS", "1000"))
	if err != nil || pollMillis < 100 {
		return Config{}, fmt.Errorf("LIFE_OS_PROCESSOR_POLL_MILLIS must be an integer of at least 100")
	}
	c := Config{Model: env("LIFE_OS_CODEX_MODEL", "gpt-5.6-luna"), CodexCommand: env("LIFE_OS_CODEX_COMMAND", "codex"), InboxURL: env("INBOX_SERVICE_URL", "http://inbox:8080"), MCPURL: env("MCP_SERVICE_URL", "http://mcp:8080/mcp"), WorkDir: env("LIFE_OS_CODEX_WORKDIR", "."), PollInterval: time.Duration(pollMillis) * time.Millisecond, HealthPort: env("LIFE_OS_PROCESSOR_HEALTH_PORT", "8081")}
	timeout, err := strconv.Atoi(env("LIFE_OS_CODEX_TIMEOUT_SECONDS", "300"))
	if err != nil || timeout < 1 {
		return c, fmt.Errorf("LIFE_OS_CODEX_TIMEOUT_SECONDS must be a positive integer")
	}
	concurrency, err := strconv.Atoi(env("LIFE_OS_CODEX_MAX_CONCURRENCY", "1"))
	if err != nil || concurrency < 1 {
		return c, fmt.Errorf("LIFE_OS_CODEX_MAX_CONCURRENCY must be a positive integer")
	}
	stale, err := strconv.Atoi(env("LIFE_OS_PROCESSOR_STALE_AFTER_SECONDS", "120"))
	if err != nil || stale < 90 {
		return c, fmt.Errorf("LIFE_OS_PROCESSOR_STALE_AFTER_SECONDS must be an integer of at least 90 (three heartbeats)")
	}
	c.CodexTimeout, c.MaxConcurrency, c.StaleAfter = time.Duration(timeout)*time.Second, concurrency, time.Duration(stale)*time.Second
	c.ReasoningEffort = os.Getenv("LIFE_OS_CODEX_REASONING_EFFORT")
	if !effortPattern.MatchString(c.ReasoningEffort) {
		return c, fmt.Errorf("LIFE_OS_CODEX_REASONING_EFFORT must be a lowercase effort name such as none, low, or medium")
	}
	if c.CodexCommand == "" {
		return c, fmt.Errorf("LIFE_OS_CODEX_COMMAND must not be empty")
	}
	if c.TimeZone, err = OwnerTimeZone(os.Getenv("LIFE_OS_TIMEZONE")); err != nil {
		c.Warnings = append(c.Warnings, err.Error()+"; using UTC")
	}
	if c.IdeasAt, err = ideasTime(env("LIFE_OS_IDEAS_TIME", "16:00")); err != nil {
		return c, err
	}
	c.IdeasWebSearch = env("LIFE_OS_IDEAS_WEB_SEARCH", WebSearchDisabled)
	if c.IdeasWebSearch != WebSearchDisabled && c.IdeasWebSearch != WebSearchCached && c.IdeasWebSearch != WebSearchLive {
		return c, fmt.Errorf("LIFE_OS_IDEAS_WEB_SEARCH must be disabled, cached, or live")
	}
	return c, nil
}

// ideasTime reads LIFE_OS_IDEAS_TIME: HH:MM on the owner's clock, or off.
func ideasTime(value string) (int, error) {
	if strings.TrimSpace(value) == "off" {
		return -1, nil
	}
	at, err := time.Parse("15:04", strings.TrimSpace(value))
	if err != nil {
		return 0, fmt.Errorf("LIFE_OS_IDEAS_TIME must be HH:MM, such as 16:00, or off")
	}
	return at.Hour()*60 + at.Minute(), nil
}

// OwnerTimeZone resolves LIFE_OS_TIMEZONE. Unset means UTC. An unknown name
// also falls back to UTC, as in the Dashboard, and is reported, so a typo is
// logged instead of stopping capture processing.
func OwnerTimeZone(name string) (*time.Location, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return time.UTC, nil
	}
	location, ok := loadTimeZone(name)
	if !ok {
		return time.UTC, fmt.Errorf("LIFE_OS_TIMEZONE %q is not an IANA timezone name such as Europe/Berlin", name)
	}
	return location, nil
}

// loadTimeZone resolves an IANA timezone name such as Europe/Berlin. The
// empty name and "Local" (which would silently mean the container's own
// zone) are not timezones here.
func loadTimeZone(name string) (*time.Location, bool) {
	name = strings.TrimSpace(name)
	if name == "" || name == "Local" {
		return nil, false
	}
	location, err := time.LoadLocation(name)
	return location, err == nil
}

var effortPattern = regexp.MustCompile(`^[a-z]*$`)

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
