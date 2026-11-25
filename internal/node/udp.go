package node

import (
	"errors"
	"net"
	"sync"
	"time"
)

// udpPeer maintains one association per public source address. Its idle timeout
// bounds resources when UDP clients disappear without a close handshake.
type udpPeer struct {
	socket   *net.UDPConn
	remote   *net.UDPAddr
	incoming chan []byte
	done     chan struct{}
	once     sync.Once
}

func (p *udpPeer) Read(b []byte) (int, error) {
	timer := time.NewTimer(60 * time.Second)
	defer timer.Stop()
	select {
	case packet := <-p.incoming:
		if len(packet) > len(b) {
			return 0, errors.New("UDP buffer too small")
		}
		return copy(b, packet), nil
	case <-p.done:
		return 0, net.ErrClosed
	case <-timer.C:
		return 0, errors.New("UDP association idle timeout")
	}
}
func (p *udpPeer) Write(b []byte) (int, error) {
	select {
	case <-p.done:
		return 0, net.ErrClosed
	default:
		return p.socket.WriteToUDP(b, p.remote)
	}
}
func (p *udpPeer) Close() error                     { p.once.Do(func() { close(p.done) }); return nil }
func (p *udpPeer) LocalAddr() net.Addr              { return p.socket.LocalAddr() }
func (p *udpPeer) RemoteAddr() net.Addr             { return p.remote }
func (p *udpPeer) SetDeadline(time.Time) error      { return nil }
func (p *udpPeer) SetReadDeadline(time.Time) error  { return nil }
func (p *udpPeer) SetWriteDeadline(time.Time) error { return nil }

func (r *Runtime) serveUDP(l *portListener) {
	defer r.wg.Done()
	peers := map[string]*udpPeer{}
	var mu sync.Mutex
	defer func() {
		mu.Lock()
		defer mu.Unlock()
		for _, p := range peers {
			p.Close()
		}
	}()
	buf := make([]byte, 65535)
	for {
		n, remote, err := l.packet.ReadFromUDP(buf)
		if err != nil {
			return
		}
		key := remote.String()
		mu.Lock()
		p := peers[key]
		if p == nil {
			// Hard ceiling also applies while tunnels are unavailable.
			if len(peers) >= 4096 {
				mu.Unlock()
				continue
			}
			p = &udpPeer{socket: l.packet, remote: remote, incoming: make(chan []byte, 32), done: make(chan struct{})}
			peers[key] = p
			go func(p *udpPeer, key string) {
				r.handleEntry(p, l.port)
				p.Close()
				mu.Lock()
				if peers[key] == p {
					delete(peers, key)
				}
				mu.Unlock()
			}(p, key)
		}
		packet := append([]byte(nil), buf[:n]...)
		select {
		case p.incoming <- packet:
		default:
		} // UDP overload drops datagrams.
		mu.Unlock()
	}
}
