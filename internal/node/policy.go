package node

import (
	"context"
	"net"
	"server-aibot/internal/model"
	"strconv"
	"strings"
	"sync"
	"time"
)

func allowedPort(spec string, port int) bool {
	if spec == "" {
		return true
	}
	for _, part := range strings.Split(spec, ",") {
		lo, hi, rangeOK := strings.Cut(strings.TrimSpace(part), "-")
		a, e := strconv.Atoi(strings.TrimSpace(lo))
		if e != nil {
			continue
		}
		if !rangeOK && a == port {
			return true
		}
		b, e := strconv.Atoi(strings.TrimSpace(hi))
		if rangeOK && e == nil && port >= a && port <= b {
			return true
		}
	}
	return false
}

type activeEntry struct {
	forward model.TunnelForward
	conn    net.Conn
	cancel  context.CancelFunc
}

// bytePacer shares one aggregate bandwidth budget across both directions and
// all forwards. Cancellation interrupts outstanding reservations on revocation.
type bytePacer struct {
	mu   sync.Mutex
	next time.Time
}

func (p *bytePacer) wait(ctx context.Context, n, mbps int) error {
	if mbps <= 0 {
		return ctx.Err()
	}
	p.mu.Lock()
	now := time.Now()
	if p.next.Before(now) {
		p.next = now
	}
	due := p.next
	p.next = p.next.Add(time.Duration(float64(n) * 8 / float64(mbps*1000000) * float64(time.Second)))
	p.mu.Unlock()
	timer := time.NewTimer(time.Until(due))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
