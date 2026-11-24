package tdx

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"io"
)

const HeaderSize = 16

func ParseHeader(data []byte) (Header, error) {
	if len(data) != HeaderSize {
		return Header{}, fmt.Errorf("tdx: frame header length %d, want %d", len(data), HeaderSize)
	}
	return Header{
		Magic:     binary.LittleEndian.Uint32(data[0:4]),
		Sequence:  binary.LittleEndian.Uint32(data[4:8]),
		Method:    binary.LittleEndian.Uint32(data[8:12]),
		ZipSize:   binary.LittleEndian.Uint16(data[12:14]),
		UnzipSize: binary.LittleEndian.Uint16(data[14:16]),
	}, nil
}

func DecodeBody(header Header, raw []byte) ([]byte, error) {
	if len(raw) != int(header.ZipSize) {
		return nil, fmt.Errorf("tdx: body length %d, want %d", len(raw), header.ZipSize)
	}
	if !header.Compressed() {
		if len(raw) != int(header.UnzipSize) {
			return nil, fmt.Errorf("tdx: uncompressed body length %d, want %d", len(raw), header.UnzipSize)
		}
		return append([]byte(nil), raw...), nil
	}
	reader, err := zlib.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("tdx: zlib reader: %w", err)
	}
	body, readErr := io.ReadAll(io.LimitReader(reader, int64(header.UnzipSize)+1))
	closeErr := reader.Close()
	if readErr != nil {
		return nil, fmt.Errorf("tdx: zlib body: %w", readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("tdx: close zlib body: %w", closeErr)
	}
	if len(body) != int(header.UnzipSize) {
		return nil, fmt.Errorf("tdx: decoded body length %d, want %d", len(body), header.UnzipSize)
	}
	return body, nil
}

func EncodeMACRequest(messageID uint16, body []byte, headFlag byte) ([]byte, error) {
	if headFlag == 0 {
		headFlag = 0x1c
	}
	if len(body)+2 > 0xffff {
		return nil, fmt.Errorf("tdx: MAC body too large: %d", len(body))
	}
	inner := make([]byte, 2+len(body))
	binary.LittleEndian.PutUint16(inner, messageID)
	copy(inner[2:], body)
	frame := make([]byte, 10+len(inner))
	frame[0] = headFlag
	frame[5] = 1
	binary.LittleEndian.PutUint16(frame[6:8], uint16(len(inner)))
	binary.LittleEndian.PutUint16(frame[8:10], uint16(len(inner)))
	copy(frame[10:], inner)
	return frame, nil
}
