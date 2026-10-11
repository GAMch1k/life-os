package coreclient

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type Client struct {
	BaseURL string
	HTTP    *http.Client
}

func (c Client) Do(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	return c.DoWithHeaders(ctx, method, path, body, nil)
}
func (c Client) DoWithHeaders(ctx context.Context, method, path string, body []byte, headers http.Header) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if len(body) > 0 {
		req.Header.Set("content-type", "application/json")
	}
	for key, values := range headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	r, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("service unavailable: %w", err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	if r.StatusCode < 200 || r.StatusCode > 299 {
		return nil, StatusError{Status: r.StatusCode, Body: b}
	}
	return b, nil
}

// StatusError is a service's answer outside 2xx: its status and body, the
// error envelope.
type StatusError struct {
	Status int
	Body   []byte
}

func (e StatusError) Error() string {
	return fmt.Sprintf("service returned %d: %s", e.Status, string(e.Body))
}
