// Package fetch downloads web pages for recipe-import, safely: http or https
// only, public addresses only (checked on every connection, so after DNS
// resolution and on every redirect), within a time and size limit, without
// cookies or an environment proxy. The address guard is calendar-feeds'.
package fetch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"syscall"
	"time"

	"golang.org/x/net/html/charset"
)

// Limits.
const (
	DefaultMaxBytes = 5 << 20
	DefaultTimeout  = 10 * time.Second
	maxRedirects    = 5
	// userAgent reads like a browser's: many recipe sites answer a bare
	// client with a blocked page. Life OS identifies itself at the end.
	userAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0 Safari/537.36 LifeOS-RecipeImport/1.0"
)

// Error is a fetch failure in plain words, safe to show the owner.
type Error struct{ Message string }

func (e *Error) Error() string { return e.Message }

func failure(format string, args ...any) error { return &Error{Message: fmt.Sprintf(format, args...)} }

// Page is a downloaded web page.
type Page struct {
	// URL is where the page was found, after redirects.
	URL *url.URL
	// Body is the page as UTF-8.
	Body []byte
}

// Fetcher downloads pages.
type Fetcher struct {
	MaxBytes int64
	Timeout  time.Duration
	client   *http.Client
	// allowHosts may be fetched from any address (the verification stack's
	// fixture server only).
	allowHosts map[string]bool
	// allowDial decides a resolved address; tests narrow it.
	allowDial func(netip.AddrPort) bool
}

// New returns a Fetcher with the given limits (zero means the default).
// allowHosts are host names that skip the address check: for the
// verification stack's fixture server only, never on the deployed stack.
func New(maxBytes int64, timeout time.Duration, allowHosts []string) *Fetcher {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	f := &Fetcher{MaxBytes: maxBytes, Timeout: timeout, allowHosts: map[string]bool{}}
	for _, host := range allowHosts {
		if host = strings.ToLower(strings.TrimSpace(host)); host != "" {
			f.allowHosts[host] = true
		}
	}
	f.allowDial = func(address netip.AddrPort) bool { return PublicAddress(address.Addr()) }
	guarded := &net.Dialer{Timeout: 5 * time.Second, Control: f.checkDial}
	open := &net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{
		// Never through an environment proxy: it would connect on our behalf
		// and bypass the address check.
		Proxy: nil,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, _, _ := net.SplitHostPort(address)
			if f.allowHosts[strings.ToLower(host)] {
				return open.DialContext(ctx, network, address)
			}
			return guarded.DialContext(ctx, network, address)
		},
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: timeout,
		MaxIdleConns:          4,
		IdleConnTimeout:       30 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	f.client = &http.Client{
		Transport: transport,
		Timeout:   timeout,
		// No cookie jar: no site's cookies are kept or sent.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return failure("The page redirected too many times.")
			}
			// Host names are checked again when connecting.
			_, err := f.CheckURL(req.URL.String())
			return err
		},
	}
	return f
}

// CheckURL validates a link before anything is fetched: absolute, http or
// https, with a host, and not a literal private, loopback, or link-local
// address (host names are checked when connecting).
func (f *Fetcher) CheckURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, failure("The link is not a valid URL.")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if err := checkURL(parsed); err != nil {
		return nil, err
	}
	host := strings.ToLower(parsed.Hostname())
	if f.allowHosts[host] {
		return parsed, nil
	}
	if addr, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil && !PublicAddress(addr) {
		return nil, failure("The link points to a private, loopback, or link-local address, which Life OS does not fetch.")
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return nil, failure("The link points to a private, loopback, or link-local address, which Life OS does not fetch.")
	}
	return parsed, nil
}

func checkURL(u *url.URL) error {
	if u.Scheme != "http" && u.Scheme != "https" {
		return failure("The link must start with http:// or https://.")
	}
	if u.Hostname() == "" {
		return failure("The link has no host.")
	}
	return nil
}

// checkDial runs for every connection attempt with the resolved address, so
// the check applies to the address actually connected to.
func (f *Fetcher) checkDial(_, address string, _ syscall.RawConn) error {
	parsed, err := netip.ParseAddrPort(address)
	if err != nil || !f.allowDial(parsed) {
		return failure("The site's address is private, loopback, or link-local, which Life OS does not fetch.")
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

// PublicAddress reports whether a page may be fetched from addr: a globally
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

// Fetch downloads a web page and returns it as UTF-8.
func (f *Fetcher) Fetch(ctx context.Context, raw string) (Page, error) {
	return f.get(ctx, raw, "text/html,application/xhtml+xml;q=0.9,*/*;q=0.5", "The link is not a web page (%s).", "text/html", "application/xhtml+xml")
}

// FetchJSON downloads a JSON document (an API's answer) with the same
// guard and limits as a page.
func (f *Fetcher) FetchJSON(ctx context.Context, raw string) ([]byte, error) {
	page, err := f.get(ctx, raw, "application/json", "The answer is not JSON (%s).", "application/json", "text/json")
	return page.Body, err
}

// get downloads raw when its media type is one of media (or not given);
// otherwise it fails with wrongType naming the type.
func (f *Fetcher) get(ctx context.Context, raw, accept, wrongType string, media ...string) (Page, error) {
	parsed, err := f.CheckURL(raw)
	if err != nil {
		return Page{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return Page{}, failure("The link is not a valid URL.")
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", accept)
	req.Header.Set("Accept-Language", "de,en;q=0.8,ru;q=0.6")
	resp, err := f.client.Do(req)
	if err != nil {
		return Page{}, describe(err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		return Page{}, failure("The site would not show the page to Life OS (HTTP %d).", resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return Page{}, failure("The page answered HTTP %d.", resp.StatusCode)
	case resp.ContentLength > f.MaxBytes:
		return Page{}, failure("The page is larger than %d MB.", f.MaxBytes>>20)
	}
	contentType := resp.Header.Get("Content-Type")
	if kind, _, err := mime.ParseMediaType(contentType); contentType != "" && (err != nil || !slices.Contains(media, kind)) {
		return Page{}, failure(wrongType, strings.SplitN(contentType, ";", 2)[0])
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, f.MaxBytes+1))
	if err != nil {
		return Page{}, describe(err)
	}
	if int64(len(body)) > f.MaxBytes {
		return Page{}, failure("The page is larger than %d MB.", f.MaxBytes>>20)
	}
	// Pages in another charset (windows-1252, for example) become UTF-8.
	if reader, err := charset.NewReader(bytes.NewReader(body), contentType); err == nil {
		if decoded, err := io.ReadAll(reader); err == nil {
			body = decoded
		}
	}
	return Page{URL: resp.Request.URL, Body: body}, nil
}

// describe turns a transport error into plain words.
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
		return failure("The site did not answer in time.")
	case errors.Is(err, context.Canceled):
		return failure("The request was cancelled.")
	}
	var dnsError *net.DNSError
	if errors.As(err, &dnsError) {
		return failure("The site %s could not be found.", dnsError.Name)
	}
	return failure("The page could not be downloaded.")
}
