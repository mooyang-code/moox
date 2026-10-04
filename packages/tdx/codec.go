package tdx

import (
	"encoding/binary"
	"fmt"
	"math"
)

func DecodePrice(data []byte, offset *int) (int64, error) {
	if offset == nil || *offset >= len(data) {
		return 0, fmt.Errorf("tdx: price varint is truncated")
	}
	return decodePrice(data, offset)
}

func decodePrice(data []byte, offset *int) (int64, error) {
	if offset == nil || *offset >= len(data) {
		return 0, fmt.Errorf("tdx: price varint is truncated")
	}
	start := *offset
	first := data[start]
	value := uint64(first & 0x3f)
	pos := start + 1
	shift := uint(6)
	for first&0x80 != 0 {
		if pos >= len(data) || shift >= 64 {
			return 0, fmt.Errorf("tdx: price varint is truncated or overflows")
		}
		part := data[pos]
		value |= uint64(part&0x7f) << shift
		pos++
		shift += 7
		first = part
	}
	*offset = pos
	result := int64(value)
	if data[start]&0x40 != 0 {
		result = -result
	}
	return result, nil
}

func EncodePrice(value int64) []byte {
	negative := value < 0
	if negative {
		value = -value
	}
	n := uint64(value)
	first := byte(n & 0x3f)
	n >>= 6
	if negative {
		first |= 0x40
	}
	result := []byte{first}
	if n != 0 {
		result[0] |= 0x80
	}
	for n != 0 {
		part := byte(n & 0x7f)
		n >>= 7
		if n != 0 {
			part |= 0x80
		}
		result = append(result, part)
	}
	return result
}

func DecodeVolume(data []byte, offset *int) (float64, error) {
	if offset == nil || *offset < 0 || *offset+4 > len(data) {
		return 0, fmt.Errorf("tdx: volume is truncated")
	}
	value := binary.LittleEndian.Uint32(data[*offset : *offset+4])
	*offset += 4
	return decodeVolumeRaw(value), nil
}

func decodeVolumeRaw(value uint32) float64 {
	if value == 0 {
		return 0
	}
	logPoint := int((value >> 24) & 0xff)
	hi := float64((value >> 16) & 0xff)
	mid := float64((value >> 8) & 0xff)
	lo := float64(value & 0xff)
	base := math.Ldexp(1, logPoint*2-0x7f)
	hiExp := logPoint*2 - 0x86
	if hi > 0x80 {
		hiValue := math.Ldexp(128, hiExp) + (hi-128)*math.Ldexp(1, hiExp+1)
		hi = hiValue
	} else {
		hi = math.Ldexp(hi, hiExp)
	}
	mid *= math.Ldexp(1, logPoint*2-0x8e)
	lo *= math.Ldexp(1, logPoint*2-0x96)
	if uint32(value>>16)&0x80 != 0 {
		mid *= 2
		lo *= 2
	}
	return base + hi + mid + lo
}
