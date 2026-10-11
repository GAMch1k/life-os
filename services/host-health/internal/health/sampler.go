package health

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/GAMch1k/life-os/services/host-health/internal/docker"
	"github.com/GAMch1k/life-os/services/host-health/internal/host"
)

// HostSource reads the host's facts: host.Reader, or a fixture.
type HostSource interface {
	Read(now time.Time) (host.Snapshot, error)
}

// DockerSource lists the containers: docker.Client, or a fixture.
type DockerSource interface {
	Containers(ctx context.Context) ([]docker.Container, error)
	Events(ctx context.Context, since, until time.Time) ([]docker.Event, error)
	Memory(ctx context.Context, id string) (docker.Memory, error)
}

// Options configure a Sampler.
type Options struct {
	Mode string
	Host HostSource
	// Docker is nil when no gateway is set up: container facts are then
	// not available, which is not a finding.
	Docker DockerSource
	// IgnoreProjects are compose projects left out of findings and the top
	// memory users (HOST_HEALTH_IGNORE_PROJECTS: the verification stack).
	IgnoreProjects []string
	Thresholds     Thresholds
	// Interval is the time between samples (a minute); MemoryEvery how
	// often the containers' memory is read while memory is not low.
	Interval    time.Duration
	MemoryEvery time.Duration
	Logger      *slog.Logger
	Now         func() time.Time
}

// Sampler samples the host every Interval and keeps the latest status and
// the last 24 hours of samples.
type Sampler struct {
	opts    Options
	ring    *Ring
	tracker *Tracker

	mu      sync.Mutex
	status  Status
	sampled bool

	// Used by the sampling goroutine only.
	eventsTo time.Time
	memoryAt time.Time
}

// NewSampler creates a Sampler.
func NewSampler(opts Options) *Sampler {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Interval <= 0 {
		opts.Interval = time.Minute
	}
	if opts.MemoryEvery <= 0 {
		opts.MemoryEvery = 5 * time.Minute
	}
	return &Sampler{opts: opts, ring: NewRing(HistorySize), tracker: NewTracker(opts.Thresholds.RestartLoopWindow)}
}

// Prefill adds history samples, such as a fixture's day.
func (s *Sampler) Prefill(samples []Sample) {
	for _, sample := range samples {
		s.ring.Add(sample)
	}
}

// Run samples now and every Interval until ctx ends.
func (s *Sampler) Run(ctx context.Context) {
	for {
		s.SampleOnce(ctx)
		timer := time.NewTimer(s.opts.Interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// SampleOnce takes one sample and updates the status.
func (s *Sampler) SampleOnce(ctx context.Context) {
	now := s.opts.Now().UTC()
	snapshot, err := s.opts.Host.Read(now)
	var diskErrors []string
	if err != nil {
		if snapshot.Memory.TotalBytes == 0 {
			s.opts.Logger.Error("reading the host failed", "error", err)
			return
		}
		// Only disks failed: the rest of the snapshot is good.
		for _, e := range unwrapAll(err) {
			diskErrors = append(diskErrors, e.Error())
		}
		s.opts.Logger.Warn("reading a disk failed", "error", err)
	}
	s.ring.Add(SampleOf(snapshot))

	containers := s.readContainers(ctx, now, snapshot.Memory.AvailablePercent() < s.opts.Thresholds.MemoryWarningPercent)
	// Two hours of samples tell how long memory has been low.
	findings := Evaluate(snapshot, s.ring.Last(120), containers, s.opts.Docker != nil, s.opts.Thresholds)

	s.mu.Lock()
	s.status = Status{Mode: s.opts.Mode, Snapshot: snapshot, Containers: containers, Thresholds: s.opts.Thresholds, Findings: findings, DiskErrors: diskErrors}
	s.sampled = true
	s.mu.Unlock()
}

// readContainers lists the containers and their recent events, and reads
// their memory every MemoryEvery, or every sample while memory is low.
func (s *Sampler) readContainers(ctx context.Context, now time.Time, memoryLow bool) Containers {
	if s.opts.Docker == nil {
		return Containers{Error: "no docker gateway is set up"}
	}
	ctx, cancel := context.WithTimeout(ctx, s.opts.Interval/2)
	defer cancel()
	list, err := s.opts.Docker.Containers(ctx)
	if err != nil {
		return Containers{Error: err.Error()}
	}
	since := s.eventsTo
	if since.IsZero() {
		since = now.Add(-s.opts.Thresholds.RestartLoopWindow)
	}
	events, err := s.opts.Docker.Events(ctx, since, now)
	if err != nil {
		return Containers{Error: fmt.Sprintf("events: %v", err)}
	}
	s.tracker.Events(events)
	s.eventsTo = now

	if memoryLow || now.Sub(s.memoryAt) >= s.opts.MemoryEvery {
		s.tracker.SetMemory(s.readMemory(ctx, list))
		s.memoryAt = now
	}
	items := s.tracker.Update(now, list)
	for i := range items {
		items[i].TestStack = s.ignored(items[i].Project)
	}
	return Containers{Available: true, Items: items, MemoryAt: s.memoryAt}
}

// ignored says whether a compose project is one of IgnoreProjects.
func (s *Sampler) ignored(project string) bool {
	return project != "" && slices.Contains(s.opts.IgnoreProjects, project)
}

// readMemory reads the running containers' memory, four at a time. One that
// cannot be read is left out.
func (s *Sampler) readMemory(ctx context.Context, list []docker.Container) map[string]uint64 {
	readings := map[string]uint64{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	limit := make(chan struct{}, 4)
	for _, container := range list {
		// The test stack is never among the biggest users: not read.
		if container.State != "running" || s.ignored(container.Project) {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			limit <- struct{}{}
			defer func() { <-limit }()
			memory, err := s.opts.Docker.Memory(ctx, container.ID)
			if err != nil {
				return
			}
			mu.Lock()
			readings[container.ID] = memory.UsageBytes
			mu.Unlock()
		}()
	}
	wg.Wait()
	return readings
}

// Status is the latest status; false before the first good sample.
func (s *Sampler) Status() (Status, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status, s.sampled
}

// History is the samples since from, oldest first.
func (s *Sampler) History(from time.Time) []Sample {
	return s.ring.Since(from)
}

// Ready fails before the first good sample, and when the last one is more
// than three intervals old.
func (s *Sampler) Ready(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.sampled {
		return errors.New("the host has not been read yet")
	}
	if age := s.opts.Now().Sub(s.status.Snapshot.At); age > 3*s.opts.Interval {
		return fmt.Errorf("the last sample is %s old", age.Round(time.Second))
	}
	return nil
}

func unwrapAll(err error) []error {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		return joined.Unwrap()
	}
	return []error{err}
}
