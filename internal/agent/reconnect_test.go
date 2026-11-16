package agent

import (
	"testing"
	"time"
)

func TestBackoff(t *testing.T) {
	c := &Client{
		reconnectBase: time.Second,
		reconnectMax:  60 * time.Second,
		jitter:        func(d time.Duration) time.Duration { return d },
	}
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{0, time.Second},
		{1, 2 * time.Second},
		{2, 4 * time.Second},
		{5, 32 * time.Second},
		{6, 60 * time.Second},
		{20, 60 * time.Second},
	}
	for _, tc := range cases {
		if got := c.backoff(tc.attempt); got != tc.want {
			t.Errorf("backoff(%d) = %v, want %v", tc.attempt, got, tc.want)
		}
	}
}

func TestFullJitterBounds(t *testing.T) {
	for _, bound := range []time.Duration{time.Second, 2 * time.Second, 60 * time.Second} {
		for i := 0; i < 200; i++ {
			got := fullJitter(bound)
			if got < 0 || got > bound {
				t.Fatalf("fullJitter(%v) = %v, out of range", bound, got)
			}
		}
	}
	if got := fullJitter(0); got != 0 {
		t.Fatalf("fullJitter(0) = %v, want 0", got)
	}
}
