// Package docker gives host-health the containers on the host without
// giving it the Docker socket.
//
// The gateway (Gateway, `service docker-gateway`) is the only process that
// opens the socket. It is not a proxy: it answers three fixed GET requests,
// makes fixed requests of its own to the Docker Engine API with validated
// parameters, and re-encodes only the fields host-health needs:
//
//	GET /containers                -> GET /containers/json?all=1: name, compose project and service, image, state, health
//	GET /events?since=             -> GET /events?since= (container die, start, kill, stop, and destroy only), kept open: time, id, name, project, action, a die's exit code, one per line as they happen
//	GET /containers/{id}/memory    -> GET /containers/{id}/stats?stream=false&one-shot=true: memory in use and limit
//
// Nothing else of the API is reachable through it: not inspect (which holds
// every container's environment), logs, file archives, exec, or any write.
// Client is host-health's side.
package docker

import (
	"strings"
	"time"
)

// Container is what the gateway tells of one container.
type Container struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Project string `json:"project,omitempty"`
	Service string `json:"service,omitempty"`
	Image   string `json:"image"`
	// State is Docker's: created, running, paused, restarting, removing,
	// exited, or dead.
	State string `json:"state"`
	// Health is healthy, unhealthy, starting, or empty without a health
	// check.
	Health string `json:"health,omitempty"`
}

// Event is one container event: die, start, kill, stop, or destroy.
type Event struct {
	Time time.Time `json:"time"`
	// ID is the container's short id: a recreated container has a new one.
	ID      string `json:"id"`
	Name    string `json:"name"`
	Project string `json:"project,omitempty"`
	// Action is die, start, kill, stop, or destroy.
	Action string `json:"action"`
	// ExitCode is a die's exit code.
	ExitCode *int `json:"exitCode,omitempty"`
}

// EventStream is an open stream of container events, oldest first.
type EventStream interface {
	// Since is when the stream began to carry every event. The events
	// before it are only those the engine still held when it opened.
	Since() time.Time
	// Next waits for the next event; an error means the stream is over.
	Next() (Event, error)
	Close() error
}

// Memory is a container's memory use.
type Memory struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// UsageBytes is what docker stats shows: usage less inactive page
	// cache.
	UsageBytes uint64 `json:"usageBytes"`
	LimitBytes uint64 `json:"limitBytes"`
}

// healthFromStatus reads the health Docker puts in a container's status,
// such as "Up 3 hours (healthy)" or "Up 5 seconds (health: starting)".
func healthFromStatus(status string) string {
	switch {
	case strings.Contains(status, "(healthy)"):
		return "healthy"
	case strings.Contains(status, "(unhealthy)"):
		return "unhealthy"
	case strings.Contains(status, "(health: starting)"):
		return "starting"
	}
	return ""
}
