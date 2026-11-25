package node

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"server-aibot/internal/model"
)

func (r *Runtime) AckQuota(frame model.AgentFrame) {
	r.quotaMu.Lock()
	ch := r.quotaWait[frame.Ref]
	if ch != nil {
		select {
		case ch <- frame:
		default:
		}
	}
	r.quotaMu.Unlock()
}

// reserveQuota obtains a durable grant before writing any limited payload.
// Unique requests are not reused after timeout, and replies are consumed once.
func (r *Runtime) reserveQuota(ctx context.Context, requested int) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	r.mu.Lock()
	limited := r.policy.LimitTotalGB > 0
	r.mu.Unlock()
	if !limited || requested == 0 {
		return requested, ctx.Err()
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return 0, err
	}
	ref := hex.EncodeToString(id[:])
	ch := make(chan model.AgentFrame, 1)
	r.quotaMu.Lock()
	if r.quotaWait == nil {
		r.quotaWait = make(map[string]chan model.AgentFrame)
	}
	r.quotaWait[ref] = ch
	r.quotaMu.Unlock()
	defer func() { r.quotaMu.Lock(); delete(r.quotaWait, ref); r.quotaMu.Unlock() }()
	r.sendFrame(model.AgentFrame{Type: "quota_request", Ref: ref, TrafficBytes: int64(requested)})
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-timer.C:
		return 0, errors.New("quota reservation timed out")
	case frame := <-ch:
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if !frame.OK || frame.TrafficBytes <= 0 || frame.TrafficBytes > int64(requested) || frame.TS <= time.Now().Unix() {
			return 0, errors.New("quota exhausted or grant expired")
		}
		return int(frame.TrafficBytes), nil
	}
}
