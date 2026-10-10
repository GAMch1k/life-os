package testsupport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/GAMch1k/life-os/pkg/activity"
)

// The real Calendar service, run as a process for the integration tests:
// this service reaches it only over HTTP, as it does in production, so the
// tests prove the integration against Calendar's actual integration API.
// Calendar gets a private schema on the test database, migrated with its
// own migrations.

var (
	buildOnce   sync.Once
	buildDir    string
	buildBinary string
	buildErr    error
	schemas     atomic.Int64
)

// calendarDir is services/calendar.
func calendarDir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "calendar")
}

// buildCalendar builds Calendar's server once per test binary.
func buildCalendar() (string, error) {
	buildOnce.Do(func() {
		buildDir, buildErr = os.MkdirTemp("", "google-calendar-test-calendar-")
		if buildErr != nil {
			return
		}
		buildBinary = filepath.Join(buildDir, "calendar")
		command := exec.Command("go", "build", "-o", buildBinary, "./cmd/server")
		command.Dir = calendarDir()
		command.Env = append(os.Environ(), "GOWORK=off", "CGO_ENABLED=0")
		if output, err := command.CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("build Calendar: %v: %s", err, output)
		}
	})
	return buildBinary, buildErr
}

// Cleanup removes the Calendar binary (TestMain calls it).
func Cleanup() {
	if buildDir != "" {
		_ = os.RemoveAll(buildDir)
	}
}

// CalendarSchema creates a private schema migrated with all of Calendar's
// migrations, dropped when the test ends, and returns a pool on it.
func CalendarSchema(t testing.TB, databaseURL string) (*pgxpool.Pool, string) {
	t.Helper()
	ctx := context.Background()
	schema := fmt.Sprintf("calendar_it_%d_%d", time.Now().UnixNano(), schemas.Add(1))
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	config.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	// Calendar's tests install pgcrypto once, outside the test schemas.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.Exec(ctx, "SELECT pg_advisory_lock(4210001)")
	if err == nil {
		_, err = conn.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS pgcrypto SCHEMA public")
		_, _ = conn.Exec(ctx, "SELECT pg_advisory_unlock(4210001)")
	}
	conn.Release()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	for _, path := range calendarMigrations(t) {
		applyFile(t, pool, path)
	}
	return pool, schema
}

func calendarMigrations(t testing.TB) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(calendarDir(), "db", "migrations", "*.up.sql"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("Calendar migrations: %v %v", paths, err)
	}
	sort.Strings(paths)
	return paths
}

func applyFile(t testing.TB, pool *pgxpool.Pool, path string) {
	t.Helper()
	migration, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), string(migration)); err != nil {
		t.Fatalf("%s: %v", filepath.Base(path), err)
	}
}

// Calendar is a running Calendar service.
type Calendar struct {
	t   testing.TB
	URL string
}

// StartCalendar runs Calendar on schema (see CalendarSchema) until the test
// ends.
func StartCalendar(t testing.TB, databaseURL, schema string) *Calendar {
	t.Helper()
	binary, err := buildCalendar()
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	ctx, cancel := context.WithCancel(context.Background())
	command := exec.CommandContext(ctx, binary)
	command.Env = []string{
		"SERVICE_NAME=calendar", fmt.Sprintf("PORT=%d", port), "DATABASE_URL=" + parsed.String(), "LOG_LEVEL=error",
		"LIFE_OS_TIMEZONE=Europe/Berlin", "PATH=" + os.Getenv("PATH"),
	}
	command.Stdout, command.Stderr = io.Discard, os.Stderr
	if err := command.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		_ = command.Wait()
	})
	calendar := &Calendar{t: t, URL: fmt.Sprintf("http://127.0.0.1:%d", port)}
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(20 * time.Second)
	for {
		response, err := client.Get(calendar.URL + "/ready")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return calendar
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("Calendar did not become ready: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Do makes a request to Calendar as actor and returns the status and body.
func (c *Calendar) Do(method, path string, body any, actor string) (int, []byte) {
	c.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			c.t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	request, err := http.NewRequest(method, c.URL+path, reader)
	if err != nil {
		c.t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	if actor != "" {
		request.Header.Set(activity.ActorHeader, actor)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		c.t.Fatal(err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	return response.StatusCode, raw
}

// Call makes a request as the owner and decodes the answer into out,
// failing the test unless the status is want.
func (c *Calendar) Call(method, path string, body any, want int, out any) {
	c.t.Helper()
	status, raw := c.Do(method, path, body, "user")
	if status != want {
		c.t.Fatalf("Calendar %s %s: %d %s, want %d", method, path, status, raw, want)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			c.t.Fatalf("Calendar %s %s: %v in %s", method, path, err, raw)
		}
	}
}
