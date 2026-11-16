// Package edge routes TLS connections by the SNI name in the ClientHello.
// It terminates nothing: the original bytes are forwarded to the upstream
// listener, which keeps the end-to-end TLS handshake intact.
package edge

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	recordTypeHandshake  = 22
	handshakeClientHello = 1
	// maxHelloBytes bounds reassembly of a fragmented ClientHello.
	maxHelloBytes = 64 << 10
)

// ErrNotTLS is returned when the first bytes are not a TLS handshake record.
var ErrNotTLS = errors.New("connection did not start with a TLS handshake")

// ReadClientHello reads TLS handshake records from r until the ClientHello is
// complete and returns the SNI host (possibly empty when absent) together with
// every byte consumed, so the caller can replay them to the upstream.
func ReadClientHello(r io.Reader) (host string, prefix []byte, err error) {
	header := make([]byte, 5)
	for {
		if _, err := io.ReadFull(r, header); err != nil {
			return "", prefix, err
		}
		if header[0] != recordTypeHandshake {
			return "", prefix, ErrNotTLS
		}
		size := int(binary.BigEndian.Uint16(header[3:5]))
		if size < 0 || size > maxHelloBytes {
			return "", prefix, fmt.Errorf("TLS record too large: %d", size)
		}
		body := make([]byte, size)
		if _, err := io.ReadFull(r, body); err != nil {
			return "", prefix, err
		}
		prefix = append(prefix, header...)
		prefix = append(prefix, body...)

		host, complete, err := parseClientHello(prefix[5:])
		if err != nil {
			return "", prefix, err
		}
		if complete {
			return host, prefix, nil
		}
		if len(prefix) > maxHelloBytes {
			return "", prefix, errors.New("ClientHello exceeds reassembly limit")
		}
	}
}

// parseClientHello parses one or more concatenated handshake messages and
// extracts the server_name extension of the ClientHello.
func parseClientHello(b []byte) (host string, complete bool, err error) {
	if len(b) < 4 {
		return "", false, nil
	}
	if b[0] != handshakeClientHello {
		return "", false, fmt.Errorf("unexpected handshake type %d", b[0])
	}
	length := int(b[1])<<16 | int(b[2])<<8 | int(b[3])
	if len(b) < 4+length {
		return "", false, nil
	}
	body := b[4 : 4+length]

	// version(2) random(32)
	if len(body) < 34 {
		return "", false, errors.New("ClientHello truncated")
	}
	p := 34
	sessionLen := int(body[p])
	p++
	if p+sessionLen > len(body) {
		return "", false, errors.New("ClientHello session id truncated")
	}
	p += sessionLen
	if p+2 > len(body) {
		return "", false, errors.New("ClientHello cipher suites truncated")
	}
	suitesLen := int(binary.BigEndian.Uint16(body[p : p+2]))
	p += 2 + suitesLen
	if p+1 > len(body) {
		return "", false, errors.New("ClientHello compression truncated")
	}
	compLen := int(body[p])
	p++
	p += compLen
	if p+2 > len(body) {
		return "", false, errors.New("ClientHello extensions truncated")
	}
	extLen := int(binary.BigEndian.Uint16(body[p : p+2]))
	p += 2
	if p+extLen > len(body) {
		return "", false, errors.New("ClientHello extension block truncated")
	}
	end := p + extLen
	for p+4 <= end {
		extType := binary.BigEndian.Uint16(body[p : p+2])
		extSize := int(binary.BigEndian.Uint16(body[p+2 : p+4]))
		p += 4
		if p+extSize > end {
			return "", false, errors.New("ClientHello extension overruns block")
		}
		if extType == 0 { // server_name
			return parseServerName(body[p : p+extSize]), true, nil
		}
		p += extSize
	}
	return "", true, nil
}

func parseServerName(data []byte) string {
	if len(data) < 2 {
		return ""
	}
	listLen := int(binary.BigEndian.Uint16(data[:2]))
	if listLen > len(data)-2 {
		listLen = len(data) - 2
	}
	p := 2
	for p+3 <= 2+listLen {
		nameType := data[p]
		nameLen := int(binary.BigEndian.Uint16(data[p+1 : p+3]))
		p += 3
		if p+nameLen > 2+listLen {
			return ""
		}
		if nameType == 0 {
			return normalizeHost(string(data[p : p+nameLen]))
		}
		p += nameLen
	}
	return ""
}

// normalizeHost lowercases a host and strips a trailing dot. SNI names never
// carry a port, so only the name form is normalized.
func normalizeHost(raw string) string {
	host := strings.TrimSpace(strings.ToLower(raw))
	return strings.TrimSuffix(host, ".")
}
