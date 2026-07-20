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
	key.GetTimeSeries().DataTime = "2026-07-19T10:00:01Z"
	b, err := encodeFieldKey(key, "close", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, b) {
		t.Fatal("different buckets must have different physical keys")
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
