package access

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"
	"server-aibot/internal/relay"
	"server-aibot/internal/store"
	"server-aibot/internal/vault"
)

func sshFixture(t *testing.T) (net.Listener, ssh.Signer, *atomic.Int64) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	count := &atomic.Int64{}
	cfg := &ssh.ServerConfig{PasswordCallback: func(c ssh.ConnMetadata, b []byte) (*ssh.Permissions, error) {
		count.Add(1)
		if c.User() == "tester" && string(b) == "fixture-secret" {
			return nil, nil
		}
		return nil, fmt.Errorf("denied")
	}}
	cfg.AddHostKey(signer)
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
					if incoming.ChannelType() != "direct-tcpip" {
						incoming.Reject(ssh.UnknownChannelType, "unsupported")
						continue
					}
					var d struct {
						Host       string
						Port       uint32
						Origin     string
						OriginPort uint32
					}
					if ssh.Unmarshal(incoming.ExtraData(), &d) != nil {
						incoming.Reject(ssh.ConnectionFailed, "bad request")
						continue
					}
					up, err := net.Dial("tcp", net.JoinHostPort(d.Host, strconv.Itoa(int(d.Port))))
					if err != nil {
						incoming.Reject(ssh.ConnectionFailed, "dial")
						continue
					}
					ch, rq, err := incoming.Accept()
					if err != nil {
						up.Close()
						continue
					}
					go ssh.DiscardRequests(rq)
					go func() {
						defer ch.Close()
						defer up.Close()
						go func() { io.Copy(up, ch); up.Close() }()
						io.Copy(ch, up)
					}()
				}
			}()
		}
	}()
	return ln, signer, count
}
func testEngine(t *testing.T) (*Engine, *store.CredentialMeta) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "access.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	v, err := vault.New([]byte(strings.Repeat("a", 32)))
	if err != nil {
		t.Fatal(err)
	}
	cred, err := st.CreateCredential(context.Background(), v, store.CredentialInput{Name: "SSH", Kind: "ssh_" + "password", Username: "tester", Secret: "fixture-secret"})
	if err != nil {
		t.Fatal(err)
	}
	return &Engine{Store: st, Vault: v, Broker: NewBroker()}, cred
}
func createFixtureAsset(t *testing.T, e *Engine, ln net.Listener, cred, pin, jump string) *store.Asset {
	t.Helper()
	host, p, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(p)
	a, err := e.Store.CreateAsset(context.Background(), store.Asset{Name: "fixture", Address: host, Port: port, Protocol: "ssh", Enabled: true, CredentialID: cred, HostKey: pin, ViaAssetID: jump})
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func TestDiscoveryPinAndMultiHop(t *testing.T) {
	e, cred := testEngine(t)
	ln, signer, auths := sshFixture(t)
	a := createFixtureAsset(t, e, ln, cred.ID, "", "")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	obs, err := e.Discover(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs.Fingerprint != ssh.FingerprintSHA256(signer.PublicKey()) {
		t.Fatal("wrong observation")
	}
	if auths.Load() != 0 {
		t.Fatal("discovery attempted authentication")
	}
	stored, _ := e.Store.GetAsset(ctx, a.ID)
	if stored.HostKey != "" {
		t.Fatal("discovery pinned implicitly")
	}
	if _, _, err := e.SSH(ctx, a.ID); err == nil {
		t.Fatal("unpinned host accepted")
	}
	a.HostKey = "SHA256:wrong"
	e.Store.UpdateAsset(ctx, *a)
	if _, _, err := e.SSH(ctx, a.ID); err == nil {
		t.Fatal("wrong pin accepted")
	}
	if auths.Load() != 0 {
		t.Fatal("mismatched host received credentials")
	}
	a.HostKey = obs.Fingerprint
	if _, err := e.Store.UpdateAsset(ctx, *a); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		next, sign, _ := sshFixture(t)
		a = createFixtureAsset(t, e, next, cred.ID, ssh.FingerprintSHA256(sign.PublicKey()), a.ID)
	}
	client, chain, err := e.SSH(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	client.Close()
	chain.Close()
	if auths.Load() != 1 {
		t.Fatalf("first jump authentication count %d", auths.Load())
	}
}
func TestPrivateKeyValidation(t *testing.T) {
	if ValidatePrivateKey([]byte("fake private key")) == nil {
		t.Fatal("invalid key accepted")
	}
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	block, err := ssh.MarshalPrivateKey(priv, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if err = ValidatePrivateKey(pem.EncodeToMemory(block)); err != nil {
		t.Fatal(err)
	}
}
func TestAgentReverseDialUsesPolicy(t *testing.T) {
	ln, _, _ := sshFixture(t)
	_, portText, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portText)
	broker := NewBroker()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		_ = broker.Serve(ctx, "device", websocket.NetConn(ctx, c, websocket.MessageBinary))
	}))
	defer srv.Close()
	policy, _ := relay.NewPolicy(relay.Config{AllowLoopbackPorts: []int{port}})
	go RunAgent(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), "device", "key", policy)
	deadline := time.After(3 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		broker.mu.Lock()
		ready := broker.agents["device"] != nil
		broker.mu.Unlock()
		if ready {
			break
		}
		select {
		case <-deadline:
			t.Fatal("agent did not connect")
		case <-ticker.C:
		}
	}
	dialCtx, stop := context.WithTimeout(ctx, 3*time.Second)
	defer stop()
	c, err := broker.Dial(dialCtx, "device", DialRequest{"registered", "127.0.0.1", port})
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatal(err)
	}
	c.Close()
	if string(buf) != "SSH-" {
		t.Fatalf("no SSH banner: %q", buf)
	}
	if c, err := broker.Dial(dialCtx, "device", DialRequest{"registered", "169.254.169.254", 80}); err == nil {
		c.Close()
		t.Fatal("metadata accepted")
	}
	if _, err := broker.Dial(dialCtx, "offline", DialRequest{"registered", "127.0.0.1", port}); err == nil {
		t.Fatal("offline agent fell back to direct")
	}
}
