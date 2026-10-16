package health

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/GAMch1k/life-os/services/host-health/internal/docker"
)

// loop is the forgejo runner's restart-policy loop: a death and its start
// at each of the given moments.
func loop(moments ...time.Time) []docker.Event {
	var events []docker.Event
	for _, at := range moments {
		events = append(events,
			event(at, "ffffffffffff", "runner", "forgejo", "die", 1),
			event(at.Add(2*time.Second), "ffffffffffff", "runner", "forgejo", "start"),
		)
	}
	return events
}

func runnerOf(status Status) ContainerState {
	for _, item := range status.Containers.Items {
		if item.Name == "runner" {
			return item
		}
	}
	return ContainerState{}
}

// BUG-44: host-health restarted during a loop that had died about once a
// minute for a while. Docker answers a since query only from its last 256
// events, so the new stream's replay held the last two minutes only, and
// the count read as cleared ("Server recovered") until three more deaths.
func TestRestartCountsAreIncompleteUntilTheStreamHasWatchedTheWindow(t *testing.T) {
	now := &clock{now: start}
	stream := newFakeStream(start)
	source := &fakeDocker{
		containers: []docker.Container{{ID: "ffffffffffff", Name: "runner", Project: "forgejo", State: "running"}},
		streams:    []*fakeStream{stream},
	}
	sampler := NewSampler(Options{Mode: "host", Host: &fakeHost{snapshot: snapshot(4)}, Docker: source, Thresholds: DefaultThresholds(), Now: now.Now})
	watch(t, sampler)
	if asked := source.asked(); len(asked) != 1 || !asked[0].Equal(start.Add(-15*time.Minute)) {
		t.Fatalf("events asked from %v, want the whole window", asked)
	}
	sample := func(at time.Time) Status {
		t.Helper()
		now.set(at)
		sampler.SampleOnce(context.Background())
		status, _ := sampler.Status()
		return status
	}

	// The replay: what the engine still held, two of the window's fifteen
	// deaths.
	stream.feed(t, loop(start.Add(-110*time.Second), start.Add(-50*time.Second))...)
	status := sample(start.Add(time.Second))
	if runner := runnerOf(status); runner.RecentDeaths != 2 || len(status.Findings) != 0 {
		t.Fatalf("after the replay: %d deaths, findings %+v", runner.RecentDeaths, status.Findings)
	}
	// Two is below the line, and not known to be all: not complete.
	if status.Containers.RestartsComplete || !status.Containers.RestartsSince.Equal(start) {
		t.Fatalf("restarts since %s, complete %t", status.Containers.RestartsSince, status.Containers.RestartsComplete)
	}

	// The next death, seen as it happens, makes three: a loop, complete or
	// not.
	stream.feed(t, loop(start.Add(10*time.Second))...)
	status = sample(start.Add(time.Minute))
	if len(status.Findings) != 1 || status.Findings[0].Kind != ContainerRestarting || status.Findings[0].Count != 3 || status.Containers.RestartsComplete {
		t.Fatalf("a minute in: findings %+v, complete %t", status.Findings, status.Containers.RestartsComplete)
	}

	// The loop stops there. Until the stream has watched fifteen minutes,
	// a count below the line is still not complete …
	status = sample(start.Add(14*time.Minute + 59*time.Second))
	if runner := runnerOf(status); runner.RecentDeaths != 1 || len(status.Findings) != 0 || status.Containers.RestartsComplete {
		t.Fatalf("at 14:59: %d deaths, findings %+v, complete %t", runner.RecentDeaths, status.Findings, status.Containers.RestartsComplete)
	}
	// … and then it is: the loop has really stopped.
	status = sample(start.Add(15 * time.Minute))
	if runner := runnerOf(status); runner.RecentDeaths != 1 || len(status.Findings) != 0 || !status.Containers.RestartsComplete {
		t.Fatalf("at 15:00: %d deaths, findings %+v, complete %t", runner.RecentDeaths, status.Findings, status.Containers.RestartsComplete)
	}
}

