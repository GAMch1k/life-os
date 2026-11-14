package delivery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

// NtfyConfig configures the ntfy channel: the self-hosted ntfy server that
// pushes notifications to the owner's phone (docs/operations/ntfy.md).
type NtfyConfig struct {
	// URL is the ntfy server as this service reaches it on the private
	// network, such as http://ntfy:80.
	URL string
	// Topic is the one private topic the owner's phone subscribes to.
	Topic string
	// Token is the publisher's access token, allowed to write to Topic only.
	// It is a secret: never logged or put in an error.
	Token string
	// LinkBaseURL is where the phone opens Life OS, such as
	// https://lifeos.example.com. Tapping a notification opens its link
	// there, or the notification list when it has none.
	LinkBaseURL string
	// Client sends the requests; nil uses one that never follows redirects.
	// Each attempt is bounded by the worker's send timeout (its context).
	Client *http.Client
}

// NtfyChannel publishes each notification to one ntfy topic with ntfy's
// JSON publish API. Importance becomes the ntfy priority, the category's
// domain a tag (an emoji in the app), and the link the tap target.
type NtfyChannel struct {
	endpoint string
	topic    string
	token    string
	linkBase string
	client   *http.Client
}

// NtfyFallbackLink is opened when a notification has no link of its own.
const NtfyFallbackLink = "/notifications"

// ntfyMessageLimit bounds the message in bytes. ntfy turns a message over
// its 4 KiB limit into an attachment, which this server refuses, and caps a
// JSON request at 8 KiB; the title adds at most 800 bytes.
const ntfyMessageLimit = 3500

// ntfyErrorLimit bounds how much of an error response is read.
const ntfyErrorLimit = 1024

// ntfyTopic is ntfy's topic name rule.
var ntfyTopic = regexp.MustCompile(`^[-_A-Za-z0-9]{1,64}$`)

// ntfyTags maps a category's domain (its first part) to an ntfy tag. Tags
// that are emoji short codes show as the emoji before the title.
var ntfyTags = map[string]string{
	"calendar": "calendar",
	"inbox":    "inbox_tray",
	"status":   "warning",
	"test":     "test_tube",
	"tasks":    "ballot_box_with_check",
	"finance":  "moneybag",
}

// NewNtfy returns the ntfy channel, or an error naming what is missing.
func NewNtfy(cfg NtfyConfig) (*NtfyChannel, error) {
	var problems []error
	server, err := url.Parse(strings.TrimSpace(cfg.URL))
	if cfg.URL == "" || err != nil || (server.Scheme != "http" && server.Scheme != "https") || server.Host == "" {
		problems = append(problems, errors.New("NOTIFICATIONS_NTFY_URL must be the ntfy server's http(s) URL, such as http://ntfy:80"))
	}
	if !ntfyTopic.MatchString(cfg.Topic) {
		problems = append(problems, errors.New("NOTIFICATIONS_NTFY_TOPIC must be a topic name of 1-64 letters, digits, '-', or '_'"))
	}
	if cfg.Token == "" {
		problems = append(problems, errors.New("NOTIFICATIONS_NTFY_TOKEN must be the publisher's access token"))
	}
	base, err := url.Parse(strings.TrimSpace(cfg.LinkBaseURL))
	if cfg.LinkBaseURL == "" || err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		problems = append(problems, errors.New("NOTIFICATIONS_LINK_BASE_URL must be Life OS's public URL, such as https://lifeos.example.com"))
	}
	if len(problems) > 0 {
		return nil, errors.Join(problems...)
	}
	client := cfg.Client
	if client == nil {
		client = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	// JSON is published to the server root; the topic is in the body.
	server.Path, server.RawQuery, server.Fragment = "/", "", ""
	return &NtfyChannel{
		endpoint: server.String(),
		topic:    cfg.Topic,
		token:    cfg.Token,
		linkBase: strings.TrimRight(strings.TrimSpace(cfg.LinkBaseURL), "/"),
		client:   client,
	}, nil
}

func (*NtfyChannel) Name() string { return "ntfy" }

