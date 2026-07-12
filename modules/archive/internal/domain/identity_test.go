package domain

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPartitionPathCarriesAllIdentityFields(t *testing.T) {
	key := PartitionKey{SpaceID: "crypto_binance", DatasetID: "spot_kline", SubjectID: "BTC-USDT", Freq: "1m", Month: "202606"}
	want := filepath.Join("crypto_binance", "spot_kline", "1m", "BTC-USDT", "crypto_binance__spot_kline__BTC-USDT__1m__202606.parquet")
	got, err := key.RelativePath()
	if err != nil || got != want {
		t.Fatalf("RelativePath() = %q, %v; want %q", got, err, want)
	}
}

func TestIdentityEncodingIsReversibleAndUnambiguous(t *testing.T) {
	raw := "../BTC__USDT/季度"
	encoded := EncodeIdentity(raw)
	if strings.Contains(encoded, "/") || strings.Contains(encoded, "__") || encoded == ".." {
		t.Fatalf("unsafe encoded identity %q", encoded)
	}
	decoded, err := DecodeIdentity(encoded)
	if err != nil || decoded != raw {
		t.Fatalf("DecodeIdentity(%q) = %q, %v", encoded, decoded, err)
	}
}

func TestMonthUsesUTC(t *testing.T) {
	ts := time.Date(2026, 7, 1, 0, 30, 0, 0, time.FixedZone("UTC+8", 8*3600))
	if got := MonthOf(ts); got != "202606" {
		t.Fatalf("MonthOf() = %s, want 202606", got)
	}
}

func TestParseFileName_RoundTripsPartitionKey(t *testing.T) {
	key := PartitionKey{SpaceID: "crypto", DatasetID: "kline", SubjectID: "BTC", Freq: "1m", Month: "202601"}
	name, err := key.FileName()
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseFileName(name)
	if err != nil {
		t.Fatal(err)
	}
	if got != key {
		t.Fatalf("ParseFileName() = %+v, want %+v", got, key)
	}
}

func TestPartitionKeyValidate_RejectsInvalidMonth(t *testing.T) {
	key := PartitionKey{SpaceID: "crypto", DatasetID: "kline", SubjectID: "BTC", Freq: "1m", Month: "bad"}
	if err := key.Validate(); err == nil {
		t.Fatal("expected invalid month")
	}
}
