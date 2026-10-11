// Package health turns the host's facts and its containers into findings,
// and keeps the last 24 hours of samples in memory.
package health

import (
	"slices"
	"time"

	"github.com/GAMch1k/life-os/services/host-health/internal/docker"
	"github.com/GAMch1k/life-os/services/host-health/internal/host"
)

// Thresholds decide when something needs attention (warning) or is a
// problem.
type Thresholds struct {
	// MemoryWarningPercent of memory available, held for MemoryWarningSamples
	// samples in a row, is a warning; MemoryProblemPercent at once a problem.
	MemoryWarningPercent float64
	MemoryWarningSamples int
	MemoryProblemPercent float64
	// A disk with less than DiskWarningBytes or DiskWarningPercent free is a
	// warning, with less than DiskProblemBytes a problem.
	DiskWarningBytes   uint64
	DiskWarningPercent float64
	DiskProblemBytes   uint64
	// A 15-minute load above LoadWarningPerCPU times the CPUs is a warning.
	LoadWarningPerCPU float64
	// A container that died RestartLoopCount times within RestartLoopWindow
	// is restarting in a loop; one unhealthy for UnhealthyFor is a warning.
	RestartLoopCount  int
	RestartLoopWindow time.Duration
	UnhealthyFor      time.Duration
}

// DefaultThresholds are the owner's defaults.
func DefaultThresholds() Thresholds {
	return Thresholds{
		MemoryWarningPercent: 15,
		MemoryWarningSamples: 3,
		MemoryProblemPercent: 7,
		DiskWarningBytes:     10 << 30,
		DiskWarningPercent:   10,
		DiskProblemBytes:     3 << 30,
		LoadWarningPerCPU:    1,
		RestartLoopCount:     3,
		RestartLoopWindow:    15 * time.Minute,
		UnhealthyFor:         5 * time.Minute,
	}
}

// Severity is how bad a finding is.
type Severity string

const (
	Warning Severity = "warning"
	Problem Severity = "problem"
)

// Kind is what a finding is about.
type Kind string

const (
	MemoryLow             Kind = "memory_low"
	DiskLow               Kind = "disk_low"
	LoadHigh              Kind = "load_high"
	ContainerRestarting   Kind = "container_restarting"
	ContainerUnhealthy    Kind = "container_unhealthy"
	ContainersUnavailable Kind = "containers_unavailable"
)

// Finding is one thing that needs attention. Which fields are set depends
// on the kind.
type Finding struct {
	Kind     Kind
	Severity Severity
	// Subject is the disk's label or the container's name.
	Subject string
	Project string
	// For memory_low: the memory; for disk_low: the disk.
	AvailableBytes uint64
	TotalBytes     uint64
	// For memory_low: minutes held; for container_unhealthy: minutes
	// unhealthy; for container_restarting: the window in minutes.
	Minutes int
	// For container_restarting: deaths in the window.
	Count int
	// For load_high: the 15-minute load and the CPUs.
	Load float64
	CPUs int
	// For containers_unavailable: why.
	Error string
}

// ContainerState is a container with what host-health tracked of it.
type ContainerState struct {
	docker.Container
	// RecentDeaths is how often it died within the restart-loop window.
	RecentDeaths int
	// UnhealthySince is when it was first seen unhealthy, zero while not.
	UnhealthySince time.Time
	// MemoryBytes is its last memory reading; zero when not read.
	MemoryBytes uint64
}

// Containers is the containers on the host as host-health last saw them.
type Containers struct {
	// Available is false when the gateway is not set up or could not be
	// read; Error says why.
	Available bool
	Error     string
	Items     []ContainerState
	// MemoryAt is when the memory readings were taken.
	MemoryAt time.Time
}

// Counts are the containers by state.
func (c Containers) Counts() (total, running, unhealthy, restarting int) {
	for _, item := range c.Items {
		total++
		switch item.State {
		case "running":
			running++
		case "restarting":
			restarting++
		}
		if item.Health == "unhealthy" {
			unhealthy++
		}
	}
	return total, running, unhealthy, restarting
}

// Top is the n containers using the most memory, biggest first.
func (c Containers) Top(n int) []ContainerState {
	var read []ContainerState
	for _, item := range c.Items {
		if item.MemoryBytes > 0 {
			read = append(read, item)
		}
	}
	slices.SortFunc(read, func(a, b ContainerState) int {
		switch {
		case a.MemoryBytes > b.MemoryBytes:
			return -1
		case a.MemoryBytes < b.MemoryBytes:
			return 1
		}
		return 0
	})
	if len(read) > n {
		read = read[:n]
	}
	return read
}

// Status is the host now, with what needs attention.
type Status struct {
	// Mode is host, or fixture for the verification stack.
	Mode       string
	Snapshot   host.Snapshot
	Containers Containers
	Thresholds Thresholds
	Findings   []Finding
	// DiskErrors are disks that could not be read.
	DiskErrors []string
}

// Sample is one minute of history.
type Sample struct {
	At                   time.Time
	MemoryAvailable      uint64
	MemoryTotal          uint64
	Load1, Load5, Load15 float64
	Disks                []DiskSample
}

// DiskSample is one disk's free space in a sample.
type DiskSample struct {
	Label     string
	FreeBytes uint64
}

// SampleOf is the history sample of a snapshot.
func SampleOf(snapshot host.Snapshot) Sample {
	sample := Sample{
		At:              snapshot.At,
		MemoryAvailable: snapshot.Memory.AvailableBytes,
		MemoryTotal:     snapshot.Memory.TotalBytes,
		Load1:           snapshot.Load.One,
		Load5:           snapshot.Load.Five,
		Load15:          snapshot.Load.Fifteen,
	}
	for _, disk := range snapshot.Disks {
		sample.Disks = append(sample.Disks, DiskSample{Label: disk.Label, FreeBytes: disk.FreeBytes})
	}
	return sample
}
