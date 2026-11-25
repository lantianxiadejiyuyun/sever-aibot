package agent

import (
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"server-aibot/internal/model"
)

// metricsCollector is owned by one sampling loop; platform-specific collectors
// use native OS APIs and leave unsupported metrics null.
type metricsCollector struct {
	proc     string
	platform string
	previous []uint64
	cached   *model.Metrics
	sampled  time.Time
}

func newMetricsCollector() *metricsCollector {
	return &metricsCollector{proc: "/proc", platform: runtime.GOOS}
}

func (c *metricsCollector) collect(now time.Time) *model.Metrics {
	// Configurable fast heartbeats must not cause equally fast proc reads.
	if c.cached != nil && now.Sub(c.sampled) >= 0 && now.Sub(c.sampled) < 20*time.Second {
		return c.cached
	}
	m := &model.Metrics{CollectedAt: now.UTC(), Status: map[string]string{}}
	c.cached, c.sampled = m, now
	for _, key := range []string{"cpu", "memory", "load", "network", "disk", "diskCapacity"} {
		m.Status[key] = "unsupported"
	}
	if c.platform == runtime.GOOS {
		c.collectNative(m)
	}
	if c.platform != "linux" {
		return m
	}
	for _, key := range []string{"cpu", "memory", "load", "network", "disk"} {
		m.Status[key] = "unavailable"
	}
	read := func(name string) string { b, _ := os.ReadFile(filepath.Join(c.proc, name)); return string(b) }
	if ticks := parseCPU(read("stat")); ticks != nil {
		m.Status["cpu"] = "warming_up"
		if len(c.previous) == len(ticks) {
			var total, idle uint64
			valid := true
			for i, v := range ticks {
				if v < c.previous[i] {
					valid = false
					break
				}
				delta := v - c.previous[i]
				total += delta
				if i == 3 || i == 4 {
					idle += delta
				}
			}
			if valid && total > 0 && idle <= total {
				value := 100 * float64(total-idle) / float64(total)
				m.CPUPercent = &value
				m.Status["cpu"] = "ok"
			}
		}
		c.previous = ticks
	} else {
		c.previous = nil
	}
	if total, used, ok := parseMemory(read("meminfo")); ok {
		m.MemTotal, m.MemUsed = &total, &used
		m.Status["memory"] = "ok"
	}
	fields := strings.Fields(read("loadavg"))
	if len(fields) >= 3 {
		values := [3]float64{}
		ok := true
		for i := range values {
			v, err := strconv.ParseFloat(fields[i], 64)
			if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
				ok = false
			}
			values[i] = v
		}
		if ok {
			m.Load1, m.Load5, m.Load15 = &values[0], &values[1], &values[2]
			m.Status["load"] = "ok"
		}
	}
	if rx, tx, ok := parseNetwork(read("net/dev")); ok {
		m.NetworkRXBytes, m.NetworkTXBytes = rx, tx
		m.Status["network"] = "ok"
	}
	if r, w, ok := parseDisk(read("diskstats")); ok {
		m.DiskReadBytes, m.DiskWriteBytes = r, w
		m.Status["disk"] = "ok"
	}
	return m
}

func setDiskCapacity(m *model.Metrics, total, free uint64) {
	if total == 0 || free > total {
		return
	}
	used := total - free
	percent := 100 * float64(used) / float64(total)
	m.RootDiskTotal, m.RootDiskUsed, m.RootDiskUsedPercent = &total, &used, &percent
	m.Status["diskCapacity"] = "ok"
}

func parseCPU(text string) []uint64 {
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || f[0] != "cpu" {
			continue
		}
		if len(f) < 5 {
			return nil
		}
		// guest/guest_nice are already counted in user/nice.
		n := len(f) - 1
		if n > 8 {
			n = 8
		}
		ticks := make([]uint64, n)
		for i := range ticks {
			v, e := strconv.ParseUint(f[i+1], 10, 64)
			if e != nil {
				return nil
			}
			ticks[i] = v
		}
		return ticks
	}
	return nil
}

func parseMemory(text string) (uint64, uint64, bool) {
	values := map[string]uint64{}
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		if f[0] != "MemTotal:" && f[0] != "MemAvailable:" {
			continue
		}
		if len(f) != 3 || f[2] != "kB" {
			return 0, 0, false
		}
		v, e := strconv.ParseUint(f[1], 10, 64)
		if e != nil || v > math.MaxUint64/1024 {
			return 0, 0, false
		}
		values[f[0]] = v * 1024
	}
	total, tok := values["MemTotal:"]
	available, aok := values["MemAvailable:"]
	if !tok || !aok || total == 0 || available > total {
		return 0, 0, false
	}
	return total, total - available, true
}

func parseNetwork(text string) (map[string]uint64, map[string]uint64, bool) {
	rx, tx := map[string]uint64{}, map[string]uint64{}
	if !strings.Contains(text, "Inter-") || !strings.Contains(text, "Receive") {
		return nil, nil, false
	}
	for _, line := range strings.Split(text, "\n") {
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		f := strings.Fields(rest)
		if len(f) < 16 || name == "" {
			return nil, nil, false
		}
		r, e1 := strconv.ParseUint(f[0], 10, 64)
		w, e2 := strconv.ParseUint(f[8], 10, 64)
		if e1 != nil || e2 != nil {
			return nil, nil, false
		}
		if name != "lo" {
			rx[name], tx[name] = r, w
		}
	}
	return rx, tx, true
}

func parseDisk(text string) (map[string]uint64, map[string]uint64, bool) {
	r, w := map[string]uint64{}, map[string]uint64{}
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		if len(f) < 14 {
			return nil, nil, false
		}
		// Linux diskstats sectors are always 512 bytes, irrespective of sector size.
		a, e1 := strconv.ParseUint(f[5], 10, 64)
		b, e2 := strconv.ParseUint(f[9], 10, 64)
		if e1 != nil || e2 != nil || a > math.MaxUint64/512 || b > math.MaxUint64/512 {
			return nil, nil, false
		}
		r[f[2]], w[f[2]] = a*512, b*512
	}
	if len(r) == 0 {
		return nil, nil, false
	}
	return r, w, true
}
