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
