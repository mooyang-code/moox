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
	key := &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: "subject", Freq: "1m", DataTime: "2026-07-19T10:14:59Z"}}}
	encoded, err := encodeFieldKey(key, "close", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	parts, ok := decodePhysicalParts(encoded[2:])
	if !ok || len(parts) != 7 {
		t.Fatalf("parts=%v ok=%v", parts, ok)
	}
	if parts[2] != "2026-07-19T10:00:00.000000000Z" || parts[3] != "subject" {
		t.Fatalf("physical order=%v", parts)
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
