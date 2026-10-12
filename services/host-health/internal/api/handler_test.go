package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/GAMch1k/life-os/pkg/httpserver"
	"github.com/GAMch1k/life-os/services/host-health/internal/api/generated"
	"github.com/GAMch1k/life-os/services/host-health/internal/docker"
	"github.com/GAMch1k/life-os/services/host-health/internal/health"
)

var now = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

func newApp(t *testing.T, scenario string, sample bool) *httpserver.App {
	t.Helper()
	fixture := health.Fixture{Scenario: scenario}
	sampler := health.NewSampler(health.Options{Mode: "fixture", Host: fixture, Docker: fixture, Usage: fixture, Thresholds: health.DefaultThresholds(), Now: func() time.Time { return now }})
	sampler.Prefill(fixture.History(now))
	if sample {
		sampler.SampleOnce(context.Background())
	}
	app := httpserver.New("host-health")
	handler := NewHandler(sampler, time.Minute)
	handler.now = func() time.Time { return now }
	Register(app, handler)
	return app
}

func get(t *testing.T, app *httpserver.App, target string, into any) int {
	t.Helper()
	response, err := app.Test(httptest.NewRequest(http.MethodGet, target, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	if into != nil && response.StatusCode == http.StatusOK {
		if err := json.Unmarshal(data, into); err != nil {
			t.Fatalf("%s: %v", data, err)
		}
	}
	return response.StatusCode
}

func TestStatusBeforeTheFirstSample(t *testing.T) {
	if status := get(t, newApp(t, health.FixtureHealthy, false), "/v1/status", nil); status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", status)
	}
}

func TestStatusOfAHealthyHost(t *testing.T) {
	var got generated.HostStatus
	if status := get(t, newApp(t, health.FixtureHealthy, true), "/v1/status", &got); status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if got.Mode != generated.Fixture || !got.SampledAt.Equal(now) || got.Memory.TotalBytes != 8_045_404*1024 || got.Load.Cpus != 6 || got.UptimeSeconds != 12*86400+5*3600 {
		t.Fatalf("status = %+v", got)
	}
	if len(got.Disks) != 1 || got.Disks[0].Label != "root" || got.Disks[0].FreePercent != 53.8 {
		t.Fatalf("disks = %+v", got.Disks)
	}
	containers := got.Containers
	if !containers.Available || containers.Total != 6 || containers.Running != 5 || containers.Unhealthy != 0 || len(containers.Top) != 5 || containers.Top[0].Name != "lifeos-whisper-1" || containers.MemorySampledAt == nil {
		t.Fatalf("containers = %+v", containers)
	}
	if got.Thresholds.MemoryWarningPercent != 15 || got.Thresholds.DiskProblemBytes != 3<<30 || got.Thresholds.RestartLoopMinutes != 15 || len(got.Findings) != 0 {
		t.Fatalf("thresholds = %+v, findings = %+v", got.Thresholds, got.Findings)
	}
	// 2390 MiB of 7.7 GiB, and 0.4 of 6 CPUs.
	usage := got.Usage
	if usage == nil || !usage.Available || usage.LifeOs == nil || usage.LifeOs.MemoryBytes != 2390<<20 || usage.LifeOs.MemoryPercent != 30.4 || *usage.LifeOs.CpuCores != 0.4 || *usage.LifeOs.CpuPercent != 6.7 {
		t.Fatalf("usage = %+v", usage)
	}
	if usage.Tests != nil || usage.Others == nil || *usage.Others.CpuCores != 1 || len(usage.Services) != 3 || usage.Services[0] != (generated.HostServiceUsage{Service: "whisper", Containers: 1, MemoryBytes: 1900 << 20}) {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestStatusWithLowMemory(t *testing.T) {
	var got generated.HostStatus
	get(t, newApp(t, health.FixtureLowMemory, true), "/v1/status", &got)
	if len(got.Findings) != 1 {
		t.Fatalf("findings = %+v", got.Findings)
	}
	finding := got.Findings[0]
	if finding.Kind != generated.MemoryLow || finding.Severity != generated.Problem || *finding.AvailableBytes != 520<<20 || *finding.TotalBytes != 8_045_404*1024 || *finding.Minutes != 11 || finding.Subject != nil {
		t.Fatalf("finding = %+v", finding)
	}
}

func TestHistory(t *testing.T) {
	app := newApp(t, health.FixtureHealthy, true)
	var day generated.HostHistory
	if status := get(t, app, "/v1/history", &day); status != http.StatusOK || day.IntervalSeconds != 60 || len(day.Samples) != health.HistorySize {
		t.Fatalf("history = %d, %d samples", status, len(day.Samples))
	}
	last := day.Samples[len(day.Samples)-1]
	if !last.At.Equal(now) || last.MemoryTotalBytes != 8_045_404*1024 || len(last.Disks) != 1 || last.Disks[0].Label != "root" {
		t.Fatalf("last = %+v", last)
	}
	if last.LifeOsMemoryBytes == nil || *last.LifeOsMemoryBytes != 2390<<20 || last.LifeOsCpuCores == nil || *last.LifeOsCpuCores != 0.4 {
		t.Fatalf("last Life OS = %v, %v", last.LifeOsMemoryBytes, last.LifeOsCpuCores)
	}
	var hour generated.HostHistory
	if get(t, app, "/v1/history?hours=1", &hour); len(hour.Samples) != 61 {
		t.Fatalf("an hour has %d samples", len(hour.Samples))
	}
	for _, target := range []string{"/v1/history?hours=0", "/v1/history?hours=25", "/v1/history?hours=many"} {
		if status := get(t, app, target, nil); status != http.StatusBadRequest {
			t.Errorf("%s = %d", target, status)
		}
	}
}

func TestContainersAndFindingsCarryTheTestStackAndExitCodes(t *testing.T) {
	one := 1
	containers := toContainers(health.Containers{Available: true, Items: []health.ContainerState{
		{Container: dockerContainer("runner", "forgejo", "restarting"), RecentDeaths: 3, LastExitCode: &one},
		{Container: dockerContainer("lifeos-verify-gateway-1", "lifeos-verify", "running"), TestStack: true, MemoryBytes: 1 << 30},
		{Container: dockerContainer("lifeos-whisper-1", "lifeos", "running"), MemoryBytes: 2 << 30},
	}})
	if containers.Total != 3 || containers.Running != 2 || containers.Restarting != 1 || containers.TestStack == nil || *containers.TestStack != 1 {
		t.Fatalf("counts = %+v", containers)
	}
	if runner := containers.Items[0]; runner.LastExitCode == nil || *runner.LastExitCode != 1 || runner.TestStack != nil {
		t.Fatalf("runner = %+v", runner)
	}
	if verify := containers.Items[1]; verify.TestStack == nil || !*verify.TestStack || verify.LastExitCode != nil {
		t.Fatalf("test stack container = %+v", verify)
	}
	if len(containers.Top) != 1 || containers.Top[0].Name != "lifeos-whisper-1" {
		t.Fatalf("top = %+v", containers.Top)
	}
	finding := toFinding(health.Finding{Kind: health.ContainerRestarting, Severity: health.Warning, Subject: "runner", Project: "forgejo", Count: 3, ExitCode: &one, Minutes: 15})
	if finding.Count == nil || *finding.Count != 3 || finding.ExitCode == nil || *finding.ExitCode != 1 || finding.Minutes == nil || *finding.Minutes != 15 {
		t.Fatalf("finding = %+v", finding)
	}
}

func TestUsageIsLeftOutWhenUnknown(t *testing.T) {
	// Not readable: only why.
	usage := toUsage(health.Usage{Error: "cgroups: open /host-cgroup: permission denied"}, 8<<30, 6)
	if usage.Available || usage.Error == nil || usage.LifeOs != nil || usage.Others != nil || usage.Services == nil {
		t.Fatalf("unavailable = %+v", usage)
	}
	// The first reading has no CPU yet; the test stack shows while it runs;
	// five services at most.
	reading := health.Usage{Available: true, LifeOS: health.Share{MemoryBytes: 2 << 30}, Tests: health.Share{MemoryBytes: 1 << 30, CPUCores: 0.333, CPUKnown: true}, TestsRunning: true}
	for _, name := range []string{"a", "b", "c", "d", "e", "f"} {
		reading.Services = append(reading.Services, health.ServiceUsage{Service: name, Containers: 1, MemoryBytes: 1 << 20})
	}
	usage = toUsage(reading, 8<<30, 6)
	if usage.LifeOs.MemoryPercent != 25 || usage.LifeOs.CpuCores != nil || usage.LifeOs.CpuPercent != nil || len(usage.Services) != 5 {
		t.Fatalf("first reading = %+v", usage)
	}
	if usage.Tests == nil || usage.Tests.MemoryPercent != 12.5 || *usage.Tests.CpuCores != 0.33 || *usage.Tests.CpuPercent != 5.6 {
		t.Fatalf("tests = %+v", usage.Tests)
	}
	reading.TestsRunning = false
	if usage := toUsage(reading, 8<<30, 6); usage.Tests != nil {
		t.Fatalf("tests after compose down = %+v", usage.Tests)
	}
}

func dockerContainer(name, project, state string) docker.Container {
	return docker.Container{ID: name, Name: name, Project: project, Image: name, State: state}
}
