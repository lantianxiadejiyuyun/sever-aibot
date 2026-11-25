package access

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func shellFixture(t *testing.T) (net.Listener, ssh.Signer, string) {
	t.Helper()
	shell, err := exec.LookPath("bash")
	if err != nil {
		shell = "C:/Program Files/Git/bin/bash.exe"
		if _, err = os.Stat(shell); err != nil {
			t.Skip("local bash unavailable for fixed-script fixture")
		}
	}
	dir := t.TempDir()
	unixDir := filepath.ToSlash(dir)
	if len(unixDir) > 2 && unixDir[1] == ':' {
		unixDir = "/" + strings.ToLower(unixDir[:1]) + unixDir[2:]
	}
	file := filepath.Join(dir, "aibot-managed.conf")
	if err = os.WriteFile(file, []byte("original configuration\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	cfg := &ssh.ServerConfig{PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) { return nil, nil }}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			raw, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer raw.Close()
				conn, chs, req, err := ssh.NewServerConn(raw, cfg)
				if err != nil {
					return
				}
				defer conn.Close()
				go ssh.DiscardRequests(req)
				for incoming := range chs {
					if incoming.ChannelType() != "session" {
						incoming.Reject(ssh.UnknownChannelType, "session only")
						continue
					}
					ch, requests, err := incoming.Accept()
					if err != nil {
						return
					}
					go func() {
						defer ch.Close()
						for request := range requests {
							if request.Type != "exec" {
								request.Reply(false, nil)
								continue
							}
							var p struct{ Command string }
							if ssh.Unmarshal(request.Payload, &p) != nil || !strings.HasPrefix(p.Command, "sudo -n /bin/sh -c ") {
								request.Reply(false, nil)
								return
							}
							request.Reply(true, nil)
							// The fixture maps the two fixed remote programs and allowlisted path into
							// an isolated temp directory, then executes the actual generated script.
							command := strings.TrimPrefix(p.Command, "sudo -n ")
							command = strings.ReplaceAll(command, "/etc/nginx/conf.d", unixDir)
							command = strings.ReplaceAll(command, "/usr/sbin/nginx -t", "/usr/bin/grep -q -v INVALID "+unixDir+"/aibot-managed.conf")
							command = strings.ReplaceAll(command, "/usr/bin/systemctl restart nginx", "true")
							command = strings.ReplaceAll(command, "/usr/bin/systemctl is-active --quiet nginx", "true")
							ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
							cmd := exec.CommandContext(ctx, shell, "-c", command)
							cmd.Stdin = ch
							cmd.Stdout = ch
							cmd.Stderr = ch.Stderr()
							err := cmd.Run()
							cancel()
							status := uint32(0)
							if err != nil {
								status = 1
								if exit, ok := err.(*exec.ExitError); ok {
									status = uint32(exit.ExitCode())
								}
							}
							ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
							return
						}
					}()
				}
			}()
		}
	}()
	return ln, signer, file
}
func TestFixedChangeSSHSuccessRollbackAndSnapshot(t *testing.T) {
	e, cred := testEngine(t)
	ln, key, file := shellFixture(t)
	a := createFixtureAsset(t, e, ln, cred.ID, ssh.FingerprintSHA256(key.PublicKey()), "")
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	p, err := e.PreviewChange(ctx, ChangePlan{AssetID: a.ID, Kind: "config_update", Service: "nginx", ConfigPath: "/etc/nginx/conf.d/aibot-managed.conf", Content: "new configuration\n# $(touch should-not-run)\n"})
	if err != nil {
		t.Fatal(err)
	}
	if p.BeforeContent != "original configuration\n" {
		t.Fatal("snapshot incorrect")
	}
	result, err := e.ExecuteChange(ctx, p, "fixture_success")
	if err != nil || result.Status != "succeeded" {
		t.Fatalf("execution: %+v %v", result, err)
	}
	current, _ := os.ReadFile(file)
	if string(current) != p.Content {
		t.Fatalf("config mismatch: %q", current)
	}
	backup, err := os.ReadFile(file + ".aibot-fixture_success.bak")
	if err != nil || string(backup) != p.BeforeContent {
		t.Fatalf("backup: %q %v", backup, err)
	}
	bad, err := e.PreviewChange(ctx, ChangePlan{AssetID: a.ID, Kind: "config_update", Service: "nginx", ConfigPath: p.ConfigPath, Content: "INVALID configuration\n"})
	if err != nil {
		t.Fatal(err)
	}
	result, err = e.ExecuteChange(ctx, bad, "fixture_rollback")
	if err == nil || result.Status != "rolled_back" {
		t.Fatalf("rollback: %+v %v", result, err)
	}
	current, _ = os.ReadFile(file)
	if string(current) != p.Content {
		t.Fatal("failed configuration was not restored")
	}
	if err = os.WriteFile(file, []byte("externally changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = e.ExecuteChange(ctx, bad, "fixture_conflict"); err == nil {
		t.Fatal("stale config snapshot executed")
	}
	if err = ValidateChangePlan(ChangePlan{Kind: "service_restart", Service: "nginx; touch /tmp/x"}); err == nil {
		t.Fatal("shell injection service accepted")
	}
	if err = ValidateChangePlan(ChangePlan{Kind: "config_update", Service: "nginx", ConfigPath: "/etc/passwd", Content: "bad"}); err == nil {
		t.Fatal("unregistered config path accepted")
	}
}
