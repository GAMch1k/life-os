package processor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestServiceHealthAndReadinessReflectProcessorRun(t *testing.T) {
	s := &Service{Run: func(context.Context) error { return nil }, PollInterval: 100 * time.Millisecond}
	request := httptest.NewRequest(http.MethodGet, "/ready", nil)
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("initial readiness = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
	if err := s.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	s.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("ready after successful run = %d, want %d", response.Code, http.StatusOK)
	}
}

func TestServiceRejectsBusyLoop(t *testing.T) {
	s := &Service{Run: func(context.Context) error { return nil }, PollInterval: time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Serve(ctx, "127.0.0.1:0"); err == nil {
		t.Fatal("Serve accepted a busy-loop poll interval")
	}
}
