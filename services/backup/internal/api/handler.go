// Package api contains the backup service's HTTP transport adapters.
package api

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/GAMch1k/life-os/pkg/activity"
	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/services/backup/internal/api/generated"
	"github.com/GAMch1k/life-os/services/backup/internal/backup"
)

// Runner is the part of backup.Runner the handler uses.
type Runner interface {
	Start(backup.Request) (*backup.Job, error)
	StartRestoreCheck(backup.CheckRequest) (*backup.CheckJob, error)
	Runs() ([]backup.Summary, error)
	Status() (backup.Status, error)
}

// Scheduler is the part of backup.Scheduler the handler uses.
type Scheduler interface {
	Next() (time.Time, backup.Trigger)
	NextRestoreCheck() time.Time
}

// Settings are the configuration the status reports.
type Settings struct {
	Schedule  backup.Schedule
	Retention backup.Retention
	// RestoreCheckDay is a weekday such as sunday, or off.
	RestoreCheckDay string
}

// Handler implements the generated transport interface.
type Handler struct {
	runner    Runner
	scheduler Scheduler
	settings  Settings
}

var _ generated.ServerInterface = (*Handler)(nil)

// NewHandler serves the backup API.
func NewHandler(runner Runner, scheduler Scheduler, settings Settings) *Handler {
	return &Handler{runner: runner, scheduler: scheduler, settings: settings}
}

// Register mounts the backup API on router. Running a backup must say who
// asks for it in X-Life-OS-Actor; activity.RequireActor rejects it
// otherwise.
func Register(router fiber.Router, handler *Handler) {
	router.Use(activity.RequireActor())
	generated.RegisterHandlers(router, handler)
}

func (h *Handler) GetBackupStatus(c fiber.Ctx) error {
	status, err := h.runner.Status()
	if err != nil {
		return httpserver.WrapError(err, fiber.StatusInternalServerError, "internal_error", "The backup directory could not be read.")
	}
	next, trigger := h.scheduler.Next()
	response := generated.BackupStatus{
		Schedule: generated.BackupSchedule{
			Time:            h.settings.Schedule.Time(),
			TimeZone:        h.settings.Schedule.Location.String(),
			NextRunAt:       next.UTC(),
			NextTrigger:     generated.BackupTrigger(trigger),
			RestoreCheckDay: generated.BackupScheduleRestoreCheckDay(h.settings.RestoreCheckDay),
		},
		Retention: generated.BackupRetention{
			Daily:      h.settings.Retention.Daily,
			Weekly:     h.settings.Retention.Weekly,
			Monthly:    h.settings.Retention.Monthly,
			FailedDays: h.settings.Retention.FailedDays,
		},
		Storage: generated.BackupStorage{
			Runs:       status.Runs,
			TotalBytes: status.TotalBytes,
			FreeBytes:  status.FreeBytes,
			SizeBytes:  status.SizeBytes,
		},
		Databases:      status.Databases,
		DatabasesError: optional(status.DatabasesError),
	}
	if status.LastRun != nil {
		run := toRun(*status.LastRun)
		response.LastRun = &run
	}
	if status.LastSuccess != nil {
		run := toRun(*status.LastSuccess)
		response.LastSuccess = &run
	}
	if status.Running != nil {
		progress := toProgress(*status.Running)
		response.Running = &progress
	}
	if at := h.scheduler.NextRestoreCheck(); !at.IsZero() {
		at = at.UTC()
		response.Schedule.NextRestoreCheckAt = &at
	}
	if status.LastRestoreCheck != nil {
		check := toCheck(*status.LastRestoreCheck)
		response.LastRestoreCheck = &check
	}
	if status.RunningCheck != nil {
		progress := toCheckProgress(*status.RunningCheck)
		response.RunningRestoreCheck = &progress
	}
	return c.JSON(response)
}

func (h *Handler) ListBackups(c fiber.Ctx, params generated.ListBackupsParams) error {
	limit := 50
	if params.Limit != nil {
		if *params.Limit < 1 || *params.Limit > 100 {
			return badRequest("limit must be between 1 and 100")
		}
		limit = *params.Limit
	}
	cursor := ""
	if params.Cursor != nil {
		if !backup.IsRunID(*params.Cursor) {
			return badRequest("cursor must be a run id such as 20261011T031500Z")
		}
		cursor = *params.Cursor
	}
	runs, err := h.runner.Runs()
	if err != nil {
		return httpserver.WrapError(err, fiber.StatusInternalServerError, "internal_error", "The backup directory could not be read.")
	}
	response := generated.BackupList{Items: []generated.BackupRun{}}
	for _, run := range runs {
		if cursor != "" && run.ID >= cursor {
			continue
		}
		if len(response.Items) == limit {
			next := response.Items[len(response.Items)-1].Id
			response.NextCursor = &next
			break
		}
		response.Items = append(response.Items, toRun(run))
	}
	return c.JSON(response)
}

