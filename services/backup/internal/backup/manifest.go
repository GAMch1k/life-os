package backup

import (
	"fmt"
	"time"
)

// Trigger is why a run started.
type Trigger string

const (
	TriggerSchedule Trigger = "schedule"
	TriggerCatchUp  Trigger = "catch-up"
	TriggerManual   Trigger = "manual"
)

// Scope is which databases a run covers.
type Scope string

const (
	// ScopeAll is every database in the registry.
	ScopeAll Scope = "all"
	// ScopeSubset is only the databases a manual run named.
	ScopeSubset Scope = "subset"
)

// Result is how a run, or one database of it, ended.
type Result string

const (
	ResultSucceeded Result = "succeeded"
	ResultFailed    Result = "failed"
	// ResultInterrupted is a run that stopped before it finished (the
	// service shut down or crashed); it leaves no files.
	ResultInterrupted Result = "interrupted"
)

// ManifestVersion is the manifest.json format version.
const ManifestVersion = 1

// Manifest describes one run. It is manifest.json in the run directory,
// written before the directory gets its final name, so a run directory
// always has one.
type Manifest struct {
	FormatVersion   int              `json:"formatVersion"`
	ID              string           `json:"id"`
	Trigger         Trigger          `json:"trigger"`
	Scope           Scope            `json:"scope"`
	Result          Result           `json:"result,omitempty"`
	Actor           string           `json:"actor,omitempty"`
	StartedAt       time.Time        `json:"startedAt"`
	FinishedAt      time.Time        `json:"finishedAt,omitzero"`
	DurationSeconds float64          `json:"durationSeconds"`
	ServerVersion   string           `json:"serverVersion,omitempty"`
	PgDumpVersion   string           `json:"pgDumpVersion,omitempty"`
	Databases       []DatabaseResult `json:"databases"`
	// Media is what the run stored of Inbox's capture images, listed from
	// the same snapshot as inbox_db's dump.
	Media *MediaSet `json:"media,omitempty"`
	Error string    `json:"error,omitempty"`
}

// DatabaseResult is one database of a run.
type DatabaseResult struct {
	Service string `json:"service"`
	Name    string `json:"name"`
	Result  Result `json:"result"`
	// File is the dump in the run directory (pg_dump custom format).
	File   string `json:"file,omitempty"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256,omitempty"`
	// Tables are the row counts of every user table in the snapshot the
	// dump was taken from.
	Tables     []TableRows `json:"tables"`
	Migration  *Migration  `json:"migration,omitempty"`
	StartedAt  time.Time   `json:"startedAt"`
	FinishedAt time.Time   `json:"finishedAt"`
	Error      string      `json:"error,omitempty"`
	// Media are the media files the database refers to in that snapshot;
	// the run stores them and lists them in its own file.
	Media []MediaRef `json:"-"`
}

// TableRows is the number of rows of one table.
type TableRows struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
	Rows   int64  `json:"rows"`
}

// Migration is a database's golang-migrate state (its schema_migrations row).
type Migration struct {
	Version int64 `json:"version"`
	Dirty   bool  `json:"dirty"`
}

// Summary is a run without the per-table counts: what the API lists and
// the run history keeps.
type Summary struct {
	ID              string            `json:"id"`
	Trigger         Trigger           `json:"trigger"`
	Scope           Scope             `json:"scope"`
	Result          Result            `json:"result"`
	Actor           string            `json:"actor,omitempty"`
	StartedAt       time.Time         `json:"startedAt"`
	FinishedAt      time.Time         `json:"finishedAt"`
	DurationSeconds float64           `json:"durationSeconds"`
	ServerVersion   string            `json:"serverVersion,omitempty"`
	TotalBytes      int64             `json:"totalBytes"`
	Databases       []DatabaseSummary `json:"databases"`
	Error           string            `json:"error,omitempty"`
	// Copies are the run's copies on other targets (none yet).
	Copies []Copy        `json:"copies,omitempty"`
	Media  *MediaSummary `json:"media,omitempty"`
}

// MediaSummary is what a run stored of a service's media.
type MediaSummary struct {
	Service string `json:"service"`
	Count   int    `json:"count"`
	Bytes   int64  `json:"bytes"`
	Copied  int    `json:"copied"`
	Missing int    `json:"missing"`
	Error   string `json:"error,omitempty"`
}

// DatabaseSummary is one database of a Summary.
type DatabaseSummary struct {
	Service   string     `json:"service"`
	Name      string     `json:"name"`
	Result    Result     `json:"result"`
	File      string     `json:"file,omitempty"`
	Bytes     int64      `json:"bytes"`
	SHA256    string     `json:"sha256,omitempty"`
	Tables    int        `json:"tables"`
	Rows      int64      `json:"rows"`
	Migration *Migration `json:"migration,omitempty"`
	Error     string     `json:"error,omitempty"`
}

// Summary summarises the manifest.
func (m Manifest) Summary() Summary {
	summary := Summary{
		ID:              m.ID,
		Trigger:         m.Trigger,
		Scope:           m.Scope,
		Result:          m.Result,
		Actor:           m.Actor,
		StartedAt:       m.StartedAt,
		FinishedAt:      m.FinishedAt,
		DurationSeconds: m.DurationSeconds,
		ServerVersion:   m.ServerVersion,
		Databases:       make([]DatabaseSummary, 0, len(m.Databases)),
		Error:           m.Error,
	}
	if m.Media != nil {
		summary.Media = &MediaSummary{
			Service: m.Media.Service,
			Count:   m.Media.Count,
			Bytes:   m.Media.Bytes,
			Copied:  m.Media.Copied,
			Missing: len(m.Media.Missing),
			Error:   m.Media.Error,
		}
	}
	for _, database := range m.Databases {
		var rows int64
		for _, table := range database.Tables {
			rows += table.Rows
		}
		summary.TotalBytes += database.Bytes
		summary.Databases = append(summary.Databases, DatabaseSummary{
			Service:   database.Service,
			Name:      database.Name,
			Result:    database.Result,
			File:      database.File,
			Bytes:     database.Bytes,
			SHA256:    database.SHA256,
			Tables:    len(database.Tables),
			Rows:      rows,
			Migration: database.Migration,
			Error:     database.Error,
		})
	}
	return summary
}

// finish sets the end of the run and its result from its databases.
func (m *Manifest) finish(at time.Time) {
	m.FinishedAt = at
	m.DurationSeconds = at.Sub(m.StartedAt).Round(time.Millisecond).Seconds()
	failed := 0
	for _, database := range m.Databases {
		if database.Result != ResultSucceeded {
			failed++
		}
	}
	switch {
	case len(m.Databases) == 0:
		m.Result = ResultFailed
		m.Error = "no databases to back up"
	case failed > 0:
		m.Result = ResultFailed
		m.Error = fmt.Sprintf("%d of %d databases failed", failed, len(m.Databases))
	case m.Media != nil && m.Media.Error != "":
		m.Result = ResultFailed
		m.Error = "the media could not be backed up: " + m.Media.Error
	default:
		m.Result = ResultSucceeded
	}
}
