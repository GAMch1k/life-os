package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPostForwardsActionIdempotencyKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Idempotency-Key"); got != "action-stable-key" {
			t.Fatalf("Idempotency-Key = %q", got)
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"id":"original-row"}`))
	}))
	defer server.Close()

	_, first, err := post(context.Background(), server.Client(), server.URL, "/v1/tasks", taskAction{Title: "Only once"}, "action-stable-key")
	if err != nil || first.Data["id"] != "original-row" {
		t.Fatalf("first post = %#v, err = %v", first, err)
	}
	_, second, err := post(context.Background(), server.Client(), server.URL, "/v1/tasks", taskAction{Title: "Only once"}, "action-stable-key")
	if err != nil || second.Data["id"] != "original-row" {
		t.Fatalf("replayed post = %#v, err = %v", second, err)
	}
}
