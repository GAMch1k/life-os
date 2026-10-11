package fetch

import (
	"context"
	"net"
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
		"8.8.8.8": true, "1.1.1.1": true, "2606:4700:4700::1111": true,
		"127.0.0.1": false, "::1": false, "0.0.0.0": false, "::": false,
		"10.1.2.3": false, "172.16.0.1": false, "192.168.1.10": false, // private, incl. Docker networks
		"169.254.169.254": false, "fe80::1": false, // link-local, cloud metadata
		"fd00:ec2::254": false, "fc00::1": false, // unique local
		"100.64.0.1": false, "192.0.2.1": false, "224.0.0.1": false,
		"::ffff:10.0.0.1": false, "::ffff:8.8.8.8": true, // IPv4-mapped
		"64:ff9b::a00:1": false, "2002:a00:1::": false,
	} {
		if got := PublicAddress(netip.MustParseAddr(address)); got != want {
			t.Errorf("PublicAddress(%s) = %v, want %v", address, got, want)
		}
	}
}

func TestCheckURL(t *testing.T) {
	strict := New(0, 0, nil)
	for _, raw := range []string{"https://www.chefkoch.de/rezepte/1", "http://example.org/a", " HTTPS://Example.org/b "} {
		if _, err := strict.CheckURL(raw); err != nil {
			t.Errorf("CheckURL(%q) = %v", raw, err)
		}
	}
	for _, raw := range []string{
		"ftp://example.org/a", "file:///etc/passwd", "example.org/a", "https://", "javascript:alert(1)",
		"http://127.0.0.1/a", "http://[::1]:8080/", "http://169.254.169.254/latest/meta-data", "https://10.0.0.5/x",
		"http://localhost/x", "http://food.localhost/x",
	} {
		if _, err := strict.CheckURL(raw); err == nil {
			t.Errorf("CheckURL(%q) accepted", raw)
		}
	}
	// A host on the test allowlist may be anywhere.
	if _, err := New(0, 0, []string{"ics-fixture"}).CheckURL("http://ics-fixture/recipe.html"); err != nil {
		t.Errorf("allowlisted host refused: %v", err)
	}
}

// testFetcher connects only to the given servers' ports, so the address
// check can be exercised on real (loopback) connections.
func testFetcher(t *testing.T, allowed ...string) *Fetcher {
	t.Helper()
	f := New(1<<20, 2*time.Second, nil)
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

// public serves as if it were on the internet: CheckURL lets it through
// because its host name is not a literal private address.
func public(t *testing.T, handler http.HandlerFunc) (*httptest.Server, string) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	parsed, _ := url.Parse(server.URL)
	return server, "http://public.test:" + parsed.Port()
}

func TestFetchRefusesLoopbackAddresses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html></html>")) }))
	defer server.Close()
	// Literal: refused before connecting.
	if _, err := New(0, 2*time.Second, nil).Fetch(context.Background(), server.URL+"/a"); err == nil || !strings.Contains(err.Error(), "private") {
		t.Fatalf("literal loopback: %v", err)
	}
	// A host name that resolves to loopback: refused when connecting.
	parsed, _ := url.Parse(server.URL)
	if _, err := New(0, 2*time.Second, nil).Fetch(context.Background(), "http://localtest.me.invalid:"+parsed.Port()); err == nil {
		t.Fatal("an unresolvable host was fetched")
	}
	f := New(0, 2*time.Second, nil)
	f.client.Transport.(*http.Transport).DialContext = resolveTo(parsed.Host, f)
	if _, err := f.Fetch(context.Background(), "http://recipes.test/a"); err == nil || !strings.Contains(err.Error(), "private, loopback, or link-local") {
		t.Fatalf("host resolving to loopback: %v", err)
	}
}

