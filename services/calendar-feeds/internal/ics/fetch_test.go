package ics

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestPublicAddress(t *testing.T) {
	for address, want := range map[string]bool{
		"8.8.8.8": true, "1.1.1.1": true, "2606:4700:4700::1111": true, "185.199.108.153": true,
		"127.0.0.1": false, "127.8.8.8": false, "::1": false, "0.0.0.0": false, "::": false,
		"10.1.2.3": false, "172.16.0.1": false, "172.31.255.254": false, "192.168.1.10": false, // private, incl. Docker networks
		"169.254.169.254": false, "fe80::1": false, // link-local, cloud metadata
		"fd00:ec2::254": false, "fc00::1": false, // unique local
		"100.64.0.1": false, "192.0.2.1": false, "198.18.0.1": false, "240.0.0.1": false, "255.255.255.255": false,
		"224.0.0.1": false, "ff02::1": false,
		"::ffff:10.0.0.1": false, "::ffff:8.8.8.8": true, // IPv4-mapped
		"64:ff9b::a00:1": false, "2002:a00:1::": false, "2001:db8::1": false,
	} {
		if got := PublicAddress(netip.MustParseAddr(address)); got != want {
			t.Errorf("PublicAddress(%s) = %v, want %v", address, got, want)
		}
	}
}

func TestNormalizeURL(t *testing.T) {
	strict := NewFetcher(0, 0, false)
	for raw, want := range map[string]string{
		"https://service.example.org/api.php?m=x&r=ical": "https://service.example.org/api.php?m=x&r=ical",
		"  webcal://calendar.example.org/feed.ics ":      "https://calendar.example.org/feed.ics",
		"HTTPS://Example.org/a.ics":                      "https://Example.org/a.ics",
	} {
		if got, err := strict.NormalizeURL(raw); err != nil || got != want {
			t.Errorf("NormalizeURL(%q) = %q %v, want %q", raw, got, err, want)
		}
	}
	for _, raw := range []string{
		"http://example.org/feed.ics", "ftp://example.org/feed.ics", "file:///etc/passwd", "feed.ics", "https://",
		"https://127.0.0.1/feed.ics", "https://[::1]/feed.ics", "https://169.254.169.254/latest/meta-data", "https://10.0.0.5:8443/x",
		"https://localhost/feed.ics", "https://calendar.localhost/x", "://bad",
	} {
		if got, err := strict.NormalizeURL(raw); err == nil {
			t.Errorf("NormalizeURL(%q) = %q, want an error", raw, got)
		}
	}
	// Development mode accepts http and local addresses.
	if got, err := NewFetcher(0, 0, true).NormalizeURL("http://127.0.0.1:8080/feed.ics"); err != nil || got != "http://127.0.0.1:8080/feed.ics" {
		t.Errorf("development NormalizeURL = %q %v", got, err)
	}
}

// testFetcher talks plain HTTP to httptest servers (which listen on
// loopback) but connects only to the given ports, so the address check can
// be exercised on real connections.
func testFetcher(t *testing.T, allowed ...string) *Fetcher {
	t.Helper()
	f := NewFetcher(1<<20, 2*time.Second, false)
	f.allowHTTP = true
	ports := map[uint16]bool{}
	for _, raw := range allowed {
		parsed, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		port, _ := strconv.Atoi(parsed.Port())
		ports[uint16(port)] = true
	}
	f.allowDial = func(address netip.AddrPort) bool { return ports[address.Port()] }
	return f
}

func noURL(t *testing.T, err error, secret string) {
	t.Helper()
	if err == nil {
		t.Fatal("want an error")
	}
	if _, ok := err.(*Error); !ok {
		t.Errorf("error %T is not a storable *Error", err)
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "://") {
		t.Errorf("error leaks the URL: %q", err)
	}
}

func TestFetchRefusesLoopbackEvenOverHTTPS(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("BEGIN:VCALENDAR")) }))
	defer server.Close()
	strict := NewFetcher(0, 2*time.Second, false)
	_, err := strict.Fetch(context.Background(), server.URL+"/feed.ics?street=secret-street", "", "")
	noURL(t, err, "secret-street")
	if !strings.Contains(err.Error(), "private, loopback, or link-local") {
		t.Fatalf("err = %v", err)
	}
	// http is refused before any connection.
	if _, err := strict.Fetch(context.Background(), "http://example.org/feed.ics", "", ""); err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("http: %v", err)
	}
}

