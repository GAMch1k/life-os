package health

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/GAMch1k/life-os/services/host-health/internal/docker"
)

// The event sequences Docker reports, as recorded on this server:
//
//	docker stop / compose stop:        kill, die (143 or 0), stop
//	compose down:                      kill, die, stop, destroy
//	compose up --force-recreate:       kill, die, stop, destroy (old id); start (new id, same name)
//	docker restart / compose restart:  kill, die, stop, start (same id)
//	restart policy after a crash:      die (1), start (same id)

// at is a moment in the window that ends at start.
func at(minutesAgo, seconds int) time.Time {
	return start.Add(-time.Duration(minutesAgo)*time.Minute + time.Duration(seconds)*time.Second)
}

func event(when time.Time, id, name, project, action string, exitCode ...int) docker.Event {
	e := docker.Event{Time: when, ID: id, Name: name, Project: project, Action: action}
	if len(exitCode) > 0 {
		e.ExitCode = &exitCode[0]
	}
	return e
}

// restartFindings feeds events to a tracker, as one sample would, and
// evaluates the containers listed at start.
func restartFindings(events []docker.Event, containers []docker.Container) ([]Finding, []ContainerState) {
	tracker := NewTracker(15 * time.Minute)
	tracker.Events(events)
	states := tracker.Update(start, containers)
	return Evaluate(snapshot(4), nil, Containers{Available: true, Items: states}, true, DefaultThresholds()), states
}

func TestIntentionalStopsAreNoRestartLoop(t *testing.T) {
	for name, tc := range map[string]struct {
		events     []docker.Event
		containers []docker.Container
	}{
		// Test runs bring a stack up and down every few minutes: new ids
		// each time, the same names.
		"compose down and up": {
			events: func() []docker.Event {
				var events []docker.Event
				for run := range 4 {
					old, next := fmt.Sprintf("0000000000%02d", run), fmt.Sprintf("0000000000%02d", run+1)
					minutes := 12 - 3*run
					events = append(events,
						event(at(minutes, 0), old, "lifeos-gateway-1", "lifeos", "kill"),
						event(at(minutes, 1), old, "lifeos-gateway-1", "lifeos", "die", 0),
						event(at(minutes, 2), old, "lifeos-gateway-1", "lifeos", "stop"),
						event(at(minutes, 3), old, "lifeos-gateway-1", "lifeos", "destroy"),
						event(at(minutes, 30), next, "lifeos-gateway-1", "lifeos", "start"),
					)
				}
				return events
			}(),
			containers: []docker.Container{{ID: "000000000004", Name: "lifeos-gateway-1", Project: "lifeos", State: "running"}},
		},
		// A redeploy: the old container is killed and destroyed, a new one
		// with a new id and the same name starts.
		"force-recreate": {
			events: func() []docker.Event {
				var events []docker.Event
				for run, id := range []string{"aaaaaaaaaaa1", "aaaaaaaaaaa2", "aaaaaaaaaaa3"} {
					minutes := 10 - 4*run
					events = append(events,
						event(at(minutes, 0), id, "lifeos-inbox-1", "lifeos", "kill"),
						event(at(minutes, 5), id, "lifeos-inbox-1", "lifeos", "die", 2),
						event(at(minutes, 6), id, "lifeos-inbox-1", "lifeos", "stop"),
						event(at(minutes, 7), id, "lifeos-inbox-1", "lifeos", "destroy"),
						event(at(minutes, 8), fmt.Sprintf("bbbbbbbbbbb%d", run+1), "lifeos-inbox-1", "lifeos", "start"),
					)
				}
				return events
			}(),
			containers: []docker.Container{{ID: "bbbbbbbbbbb3", Name: "lifeos-inbox-1", Project: "lifeos", State: "running"}},
		},
		// The owner stops and starts the same container, and restarts it.
		"docker stop and start, docker restart": {
			events: func() []docker.Event {
				var events []docker.Event
				for run := range 3 {
					minutes := 12 - 4*run
					events = append(events,
						event(at(minutes, 0), "cccccccccccc", "n8n", "n8n", "kill"),
						event(at(minutes, 10), "cccccccccccc", "n8n", "n8n", "die", 143),
						event(at(minutes, 11), "cccccccccccc", "n8n", "n8n", "stop"),
						event(at(minutes, 50), "cccccccccccc", "n8n", "n8n", "start"),
					)
				}
				// docker restart: no die without its kill, and a stop.
				return append(events,
					event(at(1, 0), "cccccccccccc", "n8n", "n8n", "kill"),
					event(at(1, 1), "cccccccccccc", "n8n", "n8n", "die", 0),
					event(at(1, 2), "cccccccccccc", "n8n", "n8n", "stop"),
					event(at(1, 3), "cccccccccccc", "n8n", "n8n", "start"),
				)
			}(),
			containers: []docker.Container{{ID: "cccccccccccc", Name: "n8n", Project: "n8n", State: "running"}},
		},
		// A container that crashed and stayed down is no loop.
		"a crash without a restart": {
			events: []docker.Event{
				event(at(9, 0), "dddddddddddd", "worker", "jobs", "die", 1),
				event(at(5, 0), "eeeeeeeeeeee", "worker-2", "jobs", "die", 1),
				event(at(5, 1), "eeeeeeeeeeee", "worker-2", "jobs", "stop"),
			},
			containers: []docker.Container{{ID: "dddddddddddd", Name: "worker", Project: "jobs", State: "exited"}},
		},
	} {
		findings, states := restartFindings(tc.events, tc.containers)
		if len(findings) != 0 {
			t.Errorf("%s: findings %+v", name, findings)
		}
		for _, state := range states {
			if state.RecentDeaths != 0 {
				t.Errorf("%s: %s has %d restarts", name, state.Name, state.RecentDeaths)
			}
		}
	}
}

