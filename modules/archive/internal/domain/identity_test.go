package domain

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPartitionPathCarriesAllIdentityFields(t *testing.T) {
	key := PartitionKey{SpaceID: "crypto", DatasetID: "spot_kline_1h", SubjectID: "BTC-USDT", Freq: "1h", SeriesTag: "venue:binance", Month: "202606"}
	want := filepath.Join("crypto", "spot_kline_1h", "1h", "BTC-USDT", "series_tag=venue%3Abinance", "crypto__spot_kline_1h__BTC-USDT__1h__series_tag=venue%3Abinance__202606.parquet")
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
	key := PartitionKey{SpaceID: "crypto", DatasetID: "kline", SubjectID: "BTC", Freq: "1m", SeriesTag: "venue:binance", Month: "202601"}
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

func TestPartitionPathsKeepSeriesTagsDistinctAndInsideRoot(t *testing.T) {
	root := t.TempDir()
	tags := []string{"venue:binance", "venue:okx", "", "%", "/", ".."}
	seen := map[string]bool{}
	for _, tag := range tags {
		key := PartitionKey{SpaceID: "crypto", DatasetID: "kline", SubjectID: "BTC", Freq: "1m", SeriesTag: tag, Month: "202601"}
		path, err := key.AbsolutePath(root)
		if err != nil {
			t.Fatalf("tag %q: %v", tag, err)
		}
		if seen[path] {
			t.Fatalf("tag %q collided at %s", tag, path)
		}
		seen[path] = true
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			t.Fatalf("tag %q escaped root: %s", tag, path)
		}
		parsed, err := ParseFileName(filepath.Base(path))
		if err != nil {
			t.Fatalf("parse tag %q: %v", tag, err)
		}
		if parsed.SeriesTag != tag {
			t.Fatalf("tag round trip: got %q want %q", parsed.SeriesTag, tag)
		}
	}
}

func TestLogicalRowIDDependsOnlyOnUTCTime(t *testing.T) {
	a := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	b := a.In(time.FixedZone("offset", 8*60*60))
	if LogicalRowID(a) != LogicalRowID(b) {
		t.Fatal("same instant must have one logical row id")
	}
}

func TestPartitionKeyValidate_RejectsInvalidMonth(t *testing.T) {
	key := PartitionKey{SpaceID: "crypto", DatasetID: "kline", SubjectID: "BTC", Freq: "1m", Month: "bad"}
	if err := key.Validate(); err == nil {
		t.Fatal("expected invalid month")
	}
}

func TestParseArchivePathRejectsLegacyAndTagMismatch(t *testing.T) {
	legacy := filepath.Join("crypto", "kline", "1h", "BTC", "crypto__kline__BTC__1h__202601.parquet")
	if _, err := ParseArchivePath(legacy); err == nil {
		t.Fatal("expected legacy path rejection")
	}
	mismatch := filepath.Join("crypto", "kline", "1h", "BTC", "series_tag=venue%3Aokx", "crypto__kline__BTC__1h__series_tag=venue%3Abinance__202601.parquet")
	if _, err := ParseArchivePath(mismatch); err == nil {
		t.Fatal("expected tag mismatch rejection")
	}
}

func TestParseArchivePathRejectsAnyParentIdentityMismatch(t *testing.T) {
	key := PartitionKey{SpaceID: "crypto", DatasetID: "kline", SubjectID: "BTC", Freq: "1h", SeriesTag: "venue:binance", Month: "202601"}
	path, err := key.RelativePath()
	require.NoError(t, err)
	parts := strings.Split(filepath.ToSlash(path), "/")
	for i := 0; i < 4; i++ {
		broken := append([]string(nil), parts...)
		broken[i] = "wrong"
		_, err := ParseArchivePath(filepath.FromSlash(strings.Join(broken, "/")))
		require.Error(t, err, "parent component %d", i)
	}
}

func TestPartitionKeyValidateRejectsInvalidSeriesTagShape(t *testing.T) {
	key := PartitionKey{SpaceID: "crypto", DatasetID: "kline", SubjectID: "BTC", Freq: "1h", SeriesTag: " venue:okx", Month: "202601"}
	if err := key.Validate(); err == nil {
		t.Fatal("expected invalid series_tag rejection")
	}
}
