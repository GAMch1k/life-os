package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReadinessProbe(t *testing.T) {
	ready := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ready" {
			t.Errorf("probed %s", r.URL.Path)
		}
	}))
	defer ready.Close()
	starting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer starting.Close()

	if !readinessProbe(ready.URL)(context.Background()) {
		t.Fatal("ready dependency reported unavailable")
	}
	if readinessProbe(starting.URL)(context.Background()) {
		t.Fatal("503 dependency reported ready")
	}
	if readinessProbe("http://127.0.0.1:1")(context.Background()) {
		t.Fatal("unreachable dependency reported ready")
	}
}
