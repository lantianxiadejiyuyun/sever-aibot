package tunnel

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"time"
)

// NATCredential is delivered exclusively over the authenticated control stream.
// UDP packets carry an ID and a MAC, never the credential key.
type NATCredential struct {
	ID          string `json:"id"`
	Key         string `json:"key"`
	Fingerprint string `json:"fingerprint,omitempty"`
}
type natPacket struct {
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	Nonce   string `json:"nonce"`
	Address string `json:"address,omitempty"`
	MAC     string `json:"mac"`
}

func randomNATToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func encodeNAT(p natPacket, key string) []byte {
	p.MAC = ""
	body, _ := json.Marshal(p)
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write(body)
	p.MAC = hex.EncodeToString(mac.Sum(nil))
	body, _ = json.Marshal(p)
	return append([]byte{0}, body...)
}
func decodeNAT(b []byte, key string) (natPacket, bool) {
	var p natPacket
	if len(b) < 2 || b[0] != 0 || json.Unmarshal(b[1:], &p) != nil {
		return p, false
	}
	signature := p.MAC
	p.MAC = ""
	body, _ := json.Marshal(p)
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write(body)
	return p, hmac.Equal([]byte(signature), []byte(hex.EncodeToString(mac.Sum(nil))))
}

type rendezvousRegistration struct {
	credential NATCredential
	group      string
	provider   bool
	expires    time.Time
	valid      func() bool
	address    net.Addr
	nonce      string
	seen       time.Time
}

// Rendezvous only exchanges observed UDP addresses. It never relays payloads.
// Each credential is scoped to an authenticated live tunnel and exact revision.
type Rendezvous struct {
	conn          net.PacketConn
	mu            sync.Mutex
	registrations map[string]*rendezvousRegistration
	done          chan struct{}
}

func NewRendezvous(conn net.PacketConn) *Rendezvous {
	r := &Rendezvous{conn: conn, registrations: map[string]*rendezvousRegistration{}, done: make(chan struct{})}
	go r.serve()
	return r
}
func (r *Rendezvous) Close() error { err := r.conn.Close(); <-r.done; return err }
func (r *Rendezvous) Issue(group string, provider bool, pin string, valid func() bool) (NATCredential, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	for id, entry := range r.registrations {
		if now.After(entry.expires) || !entry.valid() {
			delete(r.registrations, id)
		}
	}
	if len(r.registrations) >= 4096 {
		return NATCredential{}, errors.New("rendezvous capacity reached")
	}
	credential := NATCredential{ID: randomNATToken(), Key: randomNATToken(), Fingerprint: pin}
	ttl := 30 * time.Second
	if provider {
		ttl = 55 * time.Minute
		for id, e := range r.registrations {
			if e.group == group && e.provider {
				delete(r.registrations, id)
			}
		}
	}
	r.registrations[credential.ID] = &rendezvousRegistration{credential: credential, group: group, provider: provider, expires: now.Add(ttl), valid: valid}
	return credential, nil
}
func (r *Rendezvous) ProviderPin(group string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.registrations {
		if e.group == group && e.provider && time.Now().Before(e.expires) && e.valid() {
			return e.credential.Fingerprint
		}
	}
	return ""
}
func (r *Rendezvous) serve() {
	defer close(r.done)
	buf := make([]byte, 2048)
	for {
		n, addr, err := r.conn.ReadFrom(buf)
		if err != nil {
			return
		}
		r.receive(buf[:n], addr)
	}
}
func (r *Rendezvous) receive(b []byte, addr net.Addr) {
	if len(b) < 2 || b[0] != 0 {
		return
	}
	var header natPacket
	if json.Unmarshal(b[1:], &header) != nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.registrations[header.ID]
	now := time.Now()
	if e == nil || now.After(e.expires) || !e.valid() {
		return
	}
	p, ok := decodeNAT(b, e.credential.Key)
	if !ok || p.Kind != "register" || len(p.Nonce) != 64 {
		return
	}
	// Do not amplify packets from unknown addresses. An authenticated registration
	// fixes a token to its first observed endpoint for its short lifetime.
	if e.address != nil && e.address.String() != addr.String() {
		return
	}
	if now.Sub(e.seen) < 50*time.Millisecond {
		return
	}
	e.address = addr
	e.nonce = p.Nonce
	e.seen = now
	for _, peer := range r.registrations {
		if peer.group != e.group || peer.provider == e.provider || peer.address == nil || now.After(peer.expires) || now.Sub(peer.seen) > 20*time.Second || !peer.valid() {
			continue
		}
		for _, pair := range [][2]*rendezvousRegistration{{e, peer}, {peer, e}} {
			dst, other := pair[0], pair[1]
			packet := encodeNAT(natPacket{Kind: "peer", ID: dst.credential.ID, Nonce: dst.nonce, Address: other.address.String()}, dst.credential.Key)
			_, _ = r.conn.WriteTo(packet, dst.address)
		}
	}
}
