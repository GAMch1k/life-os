package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeEngine is a Docker Engine API on a unix socket that records every
// request it receives.
type fakeEngine struct {
	mu       sync.Mutex
	requests []string
}

func (e *fakeEngine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	e.requests = append(e.requests, r.Method+" "+r.URL.RequestURI())
	e.mu.Unlock()
	switch {
	case r.URL.Path == "/containers/json":
		_, _ = w.Write([]byte(`[
			{"Id":"0123456789abcdef0123","Names":["/lifeos-backup-1"],"Image":"lifeos-backup","State":"running","Status":"Up 3 hours (healthy)",
			 "Labels":{"com.docker.compose.project":"lifeos","com.docker.compose.service":"backup","secret.label":"x"},"HostConfig":{"NetworkMode":"bridge"}},
			{"Id":"fedcba9876543210fedc","Names":["/homeassistant"],"Image":"ghcr.io/home-assistant/home-assistant:stable","State":"restarting","Status":"Restarting (1) 5 seconds ago","Labels":{}},
			{"Id":"aaaaaaaaaaaabbbbbbbb","Names":["/n8n"],"Image":"n8nio/n8n","State":"running","Status":"Up 1 minute (unhealthy)","Labels":{"com.docker.compose.project":"n8n"}}
		]`))
	case r.URL.Path == "/events":
		_, _ = w.Write([]byte(`{"Type":"container","Action":"die","Actor":{"ID":"fedcba9876543210fedc","Attributes":{"name":"homeassistant","exitCode":"1"}},"time":1791720000,"timeNano":1791720000000000000}
{"Type":"container","Action":"health_status: unhealthy","Actor":{"ID":"aaaaaaaaaaaabbbbbbbb","Attributes":{"name":"n8n","com.docker.compose.project":"n8n"}},"time":1791720060,"timeNano":1791720060000000000}
`))
	case strings.HasSuffix(r.URL.Path, "/stats"):
		_, _ = w.Write([]byte(`{"name":"/lifeos-backup-1","id":"0123456789abcdef0123","memory_stats":{"usage":157286400,"limit":8238493696,"stats":{"inactive_file":52428800,"active_file":1}}}`))
	default:
		http.Error(w, "no such endpoint", http.StatusNotFound)
	}
}

func (e *fakeEngine) seen() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.requests)
}

// startGateway serves a gateway in front of a fake engine.
func startGateway(t *testing.T) (*httptest.Server, *fakeEngine) {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "docker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	engine := &fakeEngine{}
	engineServer := &http.Server{Handler: engine}
	go func() { _ = engineServer.Serve(listener) }()
	t.Cleanup(func() { _ = engineServer.Close() })
	gateway := httptest.NewServer(NewGateway(socket, nil))
	t.Cleanup(gateway.Close)
	return gateway, engine
}

func TestTheGatewayAnswersItsThreeQuestions(t *testing.T) {
	gateway, engine := startGateway(t)
	client := NewClient(gateway.URL)
	ctx := context.Background()

	containers, err := client.Containers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []Container{
		{ID: "0123456789ab", Name: "lifeos-backup-1", Project: "lifeos", Service: "backup", Image: "lifeos-backup", State: "running", Health: "healthy"},
		{ID: "fedcba987654", Name: "homeassistant", Image: "ghcr.io/home-assistant/home-assistant:stable", State: "restarting"},
		{ID: "aaaaaaaaaaaa", Name: "n8n", Project: "n8n", Image: "n8nio/n8n", State: "running", Health: "unhealthy"},
	}
	if !slices.Equal(containers, want) {
		t.Fatalf("containers = %+v", containers)
	}

	since, until := time.Unix(1791719000, 0), time.Unix(1791720100, 0)
	events, err := client.Events(ctx, since, until)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Name != "homeassistant" || events[0].Action != "die" || events[1].Action != "health_status: unhealthy" || events[1].Project != "n8n" || !events[0].Time.Equal(time.Unix(1791720000, 0)) {
		t.Fatalf("events = %+v", events)
	}

	memory, err := client.Memory(ctx, "0123456789ab")
	if err != nil {
		t.Fatal(err)
	}
	// docker stats: usage less inactive page cache.
	if memory != (Memory{ID: "0123456789ab", Name: "lifeos-backup-1", UsageBytes: 157286400 - 52428800, LimitBytes: 8238493696}) {
		t.Fatalf("memory = %+v", memory)
	}

	requests := engine.seen()
	filters := url.QueryEscape(eventFilters)
	wantRequests := []string{
		"GET /containers/json?all=1",
		fmt.Sprintf("GET /events?filters=%s&since=1791719000&until=1791720100", filters),
		"GET /containers/0123456789ab/stats?stream=false&one-shot=true",
	}
	if !slices.Equal(requests, wantRequests) {
		t.Fatalf("the engine saw %q\nwant %q", requests, wantRequests)
	}
}

