package agent

import (
	"testing"
	"time"
)

func TestWindowsNativeMetrics(t *testing.T) {
	c := newMetricsCollector()
	m := c.collect(time.Now())
	for _, key := range []string{"memory", "network", "diskCapacity"} {
		if m.Status[key] != "ok" {
			t.Fatalf("native %s status=%s", key, m.Status[key])
		}
	}
	if m.MemTotal == nil || *m.MemTotal == 0 || m.MemUsed == nil || *m.MemUsed > *m.MemTotal {
		t.Fatal("invalid physical memory")
	}
	if m.RootDiskTotal == nil || *m.RootDiskTotal == 0 || m.RootDiskUsedPercent == nil {
		t.Fatal("missing system disk capacity")
	}
	if m.Status["cpu"] != "warming_up" || m.CPUPercent != nil {
		t.Fatal("CPU must establish baseline")
	}
	// Exercise native CPU delta without sleeping: allow the collection cache to expire.
	next := c.collect(time.Now().Add(time.Minute))
	if next.CPUPercent != nil && (*next.CPUPercent < 0 || *next.CPUPercent > 100) {
		t.Fatal("invalid CPU delta")
	}
	if m.Load1 != nil || m.Status["load"] != "unsupported" {
		t.Fatal("Windows load average must be unsupported")
	}
}
