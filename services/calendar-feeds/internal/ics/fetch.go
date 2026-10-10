// Package ics is the calendar-feeds ICS/iCal adapter: it downloads a feed
// safely, parses it, and normalizes its events into the items synced to
// Calendar. It knows nothing about storage or Calendar; the feeds package
// filters the items and syncs them.
package ics

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// Fetch limits.
const (
	DefaultMaxBytes = 5 << 20
	DefaultTimeout  = 30 * time.Second
	maxRedirects    = 5
	userAgent       = "LifeOS-Calendar/1.0 (+ICS feed refresh)"
)

// Fetcher downloads feeds. Unless AllowPrivate is set (development and tests
// only), it accepts only https URLs and connects only to public addresses: a
// host that resolves to a loopback, private, link-local (including cloud
// metadata), or otherwise non-public address is refused when the connection
// is made, so neither DNS tricks nor redirects reach internal services.
type Fetcher struct {
	MaxBytes     int64
	Timeout      time.Duration
	AllowPrivate bool
	client       *http.Client
	// allowHTTP and allowDial default from AllowPrivate; tests narrow them.
	allowHTTP bool
	allowDial func(netip.AddrPort) bool
}

// NewFetcher returns a Fetcher with the given limits (zero means default).
func NewFetcher(maxBytes int64, timeout time.Duration, allowPrivate bool) *Fetcher {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	f := &Fetcher{MaxBytes: maxBytes, Timeout: timeout, AllowPrivate: allowPrivate, allowHTTP: allowPrivate}
	f.allowDial = func(address netip.AddrPort) bool { return allowPrivate || PublicAddress(address.Addr()) }
	dialer := &net.Dialer{Timeout: 10 * time.Second, Control: f.checkDial}
	transport := &http.Transport{
		// Never through an environment proxy: it would connect on our behalf
		// and bypass the address check.
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: timeout,
		MaxIdleConns:          4,
		IdleConnTimeout:       30 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	f.client = &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return errors.New("the feed redirected too many times")
			}
			return f.checkURL(req.URL)
		},
	}
	return f
}

// Result is a download's outcome.
type Result struct {
	// NotModified: the server confirmed the feed is unchanged (304).
	NotModified  bool
	Body         []byte
	ETag         string
	LastModified string
}

// Error is a refresh failure whose message is safe to store and show: it
// never contains the feed URL, which can carry personal data.
type Error struct{ Message string }

func (e *Error) Error() string { return e.Message }

func failure(format string, args ...any) error { return &Error{Message: fmt.Sprintf(format, args...)} }

// NormalizeURL validates a feed URL for storage: webcal:// becomes https://,
// and the URL must be absolute with a host and an allowed scheme.
func (f *Fetcher) NormalizeURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", errors.New("url is not a valid URL")
	}
	if strings.EqualFold(parsed.Scheme, "webcal") || strings.EqualFold(parsed.Scheme, "webcals") {
		parsed.Scheme = "https"
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if err := f.checkURL(parsed); err != nil {
		return "", err
	}
	if !f.AllowPrivate {
		// Refuse literal internal addresses up front; host names are checked
		// when the feed is downloaded.
		if addr, err := netip.ParseAddr(strings.Trim(parsed.Hostname(), "[]")); err == nil && !PublicAddress(addr) {
			return "", errors.New("url must not point to a private, loopback, or link-local address")
		}
		if host := strings.ToLower(parsed.Hostname()); host == "localhost" || strings.HasSuffix(host, ".localhost") {
			return "", errors.New("url must not point to a private, loopback, or link-local address")
		}
	}
	return parsed.String(), nil
}

func (f *Fetcher) checkURL(u *url.URL) error {
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && f.allowHTTP:
	default:
		return failure("the feed URL must use https")
	}
	if u.Hostname() == "" {
		return failure("the feed URL has no host")
	}
	return nil
}

// checkDial runs for every connection attempt with the resolved address,
// so the check applies to the address actually connected to.
func (f *Fetcher) checkDial(_, address string, _ syscall.RawConn) error {
	parsed, err := netip.ParseAddrPort(address)
	if err != nil || !f.allowDial(parsed) {
		return failure("the feed host resolves to a private, loopback, or link-local address, which Life OS does not fetch")
	}
	return nil
}

// nonPublic lists special-purpose ranges beyond what netip classifies.
var nonPublic = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), // carrier-grade NAT
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"), // NAT64 can embed internal IPv4
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"), // 6to4 can embed internal IPv4
}

// PublicAddress reports whether a feed may be downloaded from addr: a globally
// routable unicast address, not loopback, private, link-local (169.254/16,
// where cloud metadata lives, and fe80::/10), unique-local, multicast, or
// another special-purpose range.
func PublicAddress(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsValid() || addr.IsLoopback() || addr.IsPrivate() || addr.IsUnspecified() || addr.IsMulticast() ||
		addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsInterfaceLocalMulticast() || !addr.IsGlobalUnicast() {
		return false
	}
	for _, prefix := range nonPublic {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}

// Fetch downloads rawURL, sending etag and lastModified (when known) as
// conditional-request validators.
func (f *Fetcher) Fetch(ctx context.Context, rawURL, etag, lastModified string) (Result, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return Result{}, failure("the feed URL is not valid")
	}
	if err := f.checkURL(parsed); err != nil {
		return Result{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return Result{}, failure("the feed URL is not valid")
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/calendar, text/plain;q=0.8, */*;q=0.1")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if lastModified != "" {
		req.Header.Set("If-Modified-Since", lastModified)
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return Result{}, describe(err)
	}
	defer resp.Body.Close()
	result := Result{ETag: resp.Header.Get("ETag"), LastModified: resp.Header.Get("Last-Modified")}
	switch {
	case resp.StatusCode == http.StatusNotModified && (etag != "" || lastModified != ""):
		result.NotModified = true
		if result.ETag == "" {
			result.ETag = etag
		}
		if result.LastModified == "" {
			result.LastModified = lastModified
		}
		return result, nil
	case resp.StatusCode != http.StatusOK:
		return Result{}, failure("the feed answered HTTP %d", resp.StatusCode)
	case resp.ContentLength > f.MaxBytes:
		return Result{}, failure("the feed is larger than %d bytes", f.MaxBytes)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, f.MaxBytes+1))
	if err != nil {
		return Result{}, describe(err)
	}
	if int64(len(body)) > f.MaxBytes {
		return Result{}, failure("the feed is larger than %d bytes", f.MaxBytes)
	}
	result.Body = body
	return result, nil
}

// describe turns a transport error into a storable message without the URL
// (url.Error would print it).
func describe(err error) error {
	var own *Error
	if errors.As(err, &own) {
		return own
	}
	var urlError *url.Error
	if errors.As(err, &urlError) {
		err = urlError.Err
		if errors.As(err, &own) {
			return own
		}
	}
	var netError net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netError) && netError.Timeout()):
		return failure("the feed did not answer in time")
	case errors.Is(err, context.Canceled):
		return failure("the refresh was cancelled")
	}
	var dnsError *net.DNSError
	if errors.As(err, &dnsError) {
		return failure("the feed host could not be resolved: %s", dnsError.Name)
	}
	return failure("the feed could not be downloaded: %s", stripURLs(err.Error()))
}

// stripURLs removes anything URL-like from a message.
func stripURLs(message string) string {
	fields := strings.Fields(message)
	for index, field := range fields {
		if strings.Contains(field, "://") {
			fields[index] = "<url>"
		}
	}
	return strings.Join(fields, " ")
}
