// Package config defines configuration owned by the host-health service.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	sharedconfig "github.com/GAMch1k/life-os/pkg/config"
	"github.com/GAMch1k/life-os/services/host-health/internal/health"
	"github.com/GAMch1k/life-os/services/host-health/internal/host"
)

// Config holds process-level configuration for host-health.
type Config struct {
	ServiceName string `env:"SERVICE_NAME" default:"host-health"`
	Port        int    `env:"PORT" default:"8080"`
	LogLevel    string `env:"LOG_LEVEL" default:"info"`
	// ProcDir holds meminfo, loadavg, uptime, and stat: the container's own
	// /proc shows the host's.
	ProcDir string `env:"HOST_HEALTH_PROC_DIR" default:"/proc"`
	// Disks are label=path pairs of read-only bind mounts of host paths.
	Disks string `env:"HOST_HEALTH_DISKS" default:"root=/host,docker=/host-docker"`
	// DockerURL is the Docker gateway; empty means no container facts.
	DockerURL string `env:"HOST_HEALTH_DOCKER_URL"`
	// CgroupDir is a read-only bind of the directory holding the
	// containers' cgroups (/sys/fs/cgroup/system.slice); empty means Life
	// OS's use of the server is not measured.
	CgroupDir string `env:"HOST_HEALTH_CGROUP_DIR"`
	// LifeOSProject is the compose project whose containers are Life OS.
	LifeOSProject string `env:"HOST_HEALTH_LIFEOS_PROJECT" default:"lifeos"`
	// Fixture, healthy or low-memory, replaces the host and its containers
	// with a made-up one (the verification stack).
	Fixture         string `env:"HOST_HEALTH_FIXTURE"`
	IntervalSeconds int    `env:"HOST_HEALTH_INTERVAL_SECONDS" default:"60"`
	// The thresholds (docs/services/host-health.md).
	MemoryWarningPercent float64 `env:"HOST_HEALTH_MEMORY_WARNING_PERCENT" default:"15"`
	MemoryWarningSamples int     `env:"HOST_HEALTH_MEMORY_WARNING_SAMPLES" default:"3"`
	MemoryProblemPercent float64 `env:"HOST_HEALTH_MEMORY_PROBLEM_PERCENT" default:"7"`
	DiskWarningGiB       float64 `env:"HOST_HEALTH_DISK_WARNING_GIB" default:"10"`
	DiskWarningPercent   float64 `env:"HOST_HEALTH_DISK_WARNING_PERCENT" default:"10"`
	DiskProblemGiB       float64 `env:"HOST_HEALTH_DISK_PROBLEM_GIB" default:"3"`
	LoadWarningPerCPU    float64 `env:"HOST_HEALTH_LOAD_WARNING_PER_CPU" default:"1"`
	RestartLoopCount     int     `env:"HOST_HEALTH_RESTART_LOOP_COUNT" default:"3"`
	RestartLoopMinutes   int     `env:"HOST_HEALTH_RESTART_LOOP_MINUTES" default:"15"`
	UnhealthyMinutes     int     `env:"HOST_HEALTH_UNHEALTHY_MINUTES" default:"5"`
	// IgnoreProjects are comma-separated compose projects left out of
	// findings and the top memory users: the verification stack, which test
	// runs bring up and down every few minutes.
	IgnoreProjects string `env:"HOST_HEALTH_IGNORE_PROJECTS" default:"lifeos-verify"`
}

// composeProject is a compose project name: lowercase letters, digits,
// dashes, and underscores, starting with a letter or digit.
var composeProject = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// IgnoredProjects is IgnoreProjects as a list, each once.
func (c Config) IgnoredProjects() []string {
	var projects []string
	for _, project := range strings.Split(c.IgnoreProjects, ",") {
		if project = strings.TrimSpace(project); project != "" && !slices.Contains(projects, project) {
			projects = append(projects, project)
		}
	}
	return projects
}

