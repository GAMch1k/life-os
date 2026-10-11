package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Client is host-health's side of the gateway.
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

// NewClient reaches the gateway at baseURL.
func NewClient(baseURL string) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), HTTP: &http.Client{Timeout: 15 * time.Second}}
}

// Containers lists every container on the host, stopped ones included.
func (c *Client) Containers(ctx context.Context) ([]Container, error) {
	var containers []Container
	return containers, c.get(ctx, "/containers", &containers)
}

// Events lists the container die, start, and health_status events between
// since and until.
func (c *Client) Events(ctx context.Context, since, until time.Time) ([]Event, error) {
	var events []Event
	path := "/events?since=" + strconv.FormatInt(since.Unix(), 10) + "&until=" + strconv.FormatInt(until.Unix(), 10)
	return events, c.get(ctx, path, &events)
}

// Memory reads one container's memory use.
func (c *Client) Memory(ctx context.Context, id string) (Memory, error) {
	var memory Memory
	return memory, c.get(ctx, "/containers/"+id+"/memory", &memory)
}

func (c *Client) get(ctx context.Context, path string, into any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return err
	}
	response, err := c.HTTP.Do(request)
	if err != nil {
		return fmt.Errorf("the docker gateway cannot be reached: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		return fmt.Errorf("the docker gateway answered %d: %s", response.StatusCode, strings.TrimSpace(string(data)))
	}
	return json.NewDecoder(io.LimitReader(response.Body, 16<<20)).Decode(into)
}
