package coreclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDoReturnsSuccessfulCoreResponse(t *testing.T) {
	t.Parallel()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tasks" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	defer s.Close()
	got, err := (Client{BaseURL: s.URL, HTTP: s.Client()}).Do(context.Background(), http.MethodGet, "/v1/tasks", nil)
	if err != nil || string(got) != `{"items":[]}` {
		t.Fatalf("Do() = %s, %v", got, err)
	}
}

func TestDoMakesFailuresUnderstandable(t *testing.T) {
	t.Parallel()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "bad input", http.StatusBadRequest) }))
	defer s.Close()
	_, err := (Client{BaseURL: s.URL, HTTP: s.Client()}).Do(context.Background(), http.MethodGet, "/v1/tasks", nil)
	var status StatusError
	if !errors.As(err, &status) || status.Status != http.StatusBadRequest || err.Error() != "service returned 400: bad input\n" {
		t.Fatalf("error = %#v", err)
	}
}
