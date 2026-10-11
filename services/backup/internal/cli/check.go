package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/services/backup/internal/api/generated"
)

// RestoreCheck runs `backup restore-check [flags]`: it starts a restore
// check of the newest successful full run (or -run), waits for it, and
// prints the result. It exits 0 when every database restored as its
// manifest says, 1 when one did not or the check could not be followed, and
// 2 when no check could start.
func RestoreCheck(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("restore-check", flag.ContinueOnError)
	flags.SetOutput(stderr)
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	baseURL := flags.String("url", "http://127.0.0.1:"+port, "the backup service")
	actor := flags.String("actor", "dev:backup-cli", "who asks for the check (X-Life-OS-Actor)")
	runID := flags.String("run", "", "the run to check (default: the newest successful full run)")
	timeout := flags.Duration("timeout", 2*time.Hour, "how long to wait for the check")
	poll := flags.Duration("poll", 5*time.Second, "how often to look at a check that is still going")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "usage: backup restore-check [flags]")
		fmt.Fprintln(stderr, "Restores every dump of a run into scratch databases, compares them with the manifest, and waits.")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return ExitUsage
	}
	if flags.NArg() > 0 {
		flags.Usage()
		return ExitUsage
	}
	if _, ok := activity.ParseActor(*actor); !ok {
		fmt.Fprintf(stderr, "backup restore-check: -actor %q is not user, agent:<name>, system:<name>, or dev:<name>\n", *actor)
		return ExitUsage
	}
	client := &client{base: strings.TrimRight(*baseURL, "/"), actor: *actor, http: &http.Client{}}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	check, err := client.followCheck(ctx, *runID, *poll, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "backup restore-check: %v\n", err)
		var apiErr *apiError
		if errors.As(err, &apiErr) && apiErr.status < 500 {
			return ExitUsage
		}
		return ExitFailed
	}
	PrintCheck(stdout, check)
	if !check.Ok {
		return ExitFailed
	}
	return ExitSucceeded
}

func (c *client) followCheck(ctx context.Context, runID string, poll time.Duration, progress io.Writer) (generated.RestoreCheck, error) {
	var request generated.RestoreCheckCreateRequest
	if runID != "" {
		request.RunId = &runID
	}
	body, _ := json.Marshal(request)
	var check generated.RestoreCheck
	var started generated.RestoreCheckProgress
	status, err := c.do(ctx, http.MethodPost, "/v1/restore-checks?waitSeconds=600", body, map[int]any{
		http.StatusOK:       &check,
		http.StatusAccepted: &started,
	})
	if err != nil {
		return generated.RestoreCheck{}, err
	}
	if status == http.StatusOK {
		return check, nil
	}
	fmt.Fprintf(progress, "Restore check %s of backup %s is still running; waiting for it.\n", started.Id, started.RunId)
	for {
		select {
		case <-ctx.Done():
			return generated.RestoreCheck{}, fmt.Errorf("gave up waiting for restore check %s: %w", started.Id, ctx.Err())
		case <-time.After(poll):
		}
		var current generated.BackupStatus
		if _, err := c.do(ctx, http.MethodGet, "/v1/status", nil, map[int]any{http.StatusOK: &current}); err != nil {
			return generated.RestoreCheck{}, err
		}
		if current.RunningRestoreCheck != nil && current.RunningRestoreCheck.Id == started.Id {
			continue
		}
		if current.LastRestoreCheck != nil && current.LastRestoreCheck.Id == started.Id {
			return *current.LastRestoreCheck, nil
		}
		return generated.RestoreCheck{}, fmt.Errorf("restore check %s is no longer running, but its result is not the latest check", started.Id)
	}
}

// PrintCheck writes a restore check as a few readable lines.
func PrintCheck(w io.Writer, check generated.RestoreCheck) {
	outcome := "passed"
	if !check.Ok {
		outcome = "failed"
	}
	fmt.Fprintf(w, "Restore check %s of backup %s %s in %.1f s.\n", check.Id, check.RunId, outcome, check.DurationSeconds)
	if check.Error != nil {
		fmt.Fprintf(w, "  %s\n", *check.Error)
	}
	for _, database := range check.Databases {
		switch {
		case database.Error != nil:
			fmt.Fprintf(w, "  %-22s failed: %s\n", database.Name, *database.Error)
		case !database.Ok:
			var parts []string
			for _, mismatch := range database.Mismatches {
				parts = append(parts, fmt.Sprintf("%s: expected %s, restored %s", mismatch.Subject, mismatch.Expected, mismatch.Actual))
			}
			fmt.Fprintf(w, "  %-22s differs: %s\n", database.Name, strings.Join(parts, "; "))
		default:
			fmt.Fprintf(w, "  %-22s ok  %3d tables  %8d rows\n", database.Name, database.Tables, database.Rows)
		}
	}
}
