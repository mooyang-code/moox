package tdx

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"testing"
)

func TestDecodeBodyCompressedAndExactLength(t *testing.T) {
	plain := []byte("tdx wire fixture")
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	if _, err := writer.Write(plain); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	header := Header{ZipSize: uint16(compressed.Len()), UnzipSize: uint16(len(plain))}
	got, err := DecodeBody(header, compressed.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("decoded body = %q, want %q", got, plain)
	}

	bad := append([]byte(nil), compressed.Bytes()...)
	bad = append(bad, 0)
	if _, err := DecodeBody(header, bad); err == nil {
		t.Fatal("expected exact body length error")
	}
}

func TestEncodeMACRequest(t *testing.T) {
	frame, err := EncodeMACRequest(0x122e, []byte{1, 2, 3}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(frame) != 15 || frame[0] != 0x1c || frame[5] != 1 {
		t.Fatalf("unexpected MAC frame: %x", frame)
	}
	if got := binary.LittleEndian.Uint16(frame[6:8]); got != 5 {
		t.Fatalf("zip size = %d, want 5", got)
	}
}

func TestParseExtendedInstrumentInfo(t *testing.T) {
	body := make([]byte, 6+64)
	binary.LittleEndian.PutUint16(body[4:6], 1)
	body[6] = 2
	body[7] = 31
	copy(body[11:16], []byte("00700"))
	copy(body[20:27], []byte("Tencent"))
	items, err := ParseExtendedInstrumentInfo(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Category != 2 || items[0].Market != 31 || items[0].Code != "00700" {
		t.Fatalf("unexpected extended instrument: %+v", items)
	}
}
