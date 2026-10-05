package httpserver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
)

func TestRunContextGracefullyShutsDownAndRunsHooksInReverse(t *testing.T) {
	address := unusedAddress(t)
	app := New("lifecycle-test", WithShutdownTimeout(2*time.Second))
	app.Get("/work", func(c fiber.Ctx) error { return c.SendString("ok") })

	var mu sync.Mutex
	var calls []string
	app.OnShutdown(func(ctx context.Context) error {
		if ctx.Err() != nil {
			t.Errorf("first hook context already canceled: %v", ctx.Err())
		}
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, "first")
		return nil
	})
	app.OnShutdown(func(context.Context) error {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, "second")
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- app.RunContext(ctx, address) }()
	waitUntilServing(t, "http://"+address+"/work")
	cancel()

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("RunContext: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RunContext did not stop after cancellation")
	}

	mu.Lock()
	defer mu.Unlock()
	if fmt.Sprint(calls) != "[second first]" {
		t.Fatalf("hook calls = %v", calls)
	}
	if app.ready.Load() {
		t.Fatal("app remained ready after shutdown")
	}
}

func TestRunContextReportsHookFailure(t *testing.T) {
	address := unusedAddress(t)
	app := New("lifecycle-test", WithShutdownTimeout(time.Second))
	want := errors.New("close database")
	app.OnShutdown(func(context.Context) error { return want })

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- app.RunContext(ctx, address) }()
	waitUntilServing(t, "http://"+address+HealthPath)
	cancel()
	if err := <-result; !errors.Is(err, want) {
		t.Fatalf("RunContext error = %v, want wrapped %v", err, want)
	}
}

func TestRunContextRejectsNilContext(t *testing.T) {
	t.Parallel()
	if err := New("test").RunContext(nil, ":0"); err == nil {
		t.Fatal("RunContext(nil) returned nil")
	}
}

func unusedAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func waitUntilServing(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url) //nolint:gosec // loopback test server only
		if err == nil {
			resp.Body.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server did not start at %s", url)
}
