package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMetricsProcSamples(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "net"), 0700); err != nil {
		t.Fatal(err)
	}
	write := func(name, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("stat", "cpu 100 0 100 800 0 0 0 0 50 0\n")
	write("meminfo", "MemTotal: 1000 kB\nMemAvailable: 250 kB\n")
	write("loadavg", "1.25 2.50 3.75 1/2 3\n")
	write("net/dev", "Inter-| Receive | Transmit\n face |bytes packets errs drop fifo frame compressed multicast|bytes packets errs drop fifo colls carrier compressed\nlo: 999 0 0 0 0 0 0 0 999 0 0 0 0 0 0 0\neth0: 123 0 0 0 0 0 0 0 456 0 0 0 0 0 0 0\n")
	write("diskstats", "8 0 sda 1 0 2 0 1 0 3 0 0 0 0\n")
	c := &metricsCollector{proc: root, platform: "linux"}
	now := time.Now()
	first := c.collect(now)
	if first.CPUPercent != nil || first.Status["cpu"] != "warming_up" {
		t.Fatalf("first=%+v", first)
	}
	if *first.MemTotal != 1024000 || *first.MemUsed != 768000 || *first.Load15 != 3.75 {
		t.Fatalf("sample=%+v", first)
	}
	if len(first.NetworkRXBytes) != 1 || first.NetworkRXBytes["eth0"] != 123 || first.NetworkTXBytes["eth0"] != 456 || first.DiskReadBytes["sda"] != 1024 || first.DiskWriteBytes["sda"] != 1536 {
		t.Fatalf("counters=%+v", first)
	}
	write("stat", "cpu 110 0 110 880 0 0 0 0 60 0\n")
	if c.collect(now.Add(time.Second)) != first {
		t.Fatal("fast sampling was not cached")
	}
	second := c.collect(now.Add(20 * time.Second))
	if second.CPUPercent == nil || *second.CPUPercent != 20 {
		t.Fatalf("CPU=%v", second.CPUPercent)
	}
	write("stat", "cpu 1 0 1 8 0 0 0 0\n")
	if c.collect(now.Add(40*time.Second)).CPUPercent != nil {
		t.Fatal("counter reset produced CPU usage")
	}
	write("meminfo", "MemTotal: 1000 kB\n")
	write("loadavg", "NaN 1 2")
	write("diskstats", "bad")
	third := c.collect(now.Add(time.Minute))
	if third.MemUsed != nil || third.Load1 != nil || third.DiskReadBytes != nil {
		t.Fatal("invalid source produced values")
	}
}

func TestUnsupportedMetricsAreNull(t *testing.T) {
	c := &metricsCollector{platform: "plan9"}
	m := c.collect(time.Now())
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"cpuPercent":null`) || !strings.Contains(string(b), `"diskReadBytes":null`) {
		t.Fatalf("%s", b)
	}
	for _, status := range m.Status {
		if status != "unsupported" {
			t.Fatal(status)
		}
	}
}
