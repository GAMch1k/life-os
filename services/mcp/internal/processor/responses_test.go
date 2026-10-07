package processor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPlanUsesConfiguredModel(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("authorization") != "Bearer key" {
			t.Fatal("missing auth")
		}
		_, _ = w.Write([]byte(`{"output_text":"{\"action\":\"clarify\"}"}`))
	}))
	defer s.Close()
	got, err := (ResponsesClient{APIKey: "key", Model: "gpt-5.6-luna", Endpoint: s.URL, HTTP: s.Client()}).Plan(context.Background(), "call insurer")
	if err != nil || got == "" {
		t.Fatalf("Plan = %q, %v", got, err)
	}
}
