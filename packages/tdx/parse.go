package tdx

import (
	"encoding/binary"
	"fmt"
	"math"
	"time"
)

func ParseSecurityBars(body []byte, category KlineCategory, index bool) ([]Bar, error) {
	return parseBars(body, category, index)
}

func ParseExtendedBars(body []byte, category KlineCategory) ([]Bar, error) {
	return parseExtendedBars(body, category)
}

func ParseMACBars(body []byte, category KlineCategory) ([]Bar, error) {
	if len(body) < 33 {
		return nil, fmt.Errorf("tdx: MAC bars response header is truncated")
	}
	count := int(binary.LittleEndian.Uint16(body[27:29]))
	maxCount := (len(body) - 33) / 36
	if count > maxCount {
		count = maxCount
	}
	result := make([]Bar, 0, count)
	for i := 0; i < count; i++ {
		pos := 33 + i*36
		ymd := binary.LittleEndian.Uint32(body[pos : pos+4])
		timeNum := binary.LittleEndian.Uint32(body[pos+4 : pos+8])
		if ymd < 19900101 || ymd > 20991231 {
			continue
		}
		when, err := macDateTime(ymd, timeNum, category.Intraday())
		if err != nil {
			return nil, fmt.Errorf("tdx: MAC bar %d datetime: %w", i, err)
		}
		readFloat := func(offset int) float64 {
			return float64(float32From(body[pos+offset : pos+offset+4]))
		}
		result = append(result, Bar{
			Time: when,
			Open: readFloat(8), High: readFloat(12), Low: readFloat(16), Close: readFloat(20),
			Amount: readFloat(24), Volume: readFloat(28),
		})
	}
	return result, nil
}

func ParseSecurityList(body []byte, market Market) ([]Security, error) {
	if len(body) < 2 {
		return nil, fmt.Errorf("tdx: security list response has no count")
	}
	count := int(binary.LittleEndian.Uint16(body[:2]))
	if 2+count*29 > len(body) {
		return nil, fmt.Errorf("tdx: security list body is truncated: count=%d body=%d", count, len(body))
	}
	result := make([]Security, 0, count)
	for i := 0; i < count; i++ {
		pos := 2 + i*29
		raw := body[pos : pos+29]
		preClose, err := DecodeVolume(raw[20:24], new(int))
		if err != nil {
			return nil, fmt.Errorf("tdx: security %d pre-close: %w", i, err)
		}
		result = append(result, Security{
			Market:       market,
			Code:         string(trimZero(raw[:6])),
			Name:         decodeGBK(raw[8:16]),
			VolumeUnit:   binary.LittleEndian.Uint16(raw[6:8]),
			DecimalPoint: raw[16],
			PreClose:     preClose,
			Raw:          append([]byte(nil), raw...),
		})
	}
	return result, nil
}

func ParseExtendedMarkets(body []byte) ([]ExtendedMarket, error) {
	if len(body) < 2 {
		return nil, fmt.Errorf("tdx: extended market response has no count")
	}
	count := int(binary.LittleEndian.Uint16(body[:2]))
	result := make([]ExtendedMarket, 0, count)
	for i := 0; i < count; i++ {
		pos := 2 + i*64
		if pos+64 > len(body) {
			return nil, fmt.Errorf("tdx: extended market %d is truncated", i)
		}
		raw := body[pos : pos+64]
		if raw[0] == 0 && raw[33] == 0 {
			continue
		}
		result = append(result, ExtendedMarket{Category: raw[0], Name: decodeGBK(raw[1:33]), Market: raw[33], ShortName: decodeGBK(raw[34:36]), Raw: append([]byte(nil), raw...)})
	}
	return result, nil
}

func ParseExtendedInstrumentInfo(body []byte) ([]ExtendedSecurity, error) {
	if len(body) < 6 {
		return nil, fmt.Errorf("tdx: extended instrument response header is truncated")
	}
	count := int(binary.LittleEndian.Uint16(body[4:6]))
	result := make([]ExtendedSecurity, 0, count)
	for i := 0; i < count; i++ {
		pos := 6 + i*64
		if pos+64 > len(body) {
			return nil, fmt.Errorf("tdx: extended instrument %d is truncated", i)
		}
		raw := body[pos : pos+64]
		result = append(result, ExtendedSecurity{
			Category: raw[0], Market: raw[1], Code: decodeGBK(raw[5:14]),
			Name: decodeGBK(raw[14:31]), Desc: decodeGBK(raw[31:40]), Raw: append([]byte(nil), raw...),
		})
	}
	return result, nil
}

func trimZero(data []byte) []byte {
	for i, value := range data {
		if value == 0 {
			return data[:i]
		}
	}
	return data
}

func macDateTime(ymd, timeNum uint32, intraday bool) (time.Time, error) {
	year := int(ymd / 10000)
	month := int(ymd % 10000 / 100)
	day := int(ymd % 100)
	hour, minute := 0, 0
	if intraday && timeNum != 0 {
		hour = int(timeNum) / 3600
		minute = int(timeNum%3600) / 60
	}
	return time.Date(year, time.Month(month), day, hour, minute, 0, 0, shanghaiLocation), nil
}

func float32From(data []byte) float32 {
	return math.Float32frombits(binary.LittleEndian.Uint32(data))
}
