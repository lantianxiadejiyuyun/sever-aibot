package agent

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"io"
	"strings"
	"testing"
)

func TestProxyTargetAndBufferedPayload(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		ok          bool
	}{
		{"connect", "CONNECT service.internal:443 HTTP/1.1\r\nHost: service.internal:443\r\n\r\npayload", true},
		{"wrong target", "CONNECT evil.internal:443 HTTP/1.1\r\n\r\n", false},
		{"wrong port", "CONNECT service.internal:80 HTTP/1.1\r\n\r\n", false},
		{"http method", "GET http://service.internal/ HTTP/1.1\r\n\r\n", false},
		{"oversize", "CONNECT service.internal:443 HTTP/1.1\r\nX: " + strings.Repeat("a", 9000) + "\r\n\r\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			r := bufio.NewReaderSize(strings.NewReader(tc.input), 8192)
			reply, err := proxyHandshake(&out, r, "service.internal", 443)
			if (err == nil) != tc.ok {
				t.Fatalf("err %v", err)
			}
			if tc.ok {
				if out.Len() != 0 {
					t.Fatal("success before upstream ready")
				}
				if err := reply(true); err != nil {
					t.Fatal(err)
				}
				body, _ := io.ReadAll(r)
				if string(body) != "payload" {
					t.Fatal("pipelined bytes lost")
				}
			}
		})
	}
}
func TestSOCKSProxyTarget(t *testing.T) {
	for _, port := range []int{443, 444} {
		input := []byte{5, 1, 0, 5, 1, 0, 3, 16}
		input = append(input, []byte("service.internal")...)
		input = binary.BigEndian.AppendUint16(input, uint16(port))
		input = append(input, []byte("payload")...)
		var out bytes.Buffer
		r := bufio.NewReader(bytes.NewReader(input))
		reply, err := proxyHandshake(&out, r, "service.internal", 443)
		if port == 444 {
			if err == nil || out.Bytes()[3] != 2 {
				t.Fatal("SOCKS target bypass")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if out.Len() != 2 {
			t.Fatal("success before upstream ready")
		}
		if err = reply(true); err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(r)
		if string(body) != "payload" || out.Bytes()[3] != 0 {
			t.Fatal("SOCKS handshake/payload")
		}
	}
}
