package health

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/GAMch1k/life-os/services/host-health/internal/docker"
	"github.com/GAMch1k/life-os/services/host-health/internal/host"
)

var start = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

const gib = 1 << 30

func TestRingKeepsTheLatestSamples(t *testing.T) {
	ring := NewRing(3)
	if len(ring.All()) != 0 || ring.Since(start) != nil {
		t.Fatal("a new ring has samples")
	}
	for i := range 5 {
		ring.Add(Sample{At: start.Add(time.Duration(i) * time.Minute), MemoryAvailable: uint64(i)})
	}
	var kept []uint64
	for _, sample := range ring.All() {
		kept = append(kept, sample.MemoryAvailable)
	}
	if !slices.Equal(kept, []uint64{2, 3, 4}) {
		t.Fatalf("All = %v, want the newest three, oldest first", kept)
	}
	if since := ring.Since(start.Add(3 * time.Minute)); len(since) != 2 || since[0].MemoryAvailable != 3 {
		t.Fatalf("Since = %+v", since)
	}
	if last := ring.Last(2); len(last) != 2 || last[1].MemoryAvailable != 4 {
		t.Fatalf("Last = %+v", last)
	}
	if last := ring.Last(10); len(last) != 3 {
		t.Fatalf("Last(10) = %d samples", len(last))
	}
}

// snapshot is a healthy 8 GiB, 6-CPU host with one 250 GiB disk.
func snapshot(available float64) host.Snapshot {
	return host.Snapshot{
		At:     start,
		Memory: host.Memory{TotalBytes: 8 * gib, AvailableBytes: uint64(available * gib)},
		Load:   host.Load{One: 1, Five: 1, Fifteen: 1},
		CPUs:   6,
		Uptime: 48 * time.Hour,
		Disks:  []host.Disk{{Label: "root", TotalBytes: 250 * gib, FreeBytes: 120 * gib, Device: 1}},
	}
}

// history is n samples a minute apart, ending at start, with memory
// available as given.
func history(available ...float64) []Sample {
	var samples []Sample
	for i, value := range available {
		samples = append(samples, Sample{At: start.Add(time.Duration(i-len(available)+1) * time.Minute), MemoryAvailable: uint64(value * gib), MemoryTotal: 8 * gib})
	}
	return samples
}

func kinds(findings []Finding) []string {
	var out []string
	for _, finding := range findings {
		out = append(out, string(finding.Severity)+" "+string(finding.Kind)+" "+finding.Subject)
	}
	return out
}

func TestEvaluateFindsLowMemory(t *testing.T) {
	thresholds := DefaultThresholds()
	// 15% of 8 GiB is 1.2 GiB; 7% is 0.56 GiB.
	if findings := Evaluate(snapshot(1.1), history(4, 1.1, 1.1), Containers{}, false, thresholds); len(findings) != 0 {
		t.Fatalf("two low samples are not enough: %v", kinds(findings))
	}
	findings := Evaluate(snapshot(1.1), history(1.1, 1.1, 1.1), Containers{}, false, thresholds)
	if len(findings) != 1 || findings[0].Kind != MemoryLow || findings[0].Severity != Warning || findings[0].Minutes != 3 || findings[0].AvailableBytes != snapshot(1.1).Memory.AvailableBytes {
		t.Fatalf("three low samples = %+v", findings)
	}
	findings = Evaluate(snapshot(0.5), history(4, 4, 0.5), Containers{}, false, thresholds)
	if len(findings) != 1 || findings[0].Severity != Problem {
		t.Fatalf("below 7%% at once = %+v", findings)
	}
	if findings := Evaluate(snapshot(1.3), history(1.3, 1.3, 1.3), Containers{}, false, thresholds); len(findings) != 0 {
		t.Fatalf("above 15%% = %v", kinds(findings))
	}
}

