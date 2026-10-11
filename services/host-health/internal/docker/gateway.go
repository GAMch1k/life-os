package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// containerID is a full or short Docker container id.
var containerID = regexp.MustCompile(`^[a-f0-9]{12,64}$`)

// eventFilters keeps the events the gateway passes on to container die,
// start, and health_status, and kill, stop, and destroy, which tell an
// intentional stop (docker stop, compose down, a recreate) from a crash.
const eventFilters = `{"type":["container"],"event":["die","start","health_status","kill","stop","destroy"]}`

// passedActions are the event actions the gateway re-encodes; health_status
// carries its status after a colon. Anything else the engine might add is
// dropped.
var passedActions = map[string]bool{"die": true, "start": true, "kill": true, "stop": true, "destroy": true}

// Gateway answers host-health's three requests from the Docker Engine API
// over the socket.
type Gateway struct {
	// Engine reaches the Docker Engine API over its unix socket.
	Engine *http.Client
	Logger *slog.Logger
	// MaxEventsWindow bounds how far back an events request may reach.
	MaxEventsWindow time.Duration
}

// NewGateway reaches the Docker Engine API through the unix socket at path.
func NewGateway(socket string, logger *slog.Logger) *Gateway {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socket)
		},
		MaxIdleConns:    4,
		IdleConnTimeout: time.Minute,
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Gateway{Engine: &http.Client{Transport: transport, Timeout: 20 * time.Second}, Logger: logger, MaxEventsWindow: 24 * time.Hour}
}

// ServeHTTP answers GET /health, /containers, /events, and
// /containers/{id}/memory; everything else is 404, and any other method
// 405.
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := r.URL.EscapedPath()
	switch {
	case path == "/health":
		writeJSON(w, map[string]string{"status": "ok"})
	case path == "/containers":
		g.containers(w, r)
	case path == "/events":
		g.events(w, r)
	case strings.HasPrefix(path, "/containers/") && strings.HasSuffix(path, "/memory"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/containers/"), "/memory")
		if !containerID.MatchString(id) {
			http.NotFound(w, r)
			return
		}
		g.memory(w, r, id)
	default:
		http.NotFound(w, r)
	}
}

type engineContainer struct {
	ID     string            `json:"Id"`
	Names  []string          `json:"Names"`
	Image  string            `json:"Image"`
	State  string            `json:"State"`
	Status string            `json:"Status"`
	Labels map[string]string `json:"Labels"`
}

func (g *Gateway) containers(w http.ResponseWriter, r *http.Request) {
	var listed []engineContainer
	if err := g.get(r.Context(), "/containers/json?all=1", &listed); err != nil {
		g.fail(w, err)
		return
	}
	containers := make([]Container, 0, len(listed))
	for _, c := range listed {
		name := c.ID
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		containers = append(containers, Container{
			ID:      shortID(c.ID),
			Name:    name,
			Project: c.Labels["com.docker.compose.project"],
			Service: c.Labels["com.docker.compose.service"],
			Image:   c.Image,
			State:   c.State,
			Health:  healthFromStatus(c.Status),
		})
	}
	writeJSON(w, containers)
}

type engineEvent struct {
	Action   string `json:"Action"`
	TimeNano int64  `json:"timeNano"`
	Actor    struct {
		ID         string            `json:"ID"`
		Attributes map[string]string `json:"Attributes"`
	} `json:"Actor"`
}

