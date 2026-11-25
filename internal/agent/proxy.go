package agent

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/yamux"
	"server-aibot/internal/model"
	"server-aibot/internal/tunnel"
)

// Proxy listeners are loopback-only, authenticated by the visitor device's
// tunnel ticket, and restricted to the one target configured for this rule.
// They intentionally do not offer arbitrary Internet or LAN proxy access.
func (m *tunnelManager) serveProxyVisitor(session *yamux.Session, f model.TunnelForward, conn net.Conn) {
	_ = conn.SetDeadline(time.Now().Add(tunnelHeaderLimit))
	reader := bufio.NewReaderSize(conn, 8192)
	reply, err := proxyHandshake(conn, reader, f.TargetIP, f.TargetPort)
	if err != nil {
		return
	}
	stream, err := privateControl(session, f, "@relay")
	if err != nil {
		reply(false)
		return
	}
	defer stream.Close()
	status, _, err := tunnel.ReadResult(stream)
	if err != nil || status != tunnel.StatusOK {
		reply(false)
		return
	}
	if err = reply(true); err != nil {
		return
	}
	_ = conn.SetDeadline(time.Time{})
	_ = stream.SetDeadline(time.Time{})
	tunnel.Pipe(&bufferedProxyConn{Conn: conn, reader: reader}, stream)
}

type bufferedProxyConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedProxyConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

func sameProxyTarget(host string, port int, expected string, expectedPort int) bool {
	if port != expectedPort {
		return false
	}
	a, b := net.ParseIP(host), net.ParseIP(expected)
	if a != nil || b != nil {
		return a != nil && b != nil && a.Equal(b)
	}
	return strings.EqualFold(strings.TrimSuffix(host, "."), strings.TrimSuffix(expected, "."))
}
func proxyHandshake(conn io.Writer, r *bufio.Reader, target string, port int) (func(bool) error, error) {
	first, err := r.Peek(1)
	if err != nil {
		return nil, err
	}
	if first[0] == 5 {
		return socksHandshake(conn, r, target, port)
	}
	fail := func(code int) error {
		_, err := fmt.Fprintf(conn, "HTTP/1.1 %d Proxy request rejected\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", code)
		return err
	}
	total := 0
	line, err := r.ReadSlice('\n')
	total += len(line)
	if err != nil {
		fail(431)
		return nil, errors.New("proxy header too large")
	}
	fields := strings.Fields(string(line))
	if len(fields) != 3 || fields[0] != "CONNECT" || (fields[2] != "HTTP/1.1" && fields[2] != "HTTP/1.0") {
		fail(405)
		return nil, errors.New("only CONNECT supported")
	}
	host, p, err := net.SplitHostPort(fields[1])
	n, e := strconv.Atoi(p)
	if err != nil || e != nil || !sameProxyTarget(host, n, target, port) {
		fail(403)
		return nil, errors.New("proxy target denied")
	}
	for {
		line, err = r.ReadSlice('\n')
		total += len(line)
		if err != nil || total > 8192 {
			fail(431)
			return nil, errors.New("proxy header too large")
		}
		if string(line) == "\r\n" || string(line) == "\n" {
			break
		}
	}
	return func(ok bool) error {
		if !ok {
			return fail(502)
		}
		_, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n")
		return err
	}, nil
}
func socksHandshake(conn io.Writer, r *bufio.Reader, target string, port int) (func(bool) error, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	methods := make([]byte, int(hdr[1]))
	if _, err := io.ReadFull(r, methods); err != nil {
		return nil, err
	}
	noAuth := false
	for _, m := range methods {
		noAuth = noAuth || m == 0
	}
	if !noAuth {
		conn.Write([]byte{5, 255})
		return nil, errors.New("unsupported auth")
	}
	if _, err := conn.Write([]byte{5, 0}); err != nil {
		return nil, err
	}
	reply := func(code byte) error { _, err := conn.Write([]byte{5, code, 0, 1, 0, 0, 0, 0, 0, 0}); return err }
	var req [4]byte
	if _, err := io.ReadFull(r, req[:]); err != nil {
		return nil, err
	}
	if req[0] != 5 || req[1] != 1 || req[2] != 0 {
		reply(7)
		return nil, errors.New("only SOCKS CONNECT supported")
	}
	var host string
	switch req[3] {
	case 1, 4:
		size := 4
		if req[3] == 4 {
			size = 16
		}
		b := make([]byte, size)
		if _, err := io.ReadFull(r, b); err != nil {
			return nil, err
		}
		host = net.IP(b).String()
	case 3:
		size, err := r.ReadByte()
		if err != nil {
			return nil, err
		}
		b := make([]byte, int(size))
		if _, err = io.ReadFull(r, b); err != nil {
			return nil, err
		}
		host = string(b)
	default:
		reply(8)
		return nil, errors.New("address type unsupported")
	}
	var p [2]byte
	if _, err := io.ReadFull(r, p[:]); err != nil {
		return nil, err
	}
	if !sameProxyTarget(host, int(binary.BigEndian.Uint16(p[:])), target, port) {
		reply(2)
		return nil, errors.New("proxy target denied")
	}
	return func(ok bool) error {
		if ok {
			return reply(0)
		}
		return reply(5)
	}, nil
}
