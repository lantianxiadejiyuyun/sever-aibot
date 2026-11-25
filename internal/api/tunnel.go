package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"server-aibot/internal/model"
	"server-aibot/internal/tunnel"
)

// Forward states used by the node-relay data plane. "applying" means the
// desired config was pushed but the node has not reported a bind result yet;
// it is persisted so a control-plane restart converges automatically.
const (
	forwardApplying = "applying"
	forwardApplied  = "applied"
	forwardObserved = "observed"
	forwardError    = "error"
)

func activeForwardState(state string) bool {
	switch state {
	case forwardApplying, forwardApplied, forwardObserved:
		return true
	}
	return false
}

// applyForwardConfig marks one forward for delivery and pushes the current
// config to both the node and the agent. Bind results arrive asynchronously
// and move the state to applied or error.
func (s *Server) applyForwardConfig(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	f, err := s.store.GetForward(r.Context(), id)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if f.Kind != "port" && f.Kind != "host" && f.Kind != "tunnel" && f.Kind != "p2p" && f.Kind != "proxy" {
		writeErr(w, http.StatusNotImplemented, "not_implemented", "该类型尚未实现真实数据通道；P2P NAT 直连不可用")
		return
	}
	if f.DeviceID == "" {
		writeErr(w, http.StatusBadRequest, "invalid_argument", "转发必须绑定内网客户端设备")
		return
	}
	node, err := s.store.GetNode(r.Context(), f.NodeID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_argument", "节点不存在")
		return
	}

	if !validProtocol(node.Protocol) {
		writeErr(w, 501, "transport_unavailable", "节点接入协议尚未实现")
		return
	}
	if strings.TrimSpace(node.Addr) == "" {
		writeErr(w, http.StatusBadRequest, "invalid_argument", "节点未配置内网客户端接入地址 addr")
		return
	}
	if !nodeFeature(node, f.Kind) {
		writeErr(w, http.StatusBadRequest, "invalid_argument", "节点未启用端口转发能力")
		return
	}
	if _, err := s.store.GetDevice(r.Context(), f.DeviceID); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_argument", "客户端设备不存在")
		return
	}

	notes := []string{}
	if !s.nodes.Online(f.NodeID) {
		notes = append(notes, "节点离线，等待节点连接后自动下发")
	}
	if !s.hub.Online(f.DeviceID) {
		notes = append(notes, "客户端离线，等待 Agent 连接后自动下发")
	}
	note := strings.Join(notes, "；")

	if err := s.validateForward(r, f); err != nil {
		writeErr(w, 400, "invalid_argument", err.Error())
		return
	}
	updatedCommand, err := s.store.CommandForwardChecked(r.Context(), id, true, f.Revision)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if note != "" {
		_ = s.store.AckForward(r.Context(), f.NodeID, id, updatedCommand.Revision, forwardApplying, note)
	}
	s.pushNodeConfig(r.Context(), f.NodeID)
	s.pushAgentConfig(r.Context(), f.DeviceID)
	s.pushAgentConfig(r.Context(), f.ClientID)

	updated, err := s.store.GetForward(r.Context(), id)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "forward.apply", id, map[string]any{
		"nodeOnline":  s.nodes.Online(f.NodeID),
		"agentOnline": s.hub.Online(f.DeviceID),
	})
	writeJSON(w, http.StatusOK, updated)
}

// pushNodeConfig sends the node's complete listener set: every active port
// forward assigned to it. A node that reconnects therefore converges without
// replaying historical commands.
var nodeConfigMu sync.Mutex

