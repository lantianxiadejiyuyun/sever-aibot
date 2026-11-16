package tunnel

import (
	"io"
	"net"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
)

const (
	keepAliveInterval = 30 * time.Second
	streamWindow      = 1 << 20
)

func yamuxConfig() *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = keepAliveInterval
	cfg.ConnectionWriteTimeout = 10 * time.Second
	cfg.MaxStreamWindowSize = streamWindow
	// yamux logs to stderr by default; the control and agent loggers own
	// logging, and frame contents must not leak into stderr.
	cfg.LogOutput = io.Discard
	return cfg
}

// ClientSession starts the node side of a tunnel. The node opens one stream
// per accepted public connection.
func ClientSession(conn net.Conn) (*yamux.Session, error) {
	return yamux.Client(conn, yamuxConfig())
}

// ServerSession starts the agent side of a tunnel. It accepts streams opened
// by the node.
func ServerSession(conn net.Conn) (*yamux.Session, error) {
	return yamux.Server(conn, yamuxConfig())
}

// PipeCount is Pipe with a byte counter. It returns the total bytes copied in
// both directions, which the node reports as traffic for the forward.
func PipeCount(a, b io.ReadWriteCloser) int64 {
	var total int64
	var mu sync.Mutex
	var wg sync.WaitGroup
	cp := func(dst, src io.ReadWriteCloser) {
		defer wg.Done()
		n, _ := io.Copy(dst, src)
		mu.Lock()
		total += n
		mu.Unlock()
		_ = dst.Close()
	}
	wg.Add(2)
	go cp(a, b)
	go cp(b, a)
	wg.Wait()
	return total
}

// Pipe copies bytes in both directions and closes both sides when either
// direction ends. It is used for the public connection ↔ yamux stream leg and
// for the stream ↔ target leg.
func Pipe(a, b io.ReadWriteCloser) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(a, b)
		_ = a.Close()
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(b, a)
		_ = b.Close()
	}()
	wg.Wait()
}
