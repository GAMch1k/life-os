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
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if len(body) > 0 {
		req.Header.Set("content-type", "application/json")
	}
	r, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("service unavailable: %w", err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	if r.StatusCode < 200 || r.StatusCode > 299 {
		return nil, fmt.Errorf("service returned %d: %s", r.StatusCode, string(b))
	}
	return b, nil
}
