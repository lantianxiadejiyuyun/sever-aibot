package operations

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
)

var ErrUnsupported = errors.New("unsupported")

// DialPublicTarget probes the actual public listener from the control plane.
// It never connects directly to a forward's private TargetIP/TargetPort.
func DialPublicTarget(ctx context.Context, t Target) (net.Conn, error) {
	host := t.DialHost
	if host == "" {
		host = t.Host
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, errors.New("公网入口 DNS 解析失败")
	}
	if len(ips) == 0 {
		return nil, errors.New("公网入口没有地址")
	}
	for _, ip := range ips {
		if !publicIP(ip) {
			return nil, fmt.Errorf("%w: 控制面公网入口探测不支持私有、保留或 metadata 地址", ErrUnsupported)
		}
	}
	for _, ip := range ips {
		conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(ip.String(), strconv.Itoa(t.Port)))
		if err == nil {
			return conn, nil
		}
	}
	return nil, errors.New("公网入口连接失败")
}
