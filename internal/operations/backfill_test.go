package operations

import (
	"context"
	"strings"
	"testing"
	"time"

	"server-aibot/internal/model"
	"server-aibot/internal/store"
)

func TestMetricBackfillUsesCollectionTimeAndRootDisk(t *testing.T) {
	svc, _ := fixture(t)
	ctx := context.Background()
	device, _, err := svc.Store.CreateDevice(ctx, "backfill", "", "")
	if err != nil {
		t.Fatal(err)
	}
	disk := 95.0
	old := model.Metrics{SampleID: "00112233445566778899aabbccddeeff", CollectedAt: time.Now().Add(-10 * time.Minute), RootDiskUsedPercent: &disk}
	if _, err = svc.Store.SaveDeviceMetricsBatch(ctx, device.ID, []model.Metrics{old}); err != nil {
		t.Fatal(err)
	}
	c := store.OperationCheck{Name: "root disk", Kind: "metric", TargetKind: "device", TargetID: device.ID, Metric: "root_disk", Threshold: 90, MaxAgeSeconds: 120, TimeoutMS: 1000, IntervalSeconds: 30}
	if err = svc.ValidateCheck(ctx, c); err != nil {
		t.Fatal(err)
	}
	state, _ := svc.metric(ctx, c)
	if state != "stale" {
		t.Fatal("recent receipt made old backfill current", state)
	}
	fresh := old
	fresh.SampleID = "ffeeddccbbaa99887766554433221100"
	fresh.CollectedAt = time.Now()
	if _, err = svc.Store.SaveDeviceMetricsBatch(ctx, device.ID, []model.Metrics{fresh}); err != nil {
		t.Fatal(err)
	}
	state, message := svc.metric(ctx, c)
	if state != "down" || !strings.Contains(message, "采样时间") || !strings.Contains(message, "接收时间") {
		t.Fatalf("fresh disk threshold=%s %s", state, message)
	}
	legacy, _, err := svc.Store.CreateDevice(ctx, "legacy", "", "")
	if err != nil {
		t.Fatal(err)
	}
	old.SampleID = ""
	if _, err = svc.Store.SaveDeviceMetrics(ctx, legacy.ID, old); err != nil {
		t.Fatal(err)
	}
	c.TargetID = legacy.ID
	state, message = svc.metric(ctx, c)
	if state != "down" || !strings.Contains(message, "legacy 接收时间") {
		t.Fatalf("legacy fallback=%s %s", state, message)
	}
}