func (s *Server) pushNodeConfig(ctx context.Context, nodeID string) bool {
	nodeConfigMu.Lock()
	defer nodeConfigMu.Unlock()
	if nodeID == "" || !s.nodes.Online(nodeID) {
		return false
	}
	forwards, err := s.store.ListForwards(ctx, "", nodeID, "")
	if err != nil {
		s.log.Warn("list node forwards", "node", nodeID, "err", err)
		return false
	}
	payload := make([]model.TunnelForward, 0, len(forwards))
	for _, f := range forwards {
		if (!activeForwardState(f.State) && f.State != "stopping" && f.State != "stopped") || (f.Kind != "port" && f.Kind != "host" && f.Kind != "tunnel" && f.Kind != "p2p" && f.Kind != "proxy") {
			continue
		}
		payload = append(payload, model.TunnelForward{
			ID: f.ID, DeviceID: f.DeviceID, ClientID: f.ClientID, DirectAddress: directOptions(f.Options).DirectAddress, DisableRelay: !directOptions(f.Options).relayAllowed(), Name: f.Name, Kind: f.Kind, RemotePort: f.RemotePort,
			Revision: f.Revision, Disabled: f.State == "stopping" || f.State == "stopped", Protocol: forwardProtocol(f.Options), Domain: f.Domain,
		})
	}
	secret := base64.RawURLEncoding.EncodeToString(tunnel.Secret(s.cfg.MasterKey, nodeID))
	node, err := s.store.GetNode(ctx, nodeID)
	if err != nil {
		return false
	}
	revision, err := s.store.NextNodeConfigRevision(ctx, nodeID)
	if err != nil {
		return false
	}
	for i := range payload {
		if payload[i].Kind == "p2p" {
			payload[i].DisableRelay = payload[i].DisableRelay || node.P2PDisableForward
		}
	}
	revocations, err := s.store.DeviceTunnelRevocations(ctx)
	if err != nil {
		return false
	}
	frame := model.AgentFrame{Type: "config", Revision: revision, TunnelSecret: secret, Forwards: payload, NodePolicy: &model.NodeRuntimePolicy{LimitTotalGB: node.LimitTotalGB, LimitKind: node.LimitKind, LimitResetDay: node.LimitResetDay, RevokedBefore: revocations, HTTPPort: node.HTTPPort, ForwardPorts: node.ForwardPorts, LimiterMbps: node.LimiterMbps, Concurrency: node.Concurrency, Connections: node.Connections, MaxPoolCount: node.MaxPoolCount}}
	if err := s.nodes.Send(nodeID, frame); err != nil {
		s.log.Warn("push node config", "node", nodeID, "err", err)
		return false
	}
	return true
}

// pushAgentConfig sends every active forward grouped by node, with a fresh
// signed ticket per node. Nodes may be offline: the agent keeps retrying.
func (s *Server) pushAgentConfig(ctx context.Context, deviceID string) bool {
	if deviceID == "" || !s.hub.Online(deviceID) {
		return false
	}
	forwards, err := s.store.ListForwards(ctx, "", "", "")
	if err != nil {
		s.log.Warn("list device forwards", "device", deviceID, "err", err)
		return false
	}
	grouped := map[string][]model.TunnelForward{}
	for _, f := range forwards {
		if f.DeviceID != deviceID && f.ClientID != deviceID {
			continue
		}
		if !activeForwardState(f.State) || (f.Kind != "port" && f.Kind != "host" && f.Kind != "tunnel" && f.Kind != "p2p" && f.Kind != "proxy") || f.NodeID == "" {
			continue
		}
		grouped[f.NodeID] = append(grouped[f.NodeID], model.TunnelForward{
			ID: f.ID, Name: f.Name, Kind: f.Kind, RemotePort: f.RemotePort, ClientID: f.ClientID, Visitor: f.ClientID == deviceID, DirectAddress: directOptions(f.Options).DirectAddress, DirectListen: directOptions(f.Options).DirectListen, DisableRelay: !directOptions(f.Options).relayAllowed(),
			TargetIP: f.TargetIP, TargetPort: f.TargetPort, Revision: f.Revision, Protocol: forwardProtocol(f.Options), Domain: f.Domain,
		})
	}
	expires := time.Now().Add(tunnel.TicketLifetime).Unix()
	nodes := make([]model.TunnelNode, 0, len(grouped))
	for nodeID, list := range grouped {
		node, err := s.store.GetNode(ctx, nodeID)
		if err != nil {
			s.log.Warn("load forward node", "node", nodeID, "err", err)
			continue
		}
		if strings.TrimSpace(node.Addr) == "" {
			s.log.Warn("node has no tunnel address", "node", nodeID)
			continue
		}
		ids := make([]string, 0, len(list))
		for _, f := range list {
			ids = append(ids, f.ID)
		}
		ticket, err := tunnel.Sign(tunnel.Secret(s.cfg.MasterKey, nodeID), tunnel.Ticket{
			DeviceID: deviceID, NodeID: nodeID, Forwards: ids, ExpiresAt: expires, IssuedAt: time.Now().UnixNano(),
		})
		if err != nil {
			s.log.Warn("sign tunnel ticket", "node", nodeID, "err", err)
			continue
		}
		for i := range list {
			if list[i].Kind == "p2p" {
				list[i].DisableRelay = list[i].DisableRelay || node.P2PDisableForward
				list[i].RendezvousAddress = node.RendezvousAddress
				list[i].DirectDisabled = node.LimitTotalGB > 0 || node.LimiterMbps > 0 || node.Concurrency > 0 || node.Connections > 0
				mac := hmac.New(sha256.New, s.cfg.MasterKey)
				fmt.Fprintf(mac, "aibot-direct/v1/%s/%d", list[i].ID, list[i].Revision)
				list[i].P2PKey = base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
			}
		}
		nodes = append(nodes, model.TunnelNode{
			NodeID: nodeID, Addr: nodeTunnelAddress(node), TLS: s.nodes.TLS(nodeID), Ticket: ticket, Forwards: list,
		})
	}
	if err := s.hub.Send(deviceID, model.AgentFrame{Type: "config", Nodes: nodes}); err != nil {
		s.log.Warn("push agent config", "device", deviceID, "err", err)
		return false
	}
	return true
}

