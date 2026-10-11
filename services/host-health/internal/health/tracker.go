package health

import (
	"strings"
	"time"

	"github.com/GAMch1k/life-os/services/host-health/internal/docker"
)

// Tracker follows the containers between samples: their deaths within the
// restart-loop window (from Docker's die events, so no inspect is needed
// for restart counts), since when they are unhealthy, and their memory.
type Tracker struct {
	window time.Duration
	// deaths by container name, within the window.
	deaths map[string][]time.Time
	seen   map[string]time.Time
	// health is each container's latest health_status event.
	health         map[string]healthEvent
	unhealthySince map[string]time.Time
	memory         map[string]uint64
}

type healthEvent struct {
	at     time.Time
	status string
}

// NewTracker counts deaths within window.
func NewTracker(window time.Duration) *Tracker {
	return &Tracker{
		window:         window,
		deaths:         map[string][]time.Time{},
		seen:           map[string]time.Time{},
		health:         map[string]healthEvent{},
		unhealthySince: map[string]time.Time{},
		memory:         map[string]uint64{},
	}
}

// Events takes container events; one already taken is ignored.
func (t *Tracker) Events(events []docker.Event) {
	for _, event := range events {
		key := event.ID + "|" + event.Action + "|" + event.Time.Format(time.RFC3339Nano)
		if _, ok := t.seen[key]; ok {
			continue
		}
		t.seen[key] = event.Time
		name := event.Name
		switch {
		case event.Action == "die":
			t.deaths[name] = append(t.deaths[name], event.Time)
		case strings.HasPrefix(event.Action, "health_status"):
			status := strings.TrimSpace(strings.TrimPrefix(event.Action, "health_status:"))
			if previous, ok := t.health[name]; !ok || event.Time.After(previous.at) {
				t.health[name] = healthEvent{at: event.Time, status: status}
			}
		}
	}
}

// SetMemory records memory readings by container id.
func (t *Tracker) SetMemory(readings map[string]uint64) {
	t.memory = readings
}

// Update applies the current container list at now and returns each
// container with what is tracked of it.
func (t *Tracker) Update(now time.Time, containers []docker.Container) []ContainerState {
	cutoff := now.Add(-t.window)
	for name, times := range t.deaths {
		kept := times[:0]
		for _, at := range times {
			if at.After(cutoff) {
				kept = append(kept, at)
			}
		}
		if len(kept) == 0 {
			delete(t.deaths, name)
		} else {
			t.deaths[name] = kept
		}
	}
	for key, at := range t.seen {
		if at.Before(cutoff) {
			delete(t.seen, key)
		}
	}

	states := make([]ContainerState, 0, len(containers))
	present := map[string]bool{}
	for _, container := range containers {
		present[container.Name] = true
		state := ContainerState{Container: container, RecentDeaths: len(t.deaths[container.Name]), MemoryBytes: t.memory[container.ID]}
		if container.Health == "unhealthy" {
			since, ok := t.unhealthySince[container.Name]
			if !ok {
				since = now
			}
			// A health_status event says when it turned unhealthy, also
			// for a container that was so before host-health started.
			if event, ok := t.health[container.Name]; ok && event.status == "unhealthy" && event.at.Before(since) {
				since = event.at
			}
			t.unhealthySince[container.Name] = since
			state.UnhealthySince = since
		} else {
			delete(t.unhealthySince, container.Name)
		}
		states = append(states, state)
	}
	for name := range t.unhealthySince {
		if !present[name] {
			delete(t.unhealthySince, name)
		}
	}
	return states
}