func TestFetchRechecksRedirects(t *testing.T) {
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("internal data")) }))
	defer internal.Close()
	public := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/to-internal":
			http.Redirect(w, r, internal.URL+"/admin?token=secret-token", http.StatusFound)
		case "/to-file":
			http.Redirect(w, r, "file:///etc/passwd", http.StatusFound)
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		case "/to-ok":
			http.Redirect(w, r, "/ok", http.StatusMovedPermanently)
		default:
			_, _ = w.Write([]byte("BEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n"))
		}
	}))
	defer public.Close()
	f := testFetcher(t, public.URL)
	_, err := f.Fetch(context.Background(), public.URL+"/to-internal", "", "")
	noURL(t, err, "secret-token")
	if !strings.Contains(err.Error(), "private, loopback, or link-local") {
		t.Fatalf("redirect to an internal address: %v", err)
	}
	if _, err := f.Fetch(context.Background(), public.URL+"/to-file", "", ""); err == nil {
		t.Fatal("redirect to file:// followed")
	}
	if _, err := f.Fetch(context.Background(), public.URL+"/loop", "", ""); err == nil || !strings.Contains(err.Error(), "redirected too many times") {
		t.Fatalf("redirect loop: %v", err)
	}
	if result, err := f.Fetch(context.Background(), public.URL+"/to-ok", "", ""); err != nil || !strings.HasPrefix(string(result.Body), "BEGIN:VCALENDAR") {
		t.Fatalf("allowed redirect: %v %q", err, result.Body)
	}
}

func TestFetchLimitsSizeAndTime(t *testing.T) {
	big := strings.Repeat("x", 2<<20)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/big":
			_, _ = w.Write([]byte(big))
		case "/big-chunked":
			for range 40 {
				_, _ = w.Write([]byte(big[:64<<10]))
				w.(http.Flusher).Flush()
			}
		case "/slow":
			time.Sleep(3 * time.Second)
		case "/missing":
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	f := testFetcher(t, server.URL)
	for _, path := range []string{"/big", "/big-chunked"} {
		if _, err := f.Fetch(context.Background(), server.URL+path, "", ""); err == nil || !strings.Contains(err.Error(), "larger than 1048576 bytes") {
			t.Errorf("%s: %v", path, err)
		}
	}
	start := time.Now()
	if _, err := f.Fetch(context.Background(), server.URL+"/slow?k=secret-slow", "", ""); err == nil || !strings.Contains(err.Error(), "did not answer in time") {
		t.Errorf("slow: %v", err)
	} else {
		noURL(t, err, "secret-slow")
	}
	if elapsed := time.Since(start); elapsed > 2500*time.Millisecond {
		t.Errorf("timeout took %v", elapsed)
	}
	if _, err := f.Fetch(context.Background(), server.URL+"/missing", "", ""); err == nil || err.Error() != "the feed answered HTTP 404" {
		t.Errorf("404: %v", err)
	}
}

func TestFetchSendsConditionalValidators(t *testing.T) {
	const etag = `"v1"`
	modified := time.Date(2026, 10, 9, 8, 0, 0, 0, time.UTC).Format(http.TimeFormat)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == etag || r.Header.Get("If-Modified-Since") == modified {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		w.Header().Set("Last-Modified", modified)
		_, _ = w.Write([]byte("BEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n"))
	}))
	defer server.Close()
	f := testFetcher(t, server.URL)
	first, err := f.Fetch(context.Background(), server.URL, "", "")
	if err != nil || first.NotModified || first.ETag != etag || first.LastModified != modified || len(first.Body) == 0 {
		t.Fatalf("first = %+v %v", first, err)
	}
	again, err := f.Fetch(context.Background(), server.URL, first.ETag, "")
	if err != nil || !again.NotModified || again.ETag != etag || again.Body != nil {
		t.Fatalf("conditional = %+v %v", again, err)
	}
	byDate, err := f.Fetch(context.Background(), server.URL, "", first.LastModified)
	if err != nil || !byDate.NotModified || byDate.LastModified != modified {
		t.Fatalf("by date = %+v %v", byDate, err)
	}
}
