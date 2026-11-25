package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"server-aibot/internal/tunnel"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"server-aibot/internal/model"
	"server-aibot/internal/store"
)

// ---------- overview ----------

func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	counts, err := s.store.Counts(r.Context())
	if err != nil {
		s.internal(w, "读取概览失败", err)
		return
	}
	audit, _ := s.store.ListAudit(r.Context(), 8)
	writeJSON(w, http.StatusOK, map[string]any{
		"counts":      counts,
		"recentAudit": audit,
	})
}

func (s *Server) listAudit(w http.ResponseWriter, r *http.Request) {
	limit := queryInt(r, "limit", 100)
	logs, err := s.store.ListAudit(r.Context(), limit)
	if err != nil {
		s.internal(w, "读取审计日志失败", err)
		return
	}
	writeJSON(w, http.StatusOK, page(logs))
}

// ---------- devices ----------

type deviceRequest struct {
	Name   string `json:"name"`
	Remark string `json:"remark"`
	Tags   string `json:"tags"`
}

func (s *Server) listDevices(w http.ResponseWriter, r *http.Request) {
	devices, err := s.store.ListDevices(r.Context())
	if err != nil {
		s.internal(w, "读取设备失败", err)
		return
	}
	writeJSON(w, http.StatusOK, page(devices))
}

func (s *Server) getDevice(w http.ResponseWriter, r *http.Request) {
	d, err := s.store.GetDevice(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) createDevice(w http.ResponseWriter, r *http.Request) {
	var req deviceRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeErr(w, http.StatusBadRequest, "invalid_argument", "设备名称不能为空")
		return
	}
	v, ok := s.newVault(w)
	if !ok {
		return
	}
	device, key, err := s.store.CreateDevice(r.Context(), req.Name, req.Remark, req.Tags, v)
	if err != nil {
		s.internal(w, "创建设备失败", err)
		return
	}
	s.audit(r, "device.create", device.ID, map[string]string{"name": device.Name})
	writeJSON(w, http.StatusCreated, map[string]any{"device": device, "deviceKey": key})
}

func (s *Server) updateDevice(w http.ResponseWriter, r *http.Request) {
	var req deviceRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	id := chi.URLParam(r, "id")
	p := store.DevicePatch{}
	if req.Name != "" {
		p.Name = &req.Name
	}
	p.Remark = &req.Remark
	p.Tags = &req.Tags
	d, err := s.store.UpdateDevice(r.Context(), id, p)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "device.update", id, nil)
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) deleteDevice(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if s.hub.Online(id) {
		writeErr(w, http.StatusConflict, "device_online", "设备在线，请先停止 Agent 再删除")
		return
	}
	all, err := s.store.ListForwards(r.Context(), "", "", "")
	if err != nil {
		s.storeErr(w, err)
		return
	}
	forwards := []model.Forward{}
	for _, f := range all {
		if f.DeviceID == id || f.ClientID == id {
			forwards = append(forwards, f)
		}
	}
	for _, f := range forwards {
		if activeForwardState(f.State) {
			_ = s.store.SetForwardState(r.Context(), f.ID, forwardError, "设备已删除")
		}
	}
	if err := s.store.DeleteDevice(r.Context(), id); err != nil {
		s.storeErr(w, err)
		return
	}
	stamp, revokeErr := s.store.RevokeDeviceTunnels(r.Context(), id)
	if revokeErr != nil {
		s.internal(w, "撤销设备隧道失败", revokeErr)
		return
	}
	s.nodes.Broadcast(model.AgentFrame{Type: "kick_device", DeviceID: id, TS: stamp})
	seen := map[string]bool{}
	for _, f := range forwards {
		s.pushAgentConfig(r.Context(), f.DeviceID)
		s.pushAgentConfig(r.Context(), f.ClientID)
		if f.NodeID != "" && !seen[f.NodeID] {
			seen[f.NodeID] = true
			s.pushNodeConfig(r.Context(), f.NodeID)
		}
	}
	s.DisconnectAccessDevice(id)
	s.audit(r, "device.delete", id, nil)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) rotateDeviceKey(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	v, ok := s.newVault(w)
	if !ok {
		return
	}
	key, err := s.store.RotateDeviceKey(r.Context(), id, v)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	s.hub.Disconnect(id, "device key rotated")
	s.DisconnectAccessDevice(id)
	// Node sessions were authenticated with tickets for the old key. Close
	// them; a reconnect needs a fresh control-plane config.
	stamp, revokeErr := s.store.RevokeDeviceTunnels(r.Context(), id)
	if revokeErr != nil {
		s.internal(w, "撤销设备隧道失败", revokeErr)
		return
	}
	s.nodes.Broadcast(model.AgentFrame{Type: "kick_device", DeviceID: id, TS: stamp})
	allNodes, _ := s.store.ListNodes(r.Context())
	for _, n := range allNodes {
		s.pushNodeConfig(r.Context(), n.ID)
		s.pushNodeForwardsToAgents(r.Context(), n.ID)
	}
	s.audit(r, "device.rotate-key", id, nil)
	writeJSON(w, http.StatusOK, map[string]string{"deviceId": id, "deviceKey": key})
}