func TestARestartPolicyLoopIsFound(t *testing.T) {
	// The forgejo runner exits 1 and its restart policy starts it again,
	// the same container each time.
	var events []docker.Event
	for _, minutes := range []int{14, 9, 4} {
		events = append(events,
			event(at(minutes, 0), "ffffffffffff", "runner", "forgejo", "die", 1),
			event(at(minutes, 2), "ffffffffffff", "runner", "forgejo", "start"),
		)
	}
	containers := []docker.Container{{ID: "ffffffffffff", Name: "runner", Project: "forgejo", State: "restarting"}}
	findings, states := restartFindings(events, containers)
	if len(findings) != 1 || findings[0].Kind != ContainerRestarting || findings[0].Subject != "runner" || findings[0].Project != "forgejo" || findings[0].Count != 3 || findings[0].Minutes != 15 ||
		findings[0].ExitCode == nil || *findings[0].ExitCode != 1 {
		t.Fatalf("findings = %+v", findings)
	}
	if states[0].LastExitCode == nil || *states[0].LastExitCode != 1 {
		t.Fatalf("state = %+v", states[0])
	}

	// A die still waiting for its start counts once the start comes, in a
	// later sample, and an OOM kill (no kill event) is a crash too.
	tracker := NewTracker(15 * time.Minute)
	tracker.Events([]docker.Event{event(at(10, 0), "ffffffffffff", "runner", "forgejo", "die", 137)})
	if states := tracker.Update(at(10, 1), containers); states[0].RecentDeaths != 0 {
		t.Fatalf("counted before the start: %+v", states[0])
	}
	tracker.Events([]docker.Event{event(at(10, 3), "ffffffffffff", "runner", "forgejo", "start")})
	if states := tracker.Update(start, containers); states[0].RecentDeaths != 1 || *states[0].LastExitCode != 137 {
		t.Fatalf("after the start: %+v", states[0])
	}

	// A kill long before a crash is not its stop: a reload signal, say.
	reloaded := NewTracker(15 * time.Minute)
	reloaded.Events([]docker.Event{
		event(at(14, 0), "ffffffffffff", "runner", "forgejo", "kill"),
		event(at(5, 0), "ffffffffffff", "runner", "forgejo", "die", 1),
		event(at(5, 2), "ffffffffffff", "runner", "forgejo", "start"),
	})
	if states := reloaded.Update(start, containers); states[0].RecentDeaths != 1 {
		t.Fatalf("a crash after an old kill: %+v", states[0])
	}
}

