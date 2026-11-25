package tunnel

import (
	"crypto/tls"
	"net"
	"testing"
	"time"
)

func TestDirectAuthenticationRevisionAndCertificatePin(t *testing.T) {
	cert, pin, err := DirectCertificate()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, key string
		revision  int64
		pin       string
		ok        bool
	}{{"valid", "secret", 7, pin, true}, {"wrong-key", "other", 7, pin, false}, {"stale-revision", "secret", 6, pin, false}, {"wrong-pin", "secret", 7, "0000000000000000000000000000000000000000000000000000000000000000", false}} {
		t.Run(test.name, func(t *testing.T) {
			ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}})
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			result := make(chan error, 1)
			go func() {
				conn, err := ln.Accept()
				if err != nil {
					result <- err
					return
				}
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(2 * time.Second))
				result <- AcceptDirect(conn, "secret", "fwd", 7)
			}()
			conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 2 * time.Second}, "tcp", ln.Addr().String(), PinnedTLS(test.pin))
			if err == nil {
				conn.SetDeadline(time.Now().Add(2 * time.Second))
				err = ProveDirect(conn, test.key, "fwd", test.revision)
				conn.Close()
			}
			serverErr := <-result
			if test.ok && (err != nil || serverErr != nil) {
				t.Fatalf("valid direct auth: client=%v server=%v", err, serverErr)
			}
			if !test.ok && err == nil && serverErr == nil {
				t.Fatal("unauthorized direct peer accepted")
			}
		})
	}
}
func TestDirectCandidateValidation(t *testing.T) {
	for _, address := range []string{"169.254.169.254:80", "[ff02::1]:80", "0.0.0.0:80", "127.0.0.1:80", "example.com:443", "10.0.0.1:0", "[fe80::1]:443"} {
		if ValidateDirectAddress(address, false) == nil {
			t.Fatalf("accepted %s", address)
		}
	}
	for _, address := range []string{"10.0.0.1:443", "[fd00::1]:443", "203.0.113.1:443"} {
		if err := ValidateDirectAddress(address, false); err != nil {
			t.Fatalf("rejected %s: %v", address, err)
		}
	}
	if err := ValidateDirectAddress("127.0.0.1:9000", true); err != nil {
		t.Fatal(err)
	}
}
