package host

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// ContainerUsage is one container's use of the server, from its cgroup v2
// files.
type ContainerUsage struct {
	// MemoryBytes is memory in use as MemAvailable counts it for the whole
	// server: memory.current less what the kernel can reclaim at once, the
	// page cache on the file lists (active_file, inactive_file) and
	// reclaimable kernel memory (slab_reclaimable). Shared memory and tmpfs
	// stay in, as they do in MemTotal - MemAvailable. docker stats subtracts
	// inactive_file only, so it shows more.
	MemoryBytes uint64
	// CPU is the CPU time it used since it started (cpu.stat usage_usec).
	CPU time.Duration
}

// cgroupName is a container's cgroup directory: docker-<id>.scope with
// Docker's systemd cgroup driver (under system.slice), <id> with the
// cgroupfs driver (under docker).
var cgroupName = regexp.MustCompile(`^(?:docker-)?([0-9a-f]{64})(?:\.scope)?$`)

// Cgroups reads the containers' cgroups in Dir, the directory that holds
// one per container (/sys/fs/cgroup/system.slice on this server, bound
// read-only).
type Cgroups struct{ Dir string }

// Read reads the cgroups of the containers with the given ids (full, or
// the short ones the docker gateway gives), by the id given. A container
// without a cgroup directory (not running) is left out, as is one whose
// directory is removed while it is read; a directory that cannot be listed,
// or files that cannot be read, are an error.
func (c Cgroups) Read(ids []string) (map[string]ContainerUsage, error) {
	entries, err := os.ReadDir(c.Dir)
	if err != nil {
		return nil, fmt.Errorf("cgroups: %w", err)
	}
	// By the short id: the first 12 characters of the full one.
	type cgroup struct{ id, name string }
	cgroups := map[string]cgroup{}
	for _, entry := range entries {
		if match := cgroupName.FindStringSubmatch(entry.Name()); match != nil && entry.IsDir() {
			cgroups[match[1][:12]] = cgroup{id: match[1], name: entry.Name()}
		}
	}
	usage := map[string]ContainerUsage{}
	for _, id := range ids {
		if len(id) < 12 {
			continue
		}
		found, ok := cgroups[id[:12]]
		if !ok || !strings.HasPrefix(found.id, id) {
			continue
		}
		name := found.name
		reading, err := readCgroup(filepath.Join(c.Dir, name))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("cgroups: %s: %w", name, err)
		}
		usage[id] = reading
	}
	return usage, nil
}

func readCgroup(dir string) (ContainerUsage, error) {
	current, err := os.ReadFile(filepath.Join(dir, "memory.current"))
	if err != nil {
		return ContainerUsage{}, err
	}
	memory, err := strconv.ParseUint(strings.TrimSpace(string(current)), 10, 64)
	if err != nil {
		return ContainerUsage{}, fmt.Errorf("memory.current: unexpected %q", current)
	}
	stat, err := readKeyed(filepath.Join(dir, "memory.stat"), "active_file", "inactive_file", "slab_reclaimable")
	if err != nil {
		return ContainerUsage{}, err
	}
	cpu, err := readKeyed(filepath.Join(dir, "cpu.stat"), "usage_usec")
	if err != nil {
		return ContainerUsage{}, err
	}
	reclaimable := stat["active_file"] + stat["inactive_file"] + stat["slab_reclaimable"]
	// The files are read one after the other, so the parts may add up to
	// a little more than the whole.
	if reclaimable > memory {
		reclaimable = memory
	}
	return ContainerUsage{MemoryBytes: memory - reclaimable, CPU: cpuTime(cpu["usage_usec"])}, nil
}

// cpuTime is cpu.stat's usage_usec as a duration. A time.Duration counts
// nanoseconds in an int64, which holds some 292 years; a larger count is
// held at the most it can hold, not wrapped to a negative duration.
func cpuTime(usec uint64) time.Duration {
	const most = uint64(math.MaxInt64 / int64(time.Microsecond))
	if usec > most {
		return math.MaxInt64
	}
	return time.Duration(usec) * time.Microsecond
}

// readKeyed reads the given keys of a cgroup file of "key value" lines,
// such as memory.stat; each must be there.
func readKeyed(path string, keys ...string) (map[string]uint64, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	values := map[string]uint64{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		key, value, ok := strings.Cut(scanner.Text(), " ")
		if !ok || !slices.Contains(keys, key) {
			continue
		}
		parsed, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%s: unexpected %s %q", filepath.Base(path), key, value)
		}
		values[key] = parsed
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	for _, key := range keys {
		if _, ok := values[key]; !ok {
			return nil, fmt.Errorf("%s: no %s", filepath.Base(path), key)
		}
	}
	return values, nil
}
