package protocol

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

// MaxFrameSize bounds a single frame to protect against memory exhaustion.
const MaxFrameSize = 16 << 20 // 16 MiB

// Codec encodes and decodes frames as JSON.
type Codec struct{}

// Encode serializes a frame, stamping the protocol version when unset.
func (Codec) Encode(f Frame) ([]byte, error) {
	if f.V == 0 {
		f.V = Version
	}
	if f.Type == "" {
		return nil, fmt.Errorf("protocol: frame type is required")
	}
	return json.Marshal(f)
}

// Decode parses and validates a frame.
func (Codec) Decode(b []byte) (Frame, error) {
	var f Frame
	if err := json.Unmarshal(b, &f); err != nil {
		return Frame{}, fmt.Errorf("protocol: decode: %w", err)
	}
	if f.V == 0 {
		f.V = Version
	}
	if f.V > Version {
		return Frame{}, fmt.Errorf("protocol: unsupported version %d (max %d)", f.V, Version)
	}
	if f.Type == "" {
		return Frame{}, fmt.Errorf("protocol: missing frame type")
	}
	return f, nil
}

// EncodeData marshals a payload into a frame's Data field.
func EncodeData(f *Frame, v any) error {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f.Data = b
	return nil
}

// DecodeData unmarshals a frame's Data field into v.
func DecodeData(f Frame, v any) error {
	if len(f.Data) == 0 {
		return nil
	}
	return json.Unmarshal(f.Data, v)
}

// WriteFrame writes a length-prefixed frame (4-byte big-endian length).
func WriteFrame(w io.Writer, c Codec, f Frame) error {
	b, err := c.Encode(f)
	if err != nil {
		return err
	}
	if len(b) > MaxFrameSize {
		return fmt.Errorf("protocol: frame too large (%d bytes)", len(b))
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(b)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

// ReadFrame reads a length-prefixed frame.
func ReadFrame(r io.Reader, c Codec) (Frame, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return Frame{}, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > MaxFrameSize {
		return Frame{}, fmt.Errorf("protocol: frame too large (%d bytes)", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return Frame{}, err
	}
	return c.Decode(buf)
}

// Tracker tracks the last sequence number seen per session, used to build
// resume requests and to skip duplicates after reconnect.
type Tracker struct {
	mu   sync.Mutex
	last map[string]int64
}

// NewTracker creates an empty tracker.
func NewTracker() *Tracker { return &Tracker{last: map[string]int64{}} }

// Observe records a frame's sequence for its session.
func (t *Tracker) Observe(f Frame) {
	if f.SessionID == "" || f.Seq == 0 {
		return
	}
	t.mu.Lock()
	if f.Seq > t.last[f.SessionID] {
		t.last[f.SessionID] = f.Seq
	}
	t.mu.Unlock()
}

// Last returns the highest sequence observed for a session.
func (t *Tracker) Last(sessionID string) int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.last[sessionID]
}

// Resume builds a resume frame for a session.
func (t *Tracker) Resume(sessionID string) Frame {
	f := Frame{Type: TypeResume, SessionID: sessionID}
	_ = EncodeData(&f, ResumeRequest{FromSeq: t.Last(sessionID)})
	return f
}
