package database_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/GAMch1k/life-os/pkg/database"
	"github.com/GAMch1k/life-os/pkg/database/testdata/example/generated"
	"github.com/GAMch1k/life-os/pkg/httpserver"
)

func TestGeneratedQueryAndShutdownLifecycle(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_TEST_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_TEST_URL is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := database.Open(ctx, database.Config{
		URL:             databaseURL,
		ApplicationName: "database-integration-test",
		MaxConnections:  2,
		ConnectTimeout:  5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	app := httpserver.New("database-integration-test")
	app.OnShutdown(database.ClosePool(pool))
	queries := generated.New(pool)
	created, err := queries.CreateExampleRecord(ctx, "neutral fixture")
	if err != nil {
		t.Fatal(err)
	}
	got, err := queries.GetExampleRecord(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Label != "neutral fixture" {
		t.Fatalf("label = %q", got.Label)
	}

	address := unusedAddress(t)
	runCtx, stop := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- app.RunContext(runCtx, address) }()
	waitUntilServing(t, "http://"+address+httpserver.HealthPath)
	stop()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("service did not shut down")
	}
	if err := pool.Ping(ctx); err == nil {
		t.Fatal("pool remained usable after the service shutdown hook")
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
		response, err := http.Get(url) //nolint:gosec // loopback test server only
		if err == nil {
			response.Body.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(fmt.Errorf("server did not start at %s", url))
}