func (h *Handler) CreateBackup(c fiber.Ctx, params generated.CreateBackupParams) error {
	wait := 0
	if params.WaitSeconds != nil {
		if *params.WaitSeconds < 0 || *params.WaitSeconds > 3600 {
			return badRequest("waitSeconds must be between 0 and 3600")
		}
		wait = *params.WaitSeconds
	}
	var body generated.BackupCreateRequest
	if len(c.Body()) > 0 {
		if err := json.Unmarshal(c.Body(), &body); err != nil {
			return badRequest("the body must be a JSON object such as {\"databases\": [\"finance\"]}")
		}
	}
	var databases []string
	if body.Databases != nil {
		if len(*body.Databases) == 0 {
			return badRequest("databases must name at least one database, or be left out for all of them")
		}
		databases = *body.Databases
	}
	actor, _ := activity.ParseActor(c.Get(activity.ActorHeader))

	job, err := h.runner.Start(backup.Request{Trigger: backup.TriggerManual, Databases: databases, Actor: actorName(actor)})
	var unknown *backup.UnknownDatabasesError
	switch {
	case errors.Is(err, backup.ErrRunning):
		return errRunning
	case errors.Is(err, backup.ErrStopped):
		return httpserver.NewError(fiber.StatusServiceUnavailable, "service_unavailable", "The backup service is stopping.")
	case errors.As(err, &unknown):
		return httpserver.NewError(fiber.StatusUnprocessableEntity, "unknown_database",
			"Not in the database registry: "+strings.Join(unknown.Names, ", ")+".")
	case err != nil:
		return httpserver.WrapError(err, fiber.StatusInternalServerError, "internal_error", "The backup could not start: "+err.Error())
	}

	if wait > 0 {
		timer := time.NewTimer(time.Duration(wait) * time.Second)
		defer timer.Stop()
		select {
		case <-job.Done():
			return c.JSON(toRun(job.Summary()))
		case <-timer.C:
		case <-c.Context().Done():
		}
	}
	return c.Status(fiber.StatusAccepted).JSON(toProgress(job.Progress()))
}

var errRunning = httpserver.NewError(fiber.StatusConflict, "backup_running", "A backup or restore check is already running; try again when it has finished.")

func (h *Handler) CreateRestoreCheck(c fiber.Ctx, params generated.CreateRestoreCheckParams) error {
	wait := 0
	if params.WaitSeconds != nil {
		if *params.WaitSeconds < 0 || *params.WaitSeconds > 3600 {
			return badRequest("waitSeconds must be between 0 and 3600")
		}
		wait = *params.WaitSeconds
	}
	var body generated.RestoreCheckCreateRequest
	if len(c.Body()) > 0 {
		if err := json.Unmarshal(c.Body(), &body); err != nil {
			return badRequest("the body must be a JSON object such as {\"runId\": \"20261011T011500Z\"}")
		}
	}
	runID := ""
	if body.RunId != nil {
		if !backup.IsRunID(*body.RunId) {
			return badRequest("runId must be a run id such as 20261011T011500Z")
		}
		runID = *body.RunId
	}
	actor, _ := activity.ParseActor(c.Get(activity.ActorHeader))

	job, err := h.runner.StartRestoreCheck(backup.CheckRequest{RunID: runID, Trigger: backup.TriggerManual, Actor: actorName(actor)})
	var unknown *backup.UnknownRunError
	switch {
	case errors.Is(err, backup.ErrRunning):
		return errRunning
	case errors.Is(err, backup.ErrStopped):
		return httpserver.NewError(fiber.StatusServiceUnavailable, "service_unavailable", "The backup service is stopping.")
	case errors.Is(err, backup.ErrNoBackup):
		return httpserver.NewError(fiber.StatusUnprocessableEntity, "no_backup", "There is no successful backup of every database to check yet.")
	case errors.Is(err, backup.ErrNothingToCheck):
		return httpserver.NewError(fiber.StatusUnprocessableEntity, "no_backup", "That run has no dump to check.")
	case errors.As(err, &unknown):
		return httpserver.NewError(fiber.StatusNotFound, "run_not_found", "There is no completed backup run "+unknown.ID+".")
	case err != nil:
		return httpserver.WrapError(err, fiber.StatusInternalServerError, "internal_error", "The restore check could not start: "+err.Error())
	}

	if wait > 0 {
		timer := time.NewTimer(time.Duration(wait) * time.Second)
		defer timer.Stop()
		select {
		case <-job.Done():
			return c.JSON(toCheck(job.Result()))
		case <-timer.C:
		case <-c.Context().Done():
		}
	}
	return c.Status(fiber.StatusAccepted).JSON(toCheckProgress(job.Progress()))
}

