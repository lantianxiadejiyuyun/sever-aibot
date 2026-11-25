package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"
	"server-aibot/internal/store"
)

func terminalFixture(t *testing.T) (net.Listener, ssh.Signer, *atomic.Int64, *atomic.Int64) {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	cfg := &ssh.ServerConfig{PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) { return nil, nil }}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	ptys, resizes := &atomic.Int64{}, &atomic.Int64{}
	go func() {
		for {
			raw, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer raw.Close()
				c, chs, req, err := ssh.NewServerConn(raw, cfg)
				if err != nil {
					return
				}
				defer c.Close()
				go ssh.DiscardRequests(req)
				for incoming := range chs {
					if incoming.ChannelType() != "session" {
						incoming.Reject(ssh.UnknownChannelType, "unsupported")
						continue
					}
					ch, requests, err := incoming.Accept()
					if err != nil {
						return
					}
					go func() {
						defer ch.Close()
						for req := range requests {
							switch req.Type {
							case "pty-req":
								ptys.Add(1)
								req.Reply(true, nil)
							case "window-change":
								resizes.Add(1)
								req.Reply(true, nil)
							case "shell":
								req.Reply(true, nil)
								go func() { ch.Write([]byte("ready\r\n")); io.Copy(ch, ch) }()
							default:
								req.Reply(false, nil)
							}
						}
					}()
				}
			}()
		}
	}()
	return ln, signer, ptys, resizes
}
func TestAccessTerminalPTYResizeRecordTerminate(t *testing.T) {
	e := newTestEnv(t)
	ln, key, ptys, resizes := terminalFixture(t)
	resp := e.do(t, "POST", "/api/v1/credentials", map[string]any{"name": "terminal", "kind": "ssh_" + "password", "username": "tester", "secret": "fixture-secret"}, true)
	if resp.StatusCode != 201 {
		t.Fatalf("credential: %d", resp.StatusCode)
	}
	cred := decode[store.CredentialMeta](t, resp)
	host, p, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(p)
	a, err := e.store.CreateAsset(context.Background(), store.Asset{Name: "terminal", Address: host, Port: port, Protocol: "ssh", Enabled: true, CredentialID: cred.ID, HostKey: ssh.FingerprintSHA256(key.PublicKey())})
	if err != nil {
		t.Fatal(err)
	}
	resp = e.do(t, "POST", "/api/v1/access/sessions", map[string]string{"assetId": a.ID}, true)
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("step-up bypass: %d", resp.StatusCode)
	}
	resp = e.do(t, "POST", "/api/v1/auth/step-up", map[string]string{"password": e.password}, true)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("step-up: %d", resp.StatusCode)
	}
	resp = e.do(t, "POST", "/api/v1/assets/"+a.ID+"/hostkey/discover", map[string]any{}, true)
	if resp.StatusCode != 200 {
		t.Fatalf("discovery route: %d", resp.StatusCode)
	}
	resp.Body.Close()
	resp = e.do(t, "POST", "/api/v1/access/sessions", map[string]string{"assetId": a.ID}, true)
	if resp.StatusCode != 201 {
		t.Fatalf("session: %d", resp.StatusCode)
	}
	row := decode[store.AccessSession](t, resp)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	u, _ := url.Parse(e.srv.URL)
	h := http.Header{"Origin": []string{e.srv.URL}}
	for _, c := range e.client.Jar.Cookies(u) {
		h.Add("Cookie", c.Name+"="+c.Value)
	}
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(e.srv.URL, "http")+"/api/v1/access/sessions/"+row.ID+"/ws", &websocket.DialOptions{HTTPHeader: h})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	hello, _ := json.Marshal(map[string]string{"csrf": e.csrf})
	if err = ws.Write(ctx, websocket.MessageText, hello); err != nil {
		t.Fatal(err)
	}
	if _, data, err := ws.Read(ctx); err != nil || !strings.Contains(string(data), "ready") {
		t.Fatalf("terminal ready: %q %v", data, err)
	}
	ws.Write(ctx, websocket.MessageText, []byte(`{"type":"resize","cols":90,"rows":40}`))
	ws.Write(ctx, websocket.MessageBinary, []byte("interactive-input\n"))
	if _, data, err := ws.Read(ctx); err != nil || !strings.Contains(string(data), "interactive-input") {
		t.Fatalf("terminal echo: %q %v", data, err)
	}
	resp = e.do(t, "POST", "/api/v1/access/sessions/"+row.ID+"/terminate", map[string]any{}, true)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("terminate: %d", resp.StatusCode)
	}
	_, _, _ = ws.Read(ctx)
	deadline := time.After(2 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		saved, err := e.store.GetAccessSession(context.Background(), row.ID)
		if err != nil {
			t.Fatal(err)
		}
		if saved.EndedAt != "" {
			if saved.Result != "killed" || saved.BytesIn == 0 || saved.BytesOut == 0 {
				t.Fatalf("history: %+v", saved)
			}
			break
		}
		select {
		case <-deadline:
			t.Fatal("session not finalized")
		case <-tick.C:
		}
	}
	if ptys.Load() != 1 || resizes.Load() != 1 {
		t.Fatalf("PTY=%d resize=%d", ptys.Load(), resizes.Load())
	}
	resp = e.do(t, "GET", "/api/v1/access/sessions/"+row.ID+"/recording", nil, false)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(body), "interactive-input") || !strings.Contains(string(body), `"version":2`) {
		t.Fatalf("recording: %d %s", resp.StatusCode, body)
	}
}
