//go:build !linux && !windows

package agent

import "server-aibot/internal/model"

func (c *metricsCollector) collectNative(m *model.Metrics) {}
