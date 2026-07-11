package protocol

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	want := Frame{Type: TypeRun, Meta: []byte(`{"id":"1"}`), Payload: []byte("abc")}
	if err := WriteFrame(&buf, DefaultLimits(), want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFrame(&buf, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != want.Type || string(got.Meta) != string(want.Meta) || string(got.Payload) != string(want.Payload) {
		t.Fatalf("got=%+v", got)
	}
}

func TestReadFrameRejectsOversizeBeforeAllocation(t *testing.T) {
	var buf bytes.Buffer
	buf.Write([]byte{'M', 'X', byte(TypeRun)})
	var metaLen [4]byte
	binary.BigEndian.PutUint32(metaLen[:], 2)
	buf.Write(metaLen[:])
	buf.WriteString(`{}`)
	var payloadLen [8]byte
	binary.BigEndian.PutUint64(payloadLen[:], 20)
	buf.Write(payloadLen[:])
	_, err := ReadFrame(&buf, Limits{MaxMetaBytes: 10, MaxPayloadBytes: 10, MaxFrameBytes: 20})
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("err=%v", err)
	}
}