func TestEvaluateFindsLowDisksAndHighLoad(t *testing.T) {
	thresholds := DefaultThresholds()
	s := snapshot(4)
	s.Disks = []host.Disk{
		{Label: "root", TotalBytes: 250 * gib, FreeBytes: 9 * gib},      // under 10 GiB
		{Label: "docker", TotalBytes: 2000 * gib, FreeBytes: 150 * gib}, // 7.5%
		{Label: "small", TotalBytes: 50 * gib, FreeBytes: 2 * gib},      // under 3 GiB
		{Label: "fine", TotalBytes: 50 * gib, FreeBytes: 20 * gib},
	}
	s.Load.Fifteen = 6.5
	got := kinds(Evaluate(s, history(4), Containers{}, false, thresholds))
	want := []string{"problem disk_low small", "warning disk_low root", "warning disk_low docker", "warning load_high "}
	if !slices.Equal(got, want) {
		t.Fatalf("findings = %v\nwant %v", got, want)
	}
	s.Load.Fifteen = 6
	if got := kinds(Evaluate(s, nil, Containers{}, false, thresholds)); slices.Contains(got, "warning load_high ") {
		t.Fatal("a load equal to the CPUs is not high")
	}
}

func TestEvaluateFindsContainerTrouble(t *testing.T) {
	thresholds := DefaultThresholds()
	containers := Containers{Available: true, Items: []ContainerState{
		{Container: docker.Container{Name: "homeassistant", Project: "homeassistant", State: "restarting"}, RecentDeaths: 3},
		{Container: docker.Container{Name: "flaky", State: "running"}, RecentDeaths: 2},
		{Container: docker.Container{Name: "n8n", Project: "n8n", State: "running", Health: "unhealthy"}, UnhealthySince: start.Add(-6 * time.Minute)},
		{Container: docker.Container{Name: "fresh", State: "running", Health: "unhealthy"}, UnhealthySince: start.Add(-4 * time.Minute)},
	}}
	findings := Evaluate(snapshot(4), nil, containers, true, thresholds)
	if got := kinds(findings); !slices.Equal(got, []string{"warning container_restarting homeassistant", "warning container_unhealthy n8n"}) {
		t.Fatalf("findings = %v", got)
	}
	if findings[0].Count != 3 || findings[0].Minutes != 15 || findings[1].Minutes != 6 || findings[1].Project != "n8n" {
		t.Fatalf("details = %+v", findings)
	}
	unavailable := Evaluate(snapshot(4), nil, Containers{Error: "the docker gateway cannot be reached"}, true, thresholds)
	if len(unavailable) != 1 || unavailable[0].Kind != ContainersUnavailable || unavailable[0].Error == "" {
		t.Fatalf("unavailable = %+v", unavailable)
	}
	// Without a gateway set up, no container facts is not a finding.
	if findings := Evaluate(snapshot(4), nil, Containers{Error: "no docker gateway is set up"}, false, thresholds); len(findings) != 0 {
		t.Fatalf("no gateway = %+v", findings)
	}
}