// String never shows the token.
func (c *NtfyChannel) String() string { return "ntfy(" + c.endpoint + c.topic + ")" }

// NtfyPriority is the ntfy priority for an importance: low arrives silently
// (2), medium with the default sound (3), and high at max priority (5),
// which the owner may let through Do Not Disturb.
func NtfyPriority(importance string) int {
	switch importance {
	case "low":
		return 2
	case "high":
		return 5
	default:
		return 3
	}
}

// NtfyTags are the ntfy tags for a category, such as [calendar] for
// calendar.reminder; none for a domain without an emoji.
func NtfyTags(category string) []string {
	domain, _, _ := strings.Cut(category, ".")
	if tag, ok := ntfyTags[domain]; ok {
		return []string{tag}
	}
	return nil
}

// ntfyMessage is ntfy's JSON publish request. Markdown stays off: bodies are
// plain text.
type ntfyMessage struct {
	Topic    string   `json:"topic"`
	Title    string   `json:"title"`
	Message  string   `json:"message"`
	Priority int      `json:"priority"`
	Tags     []string `json:"tags,omitempty"`
	Click    string   `json:"click"`
}

func (c *NtfyChannel) message(n Notification) ntfyMessage {
	message := n.Body
	if strings.TrimSpace(message) == "" {
		message = n.Title
	}
	link := n.Link
	if link == "" {
		link = NtfyFallbackLink
	}
	return ntfyMessage{
		Topic:    c.topic,
		Title:    n.Title,
		Message:  truncateBytes(message, ntfyMessageLimit),
		Priority: NtfyPriority(n.Importance),
		Tags:     NtfyTags(n.Category),
		Click:    c.linkBase + link,
	}
}

// Send publishes n. A request ntfy refuses (400, 401, 403, 404, and other
// 4xx) is permanent: the configuration or the message is wrong, and a retry
// would be refused again. Rate limiting (429), a timeout (408), server
// errors, and an unreachable server are retried.
func (c *NtfyChannel) Send(ctx context.Context, n Notification) error {
	var body bytes.Buffer
	encoder := json.NewEncoder(&body)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(c.message(n)); err != nil {
		return Permanent(fmt.Errorf("encode the ntfy message: %w", err))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, &body)
	if err != nil {
		return Permanent(fmt.Errorf("build the ntfy request: %w", err))
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.client.Do(req)
	if err != nil {
		// The error names the URL, never the headers.
		return fmt.Errorf("ntfy is unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
		return nil
	}
	err = &ntfyRefusal{status: resp.StatusCode, text: ntfyErrorText(resp)}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode == http.StatusRequestTimeout, resp.StatusCode >= 500:
		return err
	default:
		return Permanent(err)
	}
}

// ntfyRefusal is ntfy's refusal: its status, which the log keeps
// (pkg/diagnostics.ErrorCode), and its words, which the delivery's last
// error keeps for Status.
type ntfyRefusal struct {
	status int
	text   string
}

func (e *ntfyRefusal) Error() string {
	return fmt.Sprintf("ntfy refused the notification: %d %s", e.status, e.text)
}

func (e *ntfyRefusal) HTTPStatus() int { return e.status }

// ntfyErrorText is ntfy's own error (`{"code":40101,"error":"unauthorized"}`)
// or the start of the response body.
func ntfyErrorText(resp *http.Response) string {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, ntfyErrorLimit))
	var parsed struct {
		Code  int    `json:"code"`
		Error string `json:"error"`
	}
	if json.Unmarshal(raw, &parsed) == nil && parsed.Error != "" {
		if parsed.Code != 0 {
			return fmt.Sprintf("%s (ntfy code %d)", parsed.Error, parsed.Code)
		}
		return parsed.Error
	}
	if text := strings.TrimSpace(string(raw)); text != "" && utf8.ValidString(text) {
		return text
	}
	return http.StatusText(resp.StatusCode)
}

// truncateBytes shortens s to at most limit bytes on a character boundary,
// ending it with an ellipsis.
func truncateBytes(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	const ellipsis = "…"
	cut := limit - len(ellipsis)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.TrimRight(s[:cut], " \t\r\n") + ellipsis
}
