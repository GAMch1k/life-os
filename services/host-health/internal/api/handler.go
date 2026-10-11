// Package api contains host-health's HTTP transport adapters.
package api

import (
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/services/host-health/internal/api/generated"
	"github.com/GAMch1k/life-os/services/host-health/internal/health"
)

// Sampler is the part of health.Sampler the handler uses.
type Sampler interface {
	Status() (health.Status, bool)
	History(from time.Time) []health.Sample
}

// Handler implements the generated transport interface.
type Handler struct {
	sampler  Sampler
	interval time.Duration
	now      func() time.Time
}

var _ generated.ServerInterface = (*Handler)(nil)

// NewHandler serves the host's state; interval is the time between samples.
func NewHandler(sampler Sampler, interval time.Duration) *Handler {
	return &Handler{sampler: sampler, interval: interval, now: time.Now}
}

// Register mounts the API. It has no writes.
func Register(router fiber.Router, handler *Handler) {
	generated.RegisterHandlers(router, handler)
}

func (h *Handler) GetHostStatus(c fiber.Ctx) error {
	status, ok := h.sampler.Status()
	if !ok {
		return httpserver.NewError(fiber.StatusServiceUnavailable, "not_ready", "The host has not been sampled yet.")
	}
	return c.JSON(toStatus(status))
}

func (h *Handler) GetHostHistory(c fiber.Ctx, params generated.GetHostHistoryParams) error {
	hours := 24
	if params.Hours != nil {
		if *params.Hours < 1 || *params.Hours > 24 {
			return httpserver.NewError(fiber.StatusBadRequest, "bad_request", "hours must be between 1 and 24")
		}
		hours = *params.Hours
	}
	samples := h.sampler.History(h.now().Add(-time.Duration(hours) * time.Hour))
	response := generated.HostHistory{IntervalSeconds: int(h.interval.Seconds()), Samples: make([]generated.HostSample, 0, len(samples))}
	for _, sample := range samples {
		item := generated.HostSample{
			At:                   sample.At.UTC(),
			MemoryAvailableBytes: int64(sample.MemoryAvailable),
			MemoryTotalBytes:     int64(sample.MemoryTotal),
			Load1:                sample.Load1,
			Load5:                sample.Load5,
			Load15:               sample.Load15,
		}
		item.Disks = make([]struct {
			FreeBytes int64  `json:"freeBytes"`
			Label     string `json:"label"`
		}, 0, len(sample.Disks))
		for _, disk := range sample.Disks {
			item.Disks = append(item.Disks, struct {
				FreeBytes int64  `json:"freeBytes"`
				Label     string `json:"label"`
			}{FreeBytes: int64(disk.FreeBytes), Label: disk.Label})
		}
		response.Samples = append(response.Samples, item)
	}
	return c.JSON(response)
}

func toStatus(status health.Status) generated.HostStatus {
	snapshot := status.Snapshot
	t := status.Thresholds
	response := generated.HostStatus{
		SampledAt: snapshot.At.UTC(),
		Mode:      generated.HostStatusMode(status.Mode),
		Memory: generated.HostMemory{
			TotalBytes:       int64(snapshot.Memory.TotalBytes),
			AvailableBytes:   int64(snapshot.Memory.AvailableBytes),
			AvailablePercent: round(snapshot.Memory.AvailablePercent()),
		},
		Load:          generated.HostLoad{One: snapshot.Load.One, Five: snapshot.Load.Five, Fifteen: snapshot.Load.Fifteen, Cpus: snapshot.CPUs},
		UptimeSeconds: int64(snapshot.Uptime.Seconds()),
		Disks:         make([]generated.HostDisk, 0, len(snapshot.Disks)),
		Thresholds: generated.HostThresholds{
			MemoryWarningPercent: t.MemoryWarningPercent,
			MemoryWarningSamples: t.MemoryWarningSamples,
			MemoryProblemPercent: t.MemoryProblemPercent,
			DiskWarningBytes:     int64(t.DiskWarningBytes),
			DiskWarningPercent:   t.DiskWarningPercent,
			DiskProblemBytes:     int64(t.DiskProblemBytes),
			LoadWarningPerCpu:    t.LoadWarningPerCPU,
			RestartLoopCount:     t.RestartLoopCount,
			RestartLoopMinutes:   int(t.RestartLoopWindow.Minutes()),
			UnhealthyMinutes:     int(t.UnhealthyFor.Minutes()),
		},
		Findings: make([]generated.HostFinding, 0, len(status.Findings)),
	}
	for _, disk := range snapshot.Disks {
		response.Disks = append(response.Disks, generated.HostDisk{Label: disk.Label, TotalBytes: int64(disk.TotalBytes), FreeBytes: int64(disk.FreeBytes), FreePercent: round(disk.FreePercent())})
	}
	if len(status.DiskErrors) > 0 {
		errs := append([]string(nil), status.DiskErrors...)
		response.DiskErrors = &errs
	}
	response.Containers = toContainers(status.Containers)
	for _, finding := range status.Findings {
		response.Findings = append(response.Findings, toFinding(finding))
	}
	return response
}