// pushNodeForwardsToAgents refreshes agent-side node addresses and tickets
// after a node edit.
func (s *Server) pushNodeForwardsToAgents(ctx context.Context, nodeID string) {
	if nodeID == "" {
		return
	}
	forwards, err := s.store.ListForwards(ctx, "", nodeID, "")
	if err != nil {
		return
	}
	seen := map[string]bool{}
	for _, f := range forwards {
		if !activeForwardState(f.State) {
			continue
		}
		for _, deviceID := range []string{f.DeviceID, f.ClientID} {
			if deviceID != "" && !seen[deviceID] {
				seen[deviceID] = true
				s.pushAgentConfig(ctx, deviceID)
			}
		}
	}

}

// handleNodeFrame processes asynchronous node results. The node ID is checked
// against the forward row so one node cannot alter another node's state.
func (s *Server) handleNodeFrame(nodeID string, frame model.AgentFrame) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	switch frame.Type {
	case "bind_result":
		if frame.ForwardID == "" {
			return
		}
		f, err := s.store.GetForward(ctx, frame.ForwardID)
		if err != nil || f.NodeID != nodeID {
			s.log.Warn("bind result for unknown forward", "node", nodeID, "forward", frame.ForwardID)
			return
		}
		state := forwardApplied
		message := ""
		if !frame.OK {
			state = forwardError
			message = frame.Message
			if message == "" {
				message = "节点监听入口端口失败"
			}
		}
		if err := s.store.AckForward(ctx, nodeID, f.ID, frame.Revision, state, message); err != nil {
			s.log.Warn("save bind result", "forward", f.ID, "err", err)
			return
		}
		s.log.Info("forward bind result", "node", nodeID, "forward", f.ID, "ok", frame.OK, "message", message)
	case "path_result":
		if frame.OK {
			_ = s.store.RecordForwardPath(ctx, nodeID, frame.ForwardID, frame.Revision, frame.Message)
		}
	case "revoke_result":
		if frame.OK {
			_ = s.store.AckForward(ctx, nodeID, frame.ForwardID, frame.Revision, "stopped", "")
			if f, err := s.store.GetForward(ctx, frame.ForwardID); err == nil && f.NodeID == nodeID && f.Revision == frame.Revision && f.State == "stopped" {
				s.pushAgentConfig(ctx, f.DeviceID)
				s.pushAgentConfig(ctx, f.ClientID)
			}
		} else {
			_ = s.store.AckForward(ctx, nodeID, frame.ForwardID, frame.Revision, "stopping", frame.Message)
		}
	case "quota_request":
		granted, expires, err := s.store.ReserveNodeQuota(ctx, nodeID, frame.Ref, frame.TrafficBytes)
		_ = s.nodes.Send(nodeID, model.AgentFrame{Type: "quota_ack", Ref: frame.Ref, OK: err == nil, TrafficBytes: granted, TS: expires})
	case "traffic":
		if err := s.store.RecordForwardTraffic(ctx, nodeID, frame); err != nil {
			s.log.Warn("save traffic report", "forward", frame.ForwardID, "err", err)
		} else {
			_ = s.nodes.Send(nodeID, model.AgentFrame{Type: "traffic_ack", Ref: frame.Ref, OK: true})
		}

	default:
		s.log.Debug("unhandled node frame", "node", nodeID, "type", frame.Type)
	}
}

