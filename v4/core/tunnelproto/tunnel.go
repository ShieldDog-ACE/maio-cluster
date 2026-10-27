// Package tunnelproto is the framed multiplexing protocol used by maio's
// reverse tunnel.
//
// One TCP connection carries many logical streams. The coordinator opens a
// stream for every connection llama.cpp makes to its local tunnel port; the
// worker dials its local RPC/HTTP target for that stream. This is required
// because llama.cpp's RPC backend opens more than one TCP connection per
// device.
package tunnelproto

import (
	"encoding/binary"
	"fmt"
	"io"
	"sync"
)

// Frame types.
const (
	Open  byte = 1
	Data  byte = 2
	Close byte = 3
)

// MaxPayload bounds one data frame.
const MaxPayload = 1 << 20

// Frame is one tunnel message.
type Frame struct {
	Type   byte
	Stream uint32
	Data   []byte
}

// WriteFrame serializes one frame. Callers must hold mu when several
// goroutines write to the same connection.
func WriteFrame(w io.Writer, mu *sync.Mutex, f Frame) error {
	if len(f.Data) > MaxPayload {
		return fmt.Errorf("tunnel frame too large: %d", len(f.Data))
	}
	var hdr [9]byte
	hdr[0] = f.Type
	binary.BigEndian.PutUint32(hdr[1:5], f.Stream)
	binary.BigEndian.PutUint32(hdr[5:9], uint32(len(f.Data)))
	mu.Lock()
	defer mu.Unlock()
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if len(f.Data) > 0 {
		if _, err := w.Write(f.Data); err != nil {
			return err
		}
	}
	return nil
}

// ReadFrame reads one frame and allocates its payload.
func ReadFrame(r io.Reader) (Frame, error) {
	var hdr [9]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return Frame{}, err
	}
	f := Frame{Type: hdr[0], Stream: binary.BigEndian.Uint32(hdr[1:5])}
	n := binary.BigEndian.Uint32(hdr[5:9])
	if n > MaxPayload {
		return Frame{}, fmt.Errorf("tunnel frame too large: %d", n)
	}
	switch f.Type {
	case Open, Close:
		if n != 0 {
			return Frame{}, fmt.Errorf("control frame has a payload")
		}
	case Data:
		if n == 0 {
			return Frame{}, fmt.Errorf("empty data frame")
		}
		f.Data = make([]byte, n)
		if _, err := io.ReadFull(r, f.Data); err != nil {
			return Frame{}, err
		}
	default:
		return Frame{}, fmt.Errorf("unknown frame type %d", f.Type)
	}
	return f, nil
}
