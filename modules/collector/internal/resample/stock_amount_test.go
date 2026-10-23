package resample

import (
	"context"
	"testing"
	"time"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

func (f *fakeStorage) putStockSource(subject string, at time.Time, volume float64, amount *float64) {
	fields := []*storagepb.FieldValue{doubleField("open", 10), doubleField("high", 11), doubleField("low", 9), doubleField("close", 10), doubleField("volume", volume)}
	if amount != nil {
		fields = append(fields, doubleField("amount", *amount))
	} else {
		fields = append(fields, &storagepb.FieldValue{FieldId: "amount", Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_NullValue{NullValue: storagepb.NullValue_NULL_VALUE_NULL}}})
	}
	row := &storagepb.RowFieldUpsert{Key: rowKey("stockcn", "dataset_src", subject, "1m", at, "default"), Fields: fields}
	f.rows[keyText(row.GetKey())] = row
}

func amountOf(row *storagepb.RowFieldUpsert) (float64, bool) {
	for _, field := range row.GetFields() {
		if field.GetFieldId() == "amount" {
			if _, isNull := field.GetValue().GetValue().(*storagepb.TypedValue_NullValue); isNull {
				return 0, false
			}
			return field.GetValue().GetDoubleValue(), true
		}
	}
	return 0, false
}

func TestRunBucketAStockUsesAmountInsteadOfQuoteVolume(t *testing.T) {
	storage := newFakeStorage()
	task := makeTask(t, "stockcn", "1m", "1h")
	task.Spec.SourceSeriesTag = "default"
	bucket, _, err := task.Clock.BucketAt(utc("2026-10-21T02:45:00Z"))
	require.NoError(t, err)
	amount := 1000.0
	for index := 0; index < 60; index++ {
		storage.putStockSource("600000", bucket.Start.Add(time.Duration(index)*time.Minute), 100, &amount)
		storage.putStockSource("600001", bucket.Start.Add(time.Duration(index)*time.Minute), 100, &amount)
	}
	// 600001 有一根源 K 线没有真实成交额：目标成交额写 NULL，其余字段照常合成。
	storage.putStockSource("600001", bucket.Start.Add(3*time.Minute), 100, nil)
	engine := &Engine{Primary: storage}
	result, err := engine.RunBucket(context.Background(), task, bucket, []string{"600000", "600001"}, "")
	require.NoError(t, err)
	require.Equal(t, 2, result.Written, "没有 quote_volume / trade_num 的 A 股源数据不能被当作缺字段跳过")
	got, ok := amountOf(storage.target("stockcn", "dataset_task", "600000", "1h", "default", bucket.Key))
	require.True(t, ok)
	require.InDelta(t, 60000, got, 1e-9)
	_, ok = amountOf(storage.target("stockcn", "dataset_task", "600001", "1h", "default", bucket.Key))
	require.False(t, ok)
}
