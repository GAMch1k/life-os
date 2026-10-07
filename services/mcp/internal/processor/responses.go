package processor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// ResponsesClient is a deliberately narrow adapter around OpenAI Responses.
// It makes the configured lightweight model inspect raw Inbox content but does
// not grant it database or arbitrary-shell access.
type ResponsesClient struct {
	APIKey, Model, Endpoint string
	HTTP                    *http.Client
}
type planRequest struct {
	Model        string `json:"model"`
	Instructions string `json:"instructions"`
	Input        string `json:"input"`
	Store        bool   `json:"store"`
}
type planResponse struct {
	OutputText string `json:"output_text"`
}

func (c ResponsesClient) Plan(ctx context.Context, content string) (string, error) {
	body, err := json.Marshal(planRequest{Model: c.Model, Store: false, Instructions: "Classify this Life OS Inbox item. Return only a concise JSON action plan; do not claim an action succeeded.", Input: content})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(), bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("authorization", "Bearer "+c.APIKey)
	req.Header.Set("content-type", "application/json")
	resp, err := c.client().Do(req)
	if err != nil {
		return "", fmt.Errorf("request Codex response: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", fmt.Errorf("Codex response %d: %s", resp.StatusCode, string(raw))
	}
	var out planResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("decode Codex response: %w", err)
	}
	return out.OutputText, nil
}
func (c ResponsesClient) endpoint() string {
	if c.Endpoint != "" {
		return c.Endpoint
	}
	return "https://api.openai.com/v1/responses"
}
func (c ResponsesClient) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}
