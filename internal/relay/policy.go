// Package relay contains the dial policy for the node-relay data plane.
//
// The agent must never become an open proxy: only private, explicitly allowed
// destinations may be reached. Policy.Check resolves every address a hostname
// maps to and rejects the destination if any address is denied, which also
// prevents DNS-rebinding tricks.
package relay

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

// Defaults applied by NewPolicy.
const defaultDialTimeout = 10 * time.Second

var (
	// ErrDenied is wrapped by every policy rejection.
	ErrDenied = errors.New("relay: destination denied")
	// ErrInvalidPort is wrapped by every port validation failure.
	ErrInvalidPort = errors.New("relay: invalid port")
	// ErrInvalidConfig is wrapped by configuration problems.
	ErrInvalidConfig = errors.New("relay: invalid config")
)

// Config controls which destinations the agent may dial.
//
// RFC1918 (10/8, 172.16/12, 192.168/16) and IPv6 ULA (fc00::/7) are always
// allowed. AllowCIDRs adds further ranges, for example a specific public
// upstream. Loopback is allowed only for the listed ports. Link-local
// addresses are rejected unless AllowLinkLocal is true. Cloud metadata
// addresses are never dialable.
type Config struct {
	AllowCIDRs         []string
	AllowLoopbackPorts []int
	AllowLinkLocal     bool
	Timeout            time.Duration
}

// Resolver resolves hostnames to every address they currently map to.
// *net.Resolver satisfies this interface; tests can substitute a stub.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Policy is an immutable dial gate. It is safe for concurrent use.
type Policy struct {
	allow          []netip.Prefix
	loopbackPorts  map[int]struct{}
	allowLinkLocal bool
	timeout        time.Duration
	resolver       Resolver
	dialer         *net.Dialer
}

var (
	defaultAllowed = []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("172.16.0.0/12"),
		netip.MustParsePrefix("192.168.0.0/16"),
		netip.MustParsePrefix("fc00::/7"),
	}
	metadataV4 = netip.MustParseAddr("169.254.169.254")
	metadataV6 = netip.MustParseAddr("fd00:ec2::254")
)

// NewPolicy parses cfg and returns a Policy. A zero Timeout means 10s.
func NewPolicy(cfg Config) (*Policy, error) {
	return NewPolicyWithResolver(cfg, net.DefaultResolver)
}

// NewPolicyWithResolver is NewPolicy with an injected hostname resolver.
func NewPolicyWithResolver(cfg Config, resolver Resolver) (*Policy, error) {
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultDialTimeout
	}

	allow := make([]netip.Prefix, 0, len(defaultAllowed)+len(cfg.AllowCIDRs))
	allow = append(allow, defaultAllowed...)
	for _, raw := range cfg.AllowCIDRs {
		prefix, err := parsePrefix(raw)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidConfig, err)
		}
		allow = append(allow, prefix)
	}

	ports := make(map[int]struct{}, len(cfg.AllowLoopbackPorts))
	for _, port := range cfg.AllowLoopbackPorts {
		if err := validatePort(port); err != nil {
			return nil, fmt.Errorf("%w: loopback %v", ErrInvalidConfig, err)
		}
		ports[port] = struct{}{}
	}

	return &Policy{
		allow:          allow,
		loopbackPorts:  ports,
		allowLinkLocal: cfg.AllowLinkLocal,
		timeout:        timeout,
		resolver:       resolver,
		// The context bounds every attempt; Timeout is applied per candidate
		// address in DialContext.
		dialer: &net.Dialer{},
	}, nil
}

func parsePrefix(raw string) (netip.Prefix, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return netip.Prefix{}, errors.New("empty CIDR")
	}
	if prefix, err := netip.ParsePrefix(raw); err == nil {
		return prefix.Masked(), nil
	}
	// Allow a bare address as /32 or /128.
	addr, err := netip.ParseAddr(raw)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("invalid CIDR %q", raw)
	}
	addr = addr.Unmap()
	return netip.PrefixFrom(addr, addr.BitLen()), nil
}

func validatePort(port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("%w: %d (want 1-65535)", ErrInvalidPort, port)
	}
	return nil
}

// Check validates one destination. For a hostname, it resolves all addresses
// and requires every one of them to pass the policy.
func (p *Policy) Check(ctx context.Context, host string, port int) error {
	_, err := p.candidates(ctx, host, port)
	return err
}

