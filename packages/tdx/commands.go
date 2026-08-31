package tdx

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

var shanghaiLocation = time.FixedZone("Asia/Shanghai", 8*60*60)

var setupCommands = [][]byte{
	{0x0c, 0x02, 0x18, 0x93, 0x00, 0x01, 0x03, 0x00, 0x03, 0x00, 0x0d, 0x00, 0x01},
	{0x0c, 0x02, 0x18, 0x94, 0x00, 0x01, 0x03, 0x00, 0x03, 0x00, 0x0d, 0x00, 0x02},
	{0x0c, 0x03, 0x18, 0x99, 0x00, 0x01, 0x20, 0x00, 0x20, 0x00, 0xdb, 0x0f, 0xd5, 0xd0, 0xc9, 0xcc, 0xd6, 0xa4, 0xa8, 0xaf, 0x00, 0x00, 0x00, 0x8f, 0xc2, 0x25, 0x40, 0x13, 0x00, 0x00, 0xd5, 0x00, 0xc9, 0xcc, 0xbd, 0xf0, 0xd7, 0xea, 0x00, 0x00, 0x00, 0x02},
}

func SecurityBarsRequest(market Market, code string, category KlineCategory, start, count int) ([]byte, error) {
	if len([]byte(code)) > 6 || strings.TrimSpace(code) == "" {
		return nil, fmt.Errorf("tdx: security code must be 1-6 bytes")
	}
	if start < 0 || count < 0 || count > 800 {
		return nil, fmt.Errorf("tdx: invalid security bars range start=%d count=%d", start, count)
	}
	frame := make([]byte, 40)
	binary.LittleEndian.PutUint16(frame[0:2], 0x010c)
	binary.LittleEndian.PutUint32(frame[2:6], 0x01016408)
	binary.LittleEndian.PutUint16(frame[6:8], 0x001c)
	binary.LittleEndian.PutUint16(frame[8:10], 0x001c)
	binary.LittleEndian.PutUint16(frame[10:12], 0x052d)
	binary.LittleEndian.PutUint16(frame[12:14], uint16(market))
	copy(frame[14:20], []byte(code))
	binary.LittleEndian.PutUint16(frame[20:22], uint16(category))
	binary.LittleEndian.PutUint16(frame[22:24], 1)
	binary.LittleEndian.PutUint16(frame[24:26], uint16(start))
	binary.LittleEndian.PutUint16(frame[26:28], uint16(count))
	return frame, nil
}

