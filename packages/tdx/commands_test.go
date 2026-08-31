package tdx

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"
)

func TestSecurityBarsRequestMatchesWireLayout(t *testing.T) {
	request, err := SecurityBarsRequest(MarketSH, "600000", CategoryDay, 3, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(request) != 40 {
		t.Fatalf("request length = %d, want 40", len(request))
	}
	if got := binary.LittleEndian.Uint16(request[12:14]); got != uint16(MarketSH) {
		t.Fatalf("market = %d", got)
	}
	if got := string(bytes.TrimRight(request[14:20], "\x00")); got != "600000" {
		t.Fatalf("code = %q", got)
	}
	if got := binary.LittleEndian.Uint16(request[20:22]); got != uint16(CategoryDay) {
		t.Fatalf("category = %d", got)
	}
}

func TestMACBarsRequestHasMessageAndBody(t *testing.T) {
	request, err := MACBarsRequest(31, "00700", 4, 1, 0, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(request) != 58 || request[0] != 0x1c {
		t.Fatalf("unexpected request length/header: %d %x", len(request), request[:1])
	}
	if got := binary.LittleEndian.Uint16(request[10:12]); got != 0x122e {
		t.Fatalf("message id = %#x", got)
	}
}

func TestExtendedInstrumentInfoRequestUsesReferenceLayout(t *testing.T) {
	request, err := ExtendedInstrumentInfoRequest(7, 80)
	if err != nil {
		t.Fatal(err)
	}
	if len(request) != 18 || binary.LittleEndian.Uint32(request[12:16]) != 7 || binary.LittleEndian.Uint16(request[16:18]) != 80 {
		t.Fatalf("unexpected extended instrument request: %x", request)
	}
}

func TestExtendedBarsRequestIncludesMarketCodeAndBody(t *testing.T) {
	request, err := ExtendedBarsRequest(3, "ABC", CategoryDay, 7, 11)
	if err != nil {
		t.Fatal(err)
	}
	if len(request) != 40 {
		t.Fatalf("request length = %d, want 40", len(request))
	}
	if request[12] != 3 || string(bytes.TrimRight(request[13:22], "\x00")) != "ABC" {
		t.Fatalf("unexpected market/code: %x", request[12:22])
	}
	if got := binary.LittleEndian.Uint16(request[22:24]); got != uint16(CategoryDay) {
		t.Fatalf("category = %d", got)
	}
	if got := binary.LittleEndian.Uint32(request[26:30]); got != 7 {
		t.Fatalf("start = %d", got)
	}
	if got := binary.LittleEndian.Uint16(request[30:32]); got != 11 {
		t.Fatalf("count = %d", got)
	}
}

func TestParseSecurityBarsRestoresDifferentialPrices(t *testing.T) {
	body := make([]byte, 2)
	binary.LittleEndian.PutUint16(body, 2)
	appendBar := func(date uint32, open, close, high, low int64) {
		dateBytes := make([]byte, 4)
		binary.LittleEndian.PutUint32(dateBytes, date)
		body = append(body, dateBytes...)
		body = append(body, EncodePrice(open)...)
		body = append(body, EncodePrice(close)...)
		body = append(body, EncodePrice(high)...)
		body = append(body, EncodePrice(low)...)
		body = append(body, 0, 0, 0, 0, 0, 0, 0, 0)
	}
	appendBar(20250102, 10000, 100, 200, -50)
	appendBar(20250103, 0, -25, 50, -100)
	bars, err := ParseSecurityBars(body, CategoryDay, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(bars) != 2 || bars[0].Open != 10 || bars[0].Close != 10.1 || bars[1].Open != 10.1 || bars[1].Close != 10.075 {
		t.Fatalf("unexpected bars: %+v", bars)
	}
}

func TestParseExtendedBarsKeepsUnconfirmedFieldsSeparate(t *testing.T) {
	body := make([]byte, 20)
	binary.LittleEndian.PutUint16(body[18:20], 1)
	date := make([]byte, 4)
	binary.LittleEndian.PutUint32(date, 20250102)
	body = append(body, date...)
	for _, value := range []float32{10, 11, 9, 10.5} {
		bits := make([]byte, 4)
		binary.LittleEndian.PutUint32(bits, math.Float32bits(value))
		body = append(body, bits...)
	}
	position := make([]byte, 4)
	binary.LittleEndian.PutUint32(position, 7)
	body = append(body, position...)
	trade := make([]byte, 4)
	binary.LittleEndian.PutUint32(trade, 8)
	body = append(body, trade...)
	settlement := make([]byte, 4)
	binary.LittleEndian.PutUint32(settlement, math.Float32bits(10.25))
	body = append(body, settlement...)
	bars, err := ParseExtendedBars(body, CategoryDay)
	if err != nil {
		t.Fatal(err)
	}
	if len(bars) != 1 || bars[0].Position != 7 || bars[0].Trade != 8 {
		t.Fatalf("unexpected extended bar: %+v", bars)
	}
}
