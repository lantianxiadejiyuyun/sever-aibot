package agent

import (
	"context"
	"math/rand/v2"
	"time"
)

const (
	defaultReconnectBase = time.Second
	defaultReconnectMax  = 60 * time.Second
)

// backoff returns a delay in [0, current] where current doubles from base and
// is capped at max. This is "full jitter": it spreads reconnect storms while
// still converging on the cap.
func (c *Client) backoff(attempt int) time.Duration {
	current := c.reconnectBase
	for i := 0; i < attempt && current < c.reconnectMax; i++ {
		current *= 2
	}
	if current > c.reconnectMax {
		current = c.reconnectMax
	}
	if current <= 0 {
		return 0
	}
	return c.jitter(current)
}

func fullJitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(d) + 1))
}

// sleepCtx waits for d or until ctx is done.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
