package processor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/GAMch1k/life-os/pkg/activity"
)

// MCPClient speaks the Streamable HTTP MCP protocol. It is deliberately the
// processor's only Life OS execution boundary: no service database is reachable
// from the processor.
type MCPClient struct {
	URL       string
	HTTP      *http.Client
	sessionID string
}

func (c *MCPClient) Initialize(ctx context.Context) error {
	if c.sessionID != "" {
		return nil
	}
	response, headers, err := c.request(ctx, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "life-os-inbox-processor", "version": "0.1.0"}},
	})
	if err != nil {
		return err
	}
	if response == nil {
		return fmt.Errorf("MCP initialization returned no result")
	}
	c.sessionID = headers.Get("Mcp-Session-Id")
	if c.sessionID == "" {
		return fmt.Errorf("MCP initialization returned no session ID")
	}
	_, _, err = c.request(ctx, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	return err
}

// errSessionExpired means the server no longer knows our session, for example
// after the MCP service restarted. The request was not processed.
var errSessionExpired = errors.New("MCP session expired")

func (c *MCPClient) Call(ctx context.Context, name string, args map[string]any) (json.RawMessage, error) {
	if err := c.Initialize(ctx); err != nil {
		return nil, err
	}
	call := map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}}
	response, _, err := c.request(ctx, call)
	if errors.Is(err, errSessionExpired) {
		// Streamable HTTP: a 404 for a known session means start a new one.
		if err := c.Initialize(ctx); err != nil {
			return nil, err
		}
		response, _, err = c.request(ctx, call)
	}
	if err != nil {
		return nil, fmt.Errorf("call MCP tool %s: %w", name, err)
	}
	if response == nil {
		return nil, fmt.Errorf("MCP tool %s returned no result", name)
	}
	var envelope struct {
		Error  json.RawMessage `json:"error"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(response, &envelope); err != nil {
		return nil, fmt.Errorf("decode MCP tool %s result: %w", name, err)
	}
	if len(envelope.Error) > 0 && string(envelope.Error) != "null" {
		return nil, fmt.Errorf("MCP tool %s error: %s", name, envelope.Error)
	}
	var toolResult struct {
		IsError bool `json:"isError"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(envelope.Result, &toolResult); err == nil && toolResult.IsError {
		if len(toolResult.Content) > 0 {
			return nil, fmt.Errorf("MCP tool %s rejected: %s", name, toolResult.Content[0].Text)
		}
		return nil, fmt.Errorf("MCP tool %s rejected", name)
	}
	return envelope.Result, nil
}

func (c *MCPClient) request(ctx context.Context, message map[string]any) (json.RawMessage, http.Header, error) {
	body, err := json.Marshal(message)
	if err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "application/json, text/event-stream")
	if c.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", c.sessionID)
	}
	activity.TraceFrom(ctx).Apply(req.Header)
	resp, err := c.client().Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusNotFound && c.sessionID != "" {
		c.sessionID = ""
		return nil, resp.Header, fmt.Errorf("%w: %s", errSessionExpired, strings.TrimSpace(string(raw)))
	}
	if resp.StatusCode/100 != 2 {
		return nil, resp.Header, fmt.Errorf("MCP returned %s: %s", resp.Status, string(raw))
	}
	return sseData(raw), resp.Header, nil
}

func sseData(raw []byte) json.RawMessage {
	for _, line := range strings.Split(string(raw), "\n") {
		if value, ok := strings.CutPrefix(line, "data: "); ok {
			return json.RawMessage(value)
		}
	}
	return nil
}

func (c *MCPClient) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}
