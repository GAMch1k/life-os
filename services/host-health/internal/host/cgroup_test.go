package host

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fullID is a 64-character container id starting with the 12-character
// short one.
func fullID(short string) string {
	return short + strings.Repeat("0", 64-len(short))
}

// writeCgroup writes a container's cgroup v2 files as the kernel shows
// them, with the lines Cgroups reads among others.
func writeCgroup(t *testing.T, dir, name string, current, activeFile, inactiveFile, slab, usageUsec uint64) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"memory.current": fmt.Sprintf("%d\n", current),
		"memory.stat":    fmt.Sprintf("anon 1\nfile %d\nshmem 0\ninactive_anon 0\nactive_anon 1\ninactive_file %d\nactive_file %d\nslab_reclaimable %d\nslab_unreclaimable 5\n", activeFile+inactiveFile, inactiveFile, activeFile, slab),
		"cpu.stat":       fmt.Sprintf("usage_usec %d\nuser_usec 1\nsystem_usec 2\n", usageUsec),
	}
	for file, content := range files {
		if err := os.WriteFile(filepath.Join(path, file), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCgroupsReadMemoryAsTheServerCountsIt(t *testing.T) {
	dir := t.TempDir()
	// The systemd driver's docker-<id>.scope, and the cgroupfs driver's <id>.
	writeCgroup(t, dir, "docker-"+fullID("0a1b2c3d4e01")+".scope", 1000<<20, 200<<20, 300<<20, 50<<20, 90_000_000)
	writeCgroup(t, dir, fullID("0a1b2c3d4e02"), 100<<20, 0, 0, 0, 5)
	// Reclaimable parts read a moment after memory.current may add up to
	// more: none in use, never less.
	writeCgroup(t, dir, "docker-"+fullID("0a1b2c3d4e03")+".scope", 10<<20, 6<<20, 6<<20, 0, 0)
	// Neither a container nor a match.
	writeCgroup(t, dir, "ssh.service", 1<<30, 0, 0, 0, 0)

	usage, err := Cgroups{Dir: dir}.Read([]string{"0a1b2c3d4e01", fullID("0a1b2c3d4e02"), "0a1b2c3d4e03", "0a1b2c3d4eff", "short"})
	if err != nil {
		t.Fatal(err)
	}
	// memory.current less the page cache on the file lists and reclaimable
	// slab: 1000 - 200 - 300 - 50 MiB.
	if got := usage["0a1b2c3d4e01"]; got != (ContainerUsage{MemoryBytes: 450 << 20, CPU: 90 * time.Second}) {
		t.Fatalf("scope = %+v", got)
	}
	if got := usage[fullID("0a1b2c3d4e02")]; got != (ContainerUsage{MemoryBytes: 100 << 20, CPU: 5 * time.Microsecond}) {
		t.Fatalf("cgroupfs = %+v", got)
	}
	if got := usage["0a1b2c3d4e03"]; got.MemoryBytes != 0 {
		t.Fatalf("parts above the whole = %+v", got)
	}
	// A container without a cgroup (not running) is left out.
	if len(usage) != 3 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestCgroupsCPUBeyondADuration(t *testing.T) {
	// A time.Duration holds math.MaxInt64 nanoseconds: usage_usec up to
	// that in microseconds converts exactly, and more is held there rather
	// than wrapping to a negative duration.
	most := uint64(math.MaxInt64 / 1000)
	for usec, want := range map[uint64]time.Duration{
		most:           time.Duration(most) * time.Microsecond,
		most + 1:       math.MaxInt64,
		math.MaxInt64:  math.MaxInt64,
		math.MaxUint64: math.MaxInt64,
	} {
		dir := t.TempDir()
		writeCgroup(t, dir, "docker-"+fullID("0a1b2c3d4e01")+".scope", 1<<20, 0, 0, 0, usec)
		usage, err := Cgroups{Dir: dir}.Read([]string{"0a1b2c3d4e01"})
		if err != nil {
			t.Fatal(err)
		}
		if got := usage["0a1b2c3d4e01"].CPU; got != want {
			t.Errorf("usage_usec %d = %v, want %v", usec, got, want)
		}
	}
}

func TestCgroupsThatCannotBeRead(t *testing.T) {
	if _, err := (Cgroups{Dir: filepath.Join(t.TempDir(), "missing")}).Read([]string{"0a1b2c3d4e01"}); err == nil || !strings.HasPrefix(err.Error(), "cgroups: ") {
		t.Fatalf("a missing directory = %v", err)
	}
	for what, change := range map[string]func(path string) error{
		"memory.stat without the file lists": func(path string) error {
			return os.WriteFile(filepath.Join(path, "memory.stat"), []byte("anon 1\nfile 2\n"), 0o644)
		},
		"cpu.stat without usage_usec": func(path string) error {
			return os.WriteFile(filepath.Join(path, "cpu.stat"), []byte("user_usec 1\n"), 0o644)
		},
		"memory.current not a number": func(path string) error {
			return os.WriteFile(filepath.Join(path, "memory.current"), []byte("max\n"), 0o644)
		},
		"memory.current unreadable": func(path string) error {
			if os.Geteuid() == 0 {
				return os.WriteFile(filepath.Join(path, "memory.current"), []byte(""), 0o644)
			}
			return os.Chmod(filepath.Join(path, "memory.current"), 0)
		},
	} {
		dir := t.TempDir()
		name := "docker-" + fullID("0a1b2c3d4e01") + ".scope"
		writeCgroup(t, dir, name, 1<<20, 0, 0, 0, 1)
		if err := change(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
		if _, err := (Cgroups{Dir: dir}).Read([]string{"0a1b2c3d4e01"}); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: %v", what, err)
		}
	}
	// A cgroup removed while it is read (the container stopped) is left out.
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "docker-"+fullID("0a1b2c3d4e01")+".scope"), 0o755); err != nil {
		t.Fatal(err)
	}
	if usage, err := (Cgroups{Dir: dir}).Read([]string{"0a1b2c3d4e01"}); err != nil || len(usage) != 0 {
		t.Fatalf("an emptied cgroup = %+v, %v", usage, err)
	}
}
