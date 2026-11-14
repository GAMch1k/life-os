package webpush

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Subscription is where one browser receives pushes: its push service
// endpoint and its keys, base64url, as PushSubscription.toJSON() gives
// them.
type Subscription struct {
	Endpoint string
	P256dh   string
	Auth     string
}

// Urgency tells the push service how soon the browser should be woken for
// a message (RFC 8030 section 5.3); on a phone, low urgency may wait for
// the device to wake up by itself.
type Urgency string

const (
	UrgencyVeryLow Urgency = "very-low"
	UrgencyLow     Urgency = "low"
	UrgencyNormal  Urgency = "normal"
	UrgencyHigh    Urgency = "high"
)

// Message is one push: the plaintext payload (encrypted on the way), how
// long the push service keeps it for a browser that is offline, its
// urgency, and an optional topic: a newer message with the same topic
// replaces one the push service has not delivered yet.
type Message struct {
	Payload []byte
	TTL     time.Duration
	Urgency Urgency
	Topic   string
}

// topicPattern is RFC 8030's topic: at most 32 characters of the base64url
// alphabet.
var topicPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// Sender signs and sends pushes with one VAPID key pair and subject.
type Sender struct {
	keys    *Keys
	subject string
	client  *http.Client
	now     func() time.Time
}

// NewSender returns a Sender. subject is a mailto: address or an https URL
// (ValidSubject). client sends the requests; nil uses one that never follows
// redirects. Each push is bounded by its context.
func NewSender(keys *Keys, subject string, client *http.Client) (*Sender, error) {
	if keys == nil {
		return nil, errors.New("web push needs a VAPID key pair")
	}
	if !ValidSubject(subject) {
		return nil, errors.New("the VAPID subject must be a mailto: address or an https URL")
	}
	if client == nil {
		client = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	return &Sender{keys: keys, subject: subject, client: client, now: time.Now}, nil
}

// PublicKey is the VAPID public key browsers subscribe with.
func (s *Sender) PublicKey() string { return s.keys.PublicKey() }

// StatusError is a push service's refusal.
type StatusError struct {
	StatusCode int
	// Text is the start of the push service's answer, on one line.
	Text string
}

// HTTPStatus is the push service's status, which the log keeps instead of
// its answer (pkg/diagnostics.ErrorCode).
func (e *StatusError) HTTPStatus() int { return e.StatusCode }

func (e *StatusError) Error() string {
	message := fmt.Sprintf("push service answered %d %s", e.StatusCode, http.StatusText(e.StatusCode))
	if e.Text != "" {
		message += ": " + e.Text
	}
	return message
}

// IsGone reports whether the push service says the subscription no longer
// exists (404 or 410): the browser unsubscribed, its data was cleared, or
// the subscription expired. It never comes back; forget it.
func IsGone(err error) bool {
	var status *StatusError
	return errors.As(err, &status) && (status.StatusCode == http.StatusNotFound || status.StatusCode == http.StatusGone)
}

// IsRetryable reports whether a later attempt may succeed: the push service
// was unreachable or timed out, limits the rate (429), or failed (5xx).
// Any other refusal (400, 401, 403, 413, a redirect) is permanent for this
// message.
func IsRetryable(err error) bool {
	var status *StatusError
	if !errors.As(err, &status) {
		var invalid invalidError
		return !errors.As(err, &invalid)
	}
	return status.StatusCode == http.StatusTooManyRequests || status.StatusCode == http.StatusRequestTimeout || status.StatusCode >= 500
}

// invalidError is a message or subscription that cannot be sent at all,
// such as a malformed key; no retry helps.
type invalidError struct{ err error }

func (e invalidError) Error() string { return e.err.Error() }
func (e invalidError) Unwrap() error { return e.err }

// errorTextLimit bounds how much of a refusal is read.
const errorTextLimit = 512

// Send encrypts message for subscription and posts it to its push service.
// It returns nil once the push service accepted it (2xx), a *StatusError
// when it refused, and another error when it could not be reached.
func (s *Sender) Send(ctx context.Context, subscription Subscription, message Message) error {
	p256dh, err := DecodeBase64URL(subscription.P256dh)
	if err != nil {
		return invalidError{errors.New("the subscription's p256dh key is not base64url")}
	}
	auth, err := DecodeBase64URL(subscription.Auth)
	if err != nil {
		return invalidError{errors.New("the subscription's auth secret is not base64url")}
	}
	if message.Topic != "" && !topicPattern.MatchString(message.Topic) {
		return invalidError{errors.New("a push topic is at most 32 base64url characters")}
	}
	body, err := Encrypt(message.Payload, p256dh, auth)
	if err != nil {
		return invalidError{err}
	}
	authorization, err := s.keys.authorization(subscription.Endpoint, s.subject, s.now())
	if err != nil {
		return invalidError{err}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, subscription.Endpoint, bytes.NewReader(body))
	if err != nil {
		return invalidError{fmt.Errorf("build the push request: %w", err)}
	}
	req.Header.Set("Authorization", authorization)
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("TTL", strconv.FormatInt(int64(max(0, message.TTL/time.Second)), 10))
	if message.Urgency != "" {
		req.Header.Set("Urgency", string(message.Urgency))
	}
	if message.Topic != "" {
		req.Header.Set("Topic", message.Topic)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		// Only the push service's host: the endpoint's path identifies the
		// subscription and is kept out of logs and stored errors.
		var urlError *url.Error
		if errors.As(err, &urlError) {
			err = urlError.Err
		}
		return fmt.Errorf("push service %s unreachable: %w", req.URL.Host, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
		return nil
	}
	return &StatusError{StatusCode: resp.StatusCode, Text: errorText(resp.Body)}
}

// errorText is the start of a refusal's body on one line, or empty.
func errorText(body io.Reader) string {
	raw, _ := io.ReadAll(io.LimitReader(body, errorTextLimit))
	if !utf8.Valid(raw) {
		return ""
	}
	return strings.Join(strings.Fields(string(raw)), " ")
}

// ServiceHost is the push service of an endpoint, such as
// fcm.googleapis.com, or empty. The rest of the endpoint identifies the
// subscription and stays out of logs.
func ServiceHost(endpoint string) string {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return ""
	}
	return parsed.Hostname()
}