// ---------- nodes ----------

type nodeRequest struct {
	RendezvousAddress *string             `json:"rendezvousAddress"`
	Name              *string             `json:"name"`
	Remark            *string             `json:"remark"`
	Tags              *string             `json:"tags"`
	Addr              *string             `json:"addr"`
	Protocol          *string             `json:"protocol"`
	HTTPPort          *int                `json:"httpPort"`
	ReplaceAddress    *string             `json:"replaceAddress"`
	ForwardPorts      *string             `json:"forwardPorts"`
	Domain            *string             `json:"domain"`
	URLTemplate       *string             `json:"urlTemplate"`
	DenyDomainPrefix  *string             `json:"denyDomainPrefix"`
	AllowWildcard     *bool               `json:"allowWildcard"`
	Features          *model.NodeFeatures `json:"features"`
	MaxPoolCount      *int                `json:"maxPoolCount"`
	P2PDisableForward *bool               `json:"p2pDisableForward"`
	Rules             *string             `json:"rules"`
	LimitTotalGB      *float64            `json:"limitTotalGb"`
	LimitKind         *string             `json:"limitKind"`
	LimitResetDay     *int                `json:"limitResetDay"`
	LimiterMbps       *int                `json:"limiterMbps"`
	Concurrency       *int                `json:"concurrency"`
	Connections       *int                `json:"connections"`
}

func (s *Server) listNodes(w http.ResponseWriter, r *http.Request) {
	nodes, err := s.store.ListNodes(r.Context())
	if err != nil {
		s.internal(w, "读取节点失败", err)
		return
	}
	writeJSON(w, http.StatusOK, page(nodes))
}

func (s *Server) getNode(w http.ResponseWriter, r *http.Request) {
	n, err := s.store.GetNode(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, n)
}

func (s *Server) createNode(w http.ResponseWriter, r *http.Request) {
	var req nodeRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Name == nil || strings.TrimSpace(*req.Name) == "" {
		writeErr(w, http.StatusBadRequest, "invalid_argument", "节点名称不能为空")
		return
	}
	n := model.Node{Name: strings.TrimSpace(*req.Name), Protocol: "tcp", Status: "offline",
		Features: model.NodeFeatures{Web: true}}
	applyNodePatch(&n, req)
	if n.LimiterMbps < 0 || n.Concurrency < 0 || n.Connections < 0 || n.MaxPoolCount < 0 {
		writeErr(w, 400, "invalid_argument", "节点运行限制不能为负数")
		return
	}
	if !validProtocol(n.Protocol) {
		writeErr(w, http.StatusBadRequest, "invalid_argument", "接入协议必须为 tcp/ws/wss/kcp/quic")
		return
	}
	if err := validateNodeQuota(n); err != nil {
		writeErr(w, 400, "invalid_argument", err.Error())
		return
	}
	if err := validateNodeTransport(n); err != nil {
		writeErr(w, 400, "invalid_argument", err.Error())
		return
	}
	created, key, err := s.store.CreateNode(r.Context(), n)
	if err != nil {
		s.internal(w, "创建节点失败", err)
		return
	}
	s.audit(r, "node.create", created.ID, map[string]string{"name": created.Name})
	writeJSON(w, http.StatusCreated, map[string]any{"node": created, "nodeKey": key})
}

