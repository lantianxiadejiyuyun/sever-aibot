// Package model contains the wire and storage types shared by the control
// plane, the agent and the web console. Field names are camelCase on the wire.
package model

import "time"

// Device is a machine running cmd/agent. The control plane stores only a hash
// of the device key; the key itself is returned exactly once at creation.
type Device struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Remark  string `json:"remark"`
	Tags    string `json:"tags"`
	Status  string `json:"status"`
	Version string `json:"version"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
	// KeyPrefix is kept for support diagnostics but never returned to clients:
	// even a prefix narrows a brute-force search.
	KeyPrefix  string     `json:"-"`
	LastSeenAt *time.Time `json:"lastSeenAt,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
}

// NodeFeatures mirrors the capability switches exposed by gostc-open nodes.
type NodeFeatures struct {
	Web     bool `json:"web"`
	Forward bool `json:"forward"`
	Tunnel  bool `json:"tunnel"`
	P2P     bool `json:"p2p"`
	Proxy   bool `json:"proxy"`
}

// Node is a public relay endpoint (frps / gost node).
type Node struct {
	ID                string       `json:"id"`
	Name              string       `json:"name"`
	Remark            string       `json:"remark"`
	Tags              string       `json:"tags"`
	Addr              string       `json:"addr"`
	Protocol          string       `json:"protocol"`
	HTTPPort          int          `json:"httpPort"`
	ReplaceAddress    string       `json:"replaceAddress"`
	ForwardPorts      string       `json:"forwardPorts"`
	Domain            string       `json:"domain"`
	URLTemplate       string       `json:"urlTemplate"`
	DenyDomainPrefix  string       `json:"denyDomainPrefix"`
	AllowWildcard     bool         `json:"allowWildcard"`
	Features          NodeFeatures `json:"features"`
	MaxPoolCount      int          `json:"maxPoolCount"`
	P2PDisableForward bool         `json:"p2pDisableForward"`
	Rules             string       `json:"rules"`
	LimitTotalGB      float64      `json:"limitTotalGb"`
	LimitKind         string       `json:"limitKind"`
	LimitResetDay     int          `json:"limitResetDay"`
	LimiterMbps       int          `json:"limiterMbps"`
	Concurrency       int          `json:"concurrency"`
	Connections       int          `json:"connections"`
	Status            string       `json:"status"`
	LastSeenAt        *time.Time   `json:"lastSeenAt,omitempty"`
	CreatedAt         time.Time    `json:"createdAt"`
	UpdatedAt         time.Time    `json:"updatedAt"`
	// KeyPrefix is for support diagnostics only and never returned to clients.
	KeyPrefix string `json:"-"`
}

// Forward is one public entry mapped to a target behind an agent.
type Forward struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Kind         string    `json:"kind"` // port | host | tunnel | p2p | proxy
	DeviceID     string    `json:"deviceId"`
	NodeID       string    `json:"nodeId"`
	ClientID     string    `json:"clientId"`
	TargetIP     string    `json:"targetIp"`
	TargetPort   int       `json:"targetPort"`
	RemotePort   int       `json:"remotePort"`
	Domain       string    `json:"domain"`
	State        string    `json:"state"` // saved | applied | observed | error
	Revision     int64     `json:"revision"`
	LastError    string    `json:"lastError"`
	Tags         string    `json:"tags"`
	Options      string    `json:"options"`
	TrafficBytes int64     `json:"trafficBytes"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// TunnelNode is one public relay node as pushed to an agent. Ticket is a
// short-lived HMAC credential that the node verifies before accepting the
// tunnel; it never grants control-plane access.
type TunnelNode struct {
	NodeID   string          `json:"nodeId"`
	Addr     string          `json:"addr"`
	TLS      bool            `json:"tls,omitempty"`
	Ticket   string          `json:"ticket"`
	Forwards []TunnelForward `json:"forwards"`
}

// TunnelForward is one applied forward. On the node side DeviceID says which
// agent owns it; on the agent side TargetIP/TargetPort say where to dial.
type TunnelForward struct {
	ID         string `json:"id"`
	DeviceID   string `json:"deviceId,omitempty"`
	Name       string `json:"name,omitempty"`
	Kind       string `json:"kind,omitempty"`
	RemotePort int    `json:"remotePort,omitempty"`
	TargetIP   string `json:"targetIp,omitempty"`
	TargetPort int    `json:"targetPort,omitempty"`
}

// AgentFrame is the JSON control-frame envelope used on /agent/ws and
// /node/ws. Data-plane bytes never travel on these sockets.
type AgentFrame struct {
	Type string `json:"type"`
	// hello
	Version string `json:"version,omitempty"`
	OS      string `json:"os,omitempty"`
	Arch    string `json:"arch,omitempty"`
	// hello for nodes: the tunnel listener uses TLS when true
	TLS bool `json:"tls,omitempty"`
	// heartbeat
	TS       int64   `json:"ts,omitempty"`
	Load1    float64 `json:"load1,omitempty"`
	MemUsed  uint64  `json:"memUsed,omitempty"`
	MemTotal uint64  `json:"memTotal,omitempty"`
	// config push (agent receives Nodes, node receives TunnelSecret+Forwards)
	TunnelSecret string          `json:"tunnelSecret,omitempty"`
	Nodes        []TunnelNode    `json:"nodes,omitempty"`
	Forwards     []TunnelForward `json:"forwards,omitempty"`
	// per-forward result and kick instructions
	NodeID       string `json:"nodeId,omitempty"`
	DeviceID     string `json:"deviceId,omitempty"`
	ForwardID    string `json:"forwardId,omitempty"`
	TrafficBytes int64  `json:"trafficBytes,omitempty"`
	// generic acknowledgement / error
	Ref     string `json:"ref,omitempty"`
	OK      bool   `json:"ok,omitempty"`
	Message string `json:"message,omitempty"`
}

// AuditLog records a sensitive or state-changing operation.
type AuditLog struct {
	ID        int64     `json:"id"`
	Actor     string    `json:"actor"`
	Action    string    `json:"action"`
	Target    string    `json:"target"`
	Detail    string    `json:"detail"`
	IP        string    `json:"ip"`
	UserAgent string    `json:"userAgent"`
	Result    string    `json:"result"`
	CreatedAt time.Time `json:"createdAt"`
}
