package config

import "testing"

func TestLoadNode(t *testing.T) {
	t.Setenv("AIBOT_CONTROL_URL", "https://panel.example.com")
	t.Setenv("AIBOT_NODE_ID", "nod_1")
	t.Setenv("AIBOT_NODE_KEY", "node-key")
	t.Setenv("AIBOT_NODE_TUNNEL_LISTEN", "0.0.0.0:7000")
	t.Setenv("AIBOT_NODE_ENTRY_HOST", "10.0.0.1")

	n, err := LoadNode()
	if err != nil {
		t.Fatal(err)
	}
	if n.NodeID != "nod_1" || n.TunnelListen != "0.0.0.0:7000" || n.EntryHost != "10.0.0.1" {
		t.Fatalf("node config = %+v", n)
	}

	t.Setenv("AIBOT_NODE_TLS_CERT", "/tmp/node.crt")
	if _, err := LoadNode(); err == nil {
		t.Fatal("cert without key was accepted")
	}
	t.Setenv("AIBOT_NODE_TLS_KEY", "/tmp/node.key")
	if _, err := LoadNode(); err != nil {
		t.Fatalf("complete TLS pair rejected: %v", err)
	}

	t.Setenv("AIBOT_NODE_ID", "")
	if _, err := LoadNode(); err == nil {
		t.Fatal("missing node id was accepted")
	}
}

func TestNodeUDPTransportRequiresTLS(t *testing.T) {
	t.Setenv("AIBOT_CONTROL_URL", "https://panel.example.com")
	t.Setenv("AIBOT_NODE_ID", "node")
	t.Setenv("AIBOT_NODE_KEY", "key")
	for _, transport := range []string{"kcp", "quic"} {
		t.Run(transport, func(t *testing.T) {
			t.Setenv("AIBOT_NODE_TUNNEL_TRANSPORT", transport)
			t.Setenv("AIBOT_NODE_TLS_CERT", "")
			t.Setenv("AIBOT_NODE_TLS_KEY", "")
			t.Setenv("AIBOT_NODE_TUNNEL_HTTP", "0")
			if _, err := LoadNode(); err == nil {
				t.Fatal("accepted UDP tunnel without TLS")
			}
			t.Setenv("AIBOT_NODE_TLS_CERT", "node.crt")
			t.Setenv("AIBOT_NODE_TLS_KEY", "node.key")
			t.Setenv("AIBOT_NODE_RENDEZVOUS_LISTEN", "0.0.0.0:7001")
			n, err := LoadNode()
			if err != nil || n.TunnelTransport != transport || n.RendezvousListen != "0.0.0.0:7001" {
				t.Fatalf("valid UDP config: %+v %v", n, err)
			}
			t.Setenv("AIBOT_NODE_TUNNEL_HTTP", "1")
			if _, err := LoadNode(); err == nil {
				t.Fatal("accepted HTTP over UDP transport")
			}
		})
	}
}

func TestLoadAgentRelayPolicy(t *testing.T) {
	t.Setenv("AIBOT_CONTROL_URL", "https://panel.example.com")
	t.Setenv("AIBOT_DEVICE_ID", "dev_1")
	t.Setenv("AIBOT_DEVICE_KEY", "device-key")
	t.Setenv("AIBOT_RELAY_ALLOW_CIDRS", "203.0.113.0/24, 198.51.100.7")
	t.Setenv("AIBOT_RELAY_LOOPBACK_PORTS", "8080, 9090,bad")
	t.Setenv("AIBOT_RELAY_ALLOW_LINK_LOCAL", "1")

	a, err := LoadAgent()
	if err != nil {
		t.Fatal(err)
	}
	if len(a.AllowCIDRs) != 2 || a.AllowCIDRs[1] != "198.51.100.7" {
		t.Fatalf("CIDRs = %v", a.AllowCIDRs)
	}
	if len(a.AllowLoopbackPorts) != 2 || a.AllowLoopbackPorts[0] != 8080 || a.AllowLoopbackPorts[1] != 9090 {
		t.Fatalf("loopback ports = %v", a.AllowLoopbackPorts)
	}
	if !a.AllowLinkLocal {
		t.Fatal("link-local flag was not parsed")
	}
}
