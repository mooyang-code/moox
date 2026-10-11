package domain

import (
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestScalarFromFieldPreservesZeroValues(t *testing.T) {
	zero := int64(0)
	falseValue := false
	cases := map[string]*storagepb.TypedValue{
		"trade_num": {Value: &storagepb.TypedValue_IntValue{IntValue: zero}},
		"closed":    {Value: &storagepb.TypedValue_BoolValue{BoolValue: falseValue}},
	}
	for fieldID, value := range cases {
		scalar, err := ScalarFromField(fieldID, value)
		if err != nil || scalar.PointerCount() != 1 {
			t.Fatalf("ScalarFromField(%s) = %#v, %v", fieldID, scalar, err)
		}
	}
}

func TestCanonicalStringMap(t *testing.T) {
	raw, err := CanonicalStringMap(map[string]string{"b": "2", "a": "1"})
	require.NoError(t, err)
	assert.Equal(t, `{"a":"1","b":"2"}`, raw)
}

func TestMergePatchCombinesAttributesAndColumns(t *testing.T) {
	base := ArchiveRow{
		Attributes: map[string]string{"source": "live"},
		Columns: map[string]Scalar{
			"close": {Type: storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Double: ptrFloat(1.0)},
		},
	}
	patch := RowPatch{
		Partition:  PartitionKey{SpaceID: "crypto", DatasetID: "kline", SubjectID: "BTC", Freq: "1m", Month: "202601"},
		DataTime:   time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		Attributes: map[string]string{"source": "backfill", "batch": "1"},
		Columns: map[string]Scalar{
			"close": {Type: storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Double: ptrFloat(2.0)},
		},
		WrittenAt: time.Now().UTC(),
	}
	merged := MergePatch(base, patch)
	assert.Equal(t, "backfill", merged.Attributes["source"])
	assert.Equal(t, "1", merged.Attributes["batch"])
	assert.InDelta(t, 2.0, *merged.Columns["close"].Double, 0.001)
}

func TestSortedColumnNames(t *testing.T) {
	names := SortedColumnNames(map[string]storagepb.FieldValueType{
		"close": storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
		"open":  storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
	})
	assert.Equal(t, []string{"close", "open"}, names)
}

func ptrFloat(v float64) *float64 { return &v }

// 字段级最后写入者胜：重投的旧事件不能盖掉其他批次已经落盘的新值，删除留下墓碑，旧值不会把已删除字段补回来。
// 每一步都把合并结果当作下一次物化的输入（模拟跨批次、跨重启）。
func TestMergePatchIsLastWriterWinsPerFieldAcrossMaterializations(t *testing.T) {
	partition := PartitionKey{SpaceID: "crypto", DatasetID: "kline", SubjectID: "BTC", Freq: "1m", Month: "202601"}
	at := func(minute int) time.Time { return time.Date(2026, 1, 2, 10, minute, 0, 0, time.UTC) }
	double := func(v float64) Scalar {
		return Scalar{Type: storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Double: ptrFloat(v)}
	}
	patch := func(minute int, attrs map[string]string, columns map[string]Scalar) RowPatch {
		return RowPatch{Partition: partition, DataTime: at(0), WrittenAt: at(minute), Attributes: attrs, Columns: columns}
	}
	var row ArchiveRow
	row = MergePatch(row, patch(1, nil, map[string]Scalar{"close": double(1)}))                                  // t1: close=1
	row = MergePatch(row, patch(3, map[string]string{"source": "live"}, map[string]Scalar{"volume": double(9)})) // t3: volume=9（先落盘）
	row = MergePatch(row, patch(2, map[string]string{"source": "old"}, map[string]Scalar{"close": double(2)}))   // t2 迟到重投
	assert.InDelta(t, 2.0, *row.Columns["close"].Double, 0.001, "t2 的 close 比 t1 新，必须生效，不能因为整行已是 t3 而被丢弃")
	assert.InDelta(t, 9.0, *row.Columns["volume"].Double, 0.001)
	assert.Equal(t, "live", row.Attributes["source"], "属性同样按字段版本：t3 的 source 不能被 t2 覆盖")
	assert.True(t, row.WrittenAt.Equal(at(3)))

	// 较新的补丁删除字段后，较旧补丁重投不能把它补回来。
	row = MergePatch(row, patch(5, nil, map[string]Scalar{"close": {Null: true}}))
	assert.NotContains(t, row.Columns, "close")
	row = MergePatch(row, patch(4, nil, map[string]Scalar{"close": double(7)}))
	assert.NotContains(t, row.Columns, "close", "删除的墓碑版本比旧补丁新，旧值不能复活")
	// 之后更新的写入仍可重新写入该字段。
	row = MergePatch(row, patch(6, nil, map[string]Scalar{"close": double(8)}))
	assert.InDelta(t, 8.0, *row.Columns["close"].Double, 0.001)
}
