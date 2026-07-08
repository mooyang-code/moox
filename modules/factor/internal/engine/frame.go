package engine

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

var frameMagic = []byte{'M', 'X'}

// FrameType identifies a pyworker stdio frame kind.
type FrameType byte

const (
	FrameTypeReady    FrameType = 0x01
	FrameTypeRequest  FrameType = 0x02
	FrameTypeResponse FrameType = 0x03
	FrameTypeError    FrameType = 0x04
	FrameTypePing     FrameType = 0x05
	FrameTypeReload   FrameType = 0x06
)

// Frame is one decoded stdio frame.
type Frame struct {
	Type    FrameType
	Meta    map[string]any
	Payload []byte
}

// WriteFrame writes one frame using the shared MX binary layout.
func WriteFrame(w io.Writer, frameType FrameType, meta map[string]any, payload []byte) error {
	metaBytes, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("marshal frame meta: %w", err)
	}
	if _, err := w.Write(frameMagic); err != nil {
		return err
	}
	if _, err := w.Write([]byte{byte(frameType)}); err != nil {
		return err
	}
	var metaLen [4]byte
	binary.BigEndian.PutUint32(metaLen[:], uint32(len(metaBytes)))
	if _, err := w.Write(metaLen[:]); err != nil {
		return err
	}
	if _, err := w.Write(metaBytes); err != nil {
		return err
	}
	var payloadLen [8]byte
	binary.BigEndian.PutUint64(payloadLen[:], uint64(len(payload)))
	if _, err := w.Write(payloadLen[:]); err != nil {
		return err
	}
	if len(payload) > 0 {
		_, err = w.Write(payload)
	}
	return err
}

// ReadFrame reads one frame and rejects frames larger than maxFrameBytes.
func ReadFrame(r io.Reader, maxFrameBytes int64) (*Frame, error) {
	header := make([]byte, 7)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, err
	}
	if header[0] != frameMagic[0] || header[1] != frameMagic[1] {
		return nil, fmt.Errorf("invalid frame magic")
	}
	metaLen := int64(binary.BigEndian.Uint32(header[3:7]))
	if metaLen > maxFrameBytes {
		return nil, fmt.Errorf("frame meta too large: %d", metaLen)
	}
	metaBytes := make([]byte, metaLen)
	if _, err := io.ReadFull(r, metaBytes); err != nil {
		return nil, fmt.Errorf("read frame meta: %w", err)
	}
	var payloadHeader [8]byte
	if _, err := io.ReadFull(r, payloadHeader[:]); err != nil {
		return nil, fmt.Errorf("read frame payload length: %w", err)
	}
	payloadLen := int64(binary.BigEndian.Uint64(payloadHeader[:]))
	if metaLen+payloadLen > maxFrameBytes {
		return nil, fmt.Errorf("frame too large: meta=%d payload=%d max=%d", metaLen, payloadLen, maxFrameBytes)
	}
	payload := make([]byte, payloadLen)
	if payloadLen > 0 {
		if _, err := io.ReadFull(r, payload); err != nil {
			return nil, fmt.Errorf("read frame payload: %w", err)
		}
	}
	meta := map[string]any{}
	if len(metaBytes) > 0 {
		if err := json.Unmarshal(metaBytes, &meta); err != nil {
			return nil, fmt.Errorf("decode frame meta: %w", err)
		}
	}
	return &Frame{Type: FrameType(header[2]), Meta: meta, Payload: payload}, nil
}
