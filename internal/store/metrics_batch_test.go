package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"server-aibot/internal/model"
)

func TestMetricBackfillDedupAndLatest(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	d, _, err := s.CreateDevice(ctx, "backfill", "", "")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Minute)
	sample := func(id int, at time.Time) model.Metrics {
		return model.Metrics{SampleID: fmt.Sprintf("%032x", id), CollectedAt: at}
	}
	current := sample(1, now)
	if ack, e := s.saveMetricsBatchAt(ctx, d.ID, []model.Metrics{current}, now); e != nil || len(ack) != 1 {
		t.Fatalf("current: %v %v", ack, e)
	}
	old := sample(2, now.Add(-time.Hour))
	expired := sample(3, now.Add(-25*time.Hour))
	batch := []model.Metrics{old, current, expired, sample(4, old.CollectedAt.Add(time.Second))}
	for i := 0; i < 2; i++ {
		if ack, e := s.saveMetricsBatchAt(ctx, d.ID, batch, now.Add(time.Minute)); e != nil || len(ack) != 4 {
			t.Fatalf("retry: %v %v", ack, e)
		}
	}
	all, e := s.ListDeviceMetrics(ctx, d.ID, time.Time{}, time.Time{}, 100)
	if e != nil {
		t.Fatal(e)
	}
	if len(all) != 2 || all[0].Metrics.SampleID != current.SampleID || all[1].Metrics.SampleID != old.SampleID {
		t.Fatalf("backfill replaced latest or duplicated: %+v", all)
	}
	if !all[1].ReceivedAt.Equal(now.Add(time.Minute)) {
		t.Fatal("receipt timestamp replaced by agent timestamp")
	}
	// A just-received backfill outside the sampling window must not appear.
	filtered, e := s.ListDeviceMetrics(ctx, d.ID, now, now.Add(time.Minute), 10)
	if e != nil || len(filtered) != 1 || filtered[0].Metrics.SampleID != current.SampleID {
		t.Fatalf("sampling window included old backfill: %+v %v", filtered, e)
	}
	filtered, e = s.ListDeviceMetrics(ctx, d.ID, old.CollectedAt, old.CollectedAt, 10)
	if e != nil || len(filtered) != 1 || filtered[0].Metrics.SampleID != old.SampleID || !filtered[0].ReceivedAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("inclusive sample boundary or separate receipt time lost: %+v %v", filtered, e)
	}
	filtered, e = s.ListDeviceMetrics(ctx, d.ID, now.Add(time.Minute), now.Add(time.Minute), 10)
	if e != nil || len(filtered) != 0 {
		t.Fatalf("receipt-only window included samples: %+v %v", filtered, e)
	}
	if _, e = s.saveMetricsBatchAt(ctx, d.ID, []model.Metrics{sample(5, now.Add(2*time.Minute))}, now); e == nil {
		t.Fatal("future sample accepted")
	}
	if _, e = s.SaveDeviceMetricsBatch(ctx, d.ID, make([]model.Metrics, MetricsBatchLimit+1)); e == nil {
		t.Fatal("oversized batch accepted")
	}
	if _, e = s.SaveDeviceMetricsBatch(ctx, d.ID, []model.Metrics{{SampleID: "bad", CollectedAt: now}}); e == nil {
		t.Fatal("invalid ID accepted")
	}
}
