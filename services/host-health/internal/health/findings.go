package health

import (
	"slices"

	"github.com/GAMch1k/life-os/services/host-health/internal/host"
)

// Evaluate lists what needs attention, problems first: memory, disks,
// load, then containers. recent are the latest samples, the current one
// last, for how long memory has been low.
func Evaluate(snapshot host.Snapshot, recent []Sample, containers Containers, dockerSetUp bool, t Thresholds) []Finding {
	var findings []Finding

	memory := snapshot.Memory
	percent := memory.AvailablePercent()
	switch {
	case memory.TotalBytes > 0 && percent < t.MemoryProblemPercent:
		findings = append(findings, Finding{Kind: MemoryLow, Severity: Problem, AvailableBytes: memory.AvailableBytes, TotalBytes: memory.TotalBytes, Minutes: lowFor(recent, t.MemoryWarningPercent)})
	case memory.TotalBytes > 0 && percent < t.MemoryWarningPercent:
		if held := lowFor(recent, t.MemoryWarningPercent); held >= t.MemoryWarningSamples {
			findings = append(findings, Finding{Kind: MemoryLow, Severity: Warning, AvailableBytes: memory.AvailableBytes, TotalBytes: memory.TotalBytes, Minutes: held})
		}
	}

	for _, disk := range snapshot.Disks {
		switch {
		case disk.FreeBytes < t.DiskProblemBytes:
			findings = append(findings, Finding{Kind: DiskLow, Severity: Problem, Subject: disk.Label, AvailableBytes: disk.FreeBytes, TotalBytes: disk.TotalBytes})
		case disk.FreeBytes < t.DiskWarningBytes || disk.FreePercent() < t.DiskWarningPercent:
			findings = append(findings, Finding{Kind: DiskLow, Severity: Warning, Subject: disk.Label, AvailableBytes: disk.FreeBytes, TotalBytes: disk.TotalBytes})
		}
	}

	if snapshot.CPUs > 0 && snapshot.Load.Fifteen > float64(snapshot.CPUs)*t.LoadWarningPerCPU {
		findings = append(findings, Finding{Kind: LoadHigh, Severity: Warning, Load: snapshot.Load.Fifteen, CPUs: snapshot.CPUs})
	}

	switch {
	case dockerSetUp && !containers.Available:
		findings = append(findings, Finding{Kind: ContainersUnavailable, Severity: Warning, Error: containers.Error})
	case dockerSetUp && containers.EventsError != "":
		// The list is read, but nothing counts restarts.
		findings = append(findings, Finding{Kind: ContainersUnavailable, Severity: Warning, Error: "restart loops are not watched, their events cannot be followed: " + containers.EventsError})
	}
	for _, item := range containers.Items {
		if item.TestStack {
			// The verification stack comes and goes with every test run.
			continue
		}
		if item.RecentDeaths >= t.RestartLoopCount {
			findings = append(findings, Finding{Kind: ContainerRestarting, Severity: Warning, Subject: item.Name, Project: item.Project, Count: item.RecentDeaths, ExitCode: item.LastExitCode, Minutes: int(t.RestartLoopWindow.Minutes())})
		}
		if !item.UnhealthySince.IsZero() && snapshot.At.Sub(item.UnhealthySince) >= t.UnhealthyFor {
			findings = append(findings, Finding{Kind: ContainerUnhealthy, Severity: Warning, Subject: item.Name, Project: item.Project, Minutes: int(snapshot.At.Sub(item.UnhealthySince).Minutes())})
		}
	}

	slices.SortStableFunc(findings, func(a, b Finding) int {
		if a.Severity == b.Severity {
			return 0
		}
		if a.Severity == Problem {
			return -1
		}
		return 1
	})
	return findings
}

// lowFor is how many of the latest samples in a row, the current one
// included, had less than percent of memory available.
func lowFor(recent []Sample, percent float64) int {
	held := 0
	for i := len(recent) - 1; i >= 0; i-- {
		sample := recent[i]
		if sample.MemoryTotal == 0 || float64(sample.MemoryAvailable)/float64(sample.MemoryTotal)*100 >= percent {
			break
		}
		held++
	}
	return held
}
