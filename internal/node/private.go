package node

import (
	"encoding/hex"
	"encoding/json"
	"io"
	"server-aibot/internal/model"
	"server-aibot/internal/tunnel"
	"strconv"
	"strings"
	"time"
)

type directRegistration struct {
	session    *deviceTunnel
	revision   int64
	candidates tunnel.DirectCandidates
}

// Private control messages and visitor streams share authenticated yamux
// sessions. Role, scope and exact revision are checked before every operation.
func (r *Runtime) acceptPrivateStreams(peer *deviceTunnel) {
	for {
		stream, err := peer.session.AcceptStream()
		if err != nil {
			return
		}
		go func() {
			defer stream.Close()
			_ = stream.SetDeadline(time.Now().Add(streamOpenTimeout))
			id, err := tunnel.ReadForwardID(stream)
			if err != nil {
				return
			}
			kind := ""
			revision := int64(-1)
			if strings.HasPrefix(id, "@") {
				parts := strings.SplitN(id, ":", 3)
				if len(parts) != 3 {
					return
				}
				kind = parts[0]
				revision, err = strconv.ParseInt(parts[1], 10, 64)
				if err != nil {
					return
				}
				id = parts[2]
			}
			r.mu.Lock()
			f, ok := r.forwards[id]
			current := r.sessions[peer.deviceID] == peer
			registration := r.candidates[id]
			r.mu.Unlock()
			if !ok || !current || !peer.allowed[id] || (f.Kind != "tunnel" && f.Kind != "p2p" && f.Kind != "proxy") {
				return
			}
			if kind != "" && revision != f.Revision {
				return
			}
			if kind == "@nat" {
				r.handleNAT(peer, f, stream)
				return
			}
			if kind == "@publish" || kind == "@lookup" {
				r.mu.Lock()
				allowed := !f.DirectDisabled && r.directAllowedLocked()
				r.mu.Unlock()
				if !allowed {
					return
				}
			}
			if kind == "@publish" {
				if f.Kind != "p2p" || f.DeviceID != peer.deviceID {
					return
				}
				var candidates tunnel.DirectCandidates
				if json.NewDecoder(io.LimitReader(stream, 8192)).Decode(&candidates) != nil || len(candidates.Addresses) == 0 || len(candidates.Addresses) > 16 {
					return
				}
				pin, err := hex.DecodeString(candidates.Fingerprint)
				if err != nil || len(pin) != 32 {
					return
				}
				for _, address := range candidates.Addresses {
					if tunnel.ValidateDirectAddress(address, address == f.DirectAddress) != nil {
						return
					}
				}
				r.mu.Lock()
				latest, exists := r.forwards[id]
				valid := exists && latest.Revision == revision && r.sessions[peer.deviceID] == peer
				if valid {
					r.candidates[id] = directRegistration{session: peer, revision: revision, candidates: candidates}
				}
				r.mu.Unlock()
				if valid {
					_ = tunnel.WriteResult(stream, tunnel.StatusOK, "")
				}
				return
			}
			if f.ClientID != peer.deviceID {
				return
			}
			switch kind {
			case "@lookup":
				r.mu.Lock()
				valid := registration.session != nil && r.sessions[f.DeviceID] == registration.session && registration.revision == f.Revision
				r.mu.Unlock()
				if !valid {
					_ = tunnel.WriteResult(stream, tunnel.StatusDialFailed, "direct candidates unavailable")
					return
				}
				if tunnel.WriteResult(stream, tunnel.StatusOK, "") == nil {
					_ = json.NewEncoder(stream).Encode(registration.candidates)
				}
			case "@ready", "@error":
				message := ""
				if kind == "@error" {
					message = "访问设备本机端口监听失败"
				}
				r.sendFrame(model.AgentFrame{Type: "bind_result", ForwardID: f.ID, Revision: f.Revision, OK: kind == "@ready", Message: message})
			case "@path":
				var path string
				if json.NewDecoder(io.LimitReader(stream, 128)).Decode(&path) != nil {
					return
				}
				if path != "direct" && path != "relay" && path != "unavailable" {
					return
				}
				r.sendFrame(model.AgentFrame{Type: "path_result", ForwardID: f.ID, Revision: f.Revision, Message: path, OK: true})
			case "@traffic":
				var report model.AgentFrame
				if json.NewDecoder(io.LimitReader(stream, 4096)).Decode(&report) != nil {
					return
				}
				if report.UploadBytes < 0 || report.DownloadBytes < 0 || report.UploadBytes > 1<<40 || report.DownloadBytes > 1<<40 {
					return
				}
				r.recordTraffic(f, report.UploadBytes, report.DownloadBytes, time.Now())
			case "@relay":
				if (f.Kind != "p2p" && f.Kind != "proxy") || (f.Kind == "p2p" && f.DisableRelay) {
					return
				}
				r.handleForwardEntry(stream, f, true)
			case "":
				if f.Kind == "p2p" && f.DisableRelay {
					return
				}
				_ = stream.SetDeadline(time.Time{})
				r.handleForwardEntry(stream, f)
			}
		}()
	}
}