func TestABrokenStreamIsOpenedAgainAndWatchesTheWindowAgain(t *testing.T) {
	now := &clock{now: start}
	first, second := newFakeStream(start), newFakeStream(start.Add(20*time.Minute))
	source := &fakeDocker{
		containers: []docker.Container{{ID: "ffffffffffff", Name: "runner", Project: "forgejo", State: "running"}},
		streams:    []*fakeStream{first, second},
		opened:     make(chan time.Time, 2),
	}
	sampler := NewSampler(Options{Mode: "host", Host: &fakeHost{snapshot: snapshot(4)}, Docker: source, Thresholds: DefaultThresholds(), Now: now.Now})
	sampler.watcher.retry = time.Millisecond
	watch(t, sampler)
	<-source.opened

	died := loop(start.Add(19*time.Minute + 50*time.Second))
	first.feed(t, died[0])
	now.set(start.Add(20 * time.Minute))
	sampler.SampleOnce(context.Background())
	if status, _ := sampler.Status(); !status.Containers.RestartsComplete {
		t.Fatal("twenty minutes of watching are not complete")
	}

	// The gateway restarts: the stream breaks, and is opened again from
	// a little before it broke.
	first.breaks <- errors.New("unexpected EOF")
	select {
	case since := <-source.opened:
		if want := start.Add(20*time.Minute - replayMargin); !since.Equal(want) {
			t.Fatalf("opened again from %s, want %s", since, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the stream was not opened again")
	}
	// The replay repeats what the first stream saw: it counts once.
	second.feed(t, died...)
	now.set(start.Add(21 * time.Minute))
	sampler.SampleOnce(context.Background())
	status, _ := sampler.Status()
	if runner := runnerOf(status); runner.RecentDeaths != 1 {
		t.Fatalf("deaths = %d", runner.RecentDeaths)
	}
	// What happened while it was closed is only what Docker still held:
	// the window starts again.
	if status.Containers.RestartsComplete || !status.Containers.RestartsSince.Equal(start.Add(20*time.Minute)) {
		t.Fatalf("after reopening: since %s, complete %t", status.Containers.RestartsSince, status.Containers.RestartsComplete)
	}
	now.set(start.Add(35 * time.Minute))
	sampler.SampleOnce(context.Background())
	if status, _ := sampler.Status(); !status.Containers.RestartsComplete {
		t.Fatal("fifteen minutes after reopening the counts are not complete")
	}
}

func TestRestartLoopsUnwatchedWhileTheStreamStaysClosed(t *testing.T) {
	now := &clock{now: start}
	source := &fakeDocker{
		containers: []docker.Container{{ID: "ffffffffffff", Name: "runner", Project: "forgejo", State: "running"}},
		openFail:   errors.New("the docker gateway answered 404: 404 page not found"),
	}
	sampler := NewSampler(Options{Mode: "host", Host: &fakeHost{snapshot: snapshot(4)}, Docker: source, Thresholds: DefaultThresholds(), Now: now.Now})
	watch(t, sampler)

	// A stream that cannot be opened for a moment is no finding: the counts
	// are only not complete.
	now.set(start.Add(4 * time.Minute))
	sampler.SampleOnce(context.Background())
	status, _ := sampler.Status()
	if len(status.Findings) != 0 || !status.Containers.Available || status.Containers.RestartsComplete || !status.Containers.RestartsSince.IsZero() {
		t.Fatalf("after 4 minutes: %+v", status)
	}
	// Closed for five minutes, restart loops are not watched: a finding,
	// with the containers still listed.
	now.set(start.Add(5 * time.Minute))
	sampler.SampleOnce(context.Background())
	status, _ = sampler.Status()
	if len(status.Findings) != 1 || status.Findings[0].Kind != ContainersUnavailable || !strings.Contains(status.Findings[0].Error, "restart loops are not watched") ||
		!strings.Contains(status.Findings[0].Error, "404") || !status.Containers.Available || len(status.Containers.Items) != 1 {
		t.Fatalf("after 5 minutes: findings %+v", status.Findings)
	}
	// It keeps trying.
	if asked := source.asked(); len(asked) < 1 || !slices.ContainsFunc(asked, func(since time.Time) bool { return since.Equal(start.Add(-15 * time.Minute)) }) {
		t.Fatalf("asked = %v", asked)
	}
}
