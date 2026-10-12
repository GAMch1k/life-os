package host

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseMeminfo(t *testing.T) {
	file, err := os.Open("testdata/meminfo")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	memory, err := ParseMeminfo(file)
	if err != nil {
		t.Fatal(err)
	}
	if memory.TotalBytes != 8045404*1024 || memory.AvailableBytes != 3670016*1024 {
		t.Fatalf("memory = %+v", memory)
	}
	if percent := memory.AvailablePercent(); percent < 45.6 || percent > 45.7 {
		t.Errorf("AvailablePercent = %f", percent)
	}
	for name, text := range map[string]string{
		"no MemAvailable": "MemTotal: 100 kB\nMemFree: 50 kB\n",
		"no MemTotal":     "MemAvailable: 100 kB\n",
		"other unit":      "MemTotal: 100 MB\nMemAvailable: 50 kB\n",
		"not a number":    "MemTotal: lots kB\nMemAvailable: 50 kB\n",
	} {
		if _, err := ParseMeminfo(strings.NewReader(text)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if (Memory{}).AvailablePercent() != 0 {
		t.Error("an empty Memory has a percentage")
	}
}

func TestParseLoadavgUptimeAndCPUs(t *testing.T) {
	load, err := ParseLoadavg("0.93 2.21 5.02 2/1485 958160\n")
	if err != nil || load != (Load{One: 0.93, Five: 2.21, Fifteen: 5.02}) {
		t.Fatalf("ParseLoadavg = %+v, %v", load, err)
	}
	for _, value := range []string{"", "0.93 2.21", "a b c", "-1 0 0"} {
		if _, err := ParseLoadavg(value); err == nil {
			t.Errorf("ParseLoadavg(%q) accepted", value)
		}
	}
	uptime, err := ParseUptime("1043512.34 5921033.11\n")
	if err != nil || uptime.Round(time.Second) != 1043512*time.Second {
		t.Fatalf("ParseUptime = %s, %v", uptime, err)
	}
	if _, err := ParseUptime("soon"); err == nil {
		t.Error("ParseUptime accepted a word")
	}
	file, _ := os.Open("testdata/stat")
	defer file.Close()
	// Busy is user, nice, system, irq, softirq, and steal; idle and iowait
	// are not.
	if cpus, times, err := ParseStat(file); err != nil || cpus != 6 || times != (CPUTime{Busy: 4705 + 356 + 584 + 23, Total: 4705 + 356 + 584 + 3699 + 23 + 23}) {
		t.Fatalf("ParseStat = %d, %+v, %v", cpus, times, err)
	}
	for name, text := range map[string]string{
		"only the total line": "cpu  1 2 3 4 5\nintr 5\n",
		"no total line":       "cpu0 1 2 3 4 5\n",
		"a short total line":  "cpu  1 2 3\ncpu0 1 2 3 4 5\n",
		"not a number":        "cpu  1 2 x 4 5\ncpu0 1 2 3 4 5\n",
	} {
		if _, _, err := ParseStat(strings.NewReader(text)); err == nil {
			t.Errorf("ParseStat accepted %s", name)
		}
	}
}

func TestParseDiskSpecs(t *testing.T) {
	specs, err := ParseDiskSpecs(" root=/host , docker=/host-docker,")
	if err != nil || len(specs) != 2 || specs[1] != (DiskSpec{Label: "docker", Path: "/host-docker"}) {
		t.Fatalf("ParseDiskSpecs = %+v, %v", specs, err)
	}
	for _, value := range []string{"root", "root=host", "=/host"} {
		if _, err := ParseDiskSpecs(value); err == nil {
			t.Errorf("ParseDiskSpecs(%q) accepted", value)
		}
	}
}

func TestReadDisksReportsEachFilesystemOnce(t *testing.T) {
	dir := t.TempDir()
	other := filepath.Join(dir, "nested")
	if err := os.Mkdir(other, 0o700); err != nil {
		t.Fatal(err)
	}
	disks, errs := ReadDisks([]DiskSpec{{Label: "root", Path: dir}, {Label: "docker", Path: other}, {Label: "gone", Path: filepath.Join(dir, "missing")}})
	if len(disks) != 1 || disks[0].Label != "root" {
		t.Fatalf("disks = %+v; the second path is on the same filesystem", disks)
	}
	if disks[0].TotalBytes == 0 || disks[0].FreeBytes > disks[0].TotalBytes || disks[0].FreePercent() <= 0 {
		t.Fatalf("disk = %+v", disks[0])
	}
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "gone") {
		t.Fatalf("errors = %v", errs)
	}
}

func TestReaderReadsAProcDirectory(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	snapshot, err := Reader{ProcDir: "testdata", Disks: []DiskSpec{{Label: "root", Path: dir}}}.Read(now)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.At.Equal(now) || snapshot.CPUs != 6 || snapshot.CPUTime.Total != 9390 || snapshot.Load.Fifteen != 5.02 || snapshot.Memory.AvailableBytes != 3670016*1024 || len(snapshot.Disks) != 1 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	// A disk that cannot be read leaves the rest of the snapshot good.
	snapshot, err = Reader{ProcDir: "testdata", Disks: []DiskSpec{{Label: "root", Path: filepath.Join(dir, "missing")}}}.Read(now)
	if err == nil || snapshot.Memory.TotalBytes == 0 || len(snapshot.Disks) != 0 {
		t.Fatalf("snapshot = %+v, %v", snapshot, err)
	}
	if _, err := (Reader{ProcDir: dir}).Read(now); err == nil {
		t.Fatal("a directory without meminfo read without error")
	}
}
