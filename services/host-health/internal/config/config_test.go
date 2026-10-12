package config

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func mapLookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func TestLoadFromAppliesDefaults(t *testing.T) {
	cfg, err := LoadFrom(mapLookup(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ServiceName != "host-health" || cfg.ProcDir != "/proc" || cfg.DockerURL != "" || cfg.CgroupDir != "" || cfg.LifeOSProject != "lifeos" || cfg.Fixture != "" || cfg.Interval() != time.Minute {
		t.Fatalf("config = %+v", cfg)
	}
	if specs := cfg.DiskSpecs(); len(specs) != 2 || specs[0].Path != "/host" || specs[1].Label != "docker" {
		t.Fatalf("disks = %+v", specs)
	}
	thresholds := cfg.Thresholds()
	if thresholds.MemoryWarningPercent != 15 || thresholds.MemoryWarningSamples != 3 || thresholds.MemoryProblemPercent != 7 ||
		thresholds.DiskWarningBytes != 10<<30 || thresholds.DiskWarningPercent != 10 || thresholds.DiskProblemBytes != 3<<30 ||
		thresholds.LoadWarningPerCPU != 1 || thresholds.RestartLoopCount != 3 || thresholds.RestartLoopWindow != 15*time.Minute || thresholds.UnhealthyFor != 5*time.Minute {
		t.Fatalf("thresholds = %+v", thresholds)
	}
}

func TestLoadFromReadsTheSettings(t *testing.T) {
	cfg, err := LoadFrom(mapLookup(map[string]string{
		"HOST_HEALTH_DOCKER_URL":             "http://host-health-docker:8080",
		"HOST_HEALTH_CGROUP_DIR":             "/host-cgroup",
		"HOST_HEALTH_LIFEOS_PROJECT":         "lifeos-next",
		"HOST_HEALTH_FIXTURE":                "low-memory",
		"HOST_HEALTH_DISKS":                  "root=/host",
		"HOST_HEALTH_MEMORY_WARNING_PERCENT": "20",
		"HOST_HEALTH_DISK_PROBLEM_GIB":       "1.5",
		"HOST_HEALTH_LOAD_WARNING_PER_CPU":   "1.5",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DockerURL != "http://host-health-docker:8080" || cfg.CgroupDir != "/host-cgroup" || cfg.LifeOSProject != "lifeos-next" || cfg.Fixture != "low-memory" || len(cfg.DiskSpecs()) != 1 {
		t.Fatalf("config = %+v", cfg)
	}
	if thresholds := cfg.Thresholds(); thresholds.MemoryWarningPercent != 20 || thresholds.DiskProblemBytes != 3<<29 || thresholds.LoadWarningPerCPU != 1.5 {
		t.Fatalf("thresholds = %+v", thresholds)
	}
}

func TestLoadFromRejectsInvalidSettings(t *testing.T) {
	_, err := LoadFrom(mapLookup(map[string]string{
		"HOST_HEALTH_PROC_DIR":               "proc",
		"HOST_HEALTH_DISKS":                  "root",
		"HOST_HEALTH_DOCKER_URL":             "unix:///var/run/docker.sock",
		"HOST_HEALTH_CGROUP_DIR":             "sys/fs/cgroup",
		"HOST_HEALTH_LIFEOS_PROJECT":         "Life OS",
		"HOST_HEALTH_FIXTURE":                "broken",
		"HOST_HEALTH_INTERVAL_SECONDS":       "1",
		"HOST_HEALTH_MEMORY_WARNING_PERCENT": "5",
		"HOST_HEALTH_MEMORY_WARNING_SAMPLES": "0",
		"HOST_HEALTH_DISK_WARNING_GIB":       "1",
		"HOST_HEALTH_DISK_WARNING_PERCENT":   "120",
		"HOST_HEALTH_LOAD_WARNING_PER_CPU":   "0",
		"HOST_HEALTH_RESTART_LOOP_COUNT":     "1",
		"HOST_HEALTH_UNHEALTHY_MINUTES":      "0",
	}))
	if err == nil {
		t.Fatal("LoadFrom accepted invalid settings")
	}
	for _, want := range []string{"HOST_HEALTH_PROC_DIR", "HOST_HEALTH_DISKS", "HOST_HEALTH_DOCKER_URL", "HOST_HEALTH_CGROUP_DIR", "HOST_HEALTH_LIFEOS_PROJECT", "HOST_HEALTH_FIXTURE", "HOST_HEALTH_INTERVAL_SECONDS", "HOST_HEALTH_MEMORY_WARNING_PERCENT", "HOST_HEALTH_MEMORY_WARNING_SAMPLES", "HOST_HEALTH_DISK_WARNING_GIB", "HOST_HEALTH_DISK_WARNING_PERCENT", "HOST_HEALTH_LOAD_WARNING_PER_CPU", "HOST_HEALTH_RESTART_LOOP_COUNT", "HOST_HEALTH_UNHEALTHY_MINUTES"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %s", want)
		}
	}
}

func TestIgnoredProjectsDefaultToTheVerificationStack(t *testing.T) {
	cfg, err := LoadFrom(mapLookup(map[string]string{}))
	if err != nil || !slices.Equal(cfg.IgnoredProjects(), []string{"lifeos-verify"}) {
		t.Fatalf("default = %v, %v", cfg.IgnoredProjects(), err)
	}
	cfg, err = LoadFrom(mapLookup(map[string]string{"HOST_HEALTH_IGNORE_PROJECTS": " lifeos-verify, scratch_stack ,lifeos-verify,"}))
	if err != nil || !slices.Equal(cfg.IgnoredProjects(), []string{"lifeos-verify", "scratch_stack"}) {
		t.Fatalf("list = %v, %v", cfg.IgnoredProjects(), err)
	}
	if cfg, err = LoadFrom(mapLookup(map[string]string{"HOST_HEALTH_IGNORE_PROJECTS": ""})); err != nil || len(cfg.IgnoredProjects()) != 0 {
		t.Fatalf("none = %v, %v", cfg.IgnoredProjects(), err)
	}
	if _, err := LoadFrom(mapLookup(map[string]string{"HOST_HEALTH_IGNORE_PROJECTS": "lifeos-verify,Test Stack"})); err == nil || !strings.Contains(err.Error(), "HOST_HEALTH_IGNORE_PROJECTS") {
		t.Fatalf("invalid = %v", err)
	}
	// Life OS is never the test stack.
	if _, err := LoadFrom(mapLookup(map[string]string{"HOST_HEALTH_IGNORE_PROJECTS": "lifeos-verify,lifeos"})); err == nil || !strings.Contains(err.Error(), "HOST_HEALTH_LIFEOS_PROJECT") {
		t.Fatalf("Life OS ignored = %v", err)
	}
}