func TestFetchRechecksRedirects(t *testing.T) {
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("internal data")) }))
	defer internal.Close()
	server, base := public(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/to-internal":
			http.Redirect(w, r, internal.URL+"/admin", http.StatusFound)
		case "/to-intranet":
			// A host name that resolves to an internal address.
			http.Redirect(w, r, "http://intranet.example:80/admin", http.StatusFound)
		case "/to-metadata":
			http.Redirect(w, r, "http://169.254.169.254/latest/meta-data", http.StatusFound)
		case "/to-file":
			http.Redirect(w, r, "file:///etc/passwd", http.StatusFound)
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		case "/to-ok":
			http.Redirect(w, r, "/ok", http.StatusMovedPermanently)
		default:
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte("<html><title>ok</title></html>"))
		}
	})
	f := testFetcher(t, server.URL)
	f.client.Transport.(*http.Transport).DialContext = resolveTo(strings.TrimPrefix(server.URL, "http://"), f,
		"intranet.example", strings.TrimPrefix(internal.URL, "http://"))
	if page, err := f.Fetch(context.Background(), base+"/to-ok"); err != nil || !strings.Contains(string(page.Body), "ok") || page.URL.Path != "/ok" {
		t.Fatalf("redirect within the site: %v %+v", err, page)
	}
	for path, want := range map[string]string{
		"/to-internal": "private, loopback, or link-local",
		"/to-intranet": "private, loopback, or link-local",
		"/to-metadata": "private, loopback, or link-local",
		"/to-file":     "http:// or https://",
		"/loop":        "too many times",
	} {
		if _, err := f.Fetch(context.Background(), base+path); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", path, err, want)
		}
	}
}

func TestFetchLimitsAndAnswers(t *testing.T) {
	server, base := public(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/big":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(strings.Repeat("x", 2<<20)))
		case "/image":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("png"))
		case "/blocked":
			w.WriteHeader(http.StatusForbidden)
		case "/missing":
			http.NotFound(w, r)
		case "/latin1":
			w.Header().Set("Content-Type", "text/html; charset=windows-1252")
			_, _ = w.Write([]byte("<title>Kr\xe4uterbutter</title>"))
		case "/slow":
			time.Sleep(3 * time.Second)
		case "/headers":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(r.Header.Get("User-Agent") + "|" + r.Header.Get("Cookie")))
		}
	})
	f := testFetcher(t, server.URL)
	f.client.Transport.(*http.Transport).DialContext = resolveTo(strings.TrimPrefix(server.URL, "http://"), f)
	for path, want := range map[string]string{
		"/big":     "larger than 1 MB",
		"/image":   "not a web page (image/png)",
		"/blocked": "would not show the page to Life OS (HTTP 403)",
		"/missing": "answered HTTP 404",
		"/slow":    "did not answer in time",
	} {
		if _, err := f.Fetch(context.Background(), base+path); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", path, err, want)
		}
	}
	page, err := f.Fetch(context.Background(), base+"/latin1")
	if err != nil || string(page.Body) != "<title>Kräuterbutter</title>" {
		t.Fatalf("latin1 = %q, %v", page.Body, err)
	}
	headers, err := f.Fetch(context.Background(), base+"/headers")
	if err != nil || !strings.HasPrefix(string(headers.Body), "Mozilla/5.0") || !strings.HasSuffix(string(headers.Body), "|") {
		t.Fatalf("headers = %q, %v", headers.Body, err)
	}
}

func TestAllowlistedHostsSkipTheAddressCheck(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<title>fixture</title>"))
	}))
	defer server.Close()
	if page, err := New(0, 2*time.Second, []string{"127.0.0.1"}).Fetch(context.Background(), server.URL+"/recipe.html"); err != nil || !strings.Contains(string(page.Body), "fixture") {
		t.Fatalf("allowlisted: %v", err)
	}
}

// resolveTo makes the .test host names resolve to target
// ("127.0.0.1:port"), keeping the guarded dial, as if DNS answered with that
// address; other hosts are dialled as they are.
func resolveTo(target string, f *Fetcher, more ...string) func(context.Context, string, string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: time.Second, Control: f.checkDial}
	hosts := map[string]string{}
	for index := 0; index+1 < len(more); index += 2 {
		hosts[more[index]] = more[index+1]
	}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, _, _ := net.SplitHostPort(address)
		if mapped, ok := hosts[host]; ok {
			address = mapped
		} else if strings.HasSuffix(host, ".test") {
			address = target
		}
		return dialer.DialContext(ctx, network, address)
	}
}
