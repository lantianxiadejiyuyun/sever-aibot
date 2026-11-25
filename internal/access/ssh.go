package access

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"golang.org/x/crypto/ssh"
	"server-aibot/internal/store"
	"server-aibot/internal/vault"
)

type Engine struct {
	Store  *store.Store
	Vault  *vault.Vault
	Broker *Broker
}
type Connection struct {
	net.Conn
	clients []*ssh.Client
}

func (c *Connection) Close() error {
	err := c.Conn.Close()
	for i := len(c.clients) - 1; i >= 0; i-- {
		c.clients[i].Close()
	}
	return err
}
func (e *Engine) chain(ctx context.Context, id string) ([]*store.Asset, error) {
	if id == "" {
		return nil, errors.New("asset ID required")
	}
	var chain []*store.Asset
	seen := map[string]bool{}
	for id != "" {
		if seen[id] || len(chain) >= 4 {
			return nil, errors.New("asset route cycle or too many hops")
		}
		seen[id] = true
		a, err := e.Store.GetAsset(ctx, id)
		if err != nil {
			return nil, err
		}
		if !a.Enabled {
			return nil, errors.New("asset disabled")
		}
		if a.ViaAssetID != "" && a.ViaDeviceID != "" {
			return nil, errors.New("ambiguous agent and SSH jump route")
		}
		chain = append(chain, a)
		id = a.ViaAssetID
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain, nil
}
func addr(a *store.Asset) string { return net.JoinHostPort(a.Address, strconv.Itoa(a.Port)) }
func (e *Engine) Dial(ctx context.Context, id string) (*Connection, *store.Asset, error) {
	chain, err := e.chain(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	entry := chain[0]
	var raw net.Conn
	if entry.ViaDeviceID != "" {
		raw, err = e.Broker.Dial(ctx, entry.ViaDeviceID, DialRequest{entry.ID, entry.Address, entry.Port})
	} else {
		raw, err = (&net.Dialer{}).DialContext(ctx, "tcp", addr(entry))
	}
	if err != nil {
		return nil, nil, err
	}
	c := &Connection{Conn: raw}
	for i := 0; i < len(chain)-1; i++ {
		client, err := e.authenticate(ctx, c.Conn, chain[i])
		if err != nil {
			c.Close()
			return nil, nil, err
		}
		c.clients = append(c.clients, client)
		next, err := client.DialContext(ctx, "tcp", addr(chain[i+1]))
		if err != nil {
			c.Close()
			return nil, nil, err
		}
		c.Conn = next
	}
	return c, chain[len(chain)-1], nil
}
func PinnedCallback(pin string) ssh.HostKeyCallback {
	return func(_ string, _ net.Addr, k ssh.PublicKey) error {
		if pin == "" {
			return errors.New("SSH host key confirmation required")
		}
		if ssh.FingerprintSHA256(k) != pin {
			return errors.New("SSH host key does not match pinned fingerprint")
		}
		return nil
	}
}
func ValidatePrivateKey(secret []byte) error {
	_, err := ssh.ParsePrivateKey(secret)
	if err != nil {
		return errors.New("invalid or encrypted SSH private key; import an unencrypted supported key")
	}
	return nil
}
func (e *Engine) authenticate(ctx context.Context, c net.Conn, a *store.Asset) (*ssh.Client, error) {
	if a.Protocol != "ssh" {
		return nil, errors.New("SSH protocol required")
	}
	if a.HostKey == "" {
		return nil, errors.New("SSH host key confirmation required")
	}
	secret, err := e.Store.RevealCredential(ctx, e.Vault, a.CredentialID)
	if err != nil {
		return nil, errors.New("SSH credential unavailable")
	}
	defer func() {
		for i := range secret.Plaintext {
			secret.Plaintext[i] = 0
		}
	}()
	var auth ssh.AuthMethod
	switch secret.Kind {
	case "ssh_password":
		auth = ssh.Password(string(secret.Plaintext))
	case "ssh_key":
		signer, err := ssh.ParsePrivateKey(secret.Plaintext)
		if err != nil {
			return nil, errors.New("invalid SSH private key")
		}
		auth = ssh.PublicKeys(signer)
	default:
		return nil, errors.New("credential is not an SSH credential")
	}
	if secret.Username == "" {
		return nil, errors.New("SSH username required")
	}
	client, err := handshake(ctx, c, addr(a), &ssh.ClientConfig{User: secret.Username, Auth: []ssh.AuthMethod{auth}, HostKeyCallback: PinnedCallback(a.HostKey)})
	if err == nil {
		_ = e.Store.TouchCredentialUsed(ctx, a.CredentialID)
	}
	return client, err
}
func handshake(ctx context.Context, c net.Conn, address string, cfg *ssh.ClientConfig) (*ssh.Client, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	deadline := time.Now().Add(15 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = c.SetDeadline(deadline)
	conn, ch, req, err := ssh.NewClientConn(c, address, cfg)
	if err != nil {
		return nil, fmt.Errorf("SSH handshake: %w", err)
	}
	_ = c.SetDeadline(time.Time{})
	return ssh.NewClient(conn, ch, req), nil
}
func (e *Engine) SSH(ctx context.Context, id string) (*ssh.Client, *Connection, error) {
	c, a, err := e.Dial(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	client, err := e.authenticate(ctx, c.Conn, a)
	if err != nil {
		c.Close()
		return nil, nil, err
	}
	return client, c, nil
}

type Observation struct {
	Fingerprint string `json:"fingerprint"`
	Algorithm   string `json:"algorithm"`
}

func (e *Engine) Discover(ctx context.Context, id string) (Observation, error) {
	c, a, err := e.Dial(ctx, id)
	if err != nil {
		return Observation{}, err
	}
	defer c.Close()
	if a.Protocol != "ssh" {
		return Observation{}, errors.New("SSH protocol required")
	}
	var o Observation
	stop := errors.New("host key observed; authentication prohibited")
	_, err = handshake(ctx, c.Conn, addr(a), &ssh.ClientConfig{User: "hostkey-discovery", HostKeyCallback: func(_ string, _ net.Addr, k ssh.PublicKey) error {
		o = Observation{ssh.FingerprintSHA256(k), k.Type()}
		return stop
	}})
	if o.Fingerprint != "" {
		return o, nil
	}
	return o, err
}
