package node

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"server-aibot/internal/model"
	"sync"
	"sync/atomic"
	"time"
)

func (r *Runtime) AckTraffic(ref string) {
	r.reportMu.Lock()
	delete(r.reports, ref)
	r.reportMu.Unlock()
}
func (r *Runtime) retryTraffic() {
	r.reportMu.Lock()
	frames := make([]model.AgentFrame, 0, len(r.reports))
	for _, f := range r.reports {
		frames = append(frames, f)
	}
	r.reportMu.Unlock()
	for _, f := range frames {
		r.sendFrame(f)
	}
}
func (r *Runtime) recordTraffic(f model.TunnelForward, up, down int64, at time.Time) {
	if up+down == 0 {
		return
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return
	}
	frame := model.AgentFrame{Type: "traffic", ForwardID: f.ID, Revision: f.Revision, UploadBytes: up, DownloadBytes: down, TrafficBytes: up + down, Ref: hex.EncodeToString(id[:]), TS: at.Unix()}
	r.reportMu.Lock()
	r.reports[frame.Ref] = frame
	r.reportMu.Unlock()
	r.sendFrame(frame)
}

// copyTraffic counts only successfully written application bytes, after stream
// negotiation. The node-global pacer applies to both upload and download.
func (r *Runtime) copyTraffic(ctx context.Context, dst io.Writer, src io.Reader, count *atomic.Int64, datagram ...bool) {
	buf := make([]byte, 65535)
	for {
		n, err := src.Read(buf)
		if n > 0 || (err == nil && n == 0) {
			r.mu.Lock()
			limit := r.policy.LimiterMbps
			r.mu.Unlock()
			if r.pacer.wait(ctx, n, limit) != nil {
				return
			}
			granted, quotaErr := r.reserveQuota(ctx, n)
			if quotaErr != nil || (len(datagram) > 0 && datagram[0] && granted != n) {
				return
			}
			written, e := dst.Write(buf[:granted])
			count.Add(int64(written))
			if e != nil || written != n {
				return
			}
		}
		if err != nil {
			return
		}
	}
}
func (r *Runtime) pipeTraffic(ctx context.Context, f model.TunnelForward, a, b io.ReadWriteCloser) {
	var up, down atomic.Int64
	done := make(chan struct{})
	var workers sync.WaitGroup
	workers.Add(2)
	go func() { defer workers.Done(); r.copyTraffic(ctx, b, a, &up, f.Protocol == "udp"); a.Close(); b.Close() }()
	go func() {
		defer workers.Done()
		r.copyTraffic(ctx, a, b, &down, f.Protocol == "udp")
		a.Close()
		b.Close()
	}()
	go func() { workers.Wait(); close(done) }()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	// Flush every second so long-lived connections are observed immediately and
	// daily attribution has at most a one-second boundary uncertainty.
	for {
		select {
		case <-ticker.C:
			r.recordTraffic(f, up.Swap(0), down.Swap(0), time.Now())
		case <-done:
			r.recordTraffic(f, up.Swap(0), down.Swap(0), time.Now())
			return
		}
	}
}
