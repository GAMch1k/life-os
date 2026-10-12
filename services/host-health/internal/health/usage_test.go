package health

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GAMch1k/life-os/services/host-health/internal/docker"
	"github.com/GAMch1k/life-os/services/host-health/internal/host"
)

const mib = 1 << 20

// cgroupTree is a fake /sys/fs/cgroup/system.slice: one docker-<id>.scope
// per container, with the files the Meter reads.
type cgroupTree struct {
	t   *testing.T
	dir string
}

func newCgroupTree(t *testing.T) cgroupTree {
	return cgroupTree{t: t, dir: t.TempDir()}
}

func (c cgroupTree) path(id string) string {
	return filepath.Join(c.dir, "docker-"+id+strings.Repeat("0", 64-len(id))+".scope")
}

// set writes a container's memory.current, its page cache on the file
// lists, and its CPU time.
func (c cgroupTree) set(id string, current, cache uint64, cpu time.Duration) {
	c.t.Helper()
	path := c.path(id)
	if err := os.MkdirAll(path, 0o755); err != nil {
		c.t.Fatal(err)
	}
	for file, content := range map[string]string{
		"memory.current": fmt.Sprintf("%d\n", current),
		"memory.stat":    fmt.Sprintf("anon %d\nfile %d\ninactive_file %d\nactive_file 0\nslab_reclaimable 0\n", current-cache, cache, cache),
		"cpu.stat":       fmt.Sprintf("usage_usec %d\n", cpu.Microseconds()),
	} {
		if err := os.WriteFile(filepath.Join(path, file), []byte(content), 0o644); err != nil {
			c.t.Fatal(err)
		}
	}
}

// remove takes a container's cgroup away, as when it stops.
func (c cgroupTree) remove(id string) {
	if err := os.RemoveAll(c.path(id)); err != nil {
		c.t.Fatal(err)
	}
}

func item(id, name, project, service, state string, testStack bool) ContainerState {
	return ContainerState{Container: docker.Container{ID: id, Name: name, Project: project, Service: service, State: state}, TestStack: testStack}
}

// usageHost is an 8 GiB, 6-CPU host with memory available and its CPUs'
// busy and total time (clock ticks) so far.
func usageHost(availableMiB uint64, busy, total uint64) host.Snapshot {
	return host.Snapshot{Memory: host.Memory{TotalBytes: 8 * gib, AvailableBytes: availableMiB * mib}, CPUs: 6, CPUTime: host.CPUTime{Busy: busy, Total: total}}
}

func near(got, want float64) bool { return math.Abs(got-want) < 1e-9 }