func toCheck(check backup.RestoreCheck) generated.RestoreCheck {
	response := generated.RestoreCheck{
		Id:              check.ID,
		RunId:           check.RunID,
		Trigger:         generated.BackupTrigger(check.Trigger),
		Actor:           optional(check.Actor),
		StartedAt:       check.StartedAt.UTC(),
		FinishedAt:      check.FinishedAt.UTC(),
		DurationSeconds: check.DurationSeconds,
		Ok:              check.OK,
		Databases:       make([]generated.RestoreCheckDatabase, 0, len(check.Databases)),
		Error:           optional(check.Error),
	}
	for _, database := range check.Databases {
		item := generated.RestoreCheckDatabase{
			Name:       database.Name,
			Ok:         database.OK,
			Tables:     database.Tables,
			Rows:       database.Rows,
			Mismatches: make([]generated.RestoreCheckMismatch, 0, len(database.Mismatches)),
			Error:      optional(database.Error),
		}
		for _, mismatch := range database.Mismatches {
			item.Mismatches = append(item.Mismatches, generated.RestoreCheckMismatch{Subject: mismatch.Subject, Expected: mismatch.Expected, Actual: mismatch.Actual})
		}
		response.Databases = append(response.Databases, item)
	}
	return response
}

func toCheckProgress(progress backup.CheckProgress) generated.RestoreCheckProgress {
	databases := progress.Databases
	if databases == nil {
		databases = []string{}
	}
	return generated.RestoreCheckProgress{
		Id:        progress.ID,
		RunId:     progress.RunID,
		Trigger:   generated.BackupTrigger(progress.Trigger),
		Actor:     optional(progress.Actor),
		StartedAt: progress.StartedAt.UTC(),
		Databases: databases,
		Completed: progress.Completed,
		Current:   optional(progress.Current),
	}
}

func toRun(summary backup.Summary) generated.BackupRun {
	run := generated.BackupRun{
		Id:              summary.ID,
		Trigger:         generated.BackupTrigger(summary.Trigger),
		Scope:           generated.BackupScope(summary.Scope),
		Result:          generated.BackupResult(summary.Result),
		Actor:           optional(summary.Actor),
		StartedAt:       summary.StartedAt.UTC(),
		FinishedAt:      summary.FinishedAt.UTC(),
		DurationSeconds: summary.DurationSeconds,
		ServerVersion:   optional(summary.ServerVersion),
		TotalBytes:      summary.TotalBytes,
		Databases:       make([]generated.BackupDatabase, 0, len(summary.Databases)),
		Error:           optional(summary.Error),
	}
	for _, database := range summary.Databases {
		item := generated.BackupDatabase{
			Service: database.Service,
			Name:    database.Name,
			Result:  generated.BackupDatabaseResult(database.Result),
			File:    optional(database.File),
			Bytes:   database.Bytes,
			Sha256:  optional(database.SHA256),
			Tables:  database.Tables,
			Rows:    database.Rows,
			Error:   optional(database.Error),
		}
		if database.Migration != nil {
			item.Migration = &generated.BackupMigration{Version: database.Migration.Version, Dirty: database.Migration.Dirty}
		}
		run.Databases = append(run.Databases, item)
	}
	return run
}

func toProgress(progress backup.Progress) generated.BackupProgress {
	databases := progress.Databases
	if databases == nil {
		databases = []string{}
	}
	return generated.BackupProgress{
		Id:        progress.ID,
		Trigger:   generated.BackupTrigger(progress.Trigger),
		Scope:     generated.BackupScope(progress.Scope),
		Actor:     optional(progress.Actor),
		StartedAt: progress.StartedAt.UTC(),
		Databases: databases,
		Completed: progress.Completed,
		Current:   optional(progress.Current),
	}
}

// actorName is the actor as its header value, such as user or
// dev:backup-now.
func actorName(actor activity.Actor) string {
	if actor.Name == "" {
		return actor.Kind
	}
	return actor.Kind + ":" + actor.Name
}

func optional(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func badRequest(message string) error {
	return httpserver.NewError(fiber.StatusBadRequest, "bad_request", message)
}
