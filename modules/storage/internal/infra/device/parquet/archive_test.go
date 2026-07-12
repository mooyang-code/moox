package parquet

import (
	"context"
	"path/filepath"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteFacts_TimeSeriesRows_ShouldCreateParquetAndManifest(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "archive", "facts.parquet")
	rows := []*pb.TimeSeriesRow{{
		Key: &pb.TimeSeriesKey{
			SpaceId:   "crypto",
			DatasetId: "binance_kline",
			SubjectId: "BTCUSDT",
			Freq:      "1m",
			DataTime:  "2026-07-12T10:00:00Z",
		},
		Columns: []*pb.ColumnValue{{
			ColumnName: "close",
			ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
			Value:      &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 100.5}},
		}},
	}}

	manifest, err := WriteFacts(ctx, path, rows)
	require.NoError(t, err)
	require.NotNil(t, manifest)
	assert.Equal(t, uint64(1), manifest.RowCount)
	assert.Equal(t, []string{"close"}, manifest.Columns)
	assert.Equal(t, "2026-07-12T10:00:00Z", manifest.MinTime)
	assert.Equal(t, "2026-07-12T10:00:00Z", manifest.MaxTime)
	assert.NotEmpty(t, manifest.ContentHash)
}

func TestWriteFacts_EmptyRows_ShouldCreateEmptyArchive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.parquet")
	manifest, err := WriteFacts(context.Background(), path, nil)
	require.NoError(t, err)
	require.NotNil(t, manifest)
	assert.Equal(t, uint64(0), manifest.RowCount)
	assert.Empty(t, manifest.Columns)
}
