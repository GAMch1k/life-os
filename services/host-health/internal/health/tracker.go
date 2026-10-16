package health

import (
	"slices"
	"sync"
	"time"

	"github.com/GAMch1k/life-os/services/host-health/internal/docker"
)

// stopGrace is how long after a kill (docker stop, restart, compose down or
// a recreate) the container's death counts as that stop: Docker waits the
// stop timeout (10 s by default, longer when a service sets it) before it
// kills for good.
const stopGrace = 5 * time.Minute

// Tracker follows the containers between samples, by container id (a
// recreated container is a new one): their restarts within the restart-loop
// window, since when they are unhealthy, and their memory.
//
// A restart is a death after which Docker started the same container again
// by itself, as a restart policy does: a die of a container that no kill
// preceded, followed by a start of the same id with no stop or destroy in
// between. An intentional stop never counts: docker stop and compose stop
// or down kill first (and stop after), a recreate kills and destroys the
// old container and starts one with a new id, and docker restart kills,
// stops, and starts. So the verification stack's up and down and a
// redeploy are not restart loops, while a crashing container under a
// restart policy (die, start, die, start, …) is. No inspect is needed.
//
// The Watcher feeds it events as they happen while the Sampler reads it:
// it is safe for concurrent use.
type Tracker struct {
	mu     sync.Mutex
	window time.Duration
	// deaths are each container's restarts within the window: when it died.
	deaths map[string][]time.Time
	// pending is a death not yet followed by a start: it counts if the same
	// container starts again.
	pending map[string]death
	// killed is when each container was last killed, for stopGrace.
	killed map[string]time.Time
	// exitCodes are each container's last counted death's exit code.
	exitCodes map[string]int
	seen      map[string]time.Time
	// unhealthySince is when each container was first seen unhealthy.
	unhealthySince map[string]time.Time
	memory         map[string]uint64
}

type death struct {
	at       time.Time
	exitCode *int
}

// NewTracker counts restarts within window.
func NewTracker(window time.Duration) *Tracker {
	return &Tracker{
		window:         window,
		deaths:         map[string][]time.Time{},
		pending:        map[string]death{},
		killed:         map[string]time.Time{},
		exitCodes:      map[string]int{},
		seen:           map[string]time.Time{},
		unhealthySince: map[string]time.Time{},
		memory:         map[string]uint64{},
	}
}

// Events takes container events, in the order they happened; one already
// taken is ignored.
func (t *Tracker) Events(events []docker.Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	ordered := slices.Clone(events)
	slices.SortStableFunc(ordered, func(a, b docker.Event) int { return a.Time.Compare(b.Time) })
	for _, event := range ordered {
		key := event.ID + "|" + event.Action + "|" + event.Time.Format(time.RFC3339Nano)
		if _, ok := t.seen[key]; ok {
			continue
		}
		t.seen[key] = event.Time
		id := event.ID
		switch {
		case event.Action == "kill":
			t.killed[id] = event.Time
		case event.Action == "die":
			if at, ok := t.killed[id]; ok && event.Time.Sub(at) <= stopGrace {
				// Stopped on purpose: docker stop, compose down, a recreate.
				delete(t.killed, id)
				continue
			}
			t.pending[id] = death{at: event.Time, exitCode: event.ExitCode}
		case event.Action == "stop" || event.Action == "destroy":
			// Stopped or removed: it will not start again by itself.
			delete(t.pending, id)
			delete(t.killed, id)
		case event.Action == "start":
			if died, ok := t.pending[id]; ok {
				t.deaths[id] = append(t.deaths[id], died.at)
				if died.exitCode != nil {
					t.exitCodes[id] = *died.exitCode
				}
				delete(t.pending, id)
			}
			delete(t.killed, id)
		}
	}
}

// SetMemory records memory readings by container id.
func (t *Tracker) SetMemory(readings map[string]uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.memory = readings
}

// Update applies the current container list at now and returns each
// container with what is tracked of it.
func (t *Tracker) Update(now time.Time, containers []docker.Container) []ContainerState {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.prune(now)
	states := make([]ContainerState, 0, len(containers))
	present := map[string]bool{}
	for _, container := range containers {
		id := container.ID
		present[id] = true
		state := ContainerState{Container: container, RecentDeaths: len(t.deaths[id]), MemoryBytes: t.memory[id]}
		if code, ok := t.exitCodes[id]; ok && state.RecentDeaths > 0 {
			state.LastExitCode = &code
		}
		if container.Health == "unhealthy" {
			since, ok := t.unhealthySince[id]
			if !ok {
				since = now
			}
			t.unhealthySince[id] = since
			state.UnhealthySince = since
		} else {
			delete(t.unhealthySince, id)
		}
		states = append(states, state)
	}
	// A container that is gone (removed, or recreated with a new id) is
	// forgotten.
	for id := range t.unhealthySince {
		if !present[id] {
			delete(t.unhealthySince, id)
		}
	}
	return states
}

// prune forgets deaths and events older than the window, and kills older
// than stopGrace.
func (t *Tracker) prune(now time.Time) {
	cutoff := now.Add(-t.window)
	for id, times := range t.deaths {
		kept := times[:0]
		for _, at := range times {
			if at.After(cutoff) {
				kept = append(kept, at)
			}
		}
		if len(kept) == 0 {
			delete(t.deaths, id)
			delete(t.exitCodes, id)
		} else {
			t.deaths[id] = kept
		}
	}
	for id, died := range t.pending {
		if !died.at.After(cutoff) {
			delete(t.pending, id)
		}
	}
	for id, at := range t.killed {
		if now.Sub(at) > stopGrace {
			delete(t.killed, id)
		}
	}
	for key, at := range t.seen {
		if at.Before(cutoff) {
			delete(t.seen, key)
		}
	}
}
