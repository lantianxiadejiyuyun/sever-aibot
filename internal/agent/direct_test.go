package agent

import (
	"encoding/json"
	"net"
	"server-aibot/internal/model"
	"server-aibot/internal/tunnel"
	"testing"
	"time"
)

func TestDirectAttemptBudget(t *testing.T) {
	for _, phase := range []string{"lookup", "tls"} {
		t.Run(phase, func(t *testing.T) {
			a, b := net.Pipe()
			visitor, err := tunnel.ServerSession(a)
			if err != nil {
				t.Fatal(err)
			}
			defer visitor.Close()
			node, err := tunnel.ClientSession(b)
			if err != nil {
				t.Fatal(err)
			}
			defer node.Close()
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			done := make(chan struct{})
			defer close(done)
			go func() {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				<-done // Deliberately never complete TLS.
			}()
			go func() {
				stream, err := node.AcceptStream()
				if err != nil {
					return
				}
				defer stream.Close()
				if _, err := tunnel.ReadForwardID(stream); err != nil {
					return
				}
				if phase == "tls" {
					_ = tunnel.WriteResult(stream, tunnel.StatusOK, "")
					_ = json.NewEncoder(stream).Encode(tunnel.DirectCandidates{Addresses: []string{ln.Addr().String()}})
				}
				<-done // Lookup response is deliberately withheld in the other case.
			}()
			start := time.Now()
			m := &tunnelManager{}
			conn, err := m.dialDirect(visitor, model.TunnelForward{ID: "f", DirectAddress: ln.Addr().String()})
			if conn != nil {
				conn.Close()
			}
			if err == nil {
				t.Fatal("stalled direct attempt succeeded")
			}
			if elapsed := time.Since(start); elapsed > 2200*time.Millisecond {
				t.Fatalf("direct budget exceeded: %v", elapsed)
			}
		})
	}
}