func TestMeterCountsLifeOSTheTestStackAndEverythingElse(t *testing.T) {
	tree := newCgroupTree(t)
	meter := &Meter{Cgroups: host.Cgroups{Dir: tree.dir}, Project: "lifeos"}
	postgres := item("aa0000000001", "lifeos-postgres-1", "lifeos", "postgres", "running", false)
	worker1 := item("aa0000000002", "lifeos-voice-worker-1", "lifeos", "voice-worker", "running", false)
	worker2 := item("aa0000000003", "lifeos-voice-worker-2", "lifeos", "voice-worker", "running", false)
	dashboard := item("aa0000000004", "lifeos-dashboard-1", "lifeos", "dashboard", "exited", false)
	verify := item("bb0000000001", "lifeos-verify-gateway-1", "lifeos-verify", "gateway", "running", true)
	n8n := item("cc0000000001", "n8n", "n8n", "n8n", "running", false)
	tree.set("aa0000000001", 600*mib, 200*mib, 10*time.Second)
	tree.set("aa0000000002", 300*mib, 0, 5*time.Second)
	tree.set("aa0000000003", 200*mib, 0, 5*time.Second)
	tree.set("bb0000000001", 1024*mib, 0, 100*time.Second)
	// Another project's cgroup is never read: everything else is the rest.
	tree.set("cc0000000001", 3*gib, 0, time.Hour)

	// The first reading: memory, but no CPU yet.
	containers := Containers{Available: true, Items: []ContainerState{postgres, worker1, worker2, dashboard, verify, n8n}}
	usage := meter.Usage(start, usageHost(4096, 1000, 6000), containers)
	if !usage.Available || usage.LifeOS != (Share{MemoryBytes: 900 * mib}) || usage.Tests != (Share{MemoryBytes: 1024 * mib}) || !usage.TestsRunning {
		t.Fatalf("first = %+v", usage)
	}
	// 4 GiB in use on the server, less Life OS and the tests.
	if usage.Others != (Share{MemoryBytes: (4096 - 900 - 1024) * mib}) {
		t.Fatalf("others = %+v", usage.Others)
	}
	// Services by memory, replicas summed; the stopped dashboard has none.
	if len(usage.Services) != 2 || usage.Services[0] != (ServiceUsage{Service: "voice-worker", Containers: 2, MemoryBytes: 500 * mib}) || usage.Services[1] != (ServiceUsage{Service: "postgres", Containers: 1, MemoryBytes: 400 * mib}) {
		t.Fatalf("services = %+v", usage.Services)
	}

	// A minute later: CPU is the time used in between. One worker restarted
	// (its CPU time went back) and a new container started: neither adds
	// CPU until its next reading, and neither goes below zero.
	mcp := item("aa0000000005", "lifeos-mcp-1", "lifeos", "mcp", "running", false)
	tree.set("aa0000000001", 600*mib, 200*mib, 16*time.Second)
	tree.set("aa0000000002", 300*mib, 0, 17*time.Second)
	tree.set("aa0000000003", 200*mib, 0, time.Second)
	tree.set("aa0000000005", 100*mib, 0, 30*time.Second)
	tree.set("bb0000000001", 1024*mib, 0, 130*time.Second)
	containers.Items = append(containers.Items, mcp)
	// The CPUs were busy 1800 of 6000 ticks: 30% of 6, 1.8 cores.
	usage = meter.Usage(start.Add(time.Minute), usageHost(4096, 2800, 12000), containers)
	if !usage.LifeOS.CPUKnown || !near(usage.LifeOS.CPUCores, 0.1+0.2) || usage.LifeOS.MemoryBytes != 1000*mib {
		t.Fatalf("life os = %+v", usage.LifeOS)
	}
	if !usage.Tests.CPUKnown || !near(usage.Tests.CPUCores, 0.5) {
		t.Fatalf("tests = %+v", usage.Tests)
	}
	if !usage.Others.CPUKnown || !near(usage.Others.CPUCores, 1.8-0.3-0.5) {
		t.Fatalf("others = %+v", usage.Others)
	}

	// The test stack is taken down and a worker removed: they are gone, not
	// negative. Life OS used more CPU than the server's busy time read a
	// moment before (they are read apart): everything else is zero, as is
	// its memory when Life OS's exceeds what the server shows in use.
	tree.remove("bb0000000001")
	tree.remove("aa0000000002")
	containers.Items = []ContainerState{postgres, worker2, dashboard, mcp, n8n}
	tree.set("aa0000000001", 600*mib, 200*mib, 106*time.Second)
	usage = meter.Usage(start.Add(2*time.Minute), usageHost(8*1024-100, 3400, 18000), containers)
	if usage.TestsRunning || usage.Tests != (Share{}) {
		t.Fatalf("tests after compose down = %+v", usage.Tests)
	}
	if !near(usage.LifeOS.CPUCores, 1.5) || usage.LifeOS.MemoryBytes != 700*mib {
		t.Fatalf("life os = %+v", usage.LifeOS)
	}
	if usage.Others != (Share{CPUKnown: true}) {
		t.Fatalf("others = %+v", usage.Others)
	}
}

