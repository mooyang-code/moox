package domain

import (
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
	"time"
)

func TestScalarFromColumnPreservesZeroValues(t *testing.T) {
	zero := int64(0)
	falseValue := false
	cases := []*storagepb.ColumnValue{
		{ColumnName: "trade_num", ValueType: storagepb.FieldValueType_FIELD_VALUE_TYPE_INT, Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_IntValue{IntValue: zero}}},
		{ColumnName: "closed", ValueType: storagepb.FieldValueType_FIELD_VALUE_TYPE_BOOL, Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_BoolValue{BoolValue: falseValue}}},
	}
	for _, column := range cases {
		scalar, err := ScalarFromColumn(column)
		if err != nil || scalar.PointerCount() != 1 {
			t.Fatalf("ScalarFromColumn(%s) = %#v, %v", column.GetColumnName(), scalar, err)
		}
	}
}

func TestScalarFromColumnRejectsTypeBranchMismatch(t *testing.T) {
	column := &storagepb.ColumnValue{ColumnName: "close", ValueType: storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_StringValue{StringValue: "1.25"}}}
	if _, err := ScalarFromColumn(column); err == nil || !strings.Contains(err.Error(), "type mismatch") {
		t.Fatalf("ScalarFromColumn() error = %v", err)
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
