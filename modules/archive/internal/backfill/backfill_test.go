package backfill

import (
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestPlanRequiresExplicitConfirmation(t *testing.T) {
	plan := Plan{SpaceID: "crypto_binance", DatasetID: "spot_kline", SubjectID: "BTC-USDT", Freq: "1m", Start: "2026-06-01T00:00:00Z", End: "2026-07-01T00:00:00Z"}
	if plan.Confirm {
		t.Fatal("new plan unexpectedly confirmed")
	}
	if len(plan.Partitions()) != 2 {
		t.Fatalf("partitions = %v", plan.Partitions())
	}
}

func TestNormalizeTarget(t *testing.T) {
	assert.Equal(t, "ip://127.0.0.1:20102", NormalizeTarget("", "20102"))
	assert.Equal(t, "ip://127.0.0.1:20102", NormalizeTarget("127.0.0.1:20102", "20102"))
	assert.Equal(t, "http://storage:20102", NormalizeTarget("http://storage:20102", "20102"))
}

func TestRowsToPatches(t *testing.T) {
	writtenAt := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	rows := []*storagepb.TimeSeriesRow{{
		Key: &storagepb.TimeSeriesKey{
			SpaceId: "crypto", DatasetId: "kline", SubjectId: "BTC", Freq: "1m",
			DataTime: "2026-01-02T03:04:05Z",
		},
		Attributes: map[string]string{"source": "live"},
		Columns: []*storagepb.ColumnValue{{
			ColumnName: "close",
			ValueType:  storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
			Value:      &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: 1.25}},
		}},
	}}
	patches, err := rowsToPatches(rows, writtenAt)
	require.NoError(t, err)
	require.Len(t, patches, 1)
	assert.Equal(t, "crypto", patches[0].Partition.SpaceID)
	assert.InDelta(t, 1.25, *patches[0].Columns["close"].Double, 0.001)
	_, err = rowsToPatches([]*storagepb.TimeSeriesRow{nil}, writtenAt)
	require.Error(t, err)
}

func TestPlanPartitions(t *testing.T) {
	plan := Plan{Start: "2026-01-15T00:00:00Z", End: "2026-02-10T00:00:00Z"}
	assert.Equal(t, []string{"202601", "202602"}, plan.Partitions())
	assert.Nil(t, Plan{Start: "bad", End: "2026-02-10T00:00:00Z"}.Partitions())
}

func TestBackfillerRunRequiresConfirm(t *testing.T) {
	b := New(nil, nil, nil, nil)
	_, err := b.Run(nil, Plan{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "confirm")
}
