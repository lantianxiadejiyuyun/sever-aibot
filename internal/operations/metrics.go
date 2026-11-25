package operations

import (
	"context"
	"fmt"
	"time"

	"server-aibot/internal/store"
)

func (s *Service) metric(ctx context.Context, c store.OperationCheck) (string, string) {
	if _, err := s.Store.GetDevice(ctx, c.TargetID); err != nil {
		return "stale", "登记设备不存在或不可读取"
	}
	samples, err := s.Store.ListDeviceMetrics(ctx, c.TargetID, time.Time{}, time.Time{}, 1)
	if err != nil {
		return "stale", "设备指标读取失败"
	}
	if len(samples) == 0 {
		return "stale", "设备尚未上报指标"
	}
	sample := samples[0]
	observedAt := sample.ReceivedAt
	timeBasis := "legacy 接收时间"
	if sample.Metrics.SampleID != "" {
		observedAt = sample.Metrics.CollectedAt
		timeBasis = "采样时间"
	}
	if observedAt.IsZero() || time.Since(observedAt) > time.Duration(c.MaxAgeSeconds)*time.Second || observedAt.After(time.Now().Add(time.Second)) {
		return "stale", "设备指标已过期"
	}
	var value float64
	unit := "%"
	switch c.Metric {
	case "cpu":
		if sample.Metrics.CPUPercent == nil {
			return "stale", "设备未提供 CPU 指标"
		}
		value = *sample.Metrics.CPUPercent
	case "memory":
		if sample.Metrics.MemUsed == nil || sample.Metrics.MemTotal == nil || *sample.Metrics.MemTotal == 0 {
			return "stale", "设备未提供可用内存指标"
		}
		value = float64(*sample.Metrics.MemUsed) / float64(*sample.Metrics.MemTotal) * 100
	case "root_disk":
		if sample.Metrics.RootDiskUsedPercent == nil {
			return "stale", "设备未提供根分区容量指标"
		}
		value = *sample.Metrics.RootDiskUsedPercent
	case "load":
		if sample.Metrics.Load1 == nil {
			return "stale", "设备未提供一分钟负载"
		}
		value = *sample.Metrics.Load1
		unit = ""
	default:
		return "stale", "未知指标类型"
	}
	state := "up"
	if value >= c.Threshold {
		state = "down"
	}
	return state, fmt.Sprintf("%s = %.2f%s，告警阈值 ≥ %.2f%s，%s %s，接收时间 %s", c.Metric, value, unit, c.Threshold, unit, timeBasis, observedAt.UTC().Format(time.RFC3339), sample.ReceivedAt.UTC().Format(time.RFC3339))
}
