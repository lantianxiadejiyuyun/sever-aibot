// Package tunnel defines the wire protocol and credentials used by the
// node-relay data plane: an intranet agent dials out to a public node, proves
// it may serve a set of forwards, and then multiplexes target connections over
// one yamux session.
package tunnel

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// TicketLifetime bounds how long an agent may use a pushed ticket. Sessions
// may outlive it; reconnects request a fresh config from the control plane.
const TicketLifetime = 24 * time.Hour

var (
	// ErrTicket is wrapped by every ticket validation failure.
	ErrTicket = errors.New("tunnel: invalid ticket")
)

// Ticket authorizes one device to tunnel a fixed set of forwards through one
// node. It is signed with a secret derived from the control-plane master key
// and delivered to the node over its authenticated control socket.
type Ticket struct {
	IssuedAt  int64    `json:"issuedAt,omitempty"`
	DeviceID  string   `json:"deviceId"`
	NodeID    string   `json:"nodeId"`
	Forwards  []string `json:"forwards"`
	ExpiresAt int64    `json:"expiresAt"`
}

// Secret derives the per-node tunnel signing key. The control plane derives
// it on demand; the node receives it over its authenticated WebSocket and
// keeps it in memory only.
func Secret(masterKey []byte, nodeID string) []byte {
	mac := hmac.New(sha256.New, masterKey)
	mac.Write([]byte("server-aibot/node-tunnel/v1\x00"))
	mac.Write([]byte(nodeID))
	return mac.Sum(nil)
}

// Sign encodes and signs a ticket.
func Sign(secret []byte, t Ticket) (string, error) {
	if len(secret) == 0 {
		return "", errors.New("tunnel: empty signing secret")
	}
	payload, err := json.Marshal(t)
	if err != nil {
		return "", err
	}
	body := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(body))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return body + "." + sig, nil
}

// Verify checks the signature and expiry, and returns the claims.
func Verify(secret []byte, token string, now time.Time) (Ticket, error) {
	var t Ticket
	if len(secret) == 0 {
		return t, fmt.Errorf("%w: no signing secret", ErrTicket)
	}
	body, sig, ok := splitToken(token)
	if !ok {
		return t, fmt.Errorf("%w: malformed", ErrTicket)
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(body))
	want := mac.Sum(nil)
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(want, got) {
		return t, fmt.Errorf("%w: signature mismatch", ErrTicket)
	}
	payload, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return t, fmt.Errorf("%w: payload encoding", ErrTicket)
	}
	if err := json.Unmarshal(payload, &t); err != nil {
		return t, fmt.Errorf("%w: payload JSON", ErrTicket)
	}
	if t.DeviceID == "" || t.NodeID == "" || len(t.Forwards) == 0 {
		return t, fmt.Errorf("%w: missing claims", ErrTicket)
	}
	if t.ExpiresAt <= now.Unix() {
		return t, fmt.Errorf("%w: expired", ErrTicket)
	}
	return t, nil
}

func splitToken(token string) (body, sig string, ok bool) {
	for i := 0; i < len(token); i++ {
		if token[i] == '.' {
			if i == 0 || i == len(token)-1 {
				return "", "", false
			}
			return token[:i], token[i+1:], true
		}
	}
	return "", "", false
}
