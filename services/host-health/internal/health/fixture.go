package health

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/GAMch1k/life-os/services/host-health/internal/docker"
	"github.com/GAMch1k/life-os/services/host-health/internal/host"
)

// Fixture scenarios (HOST_HEALTH_FIXTURE), for the verification stack: a
// made-up host that is the same every time, so the Status page's Server
// check can be tested without the real host.
const (
	FixtureHealthy   = "healthy"
	FixtureLowMemory = "low-memory"
)

// fixtureMemoryTotal is a 7.7 GiB server, as /proc/meminfo would say.
const fixtureMemoryTotal = 8_045_404 * 1024

// Fixture is a made-up host and its containers.
type Fixture struct{ Scenario string }

// ValidFixture says whether name is a fixture scenario.
func ValidFixture(name string) bool {
	return name == FixtureHealthy || name == FixtureLowMemory
}

func (f Fixture) available(at time.Time) uint64 {
	if f.Scenario == FixtureLowMemory {
		return 520 * 1024 * 1024
	}
	// Between about 3.4 and 4.4 GiB over the day.
	hour := float64(at.Unix()%86400) / 3600
	return uint64((3.9 + 0.5*math.Sin(hour/24*2*math.Pi)) * 1024 * 1024 * 1024)
}

// Read is the made-up host at now.
func (f Fixture) Read(now time.Time) (host.Snapshot, error) {
	return host.Snapshot{
		At:     now,
		Memory: host.Memory{TotalBytes: fixtureMemoryTotal, AvailableBytes: f.available(now)},
		Load:   host.Load{One: 0.84, Five: 1.12, Fifteen: 1.31},
		CPUs:   6,
		Uptime: 12*24*time.Hour + 5*time.Hour,
		Disks: []host.Disk{
			{Label: "root", Path: "/host", TotalBytes: 249_000_000_000, FreeBytes: 134_000_000_000, Device: 1},
		},
	}, nil
}

// History is a made-up day of samples up to now, one a minute. Life OS
// used more memory and CPU in the middle of the day (a transcription, say)
// than it does now.
func (f Fixture) History(now time.Time) []Sample {
	var samples []Sample
	for minute := HistorySize - 1; minute >= 1; minute-- {
		at := now.Add(-time.Duration(minute) * time.Minute)
		snapshot, _ := Fixture{Scenario: FixtureHealthy}.Read(at)
		if f.Scenario == FixtureLowMemory && minute <= 10 {
			snapshot.Memory.AvailableBytes = f.available(at)
		}
		usage := f.Usage(at, snapshot, f.containers())
		hump := math.Sin(float64(minute) / HistorySize * math.Pi)
		usage.LifeOS.MemoryBytes += uint64(hump * 400 * 1024 * 1024)
		usage.LifeOS.CPUCores += hump * hump * 0.8
		samples = append(samples, SampleOf(snapshot, usage))
	}
	return samples
}

// fixtureContainers are the made-up containers, with their memory as
// docker stats and their cgroups show it (the same, for simplicity) and
// the CPU cores they use.
var fixtureContainers = []struct {
	container docker.Container
	memory    uint64
	cpu       float64
}{
	{docker.Container{ID: "0a1b2c3d4e01", Name: "lifeos-postgres-1", Project: "lifeos", Service: "postgres", Image: "postgres:18.6-alpine", State: "running", Health: "healthy"}, 310 * 1024 * 1024, 0.05},
	{docker.Container{ID: "0a1b2c3d4e02", Name: "lifeos-whisper-1", Project: "lifeos", Service: "whisper", Image: "lifeos-whisper", State: "running", Health: "healthy"}, 1900 * 1024 * 1024, 0.3},
	{docker.Container{ID: "0a1b2c3d4e03", Name: "lifeos-dashboard-1", Project: "lifeos", Service: "dashboard", Image: "lifeos-dashboard", State: "running"}, 180 * 1024 * 1024, 0.05},
	{docker.Container{ID: "0a1b2c3d4e04", Name: "homeassistant", Project: "homeassistant", Service: "homeassistant", Image: "ghcr.io/home-assistant/home-assistant:stable", State: "running", Health: "healthy"}, 600 * 1024 * 1024, 0.5},
	{docker.Container{ID: "0a1b2c3d4e05", Name: "n8n", Project: "n8n", Service: "n8n", Image: "n8nio/n8n", State: "running"}, 420 * 1024 * 1024, 0.3},
	{docker.Container{ID: "0a1b2c3d4e06", Name: "forgejo-runner", Project: "forgejo", Service: "runner", Image: "code.forgejo.org/forgejo/runner", State: "exited"}, 0, 0},
}

// Containers is the made-up containers.
func (f Fixture) Containers(context.Context) ([]docker.Container, error) {
	containers := make([]docker.Container, 0, len(fixtureContainers))
	for _, item := range fixtureContainers {
		containers = append(containers, item.container)
	}
	return containers, nil
}

// Events has none: nothing restarts on the made-up host.
func (f Fixture) Events(context.Context, time.Time, time.Time) ([]docker.Event, error) {
	return nil, nil
}

// Memory is a made-up container's memory.
func (f Fixture) Memory(_ context.Context, id string) (docker.Memory, error) {
	for _, item := range fixtureContainers {
		if item.container.ID == id {
			return docker.Memory{ID: id, Name: item.container.Name, UsageBytes: item.memory, LimitBytes: fixtureMemoryTotal}, nil
		}
	}
	return docker.Memory{}, fmt.Errorf("no container %s", id)
}

// containers is the made-up containers as the sampler sees them.
func (f Fixture) containers() Containers {
	containers := Containers{Available: true}
	for _, item := range fixtureContainers {
		containers.Items = append(containers.Items, ContainerState{Container: item.container})
	}
	return containers
}

// Usage is the made-up Life OS's use of the server, the same every time:
// its containers' memory and CPU, with CPU figures from the first sample
// on. Everything else is the rest of the made-up server's memory, and the
// other containers' CPU with 0.2 cores for the host itself.
func (f Fixture) Usage(_ time.Time, snapshot host.Snapshot, _ Containers) Usage {
	usage := Usage{Available: true, LifeOS: Share{CPUKnown: true}, Others: Share{CPUKnown: true, CPUCores: 0.2}}
	for _, item := range fixtureContainers {
		if item.container.State != "running" {
			continue
		}
		if item.container.Project != "lifeos" {
			usage.Others.CPUCores += item.cpu
			continue
		}
		usage.LifeOS.MemoryBytes += item.memory
		usage.LifeOS.CPUCores += item.cpu
		usage.Services = append(usage.Services, ServiceUsage{Service: item.container.Service, Containers: 1, MemoryBytes: item.memory})
	}
	slices.SortFunc(usage.Services, func(a, b ServiceUsage) int { return cmp.Compare(b.MemoryBytes, a.MemoryBytes) })
	used := snapshot.Memory.TotalBytes - snapshot.Memory.AvailableBytes
	usage.Others.MemoryBytes = used - min(used, usage.LifeOS.MemoryBytes)
	return usage
}
