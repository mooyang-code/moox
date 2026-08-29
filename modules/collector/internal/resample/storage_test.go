package resample

import (
	"context"
	"testing"
	"time"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

type fakePrimary struct {
	rows   []*storagepb.RowFieldValues
	writes []*storagepb.RowFieldUpsert
}

func (f *fakePrimary) ReadFields(_ context.Context, keys []*storagepb.RowKey, _ []string, attrs []string) ([]*storagepb.RowFieldValues, error) {
	if len(attrs) > 0 {
		for _, key := range keys {
			for _, row := range f.writes {
				if sameKey(key, row.GetKey()) {
					return []*storagepb.RowFieldValues{{Key: key, Attributes: row.GetAttributes()}}, nil
				}
			}
		}
		return nil, nil
	}
	return f.rows, nil
}
func (f *fakePrimary) UpsertFieldsWithSource(_ context.Context, rows []*storagepb.RowFieldUpsert, _ string) error {
	f.writes = append(f.writes, rows...)
	return nil
}
func sameKey(a, b *storagepb.RowKey) bool {
	return a.GetSpaceId() == b.GetSpaceId() && a.GetDatasetId() == b.GetDatasetId() && a.GetTimeSeries().GetSubjectId() == b.GetTimeSeries().GetSubjectId() && a.GetTimeSeries().GetFreq() == b.GetTimeSeries().GetFreq() && a.GetTimeSeries().GetDataTime() == b.GetTimeSeries().GetDataTime() && a.GetTimeSeries().GetSeriesTag() == b.GetTimeSeries().GetSeriesTag()
}

func TestBucketStorageProcessBucketIsIdempotentBySourceHash(t *testing.T) {
	freq1, _ := ParseFixedFrequency("1m")
	freq5, _ := ParseFixedFrequency("5m")
	start := time.Unix(300, 0).UTC()
	end := start.Add(freq5.Duration)
	rows := make([]*storagepb.RowFieldValues, 0, 5)
	for i := 0; i < 5; i++ {
		at := start.Add(time.Duration(i) * time.Minute)
		fields := []*storagepb.FieldValue{}
		for _, item := range []struct {
			name  string
			value float64
		}{{"open", float64(i + 1)}, {"high", float64(i + 2)}, {"low", float64(i)}, {"close", float64(i + 1)}, {"volume", 1}, {"quote_volume", 2}} {
			fields = append(fields, &storagepb.FieldValue{FieldId: item.name, Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: item.value}}})
		}
		fields = append(fields, &storagepb.FieldValue{FieldId: "trade_num", Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_IntValue{IntValue: 1}}})
		rows = append(rows, &storagepb.RowFieldValues{Key: rowKey("s", "src", "BTC", "1m", at, "venue:binance"), Fields: fields})
	}
	fake := &fakePrimary{rows: rows}
	spec := RuleSpec{RuleID: "r", SpaceID: "s", SourceDatasetID: "src", SourceFrequency: freq1, SourceSeriesTag: "venue:binance", TargetDatasetID: "spot_kline_derived_5m", TargetFrequency: freq5, Alignment: AlignmentEpochUTC}
	result, wrote, err := (&BucketStorage{Primary: fake}).ProcessBucket(context.Background(), spec, "BTC", start, end)
	if err != nil || !wrote {
		t.Fatalf("first process = %#v/%v/%v", result, wrote, err)
	}
	if len(fake.writes) != 1 {
		t.Fatalf("writes=%d", len(fake.writes))
	}
	_, wrote, err = (&BucketStorage{Primary: fake}).ProcessBucket(context.Background(), spec, "BTC", start, end)
	if err != nil || wrote {
		t.Fatalf("second process wrote=%v err=%v", wrote, err)
	}
}
