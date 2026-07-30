package pebble

import (
	"bytes"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func TestFieldAndAttributeNamespacesDoNotCollide(t *testing.T) {
	key := &pb.RowKey{SpaceId: "空间|%/\x00", DatasetId: "dataset", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-07-19T10:01:02Z"}}}
	field, err := encodeFieldKey(key, "volume", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	attribute, err := encodeAttributeKey(key, "source", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(field, attribute) || field[0] != fieldNamespace || attribute[0] != attributeNamespace {
		t.Fatalf("field=%x attribute=%x", field, attribute)
	}
}

func TestTimeSeriesKeyContainsBucketStart(t *testing.T) {
	key := &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: "x", Freq: "1m", DataTime: "2026-07-19T10:14:59Z"}}}
	a, err := encodeFieldKey(key, "close", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	key.GetTimeSeries().DataTime = "2026-07-19T10:15:01Z"
	b, err := encodeFieldKey(key, "close", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, b) {
		t.Fatal("different buckets must have different physical keys")
	}
}

func TestTimeSeriesKeyNormalizesEquivalentInstants(t *testing.T) {
	first := &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: "x", Freq: "1m", DataTime: "2026-01-01T00:00:00Z"}}}
	second := &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: "x", Freq: "1m", DataTime: "2026-01-01T08:00:00+08:00"}}}
	a, err := encodeFieldKey(first, "close", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	b, err := encodeFieldKey(second, "close", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("equivalent instants encoded differently:\n%x\n%x", a, b)
	}
}

func TestTimeSeriesBucketPrecedesSubjectInPhysicalKey(t *testing.T) {
	key := &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: "subject", Freq: "1m", DataTime: "2026-07-19T10:14:59Z", SeriesTag: "venue:okx"}}}
	encoded, err := encodeFieldKey(key, "close", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	parts, ok := decodePhysicalParts(encoded[2:])
	if !ok || len(parts) != 8 {
		t.Fatalf("parts=%v ok=%v", parts, ok)
	}
	want := []string{"s", "d", "2026-07-19T10:00:00.000000000Z", "subject", "1m", "2026-07-19T10:14:59.000000000Z", "venue:okx", "close"}
	for i := range want {
		if parts[i] != want[i] {
			t.Fatalf("physical order=%v, want %v", parts, want)
		}
	}
	if parts[2] != "2026-07-19T10:00:00.000000000Z" || parts[3] != "subject" {
		t.Fatalf("physical order=%v", parts)
	}
	if parts[6] != "venue:okx" {
		t.Fatalf("series tag identity=%q, want venue:okx", parts[6])
	}
}

func TestFieldAndAttributeKeysShareCompleteRowIdentity(t *testing.T) {
	key := &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{
		SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-07-19T10:14:59Z", SeriesTag: "venue:binance",
	}}}
	field, err := encodeFieldKey(key, "close", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	attribute, err := encodeAttributeKey(key, "source", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	fieldParts, fieldOK := decodePhysicalParts(field[2:])
	attributeParts, attributeOK := decodePhysicalParts(attribute[2:])
	if !fieldOK || !attributeOK || len(fieldParts) != 8 || len(attributeParts) != 8 {
		t.Fatalf("field=%v attribute=%v", fieldParts, attributeParts)
	}
	for i := 0; i < 7; i++ {
		if fieldParts[i] != attributeParts[i] {
			t.Fatalf("row identity differs: field=%v attribute=%v", fieldParts, attributeParts)
		}
	}
}

func TestTimeSeriesKeyAlwaysEndsWithSeriesTagComponent(t *testing.T) {
	key := func(tag string) *pb.RowKey {
		return &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{
			SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-07-19T10:14:59Z", SeriesTag: tag,
		}}}
	}
	encoded := make([][]byte, 0, 3)
	for _, tag := range []string{"", "venue:binance", "venue:okx"} {
		value, err := encodeFieldKey(key(tag), "close", 15*time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		parts, ok := decodePhysicalParts(value[2:])
		if !ok || len(parts) != 8 || parts[6] != tag {
			t.Fatalf("tag %q encoded as parts=%v ok=%v", tag, parts, ok)
		}
		encoded = append(encoded, value)
	}
	if bytes.Equal(encoded[0], encoded[1]) || bytes.Equal(encoded[0], encoded[2]) || bytes.Equal(encoded[1], encoded[2]) {
		t.Fatal("distinct series tags produced the same physical key")
	}
}

func TestNormalizeRowKeyPreservesSeriesTagAndNormalizesTime(t *testing.T) {
	key := &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{
		SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-01-01T08:00:00+08:00", SeriesTag: "市场:现货",
	}}}
	normalized, err := NormalizeRowKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if got := normalized.GetTimeSeries().GetDataTime(); got != "2026-01-01T00:00:00.000000000Z" {
		t.Fatalf("normalized data_time = %q", got)
	}
	if got := normalized.GetTimeSeries().GetSeriesTag(); got != "市场:现货" {
		t.Fatalf("normalized series_tag = %q", got)
	}
	if got := key.GetTimeSeries().GetDataTime(); got != "2026-01-01T08:00:00+08:00" {
		t.Fatalf("input key was mutated: %q", got)
	}
}

func TestNormalizeRowKeyRejectsInvalidSeriesTag(t *testing.T) {
	key := &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{
		SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-01-01T00:00:00Z", SeriesTag: " venue:okx",
	}}}
	if _, err := NormalizeRowKey(key); err == nil {
		t.Fatal("invalid series tag accepted")
	}
}

func TestRecordVersionIsPartOfPhysicalKey(t *testing.T) {
	base := &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}}
	first, err := encodeFieldKey(base, "value", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	base.GetRecord().Version = "10"
	second, err := encodeFieldKey(base, "value", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, second) {
		t.Fatal("versions must not overwrite one another")
	}
}

func TestRecordVersionPhysicalOrderMatchesUTF8Order(t *testing.T) {
	key := func(version string) []byte {
		t.Helper()
		value, err := encodeFieldKey(&pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: version}}}, "value", time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	if bytes.Compare(key("10"), key("2")) >= 0 {
		t.Fatal("physical version order does not match UTF-8 byte order")
	}
}

func TestRecordWriteKeyRequiresVersion(t *testing.T) {
	key := &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r"}}}
	if _, err := encodeFieldKey(key, "value", time.Hour); err == nil {
		t.Fatal("expected empty record version to be rejected")
	}
}
