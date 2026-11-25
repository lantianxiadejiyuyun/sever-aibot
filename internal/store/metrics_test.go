package store

import (
	"context"
	"server-aibot/internal/model"
	"sync"
	"testing"
	"time"
)

func TestMetricsThrottleQueryRetention(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	d, _, err := s.CreateDevice(ctx, "metrics", "", "")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Truncate(time.Millisecond)
	sample := model.Metrics{CollectedAt: base.Add(24 * time.Hour), Status: map[string]string{"cpu": "unsupported"}}
	var wg sync.WaitGroup
	results := make(chan bool, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, e := s.saveDeviceMetricsAt(ctx, d.ID, sample, base)
			results <- ok
			errs <- e
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	count := 0
	for ok := range results {
		if ok {
			count++
		}
	}
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	if count != 1 {
		t.Fatalf("concurrent inserts=%d", count)
	}
	if ok, e := s.saveDeviceMetricsAt(ctx, d.ID, sample, base.Add(59*time.Second)); ok || e != nil {
		t.Fatalf("throttle=%v %v", ok, e)
	}
	for i := 1; i <= MetricsRetentionLimit; i++ {
		if ok, e := s.saveDeviceMetricsAt(ctx, d.ID, sample, base.Add(time.Duration(i)*time.Minute)); !ok || e != nil {
			t.Fatalf("insert %d: %v %v", i, ok, e)
		}
	}
	all, e := s.ListDeviceMetrics(ctx, d.ID, time.Time{}, time.Time{}, 99999)
	if e != nil {
		t.Fatal(e)
	}
	if len(all) != MetricsRetentionLimit || !all[len(all)-1].ReceivedAt.Equal(base.Add(time.Minute)) {
		t.Fatalf("retention count=%d", len(all))
	}
	if all[0].Metrics.CPUPercent != nil || !all[0].Metrics.CollectedAt.Equal(sample.CollectedAt) {
		t.Fatal("nullable payload lost")
	}
	from := base.Add(2 * time.Minute)
	to := base.Add(3 * time.Minute)
	rows, e := s.ListDeviceMetrics(ctx, d.ID, from, to, 10)
	if e != nil || len(rows) != 2 || !rows[0].ReceivedAt.Equal(to) {
		t.Fatalf("range=%+v %v", rows, e)
	}
	rows, e = s.ListDeviceMetrics(ctx, "other", time.Time{}, time.Time{}, 10)
	if e != nil || len(rows) != 0 {
		t.Fatal("device isolation failed")
	}
	if _, e = s.ListDeviceMetrics(ctx, d.ID, to, from, 10); e == nil {
		t.Fatal("reversed range accepted")
	}
	if err = s.DeleteDevice(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = s.db.QueryRow(`SELECT count(*) FROM device_metrics`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("cascade count=%d err=%v", n, err)
	}
}

func TestMetricsValidation(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	d, _, err := s.CreateDevice(ctx, "validation", "", "")
	if err != nil {
		t.Fatal(err)
	}
	cpu := 101.0
	if _, err = s.SaveDeviceMetrics(ctx, d.ID, model.Metrics{CollectedAt: time.Now(), CPUPercent: &cpu}); err == nil {
		t.Fatal("invalid CPU accepted")
	}
	if _, err = s.SaveDeviceMetrics(ctx, "missing", model.Metrics{CollectedAt: time.Now()}); err == nil {
		t.Fatal("orphan sample accepted")
	}
}