// Load reads Config from the process environment.
func Load() (Config, error) {
	return LoadFrom(nil)
}

// LoadFrom reads Config through lookup. Passing nil reads the process
// environment and keeps tests independent from global process state.
func LoadFrom(lookup sharedconfig.LookupFunc) (Config, error) {
	var cfg Config
	var err error
	if lookup == nil {
		err = sharedconfig.Load(&cfg)
	} else {
		err = sharedconfig.LoadFrom(&cfg, lookup)
	}
	if err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate implements the shared configuration validation hook.
func (c Config) Validate() error {
	var errs []error
	if c.ServiceName == "" {
		errs = append(errs, fmt.Errorf("SERVICE_NAME must not be empty"))
	}
	if c.Port < 1 || c.Port > 65535 {
		errs = append(errs, fmt.Errorf("PORT must be between 1 and 65535"))
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(c.LogLevel)); err != nil {
		errs = append(errs, fmt.Errorf("LOG_LEVEL must be a slog level: %w", err))
	}
	if !filepath.IsAbs(c.ProcDir) {
		errs = append(errs, fmt.Errorf("HOST_HEALTH_PROC_DIR must be an absolute path"))
	}
	if _, err := host.ParseDiskSpecs(c.Disks); err != nil {
		errs = append(errs, fmt.Errorf("HOST_HEALTH_DISKS: %w", err))
	}
	if c.DockerURL != "" {
		if parsed, err := url.Parse(c.DockerURL); err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			errs = append(errs, fmt.Errorf("HOST_HEALTH_DOCKER_URL must be empty or an absolute HTTP URL"))
		}
	}
	if c.CgroupDir != "" && !filepath.IsAbs(c.CgroupDir) {
		errs = append(errs, fmt.Errorf("HOST_HEALTH_CGROUP_DIR must be empty or an absolute path"))
	}
	if !composeProject.MatchString(c.LifeOSProject) || slices.Contains(c.IgnoredProjects(), c.LifeOSProject) {
		errs = append(errs, fmt.Errorf("HOST_HEALTH_LIFEOS_PROJECT must be a compose project name, not one of HOST_HEALTH_IGNORE_PROJECTS"))
	}
	if c.Fixture != "" && !health.ValidFixture(c.Fixture) {
		errs = append(errs, fmt.Errorf("HOST_HEALTH_FIXTURE must be empty, %s, or %s", health.FixtureHealthy, health.FixtureLowMemory))
	}
	if c.IntervalSeconds < 10 || c.IntervalSeconds > 600 {
		errs = append(errs, fmt.Errorf("HOST_HEALTH_INTERVAL_SECONDS must be between 10 and 600"))
	}
	if c.MemoryProblemPercent <= 0 || c.MemoryWarningPercent <= c.MemoryProblemPercent || c.MemoryWarningPercent >= 100 {
		errs = append(errs, fmt.Errorf("HOST_HEALTH_MEMORY_PROBLEM_PERCENT and HOST_HEALTH_MEMORY_WARNING_PERCENT must satisfy 0 < problem < warning < 100"))
	}
	if c.MemoryWarningSamples < 1 || c.MemoryWarningSamples > 60 {
		errs = append(errs, fmt.Errorf("HOST_HEALTH_MEMORY_WARNING_SAMPLES must be between 1 and 60"))
	}
	if c.DiskProblemGiB <= 0 || c.DiskWarningGiB < c.DiskProblemGiB {
		errs = append(errs, fmt.Errorf("HOST_HEALTH_DISK_PROBLEM_GIB and HOST_HEALTH_DISK_WARNING_GIB must satisfy 0 < problem <= warning"))
	}
	if c.DiskWarningPercent < 0 || c.DiskWarningPercent >= 100 {
		errs = append(errs, fmt.Errorf("HOST_HEALTH_DISK_WARNING_PERCENT must be between 0 and 100"))
	}
	if c.LoadWarningPerCPU <= 0 {
		errs = append(errs, fmt.Errorf("HOST_HEALTH_LOAD_WARNING_PER_CPU must be above 0"))
	}
	if c.RestartLoopCount < 2 || c.RestartLoopMinutes < 1 || c.RestartLoopMinutes > 1440 {
		errs = append(errs, fmt.Errorf("HOST_HEALTH_RESTART_LOOP_COUNT must be at least 2, HOST_HEALTH_RESTART_LOOP_MINUTES between 1 and 1440"))
	}
	if c.UnhealthyMinutes < 1 || c.UnhealthyMinutes > 1440 {
		errs = append(errs, fmt.Errorf("HOST_HEALTH_UNHEALTHY_MINUTES must be between 1 and 1440"))
	}
	for _, project := range c.IgnoredProjects() {
		if !composeProject.MatchString(project) {
			errs = append(errs, fmt.Errorf("HOST_HEALTH_IGNORE_PROJECTS must list compose project names, comma-separated: %q is none", project))
		}
	}
	return errors.Join(errs...)
}