func TestTrackerCountsDeathsAndUnhealthyTime(t *testing.T) {
	tracker := NewTracker(15 * time.Minute)
	// A death Docker restarted by itself: die, then start of the same id.
	restart := func(minutesAgo int) []docker.Event {
		at := start.Add(-time.Duration(minutesAgo) * time.Minute)
		return []docker.Event{
			{Time: at, ID: "fedcba987654", Name: "homeassistant", Action: "die"},
			{Time: at.Add(2 * time.Second), ID: "fedcba987654", Name: "homeassistant", Action: "start"},
		}
	}
	tracker.Events(slices.Concat(restart(20), restart(10), restart(5), restart(1)))
	// The same events again (an overlapping poll) count once.
	tracker.Events(slices.Concat(restart(5), restart(1)))
	tracker.Events([]docker.Event{{Time: start.Add(-30 * time.Minute), ID: "aaaaaaaaaaaa", Name: "n8n", Action: "health_status: unhealthy"}})
	containers := []docker.Container{
		{ID: "fedcba987654", Name: "homeassistant", State: "running"},
		{ID: "aaaaaaaaaaaa", Name: "n8n", State: "running", Health: "unhealthy"},
		{ID: "bbbbbbbbbbbb", Name: "new", State: "running", Health: "unhealthy"},
	}
	tracker.SetMemory(map[string]uint64{"fedcba987654": 600 << 20})
	states := tracker.Update(start, containers)
	if states[0].RecentDeaths != 3 || states[0].MemoryBytes != 600<<20 {
		t.Fatalf("homeassistant = %+v (the death 20 minutes ago is outside the window)", states[0])
	}
	// n8n turned unhealthy before host-health saw it: the event says when.
	if !states[1].UnhealthySince.Equal(start.Add(-30*time.Minute)) || !states[2].UnhealthySince.Equal(start) {
		t.Fatalf("unhealthy since = %s, %s", states[1].UnhealthySince, states[2].UnhealthySince)
	}
	// Later, the deaths age out and n8n recovers.
	later := start.Add(20 * time.Minute)
	containers[1].Health = "healthy"
	states = tracker.Update(later, containers)
	if states[0].RecentDeaths != 0 || !states[1].UnhealthySince.IsZero() || !states[2].UnhealthySince.Equal(start) {
		t.Fatalf("later = %+v", states)
	}
}

// fakeDocker counts its calls and can fail.
type fakeDocker struct {
	mu         sync.Mutex
	containers []docker.Container
	memory     map[string]uint64
	fail       error
	memoryRead int
	eventsFrom []time.Time
}

func (f *fakeDocker) Containers(context.Context) ([]docker.Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.containers, f.fail
}

func (f *fakeDocker) Events(_ context.Context, since, _ time.Time) ([]docker.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.eventsFrom = append(f.eventsFrom, since)
	return nil, nil
}

func (f *fakeDocker) Memory(_ context.Context, id string) (docker.Memory, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.memoryRead++
	return docker.Memory{ID: id, UsageBytes: f.memory[id]}, nil
}

// fakeHost reads a snapshot it is given.
type fakeHost struct {
	snapshot host.Snapshot
	err      error
}

func (f *fakeHost) Read(now time.Time) (host.Snapshot, error) {
	s := f.snapshot
	s.At = now
	return s, f.err
}

func TestSamplerSamplesTheHostAndItsContainers(t *testing.T) {
	now := start
	hostSource := &fakeHost{snapshot: snapshot(4)}
	dockerSource := &fakeDocker{
		containers: []docker.Container{{ID: "a", Name: "whisper", State: "running"}, {ID: "b", Name: "homeassistant", State: "running"}, {ID: "c", Name: "runner", State: "exited"}},
		memory:     map[string]uint64{"a": 1900 << 20, "b": 600 << 20},
	}
	sampler := NewSampler(Options{Mode: "host", Host: hostSource, Docker: dockerSource, Thresholds: DefaultThresholds(), Now: func() time.Time { return now }})
	if err := sampler.Ready(context.Background()); err == nil {
		t.Fatal("ready before the first sample")
	}
	if _, ok := sampler.Status(); ok {
		t.Fatal("a status before the first sample")
	}
	sampler.SampleOnce(context.Background())
	status, ok := sampler.Status()
	if !ok || status.Mode != "host" || len(status.Findings) != 0 || !status.Containers.Available {
		t.Fatalf("status = %+v", status)
	}
	if top := status.Containers.Top(5); len(top) != 2 || top[0].Name != "whisper" || dockerSource.memoryRead != 2 {
		t.Fatalf("top = %+v after %d memory reads (running containers only)", top, dockerSource.memoryRead)
	}
	if total, running, _, _, _ := status.Containers.Counts(); total != 3 || running != 2 {
		t.Fatalf("counts = %d, %d", total, running)
	}
	// Events are read from 15 minutes back, then from the last read on.
	if !dockerSource.eventsFrom[0].Equal(start.Add(-15 * time.Minute)) {
		t.Fatalf("first events from %s", dockerSource.eventsFrom[0])
	}

	// A minute later memory is not read again (every 5 minutes) …
	now = now.Add(time.Minute)
	sampler.SampleOnce(context.Background())
	if dockerSource.memoryRead != 2 || !dockerSource.eventsFrom[1].Equal(start) {
		t.Fatalf("memory read %d times, events from %s", dockerSource.memoryRead, dockerSource.eventsFrom[1])
	}
	// … unless memory is low, when the biggest users matter.
	hostSource.snapshot = snapshot(0.4)
	now = now.Add(time.Minute)
	sampler.SampleOnce(context.Background())
	status, _ = sampler.Status()
	if dockerSource.memoryRead != 4 || len(status.Findings) != 1 || status.Findings[0].Severity != Problem {
		t.Fatalf("low memory: %d reads, findings %+v", dockerSource.memoryRead, status.Findings)
	}
	if history := sampler.History(start); len(history) != 3 {
		t.Fatalf("history has %d samples", len(history))
	}
	if err := sampler.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}

	// The gateway fails: container facts are unavailable, which is a finding.
	hostSource.snapshot = snapshot(4)
	dockerSource.fail = errors.New("the docker gateway cannot be reached")
	now = now.Add(time.Minute)
	sampler.SampleOnce(context.Background())
	status, _ = sampler.Status()
	if status.Containers.Available || len(status.Findings) != 1 || status.Findings[0].Kind != ContainersUnavailable {
		t.Fatalf("status = %+v", status)
	}
	// A sample older than three intervals is not ready.
	now = now.Add(4 * time.Minute)
	if err := sampler.Ready(context.Background()); err == nil {
		t.Fatal("ready with a stale sample")
	}
}

