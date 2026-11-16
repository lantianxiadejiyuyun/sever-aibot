package agent

import (
	"os"
	"sync"
	"time"
)

// State is the observable connection state of one Client. It deliberately
// holds no credentials: callers may log or expose a Snapshot safely.
type State struct {
	mu sync.Mutex

	connected       bool
	lastConnectedAt time.Time
	lastHelloAckAt  time.Time
	lastFrameAt     time.Time
	reconnects      int
	lastError       string
}

// Snapshot is a point-in-time copy of State.
type Snapshot struct {
	Connected       bool      `json:"connected"`
	LastConnectedAt time.Time `json:"lastConnectedAt,omitempty"`
	LastHelloAckAt  time.Time `json:"lastHelloAckAt,omitempty"`
	LastFrameAt     time.Time `json:"lastFrameAt,omitempty"`
	Reconnects      int       `json:"reconnects"`
	LastError       string    `json:"lastError,omitempty"`
}

func (s *State) setConnected(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.connected = true
	s.lastConnectedAt = now
}

func (s *State) setDisconnected(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.connected = false
	if err != nil {
		s.lastError = err.Error()
	}
}

func (s *State) markHelloAck(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastHelloAckAt = now
}

func (s *State) markFrame(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastFrameAt = now
}

func (s *State) markReconnect() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reconnects++
}

func (s *State) snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Snapshot{
		Connected:       s.connected,
		LastConnectedAt: s.lastConnectedAt,
		LastHelloAckAt:  s.lastHelloAckAt,
		LastFrameAt:     s.lastFrameAt,
		Reconnects:      s.reconnects,
		LastError:       s.lastError,
	}
}

// EnsureStateDir creates the agent state directory with owner-only
// permissions and tightens an existing directory as well.
//
// Only non-secret runtime state belongs in this directory. Device keys and
// session tokens must never be written to disk by the agent.
func EnsureStateDir(path string) error {
	if path == "" {
		return os.ErrInvalid
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	// MkdirAll does not change the mode of an existing directory. On Windows
	// Chmod only toggles the read-only bit, which is harmless here.
	return os.Chmod(path, 0o700)
}