// ParsedLogLevel returns the already validated slog level.
func (c Config) ParsedLogLevel() slog.Level {
	var level slog.Level
	_ = level.UnmarshalText([]byte(c.LogLevel))
	return level
}

// Interval is the validated time between samples.
func (c Config) Interval() time.Duration {
	return time.Duration(c.IntervalSeconds) * time.Second
}

// DiskSpecs are the validated disks.
func (c Config) DiskSpecs() []host.DiskSpec {
	specs, _ := host.ParseDiskSpecs(c.Disks)
	return specs
}

// Thresholds are the validated thresholds.
func (c Config) Thresholds() health.Thresholds {
	return health.Thresholds{
		MemoryWarningPercent: c.MemoryWarningPercent,
		MemoryWarningSamples: c.MemoryWarningSamples,
		MemoryProblemPercent: c.MemoryProblemPercent,
		DiskWarningBytes:     uint64(c.DiskWarningGiB * (1 << 30)),
		DiskWarningPercent:   c.DiskWarningPercent,
		DiskProblemBytes:     uint64(c.DiskProblemGiB * (1 << 30)),
		LoadWarningPerCPU:    c.LoadWarningPerCPU,
		RestartLoopCount:     c.RestartLoopCount,
		RestartLoopWindow:    time.Duration(c.RestartLoopMinutes) * time.Minute,
		UnhealthyFor:         time.Duration(c.UnhealthyMinutes) * time.Minute,
	}
}

// GatewayConfig configures `service docker-gateway`.
type GatewayConfig struct {
	Port     int    `env:"PORT" default:"8080"`
	LogLevel string `env:"LOG_LEVEL" default:"info"`
	// Socket is the Docker Engine's unix socket.
	Socket string `env:"DOCKER_GATEWAY_SOCKET" default:"/var/run/docker.sock"`
}

// LoadGateway reads GatewayConfig from the process environment.
func LoadGateway() (GatewayConfig, error) {
	var cfg GatewayConfig
	return cfg, sharedconfig.Load(&cfg)
}

// Validate implements the shared configuration validation hook.
func (c GatewayConfig) Validate() error {
	var errs []error
	if c.Port < 1 || c.Port > 65535 {
		errs = append(errs, fmt.Errorf("PORT must be between 1 and 65535"))
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(c.LogLevel)); err != nil {
		errs = append(errs, fmt.Errorf("LOG_LEVEL must be a slog level: %w", err))
	}
	if !filepath.IsAbs(c.Socket) {
		errs = append(errs, fmt.Errorf("DOCKER_GATEWAY_SOCKET must be an absolute path"))
	}
	return errors.Join(errs...)
}

// ParsedLogLevel returns the already validated slog level.
func (c GatewayConfig) ParsedLogLevel() slog.Level {
	var level slog.Level
	_ = level.UnmarshalText([]byte(c.LogLevel))
	return level
}
