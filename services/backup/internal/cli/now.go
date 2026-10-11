// Package cli is the backup binary's client side: `now` asks the running
// service, over its own API on localhost, to back up and waits for the
// result. tools/backup-now runs it inside the deployed container.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/backup/internal/api/generated"
)

// Exit codes of Now.
const (
	ExitSucceeded = 0
	ExitFailed    = 1
	ExitUsage     = 2
)

// Now runs `backup now [flags] [database…]`: it starts a backup of every
// database, or of those named (service or database names), waits for it,
// and prints the result. It exits 0 when the run succeeded and 1 when it
// failed or could not be followed.
func Now(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("now", flag.ContinueOnError)
	flags.SetOutput(stderr)
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	baseURL := flags.String("url", "http://127.0.0.1:"+port, "the backup service")
	actor := flags.String("actor", "dev:backup-cli", "who asks for the backup (X-Life-OS-Actor)")
	timeout := flags.Duration("timeout", 2*time.Hour, "how long to wait for the run")
	poll := flags.Duration("poll", 5*time.Second, "how often to check a run that is still going")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "usage: backup now [flags] [database…]")
		fmt.Fprintln(stderr, "Backs up every database, or only those named (such as finance or tasks_db), and waits.")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return ExitUsage
	}
	if _, ok := activity.ParseActor(*actor); !ok {
		fmt.Fprintf(stderr, "backup now: -actor %q is not user, agent:<name>, system:<name>, or dev:<name>\n", *actor)
		return ExitUsage
	}
	client := &client{base: strings.TrimRight(*baseURL, "/"), actor: *actor, http: &http.Client{}}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	run, err := client.follow(ctx, flags.Args(), *poll, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "backup now: %v\n", err)
		var apiErr *apiError
		if errors.As(err, &apiErr) && apiErr.status < 500 {
			return ExitUsage
		}
		return ExitFailed
	}
	Print(stdout, run)
	if run.Result != generated.BackupResultSucceeded {
		return ExitFailed
	}
	return ExitSucceeded
}

type client struct {
	base  string
	actor string
	http  *http.Client
}

type apiError struct {
	status  int
	code    string
	message string
}

func (e *apiError) Error() string {
	if e.message == "" {
		return fmt.Sprintf("the service answered %d", e.status)
	}
	return e.message
}

// follow starts the run and waits for its result: first in the request
// itself (waitSeconds), then by reading the status until the run is done.
func (c *client) follow(ctx context.Context, databases []string, poll time.Duration, progress io.Writer) (generated.BackupRun, error) {
	var request generated.BackupCreateRequest
	if len(databases) > 0 {
		request.Databases = &databases
	}
	body, _ := json.Marshal(request)
	var run generated.BackupRun
	var started generated.BackupProgress
	status, err := c.do(ctx, http.MethodPost, "/v1/backups?waitSeconds=600", body, map[int]any{
		http.StatusOK:       &run,
		http.StatusAccepted: &started,
	})
	if err != nil {
		return generated.BackupRun{}, err
	}
	if status == http.StatusOK {
		return run, nil
	}
	fmt.Fprintf(progress, "Backup %s is still running; waiting for it.\n", started.Id)
	for {
		select {
		case <-ctx.Done():
			return generated.BackupRun{}, fmt.Errorf("gave up waiting for backup %s: %w", started.Id, ctx.Err())
		case <-time.After(poll):
		}
		var current generated.BackupStatus
		if _, err := c.do(ctx, http.MethodGet, "/v1/status", nil, map[int]any{http.StatusOK: &current}); err != nil {
			return generated.BackupRun{}, err
		}
		if current.Running != nil && current.Running.Id == started.Id {
			continue
		}
		if current.LastRun != nil && current.LastRun.Id == started.Id {
			return *current.LastRun, nil
		}
		return generated.BackupRun{}, fmt.Errorf("backup %s is no longer running, but its result is not the latest run", started.Id)
	}
}

func (c *client) do(ctx context.Context, method, path string, body []byte, into map[int]any) (int, error) {
	request, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	request.Header.Set(activity.ActorHeader, c.actor)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			return 0, fmt.Errorf("the backup service at %s cannot be reached: %w", c.base, urlErr.Err)
		}
		return 0, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 16<<20))
	if err != nil {
		return 0, err
	}
	if target, ok := into[response.StatusCode]; ok {
		if err := json.Unmarshal(data, target); err != nil {
			return 0, fmt.Errorf("unreadable answer from the backup service: %w", err)
		}
		return response.StatusCode, nil
	}
	var envelope generated.Error
	_ = json.Unmarshal(data, &envelope)
	return 0, &apiError{status: response.StatusCode, code: envelope.Error.Code, message: envelope.Error.Message}
}

// Print writes a run as a few readable lines.
func Print(w io.Writer, run generated.BackupRun) {
	scope := "all databases"
	if run.Scope == generated.Subset {
		scope = "some databases"
	}
	fmt.Fprintf(w, "Backup %s %s in %.1f s (%s, %s), %s.\n", run.Id, run.Result, run.DurationSeconds, run.Trigger, scope, formatBytes(run.TotalBytes))
	if run.Error != nil {
		fmt.Fprintf(w, "  %s\n", *run.Error)
	}
	for _, database := range run.Databases {
		if database.Result != generated.BackupDatabaseResultSucceeded {
			message := "failed"
			if database.Error != nil {
				message = "failed: " + *database.Error
			}
			fmt.Fprintf(w, "  %-22s %s\n", database.Name, message)
			continue
		}
		migration := "no migrations"
		if database.Migration != nil {
			migration = fmt.Sprintf("migration %d", database.Migration.Version)
			if database.Migration.Dirty {
				migration += " (dirty)"
			}
		}
		fmt.Fprintf(w, "  %-22s %10s  %3d tables  %8d rows  %s\n", database.Name, formatBytes(database.Bytes), database.Tables, database.Rows, migration)
	}
}

func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	value, suffix := float64(n), []string{"KiB", "MiB", "GiB", "TiB"}
	i := -1
	for value >= unit && i < len(suffix)-1 {
		value /= unit
		i++
	}
	return fmt.Sprintf("%.1f %s", value, suffix[i])
}
