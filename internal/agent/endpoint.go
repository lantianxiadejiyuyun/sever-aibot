package agent

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Endpoint converts a control-plane base URL into the agent WebSocket URL.
//
// Accepted inputs are https://host, http://host, wss://host and ws://host.
// The scheme is converted to wss/ws and /agent/ws is appended to the path.
// Userinfo is always stripped: authentication uses headers, never the URL.
func Endpoint(controlURL string) (string, error) {
	return ControlEndpoint(controlURL, "/agent/ws")
}

// ControlEndpoint is Endpoint with an explicit WebSocket path. cmd/node uses
// the same URL conversion with /node/ws.
func ControlEndpoint(controlURL, wsPath string) (string, error) {
	raw := strings.TrimSpace(controlURL)
	if raw == "" {
		return "", errors.New("control URL is empty")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid control URL: %w", err)
	}
	if u.Host == "" {
		return "", errors.New("control URL must include a host")
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	case "wss", "ws":
		// Already a WebSocket URL.
	default:
		return "", fmt.Errorf("unsupported control URL scheme %q", u.Scheme)
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	u.RawPath = ""
	u.Path = strings.TrimRight(u.Path, "/") + wsPath
	return u.String(), nil
}

// RedactURL removes userinfo from a URL for safe logging. Values that cannot
// be parsed are replaced rather than echoed.
func RedactURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil {
		return "(invalid URL)"
	}
	u.User = nil
	return u.String()
}