// candidates is the shared resolve-and-validate step used by Check and
// DialContext. It returns the already-vetted addresses so DialContext can
// connect to them directly instead of resolving a second time (TOCTOU).
func (p *Policy) candidates(ctx context.Context, host string, port int) ([]netip.Addr, error) {
	if err := validatePort(port); err != nil {
		return nil, err
	}
	host = normalizeHost(host)
	if host == "" {
		return nil, fmt.Errorf("%w: empty host", ErrDenied)
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		if err := p.checkAddr(addr, port); err != nil {
			return nil, err
		}
		return []netip.Addr{addr.Unmap()}, nil
	}
	addrs, err := p.resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve %q: %v", ErrDenied, host, err)
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("%w: %q resolved to no addresses", ErrDenied, host)
	}
	out := make([]netip.Addr, 0, len(addrs))
	for _, addr := range addrs {
		addr = addr.Unmap()
		if err := p.checkAddr(addr, port); err != nil {
			return nil, fmt.Errorf("%w: %q resolves to %s: %v", ErrDenied, host, addr, err)
		}
		out = append(out, addr)
	}
	return out, nil
}

func normalizeHost(host string) string {
	host = strings.TrimSpace(host)
	if len(host) >= 2 && host[0] == '[' && host[len(host)-1] == ']' {
		host = host[1 : len(host)-1]
	}
	return host
}

func (p *Policy) checkAddr(addr netip.Addr, port int) error {
	if !addr.IsValid() {
		return fmt.Errorf("%w: invalid address", ErrDenied)
	}
	addr = addr.Unmap()

	// Cloud metadata is checked before ULA/link-local so fd00:ec2::254 and
	// 169.254.169.254 can never be re-enabled by a broad CIDR.
	if addr == metadataV4 || addr == metadataV6 {
		return fmt.Errorf("%w: cloud metadata address %s", ErrDenied, addr)
	}
	if addr.IsUnspecified() {
		return fmt.Errorf("%w: unspecified address %s", ErrDenied, addr)
	}
	if addr.IsMulticast() {
		return fmt.Errorf("%w: multicast address %s", ErrDenied, addr)
	}
	if addr.IsLoopback() {
		if _, ok := p.loopbackPorts[port]; ok {
			return nil
		}
		return fmt.Errorf("%w: loopback %s requires an allowlisted port", ErrDenied, addr)
	}
	if addr.IsLinkLocalUnicast() {
		if p.allowLinkLocal {
			return nil
		}
		return fmt.Errorf("%w: link-local address %s", ErrDenied, addr)
	}
	for _, prefix := range p.allow {
		if prefix.Contains(addr) {
			return nil
		}
	}
	return fmt.Errorf("%w: %s is outside the allowed CIDRs", ErrDenied, addr)
}

// DialContext validates address then dials it. Supported networks are tcp,
// tcp4, tcp6, udp, udp4 and udp6. The hostname is resolved once; every resolved address must
// pass the policy before any connection is attempted. Each dial attempt is
// bounded by Timeout.
func (p *Policy) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	switch network {
	case "tcp", "tcp4", "tcp6", "udp", "udp4", "udp6":
	default:
		return nil, fmt.Errorf("%w: unsupported network %q", ErrDenied, network)
	}
	host, portStr, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("%w: %q: %v", ErrDenied, address, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, fmt.Errorf("%w: %q", ErrInvalidPort, portStr)
	}
	addrs, err := p.candidates(ctx, host, port)
	if err != nil {
		return nil, err
	}

	var lastErr error
	for _, addr := range addrs {
		if (network == "tcp4" || network == "udp4") && !addr.Is4() {
			continue
		}
		if (network == "tcp6" || network == "udp6") && !addr.Is6() {
			continue
		}
		target := net.JoinHostPort(addr.String(), strconv.Itoa(port))
		attemptCtx, cancel := context.WithTimeout(ctx, p.timeout)
		conn, err := p.dialer.DialContext(attemptCtx, network, target)
		cancel()
		if err == nil {
			return conn, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	if lastErr == nil {
		return nil, fmt.Errorf("%w: no usable address for %q", ErrDenied, address)
	}
	return nil, fmt.Errorf("relay: dial %q: %w", address, lastErr)
}