// rotateNodeKey issues a new node key and drops the live node socket. Old
// tickets remain valid until they expire, so revoke device sessions too by
// pushing fresh config on reconnect.
func (s *Server) rotateNodeKey(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	key, err := s.store.RotateNodeKey(r.Context(), id)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	s.nodes.Disconnect(id, "node key rotated")
	s.audit(r, "node.rotate-key", id, nil)
	writeJSON(w, http.StatusOK, map[string]string{"nodeId": id, "nodeKey": key})
}

func (s *Server) updateNode(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	existing, err := s.store.GetNode(r.Context(), id)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	var req nodeRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	applyNodePatch(existing, req)
	if strings.TrimSpace(existing.Name) == "" {
		writeErr(w, http.StatusBadRequest, "invalid_argument", "节点名称不能为空")
		return
	}
	if existing.LimiterMbps < 0 || existing.Concurrency < 0 || existing.Connections < 0 || existing.MaxPoolCount < 0 {
		writeErr(w, 400, "invalid_argument", "节点运行限制不能为负数")
		return
	}
	if !validProtocol(existing.Protocol) {
		writeErr(w, http.StatusBadRequest, "invalid_argument", "接入协议必须为 tcp/ws/wss/kcp/quic")
		return
	}
	if err := validateNodeQuota(*existing); err != nil {
		writeErr(w, 400, "invalid_argument", err.Error())
		return
	}
	if err := validateNodeTransport(*existing); err != nil {
		writeErr(w, 400, "invalid_argument", err.Error())
		return
	}
	updated, err := s.store.UpdateNode(r.Context(), *existing)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	s.pushNodeConfig(r.Context(), id)
	// An address change affects the tunnel dial target cached by agents.
	s.pushNodeForwardsToAgents(r.Context(), id)
	s.audit(r, "node.update", id, nil)
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) deleteNode(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if s.nodes.Online(id) {
		writeErr(w, http.StatusConflict, "node_online", "节点在线，请先停止节点程序再删除")
		return
	}
	forwards, _ := s.store.ListForwards(r.Context(), "", id, "")
	for _, f := range forwards {
		if activeForwardState(f.State) {
			_ = s.store.SetForwardState(r.Context(), f.ID, forwardError, "节点已删除")
		}
		if f.DeviceID != "" {
			s.pushAgentConfig(r.Context(), f.DeviceID)
			s.pushAgentConfig(r.Context(), f.ClientID)
		}
	}
	if err := s.store.DeleteNode(r.Context(), id); err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "node.delete", id, nil)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func applyNodePatch(n *model.Node, req nodeRequest) {
	setString := func(dst *string, src *string) {
		if src != nil {
			*dst = strings.TrimSpace(*src)
		}
	}
	setInt := func(dst *int, src *int) {
		if src != nil {
			*dst = *src
		}
	}
	setString(&n.Name, req.Name)
	setString(&n.Remark, req.Remark)
	setString(&n.Tags, req.Tags)
	setString(&n.Addr, req.Addr)
	setString(&n.RendezvousAddress, req.RendezvousAddress)
	setString(&n.Protocol, req.Protocol)
	setInt(&n.HTTPPort, req.HTTPPort)
	setString(&n.ReplaceAddress, req.ReplaceAddress)
	setString(&n.ForwardPorts, req.ForwardPorts)
	setString(&n.Domain, req.Domain)
	setString(&n.URLTemplate, req.URLTemplate)
	setString(&n.DenyDomainPrefix, req.DenyDomainPrefix)
	if req.AllowWildcard != nil {
		n.AllowWildcard = *req.AllowWildcard
	}
	if req.Features != nil {
		n.Features = *req.Features
	}
	setInt(&n.MaxPoolCount, req.MaxPoolCount)
	if req.P2PDisableForward != nil {
		n.P2PDisableForward = *req.P2PDisableForward
	}
	setString(&n.Rules, req.Rules)
	if req.LimitTotalGB != nil {
		n.LimitTotalGB = *req.LimitTotalGB
	}
	setString(&n.LimitKind, req.LimitKind)
	setInt(&n.LimitResetDay, req.LimitResetDay)
	setInt(&n.LimiterMbps, req.LimiterMbps)
	setInt(&n.Concurrency, req.Concurrency)
	setInt(&n.Connections, req.Connections)
}