// upTo takes the events up to now off the front of events.
func upTo(events *[]docker.Event, now time.Time) []docker.Event {
	n := 0
	for n < len(*events) && !(*events)[n].Time.After(now) {
		n++
	}
	taken := (*events)[:n]
	*events = (*events)[n:]
	return taken
}

func TestIgnoredProjectsAreLeftOutOfFindings(t *testing.T) {
	// The verification stack's gateway crash-loops and is unhealthy; it
	// uses memory too. None of it is a finding, or among the top users.
	var events []docker.Event
	for _, minutes := range []int{12, 8, 3} {
		events = append(events,
			event(at(minutes, 0), "999999999999", "lifeos-verify-gateway-1", "lifeos-verify", "die", 1),
			event(at(minutes, 1), "999999999999", "lifeos-verify-gateway-1", "lifeos-verify", "start"),
		)
	}
	ignoredStream, watchedStream := newFakeStream(start.Add(-20*time.Minute)), newFakeStream(start.Add(-20*time.Minute))
	source := &fakeDocker{
		containers: []docker.Container{
			{ID: "999999999999", Name: "lifeos-verify-gateway-1", Project: "lifeos-verify", State: "running", Health: "unhealthy"},
			{ID: "888888888888", Name: "lifeos-verify-whisper-1", Project: "lifeos-verify", State: "restarting"},
			{ID: "777777777777", Name: "lifeos-whisper-1", Project: "lifeos", State: "running"},
		},
		memory:  map[string]uint64{"999999999999": 3 << 30, "777777777777": 1 << 30},
		streams: []*fakeStream{ignoredStream, watchedStream},
	}
	now := start.Add(-20 * time.Minute)
	sampler := NewSampler(Options{Mode: "host", Host: &fakeHost{snapshot: snapshot(4)}, Docker: source, Thresholds: DefaultThresholds(), IgnoreProjects: []string{"lifeos-verify"}, Now: func() time.Time { return now }})
	watch(t, sampler)
	pending := slices.Clone(events)
	for now.Before(start) {
		now = now.Add(time.Minute)
		ignoredStream.feed(t, upTo(&pending, now)...)
		sampler.SampleOnce(context.Background())
	}
	status, _ := sampler.Status()
	if len(status.Findings) != 0 {
		t.Fatalf("findings = %+v", status.Findings)
	}
	total, running, unhealthy, restarting, testStack := status.Containers.Counts()
	if total != 3 || running != 2 || unhealthy != 0 || restarting != 0 || testStack != 2 {
		t.Fatalf("counts = %d total, %d running, %d unhealthy, %d restarting, %d test stack", total, running, unhealthy, restarting, testStack)
	}
	top := status.Containers.Top(5)
	if len(top) != 1 || top[0].Name != "lifeos-whisper-1" {
		t.Fatalf("top = %+v", top)
	}
	// Its memory is not even read: only lifeos-whisper-1's, every five
	// minutes of the twenty.
	if source.memoryRead != 4 {
		t.Fatalf("memory read %d times", source.memoryRead)
	}
	// Twenty minutes of watching: the counts are complete.
	if !status.Containers.RestartsComplete {
		t.Fatalf("restarts since %s not complete", status.Containers.RestartsSince)
	}
	marked := slices.IndexFunc(status.Containers.Items, func(item ContainerState) bool { return item.TestStack })
	if marked != 0 || !status.Containers.Items[1].TestStack || status.Containers.Items[2].TestStack {
		t.Fatalf("items = %+v", status.Containers.Items)
	}

	// Not ignored, the same loop is a finding.
	now = start.Add(-20 * time.Minute)
	watched := NewSampler(Options{Mode: "host", Host: &fakeHost{snapshot: snapshot(4)}, Docker: source, Thresholds: DefaultThresholds(), Now: func() time.Time { return now }})
	watch(t, watched)
	pending = slices.Clone(events)
	for now.Before(start) {
		now = now.Add(time.Minute)
		watchedStream.feed(t, upTo(&pending, now)...)
		watched.SampleOnce(context.Background())
	}
	status, _ = watched.Status()
	if !slices.ContainsFunc(status.Findings, func(f Finding) bool {
		return f.Kind == ContainerRestarting && f.Subject == "lifeos-verify-gateway-1" && f.Count == 3
	}) {
		t.Fatalf("not ignored: findings %+v", status.Findings)
	}
}
