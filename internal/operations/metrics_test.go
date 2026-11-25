package operations

import (
	"context"
	"net"
	"testing"
	"time"

	"server-aibot/internal/model"
	"server-aibot/internal/store"
)

func TestMetricThresholdFreshnessAndHistoricalScores(t *testing.T) {
	svc, _ := fixture(t)
	ctx := context.Background()
	device, _, err := svc.Store.CreateDevice(ctx, "metrics device", "", "")
	if err != nil {
		t.Fatal(err)
	}
	c := store.OperationCheck{Name: "CPU threshold", Kind: "metric", TargetKind: "device", TargetID: device.ID, Metric: "cpu", Threshold: 80, MaxAgeSeconds: 120, TimeoutMS: 1000, IntervalSeconds: 30, Enabled: true}
	if err = svc.ValidateCheck(ctx, c); err != nil {
		t.Fatal(err)
	}
	c, err = svc.Store.SaveOperationCheck(ctx, c, true)
	if err != nil {
		t.Fatal(err)
	}
	run, err := svc.Run(ctx, c.ID)
	if err != nil || run.State != "stale" || run.Score != nil {
		t.Fatalf("missing sample: %+v %v", run, err)
	}
	cpu, load := 95.0, 4.0
	used, total := uint64(900), uint64(1000)
	_, err = svc.Store.SaveDeviceMetrics(ctx, device.ID, model.Metrics{CollectedAt: time.Now(), CPUPercent: &cpu, Load1: &load, MemUsed: &used, MemTotal: &total})
	if err != nil {
		t.Fatal(err)
	}
	run, err = svc.Run(ctx, c.ID)
	if err != nil || run.State != "down" || run.Score == nil || *run.Score != 0 || run.PreviousState != "stale" {
		t.Fatalf("threshold: %+v %v", run, err)
	}
	c.Threshold = 99
	c, err = svc.Store.SaveOperationCheck(ctx, c, false)
	if err != nil {
		t.Fatal(err)
	}
	run, err = svc.Run(ctx, c.ID)
	if err != nil || run.State != "up" || run.Score == nil || *run.Score != 100 || run.PreviousState != "down" {
		t.Fatalf("recovery: %+v %v", run, err)
	}
	for _, metric := range []string{"memory", "load"} {
		c.Metric = metric
		c.Threshold = 1
		state, _ := svc.metric(ctx, c)
		if state != "down" {
			t.Fatalf("%s didn't breach", metric)
		}
	}
	if _, err = svc.Store.DB().Exec(`UPDATE device_metrics SET received_at=? WHERE device_id=?`, time.Now().Add(-time.Hour).UnixMilli(), device.ID); err != nil {
		t.Fatal(err)
	}
	run, err = svc.Run(ctx, c.ID)
	if err != nil || run.State != "stale" || run.Score != nil {
		t.Fatalf("expired sample: %+v %v", run, err)
	}
	history, err := svc.Store.ListInspectionRuns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if history[0].PreviousState != "up" || history[0].Score != nil || history[1].PreviousState != "down" {
		t.Fatalf("history lost comparison: %+v", history)
	}
}
func TestServiceShutdownStopsWorkers(t *testing.T) {
	svc, c := fixture(t)
	entered := make(chan struct{}, 1)
	svc.SetDialer(DialFunc(func(ctx context.Context, _ Target) (net.Conn, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc.Start(ctx)
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("scheduler never ran")
	}
	done := make(chan struct{})
	go func() { svc.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown did not stop workers")
	}
	stored, err := svc.Store.GetOperationCheck(context.Background(), c.ID)
	if err != nil || stored.CheckedAt == 0 {
		t.Fatalf("shutdown lost completed result: %+v %v", stored, err)
	}
}
