package protocol

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

var Magic = [2]byte{'M', 'X'}

type Limits struct {
	MaxMetaBytes    int64
	MaxPayloadBytes int64
	MaxFrameBytes   int64
}

func DefaultLimits() Limits {
	return Limits{MaxMetaBytes: 4 << 20, MaxPayloadBytes: 64 << 20, MaxFrameBytes: 68 << 20}
}

type Frame struct {
	Type    MessageType
	Meta    json.RawMessage
	Payload []byte
}

var (
	ErrFrameTooLarge = errors.New("pyruntime: frame too large")
	ErrInvalidFrame  = errors.New("pyruntime: invalid frame")
)

func ReadFrame(r io.Reader, limits Limits) (Frame, error) {
	limits = normalizeLimits(limits)
	header := make([]byte, 7)
	if _, err := io.ReadFull(r, header); err != nil {
		return Frame{}, err
	}
	if header[0] != Magic[0] || header[1] != Magic[1] {
		return Frame{}, fmt.Errorf("%w: bad magic", ErrInvalidFrame)
	}
	if !knownMessageType(MessageType(header[2])) {
		return Frame{}, fmt.Errorf("%w: unknown message type=%d", ErrInvalidFrame, header[2])
	}
	metaLen := int64(binary.BigEndian.Uint32(header[3:7]))
	if metaLen > limits.MaxMetaBytes || metaLen > limits.MaxFrameBytes {
		return Frame{}, fmt.Errorf("%w: meta=%d", ErrFrameTooLarge, metaLen)
	}
	meta := make([]byte, metaLen)
	if _, err := io.ReadFull(r, meta); err != nil {
		return Frame{}, fmt.Errorf("read meta: %w", err)
	}
	if !json.Valid(meta) {
		return Frame{}, fmt.Errorf("%w: invalid meta json", ErrInvalidFrame)
	}
	var size [8]byte
	if _, err := io.ReadFull(r, size[:]); err != nil {
		return Frame{}, err
	}
	payloadLen := binary.BigEndian.Uint64(size[:])
	if payloadLen > uint64(limits.MaxPayloadBytes) || payloadLen > uint64(limits.MaxFrameBytes-metaLen) {
		return Frame{}, fmt.Errorf("%w: payload=%d", ErrFrameTooLarge, payloadLen)
	}
	payload := make([]byte, int(payloadLen))
	if payloadLen > 0 {
		if _, err := io.ReadFull(r, payload); err != nil {
			return Frame{}, fmt.Errorf("read payload: %w", err)
		}
	}
	return Frame{Type: MessageType(header[2]), Meta: meta, Payload: payload}, nil
}

func knownMessageType(t MessageType) bool {
	switch t {
	case TypeHello, TypeLoad, TypeRun, TypeResult, TypeError, TypePing, TypeDrain:
		return true
	default:
		return false
	}
}

func WriteFrame(w io.Writer, limits Limits, frame Frame) error {
	limits = normalizeLimits(limits)
	if len(frame.Meta) == 0 {
		frame.Meta = []byte(`{}`)
	}
	if !json.Valid(frame.Meta) {
		return fmt.Errorf("%w: invalid meta json", ErrInvalidFrame)
	}
	if !knownMessageType(frame.Type) {
		return fmt.Errorf("%w: unknown message type=%d", ErrInvalidFrame, frame.Type)
	}
	if int64(len(frame.Meta)) > limits.MaxMetaBytes || int64(len(frame.Payload)) > limits.MaxPayloadBytes || int64(len(frame.Meta)+len(frame.Payload)) > limits.MaxFrameBytes {
		return ErrFrameTooLarge
	}
	header := make([]byte, 7)
	header[0], header[1], header[2] = Magic[0], Magic[1], byte(frame.Type)
	binary.BigEndian.PutUint32(header[3:7], uint32(len(frame.Meta)))
	if err := writeAll(w, header); err != nil {
		return err
	}
	if err := writeAll(w, frame.Meta); err != nil {
		return err
	}
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(frame.Payload)))
	if err := writeAll(w, size[:]); err != nil {
		return err
	}
	return writeAll(w, frame.Payload)
}

func writeAll(w io.Writer, p []byte) error {
	for len(p) > 0 {
		n, err := w.Write(p)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		p = p[n:]
	}
	return nil
}
func normalizeLimits(l Limits) Limits {
	d := DefaultLimits()
	if l.MaxMetaBytes <= 0 {
		l.MaxMetaBytes = d.MaxMetaBytes
	}
	if l.MaxPayloadBytes <= 0 {
		l.MaxPayloadBytes = d.MaxPayloadBytes
	}
	if l.MaxFrameBytes <= 0 {
		l.MaxFrameBytes = d.MaxFrameBytes
	}
	return l
}
