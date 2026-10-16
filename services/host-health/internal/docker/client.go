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
	// HTTP bounds each request; Stream only waits a bounded time for the
	// event stream to open, which then stays open.
	HTTP   *http.Client
	Stream *http.Client
}

// NewClient reaches the gateway at baseURL.
func NewClient(baseURL string) *Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 15 * time.Second
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		HTTP:    &http.Client{Timeout: 15 * time.Second},
		Stream:  &http.Client{Transport: transport},
	}
}

// Containers lists every container on the host, stopped ones included.
func (c *Client) Containers(ctx context.Context) ([]Container, error) {
	var containers []Container
	return containers, c.get(ctx, "/containers", &containers)
}

// Events opens the stream of container die, start, kill, stop, and destroy
// events: those since `since` that the engine still holds, then each as it
// happens, until ctx ends or the stream breaks. Close it when done.
func (c *Client) Events(ctx context.Context, since time.Time) (EventStream, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/events?since="+strconv.FormatInt(since.Unix(), 10), nil)
	if err != nil {
		return nil, err
	}
	response, err := c.Stream.Do(request)
	if err != nil {
		return nil, fmt.Errorf("the docker gateway cannot be reached: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		defer response.Body.Close()
		data, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		return nil, fmt.Errorf("the docker gateway answered %d: %s", response.StatusCode, strings.TrimSpace(string(data)))
	}
	return &eventStream{body: response.Body, decoder: json.NewDecoder(response.Body), since: time.Now()}, nil
}

// eventStream reads the gateway's events, one JSON object per line.
type eventStream struct {
	body    io.ReadCloser
	decoder *json.Decoder
	since   time.Time
}

func (s *eventStream) Since() time.Time { return s.since }

func (s *eventStream) Next() (Event, error) {
	var event Event
	err := s.decoder.Decode(&event)
	return event, err
}

func (s *eventStream) Close() error { return s.body.Close() }

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
