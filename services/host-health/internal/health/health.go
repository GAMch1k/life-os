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
	// A container that Docker restarted by itself after RestartLoopCount
	// deaths within RestartLoopWindow is restarting in a loop (see Tracker);
	// one unhealthy for UnhealthyFor is a warning.
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
	// For container_restarting: restarts in the window, and the last
	// death's exit code when Docker reported it.
	Count    int
	ExitCode *int
	// For load_high: the 15-minute load and the CPUs.
	Load float64
	CPUs int
	// For containers_unavailable: why.
	Error string
}

// ContainerState is a container with what host-health tracked of it.
type ContainerState struct {
	docker.Container
	// RecentDeaths is how often Docker restarted it by itself within the
	// restart-loop window.
	RecentDeaths int
	// LastExitCode is the exit code of its last such death, when known.
	LastExitCode *int
	// TestStack is a container of an ignored compose project (the
	// verification stack): in the totals, never in a finding or the top
	// memory users.
	TestStack bool
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
	// RestartsSince is when the open event stream began to see every
	// event (zero while none is open), and RestartsComplete whether it
	// has been open for the whole restart-loop window. Until then a
	// restart count can be too low: the deaths before it are only those
	// Docker still held, about the last two minutes.
	RestartsSince    time.Time
	RestartsComplete bool
	// EventsError is set when the event stream has been closed for
	// eventsDownFor: restart loops are then not watched.
	EventsError string
}

// Counts are the containers by state. The test stack's containers count in
// total and running, and on their own in testStack, but never as unhealthy
// or restarting.
func (c Containers) Counts() (total, running, unhealthy, restarting, testStack int) {
	for _, item := range c.Items {
		total++
		if item.State == "running" {
			running++
		}
		if item.TestStack {
			testStack++
			continue
		}
		if item.State == "restarting" {
			restarting++
		}
		if item.Health == "unhealthy" {
			unhealthy++
		}
	}
	return total, running, unhealthy, restarting, testStack
}

// Top is the n containers using the most memory, biggest first.
func (c Containers) Top(n int) []ContainerState {
	var read []ContainerState
	for _, item := range c.Items {
		if item.MemoryBytes > 0 && !item.TestStack {
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
	// Usage is how much of the server Life OS uses.
	Usage Usage
}

// Sample is one minute of history.
type Sample struct {
	At                   time.Time
	MemoryAvailable      uint64
	MemoryTotal          uint64
	Load1, Load5, Load15 float64
	Disks                []DiskSample
	// LifeOS is Life OS's use of the server; nil when it could not be read.
	LifeOS *Share
}

// DiskSample is one disk's free space in a sample.
type DiskSample struct {
	Label     string
	FreeBytes uint64
}

// SampleOf is the history sample of a snapshot and Life OS's use.
func SampleOf(snapshot host.Snapshot, usage Usage) Sample {
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
	if usage.Available {
		lifeOS := usage.LifeOS
		sample.LifeOS = &lifeOS
	}
	return sample
}
