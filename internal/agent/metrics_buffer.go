package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	"server-aibot/internal/model"
)

const (
	metricQueueLimit = 1440
	metricQueueBytes = 4 << 20
	metricBatchLimit = 30
	metricBatchBytes = 256 << 10
)

type queuedMetric struct {
	sample model.Metrics
	size   int
}

// metricBuffer is a bounded in-memory outbox. It survives websocket reconnects,
// not process restarts. Oldest samples are dropped on overflow or after 24h.
// Samples are immutable once enqueued; only explicit ACK IDs remove them.
type metricBuffer struct {
	mu     sync.Mutex
	queue  []queuedMetric
	bytes  int
	latest *model.Metrics
}

func (b *metricBuffer) add(m model.Metrics) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return
	}
	m.SampleID = hex.EncodeToString(id[:])
	body, err := json.Marshal(m)
	if err != nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.latest = &m
	if len(body) > 64<<10 {
		return
	}
	for len(b.queue) > 0 && (len(b.queue) >= metricQueueLimit || b.bytes+len(body) > metricQueueBytes || b.queue[0].sample.CollectedAt.Before(m.CollectedAt.Add(-24*time.Hour))) {
		b.bytes -= b.queue[0].size
		b.queue[0] = queuedMetric{}
		b.queue = b.queue[1:]
	}
	b.queue = append(b.queue, queuedMetric{m, len(body)})
	b.bytes += len(body)
}

func (b *metricBuffer) snapshot() (*model.Metrics, []model.Metrics) {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]model.Metrics, 0, metricBatchLimit)
	size := 0
	// Include current data first so a large backlog never hides the current state.
	if len(b.queue) > 0 {
		last := b.queue[len(b.queue)-1]
		out = append(out, last.sample)
		size = last.size
	}
	for i := 0; i < len(b.queue)-1 && len(out) < metricBatchLimit; i++ {
		next := b.queue[i]
		if size+next.size > metricBatchBytes {
			break
		}
		out = append(out, next.sample)
		size += next.size
	}
	return b.latest, out
}

func (b *metricBuffer) ack(ids []string) {
	if len(ids) > metricBatchLimit {
		return
	}
	accepted := make(map[string]bool, len(ids))
	for _, id := range ids {
		accepted[id] = true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	old := b.queue
	keep := old[:0]
	for _, entry := range old {
		if accepted[entry.sample.SampleID] {
			b.bytes -= entry.size
		} else {
			keep = append(keep, entry)
		}
	}
	clear(old[len(keep):])
	b.queue = keep
}

func (b *metricBuffer) run(ctx context.Context, ready chan<- struct{}) {
	collector := newMetricsCollector()
	b.add(*collector.collect(time.Now()))
	close(ready)
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			b.add(*collector.collect(now))
		}
	}
}