func validProtocol(p string) bool {
	switch p {
	case "tcp", "ws", "wss", "kcp", "quic":
		return true
	}
	return false
}

// ---------- forwards ----------

type forwardRequest struct {
	Name       *string `json:"name"`
	Kind       *string `json:"kind"`
	DeviceID   *string `json:"deviceId"`
	NodeID     *string `json:"nodeId"`
	ClientID   *string `json:"clientId"`
	TargetIP   *string `json:"targetIp"`
	TargetPort *int    `json:"targetPort"`
	RemotePort *int    `json:"remotePort"`
	Domain     *string `json:"domain"`
	Tags       *string `json:"tags"`
	Options    *string `json:"options"`
}

func (s *Server) listForwards(w http.ResponseWriter, r *http.Request) {
	forwards, err := s.store.ListForwards(r.Context(), r.URL.Query().Get("device"),
		r.URL.Query().Get("node"), r.URL.Query().Get("kind"))
	if err != nil {
		s.internal(w, "读取转发失败", err)
		return
	}
	writeJSON(w, http.StatusOK, page(forwards))
}

func (s *Server) getForward(w http.ResponseWriter, r *http.Request) {
	f, err := s.store.GetForward(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

func (s *Server) createForward(w http.ResponseWriter, r *http.Request) {
	var req forwardRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	f := model.Forward{Kind: "port", State: "saved", Options: "{}"}
	applyForwardPatch(&f, req)
	if err := s.validateForward(r, &f); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	created, err := s.store.CreateForward(r.Context(), f)
	if err != nil {
		if strings.HasPrefix(err.Error(), "conflict") {
			writeErr(w, http.StatusConflict, "port_conflict", "该节点端口已被其他转发占用")
			return
		}
		s.internal(w, "创建转发失败", err)
		return
	}
	s.audit(r, "forward.create", created.ID, map[string]string{"name": created.Name, "kind": created.Kind})
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) updateForward(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	existing, err := s.store.GetForward(r.Context(), id)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if existing.State == "stopping" {
		writeErr(w, 409, "revoke_pending", "等待节点确认停止后再编辑")
		return
	}
	oldNode, oldDevice := existing.NodeID, existing.DeviceID
	oldClient := existing.ClientID
	wasActive := activeForwardState(existing.State)
	var req forwardRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	applyForwardPatch(existing, req)
	if existing.State != "saved" && existing.State != "stopped" && (existing.NodeID != oldNode || existing.DeviceID != oldDevice || existing.ClientID != oldClient) {
		writeErr(w, 409, "stop_required", "更换节点或设备前必须停止并确认撤销")
		return
	}
	if err := s.validateForward(r, existing); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	if wasActive {
		existing.State = "applying"
	} else {
		existing.State = "saved"
	}
	existing.LastError = ""
	updated, err := s.store.UpdateForwardRuntime(r.Context(), *existing)
	if err != nil {
		if strings.HasPrefix(err.Error(), "conflict") {
			writeErr(w, http.StatusConflict, "port_conflict", "该节点端口已被其他转发占用")
			return
		}
		s.storeErr(w, err)
		return
	}
	if wasActive {
		if oldNode != updated.NodeID {
			s.pushNodeConfig(r.Context(), oldNode)
		}
		if oldDevice != updated.DeviceID {
			s.pushAgentConfig(r.Context(), oldDevice)
		}
		s.pushNodeConfig(r.Context(), updated.NodeID)
		s.pushAgentConfig(r.Context(), updated.DeviceID)
		s.pushAgentConfig(r.Context(), updated.ClientID)
	}
	s.audit(r, "forward.update", id, nil)
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) deleteForward(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	f, err := s.store.GetForward(r.Context(), id)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if f.State != "saved" && f.State != "stopped" {
		writeErr(w, 409, "stop_required", "请先停止转发并等待节点撤销确认后删除")
		return
	}
	if err := s.store.DeleteInactiveForward(r.Context(), id, f.Revision); err != nil {
		s.storeErr(w, err)
		return
	}
	// Removing the row must also close the node listener and drop the
	// forward from the agent's tunnel config.
	if activeForwardState(f.State) {
		s.pushNodeConfig(r.Context(), f.NodeID)
		s.pushAgentConfig(r.Context(), f.DeviceID)
		s.pushAgentConfig(r.Context(), f.ClientID)
	}
	s.audit(r, "forward.delete", id, nil)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) applyForward(w http.ResponseWriter, r *http.Request) {
	s.applyForwardConfig(w, r)
}

func applyForwardPatch(f *model.Forward, req forwardRequest) {
	if req.Name != nil {
		f.Name = strings.TrimSpace(*req.Name)
	}
	if req.Kind != nil {
		f.Kind = strings.TrimSpace(*req.Kind)
	}
	if req.DeviceID != nil {
		f.DeviceID = strings.TrimSpace(*req.DeviceID)
	}
	if req.NodeID != nil {
		f.NodeID = strings.TrimSpace(*req.NodeID)
	}
	if req.ClientID != nil {
		f.ClientID = strings.TrimSpace(*req.ClientID)
	}
	if req.TargetIP != nil {
		f.TargetIP = strings.TrimSpace(*req.TargetIP)
	}
	if req.TargetPort != nil {
		f.TargetPort = *req.TargetPort
	}
	if req.RemotePort != nil {
		f.RemotePort = *req.RemotePort
	}
	if req.Domain != nil {
		f.Domain = strings.TrimSpace(*req.Domain)
	}
	if req.Tags != nil {
		f.Tags = *req.Tags
	}
	if req.Options != nil {
		f.Options = *req.Options
	}
}

func (s *Server) validateForward(r *http.Request, f *model.Forward) error {
	if f.Options != "" {
		var object map[string]json.RawMessage
		if err := json.Unmarshal([]byte(f.Options), &object); err != nil || object == nil {
			return errors.New("options 必须为 JSON 对象")
		}
		if raw, ok := object["protocol"]; ok {
			var protocol string
			if err := json.Unmarshal(raw, &protocol); err != nil || protocol == "" {
				return errors.New("protocol 必须为 tcp 或 udp 字符串")
			}
		}
		if raw, ok := object["relay"]; ok && string(raw) != "true" && string(raw) != "false" {
			return errors.New("relay 必须为布尔值")
		}
		var direct directForwardOptions
		if err := json.Unmarshal([]byte(f.Options), &direct); err != nil {
			return errors.New("直连选项必须为字符串")
		}
	}
	if protocol := forwardProtocol(f.Options); protocol != "tcp" && protocol != "udp" {
		return errors.New("数据平面仅支持 TCP 或 UDP")
	}
	if f.Name == "" {
		return errors.New("转发名称不能为空")
	}
	switch f.Kind {
	case "port", "host", "tunnel", "p2p", "proxy":
	default:
		return errors.New("类型必须是 port/host/tunnel/p2p/proxy")
	}
	if f.NodeID == "" {
		return errors.New("请选择节点")
	}
	node, err := s.store.GetNode(r.Context(), f.NodeID)
	if err != nil {
		return errors.New("节点不存在")
	}
	if !nodeFeature(node, f.Kind) {
		return fmt.Errorf("节点 %s 未启用 %s 能力", node.Name, f.Kind)
	}
	if f.DeviceID != "" {
		if _, err := s.store.GetDevice(r.Context(), f.DeviceID); err != nil {
			return errors.New("设备不存在")
		}
	}
	if f.Kind != "tunnel" && f.Kind != "p2p" && f.Kind != "proxy" {
		f.ClientID = ""
	}
	if f.Kind == "p2p" {
		direct := directOptions(f.Options)
		if direct.DirectAddress != "" {
			if err := tunnel.ValidateDirectAddress(direct.DirectAddress, true); err != nil {
				return err
			}
		}
		if direct.DirectListen != "" {
			host, port, err := net.SplitHostPort(direct.DirectListen)
			if err != nil {
				return errors.New("directListen 必须为 IP:port")
			}
			if host != "" && net.ParseIP(host) == nil {
				return errors.New("directListen 必须使用 IP")
			}
			n, err := strconv.Atoi(port)
			if err != nil || n < 0 || n > 65535 {
				return errors.New("directListen 端口无效")
			}
		}
	}
	if f.Kind == "tunnel" || f.Kind == "p2p" || f.Kind == "proxy" {
		if f.DeviceID == "" || f.ClientID == "" || f.DeviceID == f.ClientID {
			return errors.New("私有访问必须选择不同的服务设备和访问设备")
		}
		if _, err := s.store.GetDevice(r.Context(), f.ClientID); err != nil {
			return errors.New("访问设备不存在")
		}
		if f.TargetIP == "" || f.TargetPort < 1 || f.RemotePort < 1 {
			return errors.New("请填写目标地址、目标端口和访问设备本机端口")
		}
		if forwardProtocol(f.Options) != "tcp" {
			return errors.New("私有访问当前仅支持 TCP")
		}
		rules, err := s.store.ListForwards(r.Context(), "", "", "")
		if err != nil {
			return err
		}
		for _, other := range rules {
			if other.ID != f.ID && other.ClientID == f.ClientID && other.RemotePort == f.RemotePort && (other.Kind == "tunnel" || other.Kind == "p2p" || other.Kind == "proxy") {
				return errors.New("该访问设备本机端口已被其他私有访问占用")
			}
		}
	}
	if f.TargetPort < 0 || f.TargetPort > 65535 {
		return errors.New("目标端口必须在 1-65535 之间")
	}
	if f.RemotePort < 0 || f.RemotePort > 65535 {
		return errors.New("入口端口必须在 1-65535 之间")
	}
	if f.Kind == "host" {
		f.Domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(f.Domain), "."))
		if !validForwardDomain(f.Domain, node.AllowWildcard) {
			return errors.New("请输入有效域名；通配符需节点允许")
		}
		if node.Domain != "" {
			suffix := strings.ToLower(strings.TrimSuffix(node.Domain, "."))
			base := strings.TrimPrefix(f.Domain, "*.")
			if base != suffix && !strings.HasSuffix(base, "."+suffix) {
				return errors.New("域名不属于节点配置的域名后缀")
			}
		}
		for _, prefix := range strings.Split(node.DenyDomainPrefix, ",") {
			prefix = strings.TrimSpace(prefix)
			if prefix != "" && strings.HasPrefix(f.Domain, prefix) {
				return errors.New("域名前缀被节点策略禁止")
			}
		}
		if f.DeviceID == "" || f.TargetIP == "" || f.TargetPort == 0 {
			return errors.New("域名转发必须选择设备、目标地址和端口")
		}
		if node.HTTPPort < 1 || node.HTTPPort > 65535 {
			return errors.New("节点必须配置有效 HTTP 入口端口")
		}
		if forwardProtocol(f.Options) != "tcp" {
			return errors.New("HTTP 域名转发必须使用 TCP 目标")
		}
		all, err := s.store.ListForwards(r.Context(), "", f.NodeID, "")
		if err != nil {
			return err
		}
		for _, other := range all {
			if other.ID == f.ID {
				continue
			}
			if other.Kind == "host" && strings.EqualFold(other.Domain, f.Domain) {
				return errors.New("节点已存在相同域名")
			}
			if other.Kind == "port" && other.RemotePort == node.HTTPPort && forwardProtocol(other.Options) == "tcp" {
				return errors.New("HTTP 入口端口已被端口转发占用")
			}
		}
	} else if f.Kind == "port" {
		if f.DeviceID == "" {
			return errors.New("端口条目必须选择内网客户端设备")
		}
		if f.RemotePort == 0 {
			return errors.New("端口条目必须填写入口端口")
		}
		if f.TargetIP == "" || f.TargetPort == 0 {
			return errors.New("请填写目标地址和端口")
		}
		if f.RemotePort == node.HTTPPort && forwardProtocol(f.Options) == "tcp" {
			return errors.New("入口端口被节点 HTTP 域名服务保留")
		}
		if node.ForwardPorts != "" && !portAllowed(node.ForwardPorts, f.RemotePort) {
			return fmt.Errorf("入口端口不在节点允许的端口段 %s 内", node.ForwardPorts)
		}
	}
	return nil
}

