package node

import (
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"strconv"

	"github.com/hashicorp/yamux"
	"server-aibot/internal/model"
	"server-aibot/internal/tunnel"
)

func (r *Runtime) startRendezvous(address string) error {
	conn, err := net.ListenPacket("udp", address)
	if err != nil {
		return err
	}
	r.rendezvous = tunnel.NewRendezvous(conn)
	return nil
}

// Direct traffic cannot enforce central byte/concurrency reservations. Keep it
// on the relay whenever these controls are configured, including legacy TCP.
func (r *Runtime) directAllowedLocked() bool {
	return r.policy.LimitTotalGB <= 0 && r.policy.LimiterMbps <= 0 && r.policy.Concurrency <= 0 && r.policy.Connections <= 0
}
func (r *Runtime) handleNAT(peer *deviceTunnel, f model.TunnelForward, stream *yamux.Stream) {
	if r.rendezvous == nil || f.Kind != "p2p" || (peer.deviceID != f.DeviceID && peer.deviceID != f.ClientID) {
		return
	}
	provider := peer.deviceID == f.DeviceID
	valid := func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		latest, ok := r.forwards[f.ID]
		return ok && latest == f && r.sessions[peer.deviceID] == peer && !peer.session.IsClosed() && peer.allowed[f.ID] && !f.DirectDisabled && r.directAllowedLocked()
	}
	if !valid() {
		return
	}
	group := f.ID + ":" + strconv.FormatInt(f.Revision, 10)
	pin := ""
	if provider {
		var request tunnel.NATCredential
		if json.NewDecoder(io.LimitReader(stream, 1024)).Decode(&request) != nil {
			return
		}
		decoded, err := hex.DecodeString(request.Fingerprint)
		if err != nil || len(decoded) != 32 {
			return
		}
		pin = request.Fingerprint
	} else {
		pin = r.rendezvous.ProviderPin(group)
		if pin == "" {
			_ = tunnel.WriteResult(stream, tunnel.StatusDialFailed, "NAT provider unavailable")
			return
		}
	}
	credential, err := r.rendezvous.Issue(group, provider, pin, valid)
	if err != nil {
		return
	}
	if tunnel.WriteResult(stream, tunnel.StatusOK, "") == nil {
		_ = json.NewEncoder(stream).Encode(credential)
	}
}
