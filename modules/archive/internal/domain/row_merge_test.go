package domain

import (
	"testing"
	"time"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
		Partition: PartitionKey{SpaceID: "crypto", DatasetID: "kline", SubjectID: "BTC", Freq: "1m", Month: "202601"},
		DataTime:  time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
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