func nodeFeature(n *model.Node, kind string) bool {
	switch kind {
	case "port":
		return n.Features.Forward
	case "host":
		return n.Features.Web
	case "tunnel":
		return n.Features.Tunnel
	case "p2p":
		return n.Features.P2P
	case "proxy":
		return n.Features.Proxy
	}
	return false
}

// portAllowed understands "20000-30000,40000,41000-41010".
func portAllowed(spec string, port int) bool {
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if lo, hi, ok := strings.Cut(part, "-"); ok {
			l, err1 := strconv.Atoi(strings.TrimSpace(lo))
			h, err2 := strconv.Atoi(strings.TrimSpace(hi))
			if err1 == nil && err2 == nil && port >= l && port <= h {
				return true
			}
			continue
		}
		if p, err := strconv.Atoi(part); err == nil && p == port {
			return true
		}
	}
	return false
}

// ---------- shared errors ----------

func (s *Server) storeErr(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrForwardConflict) {
		writeErr(w, 409, "forward_conflict", "转发已被修改或尚未确认停止，请刷新后重试")
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "not_found", "记录不存在")
		return
	}
	s.internal(w, "数据库操作失败", err)
}

func (s *Server) internal(w http.ResponseWriter, message string, err error) {
	s.log.Error(message, "err", err)
	writeErr(w, http.StatusInternalServerError, "internal", message)
}

func (s *Server) audit(r *http.Request, action, target string, detail any) {
	sess := sessionFrom(r.Context())
	actor := "system"
	if sess != nil {
		actor = sess.Username
	}
	s.store.Audit(r.Context(), actor, action, target, auditDetail(detail), clientIP(r), r.UserAgent(), "ok")
}

func queryInt(r *http.Request, key string, fallback int) int {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return n
}

func validForwardDomain(domain string, wildcard bool) bool {
	if strings.HasPrefix(domain, "*.") {
		if !wildcard {
			return false
		}
		domain = domain[2:]
	}
	if len(domain) > 253 || !strings.Contains(domain, ".") {
		return false
	}
	for _, label := range strings.Split(domain, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}
