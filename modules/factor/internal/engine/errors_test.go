package engine

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/stretchr/testify/assert"
	"reflect"
	"testing"
)

func TestRetryableError_UnwrapsUnderlyingError(t *testing.T) {
	root := fmt.Errorf("transient")
	err := retryable("storage down: %s", root.Error())
	var retry RetryableError
	assert.True(t, errors.As(err, &retry))
	assert.Contains(t, err.Error(), "storage down")
	assert.Error(t, retry.Unwrap())
}

func TestNonRetryableError_UnwrapsUnderlyingError(t *testing.T) {
	root := fmt.Errorf("bad input")
	err := nonRetryable("invalid factor: %s", root.Error())
	var nerr NonRetryableError
	assert.True(t, errors.As(err, &nerr))
	assert.Error(t, nerr.Unwrap())
}

func TestFrameCodecLayoutAndRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	meta := map[string]any{"id": "task-1", "encoding": "json"}
	payload := []byte("payload")

	if err := WriteFrame(&buf, FrameTypeRequest, meta, payload); err != nil {
		t.Fatalf("WriteFrame() error = %v", err)
	}
	raw := buf.Bytes()
	wantPrefix := []byte{0x4d, 0x58, byte(FrameTypeRequest), 0, 0, 0}
	if !bytes.Equal(raw[:6], wantPrefix) {
		t.Fatalf("frame prefix = % x", raw[:6])
	}

	frame, err := ReadFrame(&buf, 1024)
	if err != nil {
		t.Fatalf("ReadFrame() error = %v", err)
	}
	if frame.Type != FrameTypeRequest || !reflect.DeepEqual(frame.Meta, meta) || string(frame.Payload) != "payload" {
		t.Fatalf("frame = %+v", frame)
	}
}

func TestReadFrameRejectsCorruptTruncatedAndOversizedFrames(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
	}{
		{name: "corrupt magic", raw: []byte("NO")},
		{name: "truncated meta", raw: []byte{0x4d, 0x58, byte(FrameTypeRequest), 0, 0, 0, 4, '{'}},
		{name: "truncated payload", raw: []byte{0x4d, 0x58, byte(FrameTypeRequest), 0, 0, 0, 2, '{', '}', 0, 0, 0, 0, 0, 0, 0, 4, 'x'}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ReadFrame(bytes.NewReader(tt.raw), 1024); err == nil {
				t.Fatal("ReadFrame() error = nil")
			}
		})
	}

	var buf bytes.Buffer
	if err := WriteFrame(&buf, FrameTypeRequest, map[string]any{"id": "too-big"}, bytes.Repeat([]byte("x"), 8)); err != nil {
		t.Fatalf("WriteFrame() error = %v", err)
	}
	if _, err := ReadFrame(&buf, 4); err == nil {
		t.Fatal("ReadFrame() oversized error = nil")
	}
}
