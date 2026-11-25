package api

import (
	"fmt"
	"net"
	"net/url"
	"server-aibot/internal/model"
	"strconv"
	"strings"
)

func nodeTunnelAddress(n *model.Node) string {
	if (n.Protocol == "kcp" || n.Protocol == "quic") && !strings.Contains(n.Addr, "://") {
		return n.Protocol + "://" + n.Addr
	}
	return n.Addr
}
func validateNodeTransport(n model.Node) error {
	if n.RendezvousAddress != "" {
		if err := validateEndpoint(n.RendezvousAddress); err != nil {
			return fmt.Errorf("UDP 会合地址: %w", err)
		}
	}
	if n.Protocol != "kcp" && n.Protocol != "quic" {
		return nil
	}
	if n.Addr == "" {
		return nil
	}
	addr := nodeTunnelAddress(&n)
	u, err := url.Parse(addr)
	if err != nil || u.Scheme != n.Protocol || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return fmt.Errorf("UDP 隧道地址必须是 %s://host:port 或 host:port", n.Protocol)
	}
	return validateEndpoint(u.Host)
}
func validateEndpoint(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || strings.TrimSpace(host) == "" {
		return fmt.Errorf("地址必须为 host:port")
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 1 || p > 65535 {
		return fmt.Errorf("端口必须为 1–65535")
	}
	return nil
}
