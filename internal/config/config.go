// Package config loads control-plane and agent configuration from the
// environment. Secrets are never written to disk by this package.
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Control is the configuration of cmd/control.
type Control struct {
	Listen        string
	DBPath        string
	MasterKey     []byte
	AdminUser     string
	AdminPassword string
	SecureCookie  bool
	SessionTTL    int // hours
	AgentWSOrigin string
	// PublicHost is the browser-facing hostname (for example
	// aibot.example.com) when the control plane runs behind a reverse proxy.
	// It is accepted as an Origin host alongside the request Host.
	PublicHost string
	// TLSCert and TLSKey enable HTTPS when both are set.
	TLSCert string
	TLSKey  string
}

// Agent is the configuration of cmd/agent.
type Agent struct {
	ControlURL string
	DeviceID   string
	DeviceKey  string
	StateDir   string

	// Tunnel target policy. Private ranges are always allowed; these extend
	// or narrow it.
	AllowCIDRs         []string
	AllowLoopbackPorts []int
	AllowLinkLocal     bool
}

// Node is the configuration of cmd/node, the runtime on a public relay node.
type Node struct {
	ControlURL       string
	NodeID           string
	NodeKey          string
	StateDir         string
	TunnelListen     string
	TunnelTransport  string
	RendezvousListen string
	EntryHost        string
	TLSCert          string
	TLSKey           string
	// TunnelHTTP serves the tunnel as a WebSocket at /tunnel so an nginx
	// vhost can terminate TLS and proxy wss:// to it.
	TunnelHTTP bool
}

// Edge is the configuration of cmd/edge, the SNI router that lets the console
// and the node tunnel share one TLS port.
type Edge struct {
	Listen  string
	Routes  string
	Default string
}

// LoadControl reads AIBOT_* variables. AIBOT_ADMIN_PASSWORD is only read to
// bootstrap the first administrator and is hashed before storage.
func LoadControl() (Control, error) {
	c := Control{
		Listen:       env("AIBOT_LISTEN", "127.0.0.1:7400"),
		DBPath:       env("AIBOT_DB", "var/aibot.sqlite3"),
		AdminUser:    env("AIBOT_ADMIN_USER", "admin"),
		SecureCookie: envBool("AIBOT_SECURE_COOKIE", false),
		SessionTTL:   envInt("AIBOT_SESSION_TTL_HOURS", 12),
		PublicHost:   strings.TrimSpace(os.Getenv("AIBOT_PUBLIC_HOST")),
		TLSCert:      strings.TrimSpace(os.Getenv("AIBOT_TLS_CERT")),
		TLSKey:       strings.TrimSpace(os.Getenv("AIBOT_TLS_KEY")),
	}
	if raw := strings.TrimSpace(os.Getenv("AIBOT_ADMIN_PASSWORD")); raw != "" {
		c.AdminPassword = raw
	}
	key, err := loadMasterKey()
	if err != nil {
		return c, err
	}
	c.MasterKey = key
	if c.SessionTTL <= 0 || c.SessionTTL > 24*30 {
		return c, errors.New("AIBOT_SESSION_TTL_HOURS must be between 1 and 720")
	}
	if (c.TLSCert == "") != (c.TLSKey == "") {
		return c, errors.New("AIBOT_TLS_CERT and AIBOT_TLS_KEY must be set together")
	}
	return c, nil
}

// LoadAgent reads agent variables. The key must come from the environment or a
// protected file, never from a command-line flag.
func LoadAgent() (Agent, error) {
	a := Agent{
		ControlURL:         strings.TrimSpace(os.Getenv("AIBOT_CONTROL_URL")),
		DeviceID:           strings.TrimSpace(os.Getenv("AIBOT_DEVICE_ID")),
		DeviceKey:          strings.TrimSpace(os.Getenv("AIBOT_DEVICE_KEY")),
		StateDir:           env("AIBOT_AGENT_STATE", "var/agent"),
		AllowCIDRs:         envList("AIBOT_RELAY_ALLOW_CIDRS"),
		AllowLoopbackPorts: envPorts("AIBOT_RELAY_LOOPBACK_PORTS"),
		AllowLinkLocal:     envBool("AIBOT_RELAY_ALLOW_LINK_LOCAL", false),
	}
	if a.ControlURL == "" || a.DeviceID == "" || a.DeviceKey == "" {
		return a, errors.New("AIBOT_CONTROL_URL, AIBOT_DEVICE_ID and AIBOT_DEVICE_KEY are required")
	}
	return a, nil
}