func TestTheGatewayRefusesEverythingElse(t *testing.T) {
	gateway, engine := startGateway(t)
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{http.MethodPost, "/containers", http.StatusMethodNotAllowed},
		{http.MethodDelete, "/containers/0123456789ab/memory", http.StatusMethodNotAllowed},
		{http.MethodGet, "/containers/0123456789ab/json", http.StatusNotFound},
		{http.MethodGet, "/containers/0123456789ab/logs", http.StatusNotFound},
		{http.MethodGet, "/containers/0123456789ab/export", http.StatusNotFound},
		{http.MethodGet, "/containers/0123456789ab/archive?path=/etc", http.StatusNotFound},
		{http.MethodGet, "/containers/lifeos-backup-1/memory", http.StatusNotFound},
		{http.MethodGet, "/containers/0123456789ab%2F..%2Fjson/memory", http.StatusNotFound},
		{http.MethodGet, "/containers/json", http.StatusNotFound},
		{http.MethodGet, "/v1.47/containers/json", http.StatusNotFound},
		{http.MethodGet, "/version", http.StatusNotFound},
		{http.MethodGet, "/images/json", http.StatusNotFound},
		{http.MethodGet, "/events", http.StatusBadRequest},
		{http.MethodGet, "/events?since=10&until=5", http.StatusBadRequest},
		{http.MethodGet, "/events?since=0&until=" + fmt.Sprint(time.Now().Unix()), http.StatusBadRequest},
		{http.MethodGet, "/events?since=1&until=99999999999", http.StatusBadRequest},
	} {
		request, _ := http.NewRequest(tc.method, gateway.URL+tc.path, nil)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != tc.status {
			t.Errorf("%s %s = %d, want %d", tc.method, tc.path, response.StatusCode, tc.status)
		}
	}
	if requests := engine.seen(); len(requests) != 0 {
		t.Fatalf("refused requests reached the engine: %q", requests)
	}
	response, err := http.Get(gateway.URL + "/health")
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("health = %v, %v", response, err)
	}
	var body map[string]string
	_ = json.NewDecoder(response.Body).Decode(&body)
	response.Body.Close()
}

func TestTheGatewayReportsAnEngineItCannotReach(t *testing.T) {
	gateway := httptest.NewServer(NewGateway(filepath.Join(t.TempDir(), "missing.sock"), nil))
	defer gateway.Close()
	_, err := NewClient(gateway.URL).Containers(context.Background())
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("Containers = %v", err)
	}
	if _, err := NewClient("http://127.0.0.1:1").Containers(context.Background()); err == nil || !strings.Contains(err.Error(), "cannot be reached") {
		t.Fatalf("an unreachable gateway: %v", err)
	}
}

func TestTheGatewaySaysWhenTheSocketRefusesIt(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "docker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := os.Chmod(socket, 0o000); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores the socket's permissions")
	}
	gateway := httptest.NewServer(NewGateway(socket, nil))
	defer gateway.Close()
	_, err = NewClient(gateway.URL).Containers(context.Background())
	if err == nil || !strings.Contains(err.Error(), "LIFE_OS_DOCKER_GID") {
		t.Fatalf("Containers = %v", err)
	}
}

func TestHealthFromStatus(t *testing.T) {
	for status, want := range map[string]string{
		"Up 3 hours (healthy)":            "healthy",
		"Up 1 minute (unhealthy)":         "unhealthy",
		"Up 5 seconds (health: starting)": "starting",
		"Up 2 days":                       "",
		"Exited (0) 3 hours ago":          "",
		"Restarting (1) 5 seconds ago":    "",
	} {
		if got := healthFromStatus(status); got != want {
			t.Errorf("healthFromStatus(%q) = %q, want %q", status, got, want)
		}
	}
}
