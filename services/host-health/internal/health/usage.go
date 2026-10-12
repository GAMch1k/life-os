package health

import (
	"cmp"
	"fmt"
	"slices"
	"time"

	"github.com/GAMch1k/life-os/services/host-health/internal/host"
)

// Share is one part of the server's use: memory in use, and the CPU it
// used over the last interval, in cores.
type Share struct {
	MemoryBytes uint64
	CPUCores    float64
	// CPUKnown is false until two readings allow CPU figures.
	CPUKnown bool
}

// ServiceUsage is one Life OS compose service's memory, its replicas
// summed.
type ServiceUsage struct {
	Service     string
	Containers  int
	MemoryBytes uint64
}

// Usage is how much of the server Life OS uses, against the test stack and
// everything else.
type Usage struct {
	// Available is false when the cgroups or the containers cannot be read;
	// Error says why.
	Available bool
	Error     string
	// LifeOS is the Life OS compose project's running containers; Tests the test stack's
	// (the ignored projects), TestsRunning while any of them runs; Others
	// everything else: the server's use less both, never below zero.
	LifeOS, Tests, Others Share
	TestsRunning          bool
	// Services are Life OS's compose services, the most memory first.
	Services []ServiceUsage
}

// UsageSource measures Life OS's use of the server: a Meter, or a fixture.
type UsageSource interface {
	Usage(now time.Time, snapshot host.Snapshot, containers Containers) Usage
}

// Meter measures Life OS's use of the server from its containers' cgroups.
// CPU is the CPU time used between two readings, so a container counts
// from its second reading on; until then, and when its CPU time went back
// (it restarted), it adds no CPU for that interval.
type Meter struct {
	Cgroups host.Cgroups
	// Project is the compose project that is Life OS (lifeos).
	Project string

	// The last good reading: each container's CPU time, the host's, and
	// when.
	cpu     map[string]time.Duration
	hostCPU host.CPUTime
	at      time.Time
}

// Usage reads the cgroups of the running containers of Life OS and of the
// test stack, now. Containers that are neither are not read: everything
// else is the server's use less theirs.
func (m *Meter) Usage(now time.Time, snapshot host.Snapshot, containers Containers) Usage {
	var usage Usage
	if !containers.Available {
		usage.Error = "the containers cannot be read"
		return usage
	}
	var ids []string
	lifeOS := 0
	for _, item := range containers.Items {
		if item.State == "running" && (item.Project == m.Project || item.TestStack) {
			ids = append(ids, item.ID)
			if !item.TestStack {
				lifeOS++
			}
		}
	}
	readings, err := m.Cgroups.Read(ids)
	if err != nil {
		usage.Error = err.Error()
		return usage
	}

	elapsed := now.Sub(m.at)
	cpuKnown := m.cpu != nil && elapsed > 0
	services := map[string]*ServiceUsage{}
	// Per share: its containers read, and of them those with CPU figures.
	var lifeOSRead, lifeOSTimed, testsRead, testsTimed int
	for _, item := range containers.Items {
		reading, ok := readings[item.ID]
		if !ok {
			continue
		}
		share, read, timed := &usage.LifeOS, &lifeOSRead, &lifeOSTimed
		if item.TestStack {
			share, read, timed = &usage.Tests, &testsRead, &testsTimed
		} else {
			name := cmp.Or(item.Service, item.Name)
			if services[name] == nil {
				services[name] = &ServiceUsage{Service: name}
			}
			services[name].Containers++
			services[name].MemoryBytes += reading.MemoryBytes
		}
		*read++
		share.MemoryBytes += reading.MemoryBytes
		// A new container, or one whose CPU time went back (restarted with
		// the same id), adds nothing until its next reading.
		if previous, seen := m.cpu[item.ID]; cpuKnown && seen && reading.CPU >= previous {
			share.CPUCores += (reading.CPU - previous).Seconds() / elapsed.Seconds()
			*timed++
		}
	}
	if lifeOS > 0 && lifeOSRead == 0 {
		// Running Life OS containers, none with a cgroup here: the wrong
		// directory, or another cgroup driver.
		usage.Error = fmt.Sprintf("cgroups: none of the %d running %s containers has a cgroup in %s", lifeOS, m.Project, m.Cgroups.Dir)
		return usage
	}
	usage.TestsRunning = testsRead > 0
	// A share has CPU figures once any of its containers was read before: a
	// stack just started (or redeployed) has none for one interval.
	usage.LifeOS.CPUKnown = cpuKnown && lifeOSTimed > 0
	usage.Tests.CPUKnown = cpuKnown && testsTimed > 0

	used := snapshot.Memory.TotalBytes - min(snapshot.Memory.AvailableBytes, snapshot.Memory.TotalBytes)
	usage.Others.MemoryBytes = used - min(used, usage.LifeOS.MemoryBytes+usage.Tests.MemoryBytes)
	if cpuKnown && (lifeOSRead == 0 || usage.LifeOS.CPUKnown) && (testsRead == 0 || usage.Tests.CPUKnown) &&
		snapshot.CPUTime.Total > m.hostCPU.Total && snapshot.CPUTime.Busy >= m.hostCPU.Busy {
		busy, total := snapshot.CPUTime.Busy-m.hostCPU.Busy, snapshot.CPUTime.Total-m.hostCPU.Total
		server := float64(busy) / float64(total) * float64(snapshot.CPUs)
		usage.Others.CPUCores = max(0, server-usage.LifeOS.CPUCores-usage.Tests.CPUCores)
		usage.Others.CPUKnown = true
	}

	for _, service := range services {
		usage.Services = append(usage.Services, *service)
	}
	slices.SortFunc(usage.Services, func(a, b ServiceUsage) int {
		return cmp.Or(cmp.Compare(b.MemoryBytes, a.MemoryBytes), cmp.Compare(a.Service, b.Service))
	})

	m.cpu = map[string]time.Duration{}
	for id, reading := range readings {
		m.cpu[id] = reading.CPU
	}
	m.hostCPU, m.at = snapshot.CPUTime, now
	usage.Available = true
	return usage
}
