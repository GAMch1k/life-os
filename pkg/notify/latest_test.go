package notify

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestLatestReadsTheServicesNewestNotificationInACategory(t *testing.T) {
	var query, accept string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/notifications" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		query, accept = r.URL.RawQuery, r.Header.Get("accept")
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"id":"7f8d2b1e-1c1a-4b7e-9a51-2f0c6d1e9a10","title":"Automatic processing has a problem","body":"","importance":"high","category":"status.processing","source":"monitor","link":"/status","dedupeKey":"status:processing:problem:2026-10-10T12:00:00Z","occurredAt":"2026-10-10T12:00:00Z","createdAt":"2026-10-10T12:01:00Z","readAt":null,"deliveries":[]}],"nextCursor":"abc"}`))
	}))
	defer server.Close()

	sent, err := New(server.URL, "Monitor_Service").Latest(context.Background(), "status.processing")
	if err != nil {
		t.Fatal(err)
	}
	if query != "category=status.processing&limit=1&source=monitor-service" || accept != "application/json" {
		t.Errorf("query = %q, accept = %q", query, accept)
	}
	want := Sent{
		Title: "Automatic processing has a problem", Importance: High, Category: "status.processing", Source: "monitor",
		DedupeKey:  "status:processing:problem:2026-10-10T12:00:00Z",
		OccurredAt: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC), CreatedAt: time.Date(2026, 10, 10, 12, 1, 0, 0, time.UTC),
	}
	if sent == nil || *sent != want {
		t.Fatalf("Latest = %+v, want %+v", sent, want)
	}
}

func TestLatestIsNilWithoutHistoryOrWhenDisabled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	defer server.Close()
	if sent, err := New(server.URL, "monitor").Latest(context.Background(), "status.inbox"); sent != nil || err != nil {
		t.Fatalf("Latest = %+v, %v", sent, err)
	}
	var nilClient *Client
	if sent, err := nilClient.Latest(context.Background(), "status.inbox"); sent != nil || err != nil {
		t.Fatalf("a nil client: Latest = %+v, %v", sent, err)
	}
	if sent, err := New("", "monitor").Latest(context.Background(), "status.inbox"); sent != nil || err != nil {
		t.Fatalf("a client without a URL: Latest = %+v, %v", sent, err)
	}
}

func TestLatestRetriesTemporaryFailuresButNotRejections(t *testing.T) {
	var calls atomic.Int32
	statuses := []int{http.StatusServiceUnavailable, http.StatusOK}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status := statuses[min(int(calls.Add(1))-1, len(statuses)-1)]
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = w.Write([]byte(`{"items":[]}`))
		}
	}))
	defer server.Close()
	if _, err := New(server.URL, "monitor").Latest(context.Background(), "status.inbox"); err != nil || calls.Load() != 2 {
		t.Fatalf("error = %v after %d requests", err, calls.Load())
	}

	calls.Store(0)
	rejecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":"bad_request","message":"category must be a dotted lowercase name or prefix"}}`))
	}))
	defer rejecting.Close()
	_, err := New(rejecting.URL, "monitor").Latest(context.Background(), "Status")
	var status *StatusError
	if !errors.As(err, &status) || status.Code != "bad_request" || calls.Load() != 1 {
		t.Fatalf("error = %v after %d requests", err, calls.Load())
	}
}
