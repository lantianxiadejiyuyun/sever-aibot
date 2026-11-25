package agent

import (
	"server-aibot/internal/model"
	"syscall"
)

func (c *metricsCollector) collectNative(m *model.Metrics) {
	m.Status["diskCapacity"] = "unavailable"
	var stat syscall.Statfs_t
	if syscall.Statfs("/", &stat) != nil || stat.Bsize <= 0 {
		return
	}
	setDiskCapacity(m, stat.Blocks*uint64(stat.Bsize), stat.Bfree*uint64(stat.Bsize))
}
