package tunnel

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

// DatagramConn transports UDP payloads over a multiplexed reliable stream.
// The length prefix preserves datagram boundaries, including empty datagrams.
// This is a UDP relay, not a TCP connection to the UDP target.
type DatagramConn struct {
	net.Conn
	writeMu sync.Mutex
}

func (c *DatagramConn) Read(p []byte) (int, error) {
	var header [2]byte
	if _, err := io.ReadFull(c.Conn, header[:]); err != nil {
		return 0, err
	}
	n := int(binary.BigEndian.Uint16(header[:]))
	if n > len(p) {
		_, err := io.CopyN(io.Discard, c.Conn, int64(n))
		if err != nil {
			return 0, err
		}
		return 0, io.ErrShortBuffer
	}
	_, err := io.ReadFull(c.Conn, p[:n])
	return n, err
}
func (c *DatagramConn) Write(p []byte) (int, error) {
	if len(p) > 65507 {
		return 0, errors.New("UDP datagram exceeds 65507 bytes")
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	var header [2]byte
	binary.BigEndian.PutUint16(header[:], uint16(len(p)))
	if _, err := c.Conn.Write(header[:]); err != nil {
		return 0, err
	}
	n, err := c.Conn.Write(p)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	return n, err
}

// PipeDatagrams preserves even zero-length UDP packets. Idle target reads close
// abandoned associations, and either leg ending releases the other leg.
func PipeDatagrams(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	cp := func(dst, src net.Conn) {
		defer wg.Done()
		defer a.Close()
		defer b.Close()
		buf := make([]byte, 65535)
		for {
			_ = src.SetReadDeadline(time.Now().Add(60 * time.Second))
			n, err := src.Read(buf)
			if err != nil {
				return
			}
			_ = dst.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if _, err = dst.Write(buf[:n]); err != nil {
				return
			}
		}
	}
	go cp(a, b)
	go cp(b, a)
	wg.Wait()
}
