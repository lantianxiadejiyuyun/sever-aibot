package api

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"
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
	if f.Kind != "port" {
		writeErr(w, http.StatusNotImplemented, "not_implemented", "当前里程碑仅支持 port 类型转发")
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
	if err := s.store.SetForwardState(r.Context(), id, forwardApplying, note); err != nil {
		s.storeErr(w, err)
		return
	}
	s.pushNodeConfig(r.Context(), f.NodeID)
	s.pushAgentConfig(r.Context(), f.DeviceID)

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
func (s *Server) pushNodeConfig(ctx context.Context, nodeID string) bool {
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
		if !activeForwardState(f.State) || f.Kind != "port" || f.RemotePort <= 0 {
			continue
		}
		payload = append(payload, model.TunnelForward{
			ID: f.ID, DeviceID: f.DeviceID, Name: f.Name, Kind: f.Kind, RemotePort: f.RemotePort,
		})
	}
	secret := base64.RawURLEncoding.EncodeToString(tunnel.Secret(s.cfg.MasterKey, nodeID))
	frame := model.AgentFrame{Type: "config", TunnelSecret: secret, Forwards: payload}
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
	forwards, err := s.store.ListForwards(ctx, deviceID, "", "")
	if err != nil {
		s.log.Warn("list device forwards", "device", deviceID, "err", err)
		return false
	}
	grouped := map[string][]model.TunnelForward{}
	for _, f := range forwards {
		if !activeForwardState(f.State) || f.Kind != "port" || f.NodeID == "" {
			continue
		}
		grouped[f.NodeID] = append(grouped[f.NodeID], model.TunnelForward{
			ID: f.ID, Name: f.Name, Kind: f.Kind, RemotePort: f.RemotePort,
			TargetIP: f.TargetIP, TargetPort: f.TargetPort,
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
			DeviceID: deviceID, NodeID: nodeID, Forwards: ids, ExpiresAt: expires,
		})
		if err != nil {
			s.log.Warn("sign tunnel ticket", "node", nodeID, "err", err)
			continue
		}
		nodes = append(nodes, model.TunnelNode{
			NodeID: nodeID, Addr: node.Addr, TLS: s.nodes.TLS(nodeID), Ticket: ticket, Forwards: list,
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
		if f.DeviceID == "" || seen[f.DeviceID] || !activeForwardState(f.State) {
			continue
		}
		seen[f.DeviceID] = true
		s.pushAgentConfig(ctx, f.DeviceID)
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
		if err := s.store.SetForwardState(ctx, f.ID, state, message); err != nil {
			s.log.Warn("save bind result", "forward", f.ID, "err", err)
			return
		}
		s.log.Info("forward bind result", "node", nodeID, "forward", f.ID, "ok", frame.OK, "message", message)
	case "traffic":
		if frame.ForwardID != "" && frame.TrafficBytes > 0 {
			if err := s.store.AddForwardTraffic(ctx, frame.ForwardID, frame.TrafficBytes); err != nil {
				s.log.Warn("add forward traffic", "forward", frame.ForwardID, "err", err)
			}
		}
	default:
		s.log.Debug("unhandled node frame", "node", nodeID, "type", frame.Type)
	}
}
