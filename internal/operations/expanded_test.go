package operations

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"server-aibot/internal/access"
	"server-aibot/internal/model"
	"server-aibot/internal/store"
)

func TestForwardUsesPublicEntryAndHostHeader(t *testing.T) {
	svc, _ := fixture(t)
	ctx := context.Background()
	node, _, err := svc.Store.CreateNode(ctx, model.Node{Name: "node", Addr: "https://203.0.113.10:8443", HTTPPort: 8080})
	if err != nil {
		t.Fatal(err)
	}
	f, err := svc.Store.CreateForward(ctx, model.Forward{Name: "entry", Kind: "port", NodeID: node.ID, TargetIP: "10.1.2.3", TargetPort: 22, RemotePort: 2222, State: "applied"})
	if err != nil {
		t.Fatal(err)
	}
	c := store.OperationCheck{TargetKind: "forward", TargetID: f.ID}
	target, err := svc.Target(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if target.Host != "203.0.113.10" || target.Port != 2222 || target.Host == f.TargetIP {
		t.Fatalf("wrong public entry: %+v", target)
	}
	f.Kind = "host"
	f.Domain = "app.example"
	if _, err = svc.Store.UpdateForward(ctx, *f); err != nil {
		t.Fatal(err)
	}
	target, err = svc.Target(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if target.Host != "app.example" || target.DialHost != "203.0.113.10" || target.Port != 8080 {
		t.Fatalf("host entry=%+v", target)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "app.example:8080" {
			t.Errorf("Host=%s", r.Host)
		}
		w.WriteHeader(204)
	}))
	defer server.Close()
	dial := DialFunc(func(ctx context.Context, got Target) (net.Conn, error) {
		if got.DialHost != target.DialHost {
			t.Error("lost node entry")
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp", strings.TrimPrefix(server.URL, "http://"))
	})
	if err = probe(ctx, dial, target, store.OperationCheck{Kind: "http", Scheme: "http", Path: "/", TimeoutMS: 1000}); err != nil {
		t.Fatal(err)
	}
	if _, err = DialPublicTarget(ctx, Target{Host: "127.0.0.1", Port: 1}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("private entry should be unsupported: %v", err)
	}
}
func TestBatchPersistsScoreAndComparesSameCheckSet(t *testing.T) {
	svc, c := fixture(t)
	t.Cleanup(svc.Close)
	ctx := context.Background()
	svc.SetDialer(DialFunc(successDial))
	first, err := svc.StartBatch(ctx, "first", []string{c.ID})
	if err != nil {
		t.Fatal(err)
	}
	first = awaitBatch(t, svc, first.ID)
	if first.Score == nil || *first.Score != 100 || first.Healthy != 1 || len(first.Runs) != 1 {
		t.Fatalf("first=%+v", first)
	}
	svc.SetDialer(DialFunc(func(context.Context, Target) (net.Conn, error) { return nil, errors.New("down") }))
	second, err := svc.StartBatch(ctx, "second", []string{c.ID})
	if err != nil {
		t.Fatal(err)
	}
	second = awaitBatch(t, svc, second.ID)
	if second.Score == nil || *second.Score != 0 || second.PreviousBatchID != first.ID || second.ScoreDelta == nil || *second.ScoreDelta != -100 {
		t.Fatalf("comparison=%+v", second)
	}
	if _, err = svc.StartBatch(ctx, "duplicate", []string{c.ID, c.ID}); err == nil {
		t.Fatal("accepted duplicate check ids")
	}
	other := c
	other.Name = "different"
	other, err = svc.Store.SaveOperationCheck(ctx, other, true)
	if err != nil {
		t.Fatal(err)
	}
	third, err := svc.StartBatch(ctx, "different set", []string{other.ID})
	if err != nil {
		t.Fatal(err)
	}
	third = awaitBatch(t, svc, third.ID)
	if third.PreviousBatchID != "" {
		t.Fatal("compared different check sets")
	}
}
func awaitBatch(t *testing.T, svc *Service, id string) store.InspectionBatch {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b, err := svc.Store.GetInspectionBatch(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if b.State != "running" { // wait until service releases its one-batch slot too
			svc.lifecycle.Lock()
			active := len(svc.batchCancels)
			svc.lifecycle.Unlock()
			if active == 0 {
				return b
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("batch did not finish")
	return store.InspectionBatch{}
}
func TestSSHFixedProfilesPinnedKeysAndEvidenceLimit(t *testing.T) {
	svc, _ := fixture(t)
	ctx := context.Background()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var executed atomic.Int32
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)
	go func() {
		for {
			raw, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer raw.Close()
				conn, channels, requests, err := ssh.NewServerConn(raw, cfg)
				if err != nil {
					return
				}
				defer conn.Close()
				go ssh.DiscardRequests(requests)
				for incoming := range channels {
					ch, reqs, err := incoming.Accept()
					if err != nil {
						continue
					}
					go func() {
						defer ch.Close()
						for req := range reqs {
							if req.Type != "exec" {
								req.Reply(false, nil)
								continue
							}
							var input struct{ Command string }
							if ssh.Unmarshal(req.Payload, &input) != nil {
								return
							}
							executed.Add(1)
							allowed := false
							for _, commands := range sshProfiles {
								for _, command := range commands {
									if command.command == input.Command {
										allowed = true
									}
								}
							}
							if !allowed {
								t.Errorf("unexpected command %q", input.Command)
								req.Reply(false, nil)
								return
							}
							req.Reply(true, nil)
							output := "ok\n"
							code := uint32(0)
							if strings.Contains(input.Command, "systemctl") {
								output = "failed.service loaded failed failed\n"
							}
							if strings.HasPrefix(input.Command, "docker") {
								output = strings.Repeat("x", 17<<10)
							}
							if strings.Contains(input.Command, "iptables") {
								code = 1
							}
							io.WriteString(ch, output)
							ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{code}))
							return
						}
					}()
				}
			}()
		}
	}()
	key, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := svc.Store.CreateCredential(ctx, svc.Vault, store.CredentialInput{Name: "test", Kind: "ssh_key", Username: "test", Secret: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}))})
	if err != nil {
		t.Fatal(err)
	}
	host, portText, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	asset, err := svc.Store.CreateAsset(ctx, store.Asset{Name: "local SSH mock", Address: host, Port: port, Protocol: "ssh", Enabled: true, HostKey: ssh.FingerprintSHA256(signer.PublicKey()), CredentialID: credential.ID})
	if err != nil {
		t.Fatal(err)
	}
	engine := &access.Engine{Store: svc.Store, Vault: svc.Vault}
	for _, test := range []struct{ profile, state string }{{"system", "up"}, {"services", "down"}, {"docker", "stale"}, {"firewall", "stale"}} {
		probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		state, _, detail := InspectSSH(probeCtx, engine, store.OperationCheck{TargetID: asset.ID, Profile: test.profile})
		cancel()
		if state != test.state || detail.Source != "ssh" || len(detail.Evidence) == 0 {
			t.Fatalf("%s state=%s detail=%+v", test.profile, state, detail)
		}
		for _, e := range detail.Evidence {
			if len(e.Output) > 16<<10 {
				t.Fatal("unbounded SSH output")
			}
		}
	}
	before := executed.Load()
	state, _, _ := InspectSSH(ctx, engine, store.OperationCheck{TargetID: asset.ID, Profile: "system; touch /tmp/injected"})
	if state != "stale" || executed.Load() != before {
		t.Fatal("untrusted profile executed")
	}
	asset.HostKey = "SHA256:wrong"
	if _, err = svc.Store.UpdateAsset(ctx, *asset); err != nil {
		t.Fatal(err)
	}
	state, _, _ = InspectSSH(ctx, engine, store.OperationCheck{TargetID: asset.ID, Profile: "system"})
	if state != "stale" || executed.Load() != before {
		t.Fatal("unpinned SSH executed")
	}
}