func TestMeterWhenNothingCanBeRead(t *testing.T) {
	tree := newCgroupTree(t)
	meter := &Meter{Cgroups: host.Cgroups{Dir: tree.dir}, Project: "lifeos"}
	postgres := item("aa0000000001", "lifeos-postgres-1", "lifeos", "postgres", "running", false)
	containers := Containers{Available: true, Items: []ContainerState{postgres}}

	// Without the containers, nothing tells which cgroups are Life OS's.
	if usage := meter.Usage(start, usageHost(4096, 0, 0), Containers{Error: "unreachable"}); usage.Available || usage.Error != "the containers cannot be read" {
		t.Fatalf("no containers = %+v", usage)
	}
	// Running Life OS containers without a cgroup: the wrong directory.
	if usage := meter.Usage(start, usageHost(4096, 0, 0), containers); usage.Available || !strings.Contains(usage.Error, "none of the 1 running lifeos containers has a cgroup in "+tree.dir) {
		t.Fatalf("no cgroups = %+v", usage)
	}
	// Files that cannot be read.
	tree.set("aa0000000001", 600*mib, 0, time.Second)
	if err := os.WriteFile(filepath.Join(tree.path("aa0000000001"), "memory.stat"), []byte("anon 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if usage := meter.Usage(start, usageHost(4096, 0, 0), containers); usage.Available || !strings.Contains(usage.Error, "memory.stat: no active_file") {
		t.Fatalf("unreadable = %+v", usage)
	}
	// A failed reading is no reading: CPU starts at the next good one.
	tree.set("aa0000000001", 600*mib, 0, time.Second)
	if usage := meter.Usage(start.Add(time.Minute), usageHost(4096, 0, 6000), containers); !usage.Available || usage.LifeOS.CPUKnown || usage.Others.CPUKnown {
		t.Fatalf("first good = %+v", usage)
	}
	// The directory itself gone.
	meter.Cgroups.Dir = filepath.Join(tree.dir, "missing")
	if usage := meter.Usage(start.Add(2*time.Minute), usageHost(4096, 0, 12000), containers); usage.Available || !strings.HasPrefix(usage.Error, "cgroups: ") {
		t.Fatalf("missing = %+v", usage)
	}
}

func TestSamplerReportsLifeOSUseAndKeepsItsHistory(t *testing.T) {
	tree := newCgroupTree(t)
	now := start
	hostSource := &fakeHost{snapshot: snapshot(4)}
	dockerSource := &fakeDocker{containers: []docker.Container{
		{ID: "aa0000000001", Name: "lifeos-whisper-1", Project: "lifeos", Service: "whisper", State: "running"},
		{ID: "bb0000000001", Name: "lifeos-verify-whisper-1", Project: "lifeos-verify", Service: "whisper", State: "running"},
	}}
	tree.set("aa0000000001", 1900*mib, 0, time.Minute)
	tree.set("bb0000000001", 1000*mib, 0, time.Minute)
	sampler := NewSampler(Options{
		Mode: "host", Host: hostSource, Docker: dockerSource, Thresholds: DefaultThresholds(),
		Usage:          &Meter{Cgroups: host.Cgroups{Dir: tree.dir}, Project: "lifeos"},
		IgnoreProjects: []string{"lifeos-verify"},
		Now:            func() time.Time { return now },
	})
	sampler.SampleOnce(context.Background())
	status, _ := sampler.Status()
	if !status.Usage.Available || status.Usage.LifeOS.MemoryBytes != 1900*mib || status.Usage.Tests.MemoryBytes != 1000*mib {
		t.Fatalf("usage = %+v", status.Usage)
	}
	// The test stack's whisper is not a Life OS service.
	if len(status.Usage.Services) != 1 || status.Usage.Services[0].Service != "whisper" || status.Usage.Services[0].MemoryBytes != 1900*mib {
		t.Fatalf("services = %+v", status.Usage.Services)
	}

	// Files that cannot be read: Life OS's use is unavailable, the rest is
	// as always.
	if err := os.WriteFile(filepath.Join(tree.path("aa0000000001"), "cpu.stat"), []byte("user_usec 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	sampler.SampleOnce(context.Background())
	status, _ = sampler.Status()
	if status.Usage.Available || !strings.Contains(status.Usage.Error, "cpu.stat: no usage_usec") || !status.Containers.Available || len(status.Findings) != 0 {
		t.Fatalf("status = %+v", status)
	}
	history := sampler.History(start)
	if len(history) != 2 || history[0].LifeOS == nil || history[0].LifeOS.MemoryBytes != 1900*mib || history[1].LifeOS != nil {
		t.Fatalf("history = %+v", history)
	}

	// Without cgroups set up, it is unavailable and says why.
	plain := NewSampler(Options{Host: hostSource, Thresholds: DefaultThresholds()})
	plain.SampleOnce(context.Background())
	if status, _ := plain.Status(); status.Usage.Available || status.Usage.Error != "no cgroups are set up" {
		t.Fatalf("not set up = %+v", status.Usage)
	}
}
