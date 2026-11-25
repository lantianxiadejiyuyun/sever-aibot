package node

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"server-aibot/internal/model"
)

func TestCopyTrafficSharesQuotaBeforeWrites(t *testing.T) {
	r := NewRuntime("n", "", "", nil, false, slog.New(slog.NewTextHandler(io.Discard, nil)))
	r.policy.LimitTotalGB = 1
	var mu sync.Mutex
	remaining := int64(1000)
	r.SetSender(func(f model.AgentFrame) error {
		if f.Type == "quota_request" {
			mu.Lock()
			g := min(remaining, f.TrafficBytes)
			remaining -= g
			mu.Unlock()
			r.AckQuota(model.AgentFrame{Ref: f.Ref, OK: true, TrafficBytes: g, TS: time.Now().Add(time.Minute).Unix()})
		}
		return nil
	})
	var total atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var count atomic.Int64
			var dst bytes.Buffer
			r.copyTraffic(context.Background(), &dst, bytes.NewReader(make([]byte, 73)), &count)
			if int64(dst.Len()) != count.Load() {
				t.Error("count mismatch")
			}
			total.Add(count.Load())
		}()
	}
	wg.Wait()
	if total.Load() != 1000 {
		t.Fatalf("wrote %d bytes", total.Load())
	}
}

func TestCopyTrafficQuotaRejectsWithoutWrite(t *testing.T) {
	for _, tc := range []struct {
		name     string
		frame    model.AgentFrame
		datagram bool
	}{
		{name: "denied", frame: model.AgentFrame{OK: true, TS: time.Now().Add(time.Minute).Unix()}},
		{name: "expired", frame: model.AgentFrame{OK: true, TrafficBytes: 1, TS: 1}},
		{name: "partial UDP", frame: model.AgentFrame{OK: true, TrafficBytes: 1, TS: time.Now().Add(time.Minute).Unix()}, datagram: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRuntime("n", "", "", nil, false, slog.Default())
			r.policy.LimitTotalGB = 1
			r.SetSender(func(f model.AgentFrame) error { reply := tc.frame; reply.Ref = f.Ref; r.AckQuota(reply); return nil })
			var dst bytes.Buffer
			var count atomic.Int64
			r.copyTraffic(context.Background(), &dst, bytes.NewBufferString("payload"), &count, tc.datagram)
			if dst.Len() != 0 {
				t.Fatal("wrote ungranted bytes")
			}
		})
	}
	r := NewRuntime("n", "", "", nil, false, slog.Default())
	r.policy.LimitTotalGB = 1
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.reserveQuota(ctx, 10); err == nil {
		t.Fatal("offline cancellation allowed write")
	}
}