func TestSamplerKeepsTheSnapshotWhenOnlyADiskFails(t *testing.T) {
	hostSource := &fakeHost{snapshot: snapshot(4), err: errors.Join(errors.New("disk docker (/host-docker): permission denied"))}
	sampler := NewSampler(Options{Host: hostSource, Thresholds: DefaultThresholds()})
	sampler.SampleOnce(context.Background())
	status, ok := sampler.Status()
	if !ok || len(status.DiskErrors) != 1 || status.Containers.Available || len(status.Findings) != 0 {
		t.Fatalf("status = %+v", status)
	}
	// A host that cannot be read at all leaves no status.
	broken := NewSampler(Options{Host: &fakeHost{err: errors.New("no meminfo")}, Thresholds: DefaultThresholds()})
	broken.SampleOnce(context.Background())
	if _, ok := broken.Status(); ok {
		t.Fatal("a status without a host reading")
	}
}

func TestFixturesAreTheSameEveryTime(t *testing.T) {
	for _, scenario := range []string{FixtureHealthy, FixtureLowMemory} {
		fixture := Fixture{Scenario: scenario}
		sampler := NewSampler(Options{Mode: "fixture", Host: fixture, Docker: fixture, Thresholds: DefaultThresholds(), Now: func() time.Time { return start }})
		sampler.Prefill(fixture.History(start))
		sampler.SampleOnce(context.Background())
		status, _ := sampler.Status()
		history := sampler.History(start.Add(-24 * time.Hour))
		if len(history) != HistorySize {
			t.Fatalf("%s: %d samples of history", scenario, len(history))
		}
		top := status.Containers.Top(2)
		if len(top) != 2 || top[0].Name != "lifeos-whisper-1" || top[1].Name != "homeassistant" {
			t.Fatalf("%s: top = %+v", scenario, top)
		}
		switch scenario {
		case FixtureHealthy:
			if len(status.Findings) != 0 {
				t.Fatalf("healthy: findings %+v", status.Findings)
			}
		case FixtureLowMemory:
			if len(status.Findings) != 1 || status.Findings[0].Kind != MemoryLow || status.Findings[0].Severity != Problem || status.Findings[0].AvailableBytes != 520<<20 {
				t.Fatalf("low memory: findings %+v", status.Findings)
			}
		}
	}
	if !ValidFixture("healthy") || ValidFixture("broken") {
		t.Fatal("ValidFixture")
	}
}
