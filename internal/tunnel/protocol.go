package tunnel

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Magic starts every tunnel connection, before the JSON auth frame.
const Magic = "SAIBOT01"

const (
	// MaxAuthFrame bounds the auth JSON.
	MaxAuthFrame = 4096
	// MaxForwardID bounds a stream header forward identifier.
	MaxForwardID = 256
	// MaxMessage bounds an error message in a status frame.
	MaxMessage = 1024
)

// Status codes are used in handshake and per-stream result frames.
const (
	StatusOK             byte = 0
	StatusUnauthorized   byte = 1
	StatusBadRequest     byte = 2
	StatusDenied         byte = 3
	StatusDialFailed     byte = 4
	StatusUnknownForward byte = 5
	StatusNoSession      byte = 6
)

var (
	// ErrProtocol is wrapped when a peer sends malformed tunnel data.
	ErrProtocol = errors.New("tunnel: protocol error")
)

// AuthRequest is sent by the agent immediately after Magic.
type AuthRequest struct {
	DeviceID string `json:"deviceId"`
	Ticket   string `json:"ticket"`
}

// WriteAuth sends Magic and the auth JSON frame.
func WriteAuth(w io.Writer, req AuthRequest) error {
	if req.DeviceID == "" || req.Ticket == "" {
		return fmt.Errorf("%w: empty device id or ticket", ErrProtocol)
	}
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	if len(body) > MaxAuthFrame {
		return fmt.Errorf("%w: auth frame too large", ErrProtocol)
	}
	buf := make([]byte, 0, len(Magic)+4+len(body))
	buf = append(buf, Magic...)
	buf = binary.BigEndian.AppendUint32(buf, uint32(len(body)))
	buf = append(buf, body...)
	return writeAll(w, buf)
}

// ReadAuth reads and validates the initial auth frame.
func ReadAuth(r io.Reader) (AuthRequest, error) {
	var req AuthRequest
	magic := make([]byte, len(Magic))
	if _, err := io.ReadFull(r, magic); err != nil {
		return req, fmt.Errorf("%w: magic: %v", ErrProtocol, err)
	}
	if string(magic) != Magic {
		return req, fmt.Errorf("%w: bad magic", ErrProtocol)
	}
	var size [4]byte
	if _, err := io.ReadFull(r, size[:]); err != nil {
		return req, fmt.Errorf("%w: length: %v", ErrProtocol, err)
	}
	n := binary.BigEndian.Uint32(size[:])
	if n == 0 || n > MaxAuthFrame {
		return req, fmt.Errorf("%w: auth length %d", ErrProtocol, n)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return req, fmt.Errorf("%w: body: %v", ErrProtocol, err)
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return req, fmt.Errorf("%w: auth JSON", ErrProtocol)
	}
	if req.DeviceID == "" || req.Ticket == "" {
		return req, fmt.Errorf("%w: empty device id or ticket", ErrProtocol)
	}
	return req, nil
}

// WriteResult sends a one-byte status and an optional message. A zero status
// carries no message.
func WriteResult(w io.Writer, status byte, message string) error {
	buf := []byte{status}
	if status != StatusOK {
		msg := []byte(message)
		if len(msg) > MaxMessage {
			msg = msg[:MaxMessage]
		}
		buf = binary.BigEndian.AppendUint32(buf, uint32(len(msg)))
		buf = append(buf, msg...)
	}
	return writeAll(w, buf)
}

// ReadResult reads a status frame.
func ReadResult(r io.Reader) (byte, string, error) {
	var status [1]byte
	if _, err := io.ReadFull(r, status[:]); err != nil {
		return 0, "", fmt.Errorf("%w: status: %v", ErrProtocol, err)
	}
	if status[0] == StatusOK {
		return status[0], "", nil
	}
	var size [4]byte
	if _, err := io.ReadFull(r, size[:]); err != nil {
		return status[0], "", fmt.Errorf("%w: message length: %v", ErrProtocol, err)
	}
	n := binary.BigEndian.Uint32(size[:])
	if n > MaxMessage {
		return status[0], "", fmt.Errorf("%w: message length %d", ErrProtocol, n)
	}
	msg := make([]byte, n)
	if _, err := io.ReadFull(r, msg); err != nil {
		return status[0], "", fmt.Errorf("%w: message: %v", ErrProtocol, err)
	}
	return status[0], string(msg), nil
}

// WriteForwardID sends the stream header that selects a forward.
func WriteForwardID(w io.Writer, forwardID string) error {
	if forwardID == "" || len(forwardID) > MaxForwardID {
		return fmt.Errorf("%w: forward id length %d", ErrProtocol, len(forwardID))
	}
	buf := make([]byte, 0, 2+len(forwardID))
	buf = binary.BigEndian.AppendUint16(buf, uint16(len(forwardID)))
	buf = append(buf, forwardID...)
	return writeAll(w, buf)
}

// ReadForwardID reads a stream header.
func ReadForwardID(r io.Reader) (string, error) {
	var size [2]byte
	if _, err := io.ReadFull(r, size[:]); err != nil {
		return "", fmt.Errorf("%w: forward id length: %v", ErrProtocol, err)
	}
	n := binary.BigEndian.Uint16(size[:])
	if n == 0 || n > MaxForwardID {
		return "", fmt.Errorf("%w: forward id length %d", ErrProtocol, n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", fmt.Errorf("%w: forward id: %v", ErrProtocol, err)
	}
	return string(buf), nil
}

func writeAll(w io.Writer, buf []byte) error {
	for len(buf) > 0 {
		n, err := w.Write(buf)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrUnexpectedEOF
		}
		buf = buf[n:]
	}
	return nil
}