func toContainers(containers health.Containers) generated.HostContainers {
	total, running, unhealthy, restarting, testStack := containers.Counts()
	response := generated.HostContainers{
		Available:  containers.Available,
		Error:      optional(containers.Error),
		Total:      total,
		Running:    running,
		Unhealthy:  unhealthy,
		Restarting: restarting,
		TestStack:  &testStack,
		Items:      make([]generated.HostContainer, 0, len(containers.Items)),
		Top:        []generated.HostMemoryUser{},
	}
	if !containers.MemoryAt.IsZero() {
		at := containers.MemoryAt.UTC()
		response.MemorySampledAt = &at
	}
	for _, item := range containers.Items {
		container := generated.HostContainer{
			Name:         item.Name,
			Project:      optional(item.Project),
			Service:      optional(item.Service),
			Image:        item.Image,
			State:        item.State,
			RecentDeaths: item.RecentDeaths,
			LastExitCode: item.LastExitCode,
		}
		if item.TestStack {
			testStack := true
			container.TestStack = &testStack
		}
		if item.Health != "" {
			health := generated.HostContainerHealth(item.Health)
			container.Health = &health
		}
		if !item.UnhealthySince.IsZero() {
			since := item.UnhealthySince.UTC()
			container.UnhealthySince = &since
		}
		if item.MemoryBytes > 0 {
			memory := int64(item.MemoryBytes)
			container.MemoryBytes = &memory
		}
		response.Items = append(response.Items, container)
	}
	for _, item := range containers.Top(5) {
		response.Top = append(response.Top, generated.HostMemoryUser{Name: item.Name, Project: optional(item.Project), MemoryBytes: int64(item.MemoryBytes)})
	}
	return response
}

func toFinding(finding health.Finding) generated.HostFinding {
	response := generated.HostFinding{
		Kind:     generated.HostFindingKind(finding.Kind),
		Severity: generated.HostFindingSeverity(finding.Severity),
		Subject:  optional(finding.Subject),
		Project:  optional(finding.Project),
		Error:    optional(finding.Error),
	}
	switch finding.Kind {
	case health.MemoryLow, health.DiskLow:
		available, total := int64(finding.AvailableBytes), int64(finding.TotalBytes)
		response.AvailableBytes, response.TotalBytes = &available, &total
	case health.LoadHigh:
		load, cpus := finding.Load, finding.CPUs
		response.Load, response.Cpus = &load, &cpus
	case health.ContainerRestarting:
		count := finding.Count
		response.Count, response.ExitCode = &count, finding.ExitCode
	}
	if finding.Kind == health.MemoryLow || finding.Kind == health.ContainerRestarting || finding.Kind == health.ContainerUnhealthy {
		minutes := finding.Minutes
		response.Minutes = &minutes
	}
	return response
}

func optional(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func round(value float64) float64 {
	return float64(int64(value*10+0.5)) / 10
}