// LoadNode reads the relay-node runtime configuration. The node key is used
// only for the control-plane WebSocket.
func LoadNode() (Node, error) {
	n := Node{
		ControlURL:       strings.TrimSpace(os.Getenv("AIBOT_CONTROL_URL")),
		NodeID:           strings.TrimSpace(os.Getenv("AIBOT_NODE_ID")),
		NodeKey:          strings.TrimSpace(os.Getenv("AIBOT_NODE_KEY")),
		StateDir:         env("AIBOT_NODE_STATE", "var/node"),
		TunnelListen:     env("AIBOT_NODE_TUNNEL_LISTEN", "0.0.0.0:7000"),
		TunnelTransport:  strings.ToLower(env("AIBOT_NODE_TUNNEL_TRANSPORT", "tcp")),
		RendezvousListen: strings.TrimSpace(os.Getenv("AIBOT_NODE_RENDEZVOUS_LISTEN")),
		EntryHost:        env("AIBOT_NODE_ENTRY_HOST", "0.0.0.0"),
		TLSCert:          strings.TrimSpace(os.Getenv("AIBOT_NODE_TLS_CERT")),
		TLSKey:           strings.TrimSpace(os.Getenv("AIBOT_NODE_TLS_KEY")),
		TunnelHTTP:       envBool("AIBOT_NODE_TUNNEL_HTTP", false),
	}
	if n.ControlURL == "" || n.NodeID == "" || n.NodeKey == "" {
		return n, errors.New("AIBOT_CONTROL_URL, AIBOT_NODE_ID and AIBOT_NODE_KEY are required")
	}
	if (n.TLSCert == "") != (n.TLSKey == "") {
		return n, errors.New("AIBOT_NODE_TLS_CERT and AIBOT_NODE_TLS_KEY must be set together")
	}
	if n.TunnelTransport != "tcp" && n.TunnelTransport != "kcp" && n.TunnelTransport != "quic" {
		return n, errors.New("AIBOT_NODE_TUNNEL_TRANSPORT must be tcp, kcp or quic")
	}
	if n.TunnelTransport != "tcp" && (n.TLSCert == "" || n.TunnelHTTP) {
		return n, errors.New("KCP/QUIC require node TLS certificate and key and cannot use AIBOT_NODE_TUNNEL_HTTP")
	}
	return n, nil
}

// LoadEdge reads the SNI router configuration.
func LoadEdge() (Edge, error) {
	e := Edge{
		Listen:  env("AIBOT_EDGE_LISTEN", "0.0.0.0:443"),
		Routes:  strings.TrimSpace(os.Getenv("AIBOT_EDGE_ROUTES")),
		Default: strings.TrimSpace(os.Getenv("AIBOT_EDGE_DEFAULT")),
	}
	if e.Routes == "" {
		return e, errors.New("AIBOT_EDGE_ROUTES is required: name=host:port,name=host:port")
	}
	return e, nil
}

func loadMasterKey() ([]byte, error) {
	if raw := strings.TrimSpace(os.Getenv("AIBOT_MASTER_KEY")); raw != "" {
		key, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return nil, fmt.Errorf("AIBOT_MASTER_KEY must be base64: %w", err)
		}
		if len(key) != 32 {
			return nil, fmt.Errorf("AIBOT_MASTER_KEY decodes to %d bytes, want 32", len(key))
		}
		return key, nil
	}
	for _, path := range masterKeyPaths() {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
		if err != nil || len(key) != 32 {
			return nil, fmt.Errorf("master key file %s is not 32-byte base64", path)
		}
		return key, nil
	}
	return nil, errors.New("no master key: set AIBOT_MASTER_KEY (base64, 32 bytes) or create /etc/server-aibot/master.key")
}

func masterKeyPaths() []string {
	paths := []string{"/etc/server-aibot/master.key"}
	if dir, err := os.UserConfigDir(); err == nil {
		paths = append(paths, filepath.Join(dir, "server-aibot", "master.key"))
	}
	return paths
}

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

func envList(key string) []string {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return nil
	}
	out := []string{}
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func envPorts(key string) []int {
	out := []int{}
	for _, part := range envList(key) {
		var port int
		if _, err := fmt.Sscanf(part, "%d", &port); err == nil && port > 0 && port <= 65535 {
			out = append(out, port)
		}
	}
	return out
}

func envInt(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	var n int
	if _, err := fmt.Sscanf(raw, "%d", &n); err != nil {
		return fallback
	}
	return n
}