// MountForwardRuntime is mounted inside the authenticated API router.
func (s *Server) MountForwardRuntime(r chi.Router) {
	r.Get("/nodes/{id}/quota", s.nodeQuota)
	r.Post("/forwards/{id}/start", s.applyForwardConfig)
	r.Post("/forwards/{id}/stop", s.stopForward)
	r.Get("/forwards/{id}/runtime", s.forwardRuntime)
	r.Get("/forwards/{id}/traffic", s.forwardTraffic)
	r.Get("/traffic", s.allTraffic)
}
func (s *Server) stopForward(w http.ResponseWriter, r *http.Request) {
	f, err := s.store.CommandForward(r.Context(), chi.URLParam(r, "id"), false)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	s.pushNodeConfig(r.Context(), f.NodeID)
	if !s.nodes.Online(f.NodeID) {
		s.pushAgentConfig(r.Context(), f.DeviceID)
		s.pushAgentConfig(r.Context(), f.ClientID)
	}
	s.audit(r, "forward.stop", f.ID, nil)
	writeJSON(w, http.StatusAccepted, f)
}
func (s *Server) forwardRuntime(w http.ResponseWriter, r *http.Request) {
	f, err := s.store.GetForward(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	node, nodeErr := s.store.GetNode(r.Context(), f.NodeID)
	natEnabled := nodeErr == nil && f.Kind == "p2p" && node.RendezvousAddress != "" && node.LimitTotalGB == 0 && node.LimiterMbps == 0 && node.Concurrency == 0 && node.Connections == 0
	writeJSON(w, 200, map[string]any{"forwardId": f.ID, "revision": f.Revision, "state": f.State, "lastError": f.LastError, "nodeOnline": s.nodes.Online(f.NodeID), "agentOnline": s.hub.Online(f.DeviceID), "p2pStatus": s.store.ForwardPath(r.Context(), f.ID, f.Revision), "automaticNATHolePunching": natEnabled, "trafficCollection": "node_memory_retry_until_ack", "trafficBytes": f.TrafficBytes})
}
func (s *Server) forwardTraffic(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := s.store.GetForward(r.Context(), id); err != nil {
		s.storeErr(w, err)
		return
	}
	days := queryInt(r, "days", 30)
	if days < 1 || days > 366 {
		writeErr(w, 400, "invalid_argument", "days 必须在 1-366 之间")
		return
	}
	items, err := s.store.ForwardTrafficDays(r.Context(), id, days)
	if err != nil {
		s.internal(w, "读取流量失败", err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items, "timezone": "UTC", "uploadDirection": "public_to_target", "downloadDirection": "target_to_public"})
}
func forwardProtocol(options string) string {
	var o struct {
		Protocol string `json:"protocol"`
	}
	_ = json.Unmarshal([]byte(options), &o)
	if o.Protocol == "" {
		return "tcp"
	}
	return o.Protocol
}

func (s *Server) allTraffic(w http.ResponseWriter, r *http.Request) {
	days := queryInt(r, "days", 7)
	if days < 1 || days > 366 {
		writeErr(w, 400, "invalid_argument", "days 必须在 1-366 之间")
		return
	}
	items, err := s.store.AllTrafficDays(r.Context(), days)
	if err != nil {
		s.internal(w, "读取流量失败", err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items, "timezone": "UTC", "uploadDirection": "public_to_target", "downloadDirection": "target_to_public"})
}

type directForwardOptions struct {
	Relay         *bool  `json:"relay"`
	DirectAddress string `json:"directAddress"`
	DirectListen  string `json:"directListen"`
}

// Omitted relay preserves the historical node-only fallback policy.
func (o directForwardOptions) relayAllowed() bool { return o.Relay == nil || *o.Relay }

func directOptions(raw string) directForwardOptions {
	var o directForwardOptions
	_ = json.Unmarshal([]byte(raw), &o)
	return o
}
