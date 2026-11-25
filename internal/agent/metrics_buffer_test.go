package agent

import (
	"fmt"
	"testing"
	"time"

	"server-aibot/internal/model"
)

func TestMetricOutboxBoundsRetryAndAck(t *testing.T) {
	b := &metricBuffer{}
	base := time.Now()
	for i := 0; i < metricQueueLimit+10; i++ {
		b.add(model.Metrics{CollectedAt: base.Add(time.Duration(i) * time.Minute)})
	}
	if len(b.queue) != metricQueueLimit || b.bytes > metricQueueBytes {
		t.Fatalf("queue=%d bytes=%d", len(b.queue), b.bytes)
	}
	latest, batch := b.snapshot()
	_, retry := b.snapshot()
	if len(batch) != metricBatchLimit || batch[0].SampleID != latest.SampleID || batch[1].SampleID != retry[1].SampleID {
		t.Fatal("retry IDs/latest changed")
	}
	ids := []string{batch[0].SampleID, batch[1].SampleID}
	b.ack(ids)
	b.ack(ids)
	if len(b.queue) != metricQueueLimit-2 {
		t.Fatal("ACK not idempotent")
	}
	_, next := b.snapshot()
	for _, sample := range next {
		for _, id := range ids {
			if sample.SampleID == id {
				t.Fatal("ACK sample resent")
			}
		}
	}
	// Large interface maps hit the byte bound before the row limit.
	large := map[string]uint64{}
	for i := 0; i < 1000; i++ {
		large[fmt.Sprintf("interface-%04d", i)] = uint64(i)
	}
	b = &metricBuffer{}
	for i := 0; i < 500; i++ {
		b.add(model.Metrics{CollectedAt: base, NetworkRXBytes: large})
	}
	if b.bytes > metricQueueBytes || len(b.queue) >= 500 {
		t.Fatalf("byte bound not enforced: %d/%d", b.bytes, len(b.queue))
	}
}
