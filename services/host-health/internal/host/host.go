// Package host reads the server's own facts: memory, load, CPUs, uptime,
// disk space, and the containers' cgroups. It only reads: /proc files,
// statfs, and cgroup files.
//
// /proc/meminfo, /proc/loadavg, /proc/uptime, and /proc/stat are not
// namespaced by Docker, so a container reads the host's values from its own
// /proc (ProcDir, /proc by default). Disks and cgroups are read through
// read-only bind mounts of host paths.
package host

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// Memory is the host's memory, from /proc/meminfo.
type Memory struct {
	TotalBytes uint64
	// AvailableBytes is MemAvailable: what can be used without swapping,
	// page cache included.
	AvailableBytes uint64
}

// AvailablePercent is the share of memory available.
func (m Memory) AvailablePercent() float64 {
	if m.TotalBytes == 0 {
		return 0
	}
	return float64(m.AvailableBytes) / float64(m.TotalBytes) * 100
}

// Load is the load average over 1, 5, and 15 minutes, from /proc/loadavg.
type Load struct {
	One, Five, Fifteen float64
}

// Disk is the space of one filesystem.
type Disk struct {
	Label string
	Path  string
	// TotalBytes is the filesystem's size; FreeBytes what an unprivileged
	// user may still write (statfs f_bavail, df's "Avail").
	TotalBytes uint64
	FreeBytes  uint64
	// Device identifies the filesystem, so two paths on one are reported
	// once.
	Device uint64
}

// FreePercent is the share of the filesystem still free.
func (d Disk) FreePercent() float64 {
	if d.TotalBytes == 0 {
		return 0
	}
	return float64(d.FreeBytes) / float64(d.TotalBytes) * 100
}

// DiskSpec names a path whose filesystem is reported.
type DiskSpec struct {
	Label string
	Path  string
}

// ParseDiskSpecs reads label=path pairs separated by commas, such as
// root=/host,docker=/host-docker.
func ParseDiskSpecs(value string) ([]DiskSpec, error) {
	var specs []DiskSpec
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		label, path, ok := strings.Cut(part, "=")
		label, path = strings.TrimSpace(label), strings.TrimSpace(path)
		if !ok || label == "" || !filepath.IsAbs(path) {
			return nil, fmt.Errorf("%q is not label=/absolute/path", part)
		}
		specs = append(specs, DiskSpec{Label: label, Path: path})
	}
	return specs, nil
}

// CPUTime is the time all CPUs together spent busy, and in all, since boot,
// in clock ticks, from the cpu line of /proc/stat. Two readings give the
// share of the CPUs used between them.
type CPUTime struct {
	// Busy is user, nice, system, irq, softirq, and steal (guest time is
	// already in user and nice); Total adds idle and iowait.
	Busy, Total uint64
}

// Snapshot is the host's facts at one moment.
type Snapshot struct {
	At      time.Time
	Memory  Memory
	Load    Load
	CPUs    int
	CPUTime CPUTime
	Uptime  time.Duration
	Disks   []Disk
}

// Reader reads Snapshots.
type Reader struct {
	ProcDir string
	Disks   []DiskSpec
}

// Read takes a Snapshot. A disk that cannot be read is left out and
// reported in the error; the other facts are required.
func (r Reader) Read(now time.Time) (Snapshot, error) {
	snapshot := Snapshot{At: now}
	proc := r.ProcDir
	if proc == "" {
		proc = "/proc"
	}
	file, err := os.Open(filepath.Join(proc, "meminfo"))
	if err != nil {
		return Snapshot{}, err
	}
	snapshot.Memory, err = ParseMeminfo(file)
	file.Close()
	if err != nil {
		return Snapshot{}, err
	}
	loadavg, err := os.ReadFile(filepath.Join(proc, "loadavg"))
	if err != nil {
		return Snapshot{}, err
	}
	if snapshot.Load, err = ParseLoadavg(string(loadavg)); err != nil {
		return Snapshot{}, err
	}
	uptime, err := os.ReadFile(filepath.Join(proc, "uptime"))
	if err != nil {
		return Snapshot{}, err
	}
	if snapshot.Uptime, err = ParseUptime(string(uptime)); err != nil {
		return Snapshot{}, err
	}
	stat, err := os.Open(filepath.Join(proc, "stat"))
	if err != nil {
		return Snapshot{}, err
	}
	snapshot.CPUs, snapshot.CPUTime, err = ParseStat(stat)
	stat.Close()
	if err != nil {
		return Snapshot{}, err
	}
	var diskErrs []error
	snapshot.Disks, diskErrs = ReadDisks(r.Disks)
	return snapshot, errors.Join(diskErrs...)
}