func (g *Gateway) events(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	since, errSince := strconv.ParseInt(query.Get("since"), 10, 64)
	until, errUntil := strconv.ParseInt(query.Get("until"), 10, 64)
	now := time.Now().Unix()
	if errSince != nil || errUntil != nil || since > until || until > now+60 || until-since > int64(g.MaxEventsWindow.Seconds()) {
		http.Error(w, "since and until must be unix seconds, since before until, at most a day apart", http.StatusBadRequest)
		return
	}
	params := url.Values{"since": {strconv.FormatInt(since, 10)}, "until": {strconv.FormatInt(until, 10)}, "filters": {eventFilters}}
	body, err := g.open(r.Context(), "/events?"+params.Encode())
	if err != nil {
		g.fail(w, err)
		return
	}
	defer body.Close()
	// The engine streams one JSON object per event until `until`.
	events := []Event{}
	decoder := json.NewDecoder(io.LimitReader(body, 16<<20))
	for {
		var e engineEvent
		if err := decoder.Decode(&e); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			g.fail(w, fmt.Errorf("read events: %w", err))
			return
		}
		if !passedActions[e.Action] && !strings.HasPrefix(e.Action, "health_status") {
			continue
		}
		event := Event{
			Time:    time.Unix(0, e.TimeNano).UTC(),
			ID:      shortID(e.Actor.ID),
			Name:    e.Actor.Attributes["name"],
			Project: e.Actor.Attributes["com.docker.compose.project"],
			Action:  e.Action,
		}
		if e.Action == "die" {
			if code, err := strconv.Atoi(e.Actor.Attributes["exitCode"]); err == nil {
				event.ExitCode = &code
			}
		}
		events = append(events, event)
	}
	writeJSON(w, events)
}

type engineStats struct {
	Name        string `json:"name"`
	ID          string `json:"id"`
	MemoryStats struct {
		Usage uint64            `json:"usage"`
		Limit uint64            `json:"limit"`
		Stats map[string]uint64 `json:"stats"`
	} `json:"memory_stats"`
}

func (g *Gateway) memory(w http.ResponseWriter, r *http.Request, id string) {
	var stats engineStats
	if err := g.get(r.Context(), "/containers/"+id+"/stats?stream=false&one-shot=true", &stats); err != nil {
		g.fail(w, err)
		return
	}
	usage := stats.MemoryStats.Usage
	// As docker stats: cgroup v2 reports inactive_file, v1
	// total_inactive_file.
	cache := stats.MemoryStats.Stats["inactive_file"]
	if cache == 0 {
		cache = stats.MemoryStats.Stats["total_inactive_file"]
	}
	if cache < usage {
		usage -= cache
	}
	writeJSON(w, Memory{ID: shortID(id), Name: strings.TrimPrefix(stats.Name, "/"), UsageBytes: usage, LimitBytes: stats.MemoryStats.Limit})
}

// errNotFound is an engine 404, such as a container that is gone.
var errNotFound = errors.New("not found")

func (g *Gateway) open(ctx context.Context, path string) (io.ReadCloser, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker"+path, nil)
	if err != nil {
		return nil, err
	}
	response, err := g.Engine.Do(request)
	if err != nil {
		return nil, fmt.Errorf("docker engine: %w", err)
	}
	if response.StatusCode == http.StatusNotFound {
		response.Body.Close()
		return nil, errNotFound
	}
	if response.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		response.Body.Close()
		return nil, fmt.Errorf("docker engine answered %d: %s", response.StatusCode, strings.TrimSpace(string(data)))
	}
	return response.Body, nil
}

func (g *Gateway) get(ctx context.Context, path string, into any) error {
	body, err := g.open(ctx, path)
	if err != nil {
		return err
	}
	defer body.Close()
	return json.NewDecoder(io.LimitReader(body, 16<<20)).Decode(into)
}

func (g *Gateway) fail(w http.ResponseWriter, err error) {
	if errors.Is(err, errNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	g.Logger.Warn("docker engine request failed", "error", err)
	if errors.Is(err, fs.ErrPermission) {
		http.Error(w, "the docker socket refused the gateway: run it with the socket's group (LIFE_OS_DOCKER_GID, stat -c %g /var/run/docker.sock)", http.StatusBadGateway)
		return
	}
	http.Error(w, "the docker engine could not be read", http.StatusBadGateway)
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