func SecurityListRequest(market Market, start int) ([]byte, error) {
	if start < 0 || start > 0xffff {
		return nil, fmt.Errorf("tdx: invalid security list start %d", start)
	}
	frame := []byte{0x0c, 0x01, 0x18, 0x64, 0x01, 0x01, 0x06, 0x00, 0x06, 0x00, 0x50, 0x04, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	binary.LittleEndian.PutUint16(frame[12:14], uint16(market))
	binary.LittleEndian.PutUint16(frame[14:16], uint16(start))
	return frame, nil
}

func SecurityCountRequest(market Market) []byte {
	frame := []byte{0x0c, 0x0c, 0x18, 0x6c, 0x00, 0x01, 0x08, 0x00, 0x08, 0x00, 0x4e, 0x04, 0x00, 0x00, 0x75, 0xc7, 0x33, 0x01}
	binary.LittleEndian.PutUint16(frame[12:14], uint16(market))
	return frame
}

func ExtendedMarketsRequest() []byte {
	return []byte{0x01, 0x02, 0x48, 0x69, 0x00, 0x01, 0x02, 0x00, 0x02, 0x00, 0xf4, 0x23}
}

func ExtendedInstrumentCountRequest() []byte {
	return []byte{0x01, 0x03, 0x48, 0x66, 0x00, 0x01, 0x02, 0x00, 0x02, 0x00, 0xf0, 0x23}
}

func ExtendedInstrumentInfoRequest(start, count int) ([]byte, error) {
	if start < 0 || count < 0 || count > 0xffff {
		return nil, fmt.Errorf("tdx: invalid extended instrument range start=%d count=%d", start, count)
	}
	frame := []byte{0x01, 0x04, 0x48, 0x67, 0x00, 0x01, 0x08, 0x00, 0x08, 0x00, 0xf5, 0x23, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	binary.LittleEndian.PutUint32(frame[12:16], uint32(start))
	binary.LittleEndian.PutUint16(frame[16:18], uint16(count))
	return frame, nil
}

func ExtendedBarsRequest(market uint8, code string, category KlineCategory, start, count int) ([]byte, error) {
	if len([]byte(code)) > 9 || strings.TrimSpace(code) == "" {
		return nil, fmt.Errorf("tdx: extended code must be 1-9 bytes")
	}
	if start < 0 || count < 0 || count > 700 {
		return nil, fmt.Errorf("tdx: invalid extended bars range start=%d count=%d", start, count)
	}
	body := make([]byte, 18)
	binary.LittleEndian.PutUint16(body[0:2], uint16(category))
	binary.LittleEndian.PutUint16(body[2:4], 1)
	binary.LittleEndian.PutUint32(body[4:8], uint32(start))
	binary.LittleEndian.PutUint16(body[8:10], uint16(count))
	inner := make([]byte, 12+1+9+len(body))
	copy(inner, []byte{0x01, 0x01, 0x08, 0x6a, 0x01, 0x01, 0x16, 0x00, 0x16, 0x00, 0xff, 0x23})
	inner[12] = market
	copy(inner[13:22], []byte(code))
	copy(inner[22:], body)
	return inner, nil
}

func MACExLoginRequest() ([]byte, error) {
	loginBody := []byte{
		0xe5, 0xbb, 0x1c, 0x2f, 0xaf, 0xe5, 0x25, 0x94, 0x1f, 0x32, 0xc6, 0xe5, 0xd5, 0x3d, 0xfb, 0x41,
		0x5b, 0x73, 0x4c, 0xc9, 0xcd, 0xbf, 0x0a, 0xc9, 0x20, 0x21, 0xbf, 0xdd, 0x1e, 0xb0, 0x6d, 0x22,
		0xd0, 0x08, 0x88, 0x4c, 0x16, 0x11, 0xcb, 0x13, 0x78, 0xf6, 0xab, 0xd8, 0x24, 0xd8, 0x99, 0xd2,
		0x1f, 0x32, 0xc6, 0xe5, 0xd5, 0x3d, 0xfb, 0x41, 0x1f, 0x32, 0xc6, 0xe5, 0xd5, 0x3d, 0xfb, 0x41,
		0xa9, 0x32, 0x5a, 0xc9, 0x35, 0xdc, 0x08, 0x37, 0x33, 0x5a, 0x16, 0xe4, 0xce, 0x17, 0xc1, 0xbb,
	}
	return EncodeMACRequest(0x2454, loginBody, 0x01)
}

func MACBarsRequest(market uint16, code string, period, times, start, count int, adjustment int8) ([]byte, error) {
	if len([]byte(code)) > 22 || strings.TrimSpace(code) == "" {
		return nil, fmt.Errorf("tdx: MAC code must be 1-22 bytes")
	}
	if period < 0 || times <= 0 || start < 0 || count < 0 {
		return nil, fmt.Errorf("tdx: invalid MAC bars range")
	}
	body := make([]byte, 46)
	binary.LittleEndian.PutUint16(body[0:2], market)
	copy(body[2:24], []byte(code))
	binary.LittleEndian.PutUint16(body[24:26], uint16(period))
	binary.LittleEndian.PutUint16(body[26:28], uint16(times))
	binary.LittleEndian.PutUint32(body[28:32], uint32(start))
	binary.LittleEndian.PutUint16(body[32:34], uint16(count))
	binary.LittleEndian.PutUint16(body[34:36], uint16(adjustment))
	body[36] = 1
	body[37] = 1
	body[38] = 0
	body[39] = 1
	return EncodeMACRequest(0x122e, body, 0x1c)
}

func parseDateTime(category KlineCategory, data []byte, pos *int) (time.Time, error) {
	if category.Intraday() {
		if *pos < 0 || *pos+4 > len(data) {
			return time.Time{}, fmt.Errorf("tdx: minute datetime is truncated")
		}
		zipDay := binary.LittleEndian.Uint16(data[*pos : *pos+2])
		minutes := binary.LittleEndian.Uint16(data[*pos+2 : *pos+4])
		*pos += 4
		year := int(zipDay>>11) + 2004
		month := int(zipDay%2048) / 100
		day := int(zipDay % 100)
		hour := int(minutes) / 60
		minute := int(minutes) % 60
		return time.Date(year, time.Month(month), day, hour, minute, 0, 0, shanghaiLocation), nil
	}
	if *pos < 0 || *pos+4 > len(data) {
		return time.Time{}, fmt.Errorf("tdx: day datetime is truncated")
	}
	value := binary.LittleEndian.Uint32(data[*pos : *pos+4])
	*pos += 4
	year := int(value / 10000)
	month := time.Month(value % 10000 / 100)
	day := int(value % 100)
	return time.Date(year, month, day, 15, 0, 0, 0, shanghaiLocation), nil
}

func decodeGBK(data []byte) string {
	data = bytes.TrimRight(data, "\x00")
	reader := transform.NewReader(bytes.NewReader(data), simplifiedchinese.GBK.NewDecoder())
	decoded, err := io.ReadAll(reader)
	if err != nil {
		return string(data)
	}
	return strings.TrimRight(string(decoded), "\x00")
}

func parseBars(body []byte, category KlineCategory, index bool) ([]Bar, error) {
	if len(body) < 2 {
		return nil, fmt.Errorf("tdx: bars response has no count")
	}
	count := int(binary.LittleEndian.Uint16(body[:2]))
	pos := 2
	result := make([]Bar, 0, count)
	var previous int64
	for i := 0; i < count; i++ {
		start := pos
		when, err := parseDateTime(category, body, &pos)
		if err != nil {
			return nil, fmt.Errorf("tdx: bar %d datetime: %w", i, err)
		}
		openDiff, err := DecodePrice(body, &pos)
		if err != nil {
			return nil, fmt.Errorf("tdx: bar %d open: %w", i, err)
		}
		closeDiff, err := DecodePrice(body, &pos)
		if err != nil {
			return nil, fmt.Errorf("tdx: bar %d close: %w", i, err)
		}
		highDiff, err := DecodePrice(body, &pos)
		if err != nil {
			return nil, fmt.Errorf("tdx: bar %d high: %w", i, err)
		}
		lowDiff, err := DecodePrice(body, &pos)
		if err != nil {
			return nil, fmt.Errorf("tdx: bar %d low: %w", i, err)
		}
		volume, err := DecodeVolume(body, &pos)
		if err != nil {
			return nil, fmt.Errorf("tdx: bar %d volume: %w", i, err)
		}
		amount, err := DecodeVolume(body, &pos)
		if err != nil {
			return nil, fmt.Errorf("tdx: bar %d amount: %w", i, err)
		}
		if index {
			if pos+4 > len(body) {
				return nil, fmt.Errorf("tdx: index bar %d breadth fields are truncated", i)
			}
			pos += 4
		}
		open := openDiff + previous
		close := open + closeDiff
		high := open + highDiff
		low := open + lowDiff
		previous = open + closeDiff
		result = append(result, Bar{
			Time: when, Open: float64(open) / 1000, Close: float64(close) / 1000,
			High: float64(high) / 1000, Low: float64(low) / 1000,
			Volume: volume, Amount: amount, Raw: append([]byte(nil), body[start:pos]...),
		})
	}
	return result, nil
}

func parseExtendedBars(body []byte, category KlineCategory) ([]Bar, error) {
	if len(body) < 20 {
		return nil, fmt.Errorf("tdx: extended bars response header is truncated")
	}
	pos := 18
	count := int(binary.LittleEndian.Uint16(body[pos : pos+2]))
	pos += 2
	result := make([]Bar, 0, count)
	for i := 0; i < count; i++ {
		start := pos
		when, err := parseDateTime(category, body, &pos)
		if err != nil {
			return nil, fmt.Errorf("tdx: extended bar %d datetime: %w", i, err)
		}
		if pos+28 > len(body) {
			return nil, fmt.Errorf("tdx: extended bar %d is truncated", i)
		}
		open := float64(math.Float32frombits(binary.LittleEndian.Uint32(body[pos : pos+4])))
		high := float64(math.Float32frombits(binary.LittleEndian.Uint32(body[pos+4 : pos+8])))
		low := float64(math.Float32frombits(binary.LittleEndian.Uint32(body[pos+8 : pos+12])))
		close := float64(math.Float32frombits(binary.LittleEndian.Uint32(body[pos+12 : pos+16])))
		position := binary.LittleEndian.Uint32(body[pos+16 : pos+20])
		trade := binary.LittleEndian.Uint32(body[pos+20 : pos+24])
		amount := float64(math.Float32frombits(binary.LittleEndian.Uint32(body[pos+16 : pos+20])))
		pos += 28
		result = append(result, Bar{Time: when, Open: open, High: high, Low: low, Close: close, Amount: amount, Position: position, Trade: trade, Raw: append([]byte(nil), body[start:pos]...)})
	}
	return result, nil
}