// ParseMeminfo reads MemTotal and MemAvailable from /proc/meminfo.
func ParseMeminfo(r io.Reader) (Memory, error) {
	var memory Memory
	var total, available bool
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		key, rest, ok := strings.Cut(scanner.Text(), ":")
		if !ok || (key != "MemTotal" && key != "MemAvailable") {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) != 2 || fields[1] != "kB" {
			return Memory{}, fmt.Errorf("meminfo: unexpected %s line %q", key, scanner.Text())
		}
		kilobytes, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			return Memory{}, fmt.Errorf("meminfo: %s: %w", key, err)
		}
		if key == "MemTotal" {
			memory.TotalBytes, total = kilobytes*1024, true
		} else {
			memory.AvailableBytes, available = kilobytes*1024, true
		}
	}
	if err := scanner.Err(); err != nil {
		return Memory{}, err
	}
	if !total || !available {
		return Memory{}, errors.New("meminfo: MemTotal or MemAvailable is missing")
	}
	return memory, nil
}

// ParseLoadavg reads the three load averages from /proc/loadavg, such as
// "0.93 2.21 5.02 2/1485 958160".
func ParseLoadavg(value string) (Load, error) {
	fields := strings.Fields(value)
	if len(fields) < 3 {
		return Load{}, fmt.Errorf("loadavg: unexpected %q", value)
	}
	var load Load
	for i, target := range []*float64{&load.One, &load.Five, &load.Fifteen} {
		parsed, err := strconv.ParseFloat(fields[i], 64)
		if err != nil || parsed < 0 {
			return Load{}, fmt.Errorf("loadavg: unexpected %q", value)
		}
		*target = parsed
	}
	return load, nil
}

// ParseUptime reads the seconds since boot from /proc/uptime, such as
// "1043512.34 5921033.11".
func ParseUptime(value string) (time.Duration, error) {
	fields := strings.Fields(value)
	if len(fields) < 1 {
		return 0, fmt.Errorf("uptime: unexpected %q", value)
	}
	seconds, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || seconds < 0 {
		return 0, fmt.Errorf("uptime: unexpected %q", value)
	}
	return time.Duration(seconds * float64(time.Second)), nil
}

// ParseStat reads /proc/stat: it counts the cpuN lines (the host's CPUs,
// whatever CPUs the container itself may use) and reads the time of the cpu
// line, the CPUs together.
func ParseStat(r io.Reader) (int, CPUTime, error) {
	count := 0
	var times CPUTime
	var total bool
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "cpu") || len(line) < 4 {
			continue
		}
		if line[3] >= '0' && line[3] <= '9' {
			count++
			continue
		}
		fields := strings.Fields(line)
		if fields[0] != "cpu" {
			continue
		}
		// user nice system idle iowait irq softirq steal guest guest_nice
		if len(fields) < 5 {
			return 0, CPUTime{}, fmt.Errorf("stat: unexpected cpu line %q", line)
		}
		for i, field := range fields[1:min(len(fields), 9)] {
			ticks, err := strconv.ParseUint(field, 10, 64)
			if err != nil {
				return 0, CPUTime{}, fmt.Errorf("stat: unexpected cpu line %q", line)
			}
			times.Total += ticks
			if i != 3 && i != 4 {
				times.Busy += ticks
			}
		}
		total = true
	}
	if err := scanner.Err(); err != nil {
		return 0, CPUTime{}, err
	}
	if count == 0 {
		return 0, CPUTime{}, errors.New("stat: no cpu lines")
	}
	if !total {
		return 0, CPUTime{}, errors.New("stat: no cpu line")
	}
	return count, times, nil
}

// ReadDisks reads each spec's filesystem, once per filesystem, in order.
func ReadDisks(specs []DiskSpec) ([]Disk, []error) {
	var disks []Disk
	var errs []error
	seen := map[uint64]bool{}
	for _, spec := range specs {
		disk, err := StatDisk(spec)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if seen[disk.Device] {
			continue
		}
		seen[disk.Device] = true
		disks = append(disks, disk)
	}
	return disks, errs
}

// StatDisk reads the filesystem path is on. It needs no permission on the
// path itself, only on the directories above it.
func StatDisk(spec DiskSpec) (Disk, error) {
	var fs unix.Statfs_t
	if err := unix.Statfs(spec.Path, &fs); err != nil {
		return Disk{}, fmt.Errorf("disk %s (%s): %w", spec.Label, spec.Path, err)
	}
	var st unix.Stat_t
	if err := unix.Stat(spec.Path, &st); err != nil {
		return Disk{}, fmt.Errorf("disk %s (%s): %w", spec.Label, spec.Path, err)
	}
	return Disk{
		Label:      spec.Label,
		Path:       spec.Path,
		TotalBytes: fs.Blocks * uint64(fs.Bsize),
		FreeBytes:  fs.Bavail * uint64(fs.Bsize),
		Device:     uint64(st.Dev),
	}, nil
}
