package parquet_test

import (
	"context"
	"path/filepath"
	"testing"

	deviceparquet "github.com/mooyang-code/moox/modules/storage/internal/infra/device/parquet"
	"github.com/mooyang-code/moox/modules/storage/internal/testutil"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	parquetgo "github.com/parquet-go/parquet-go"
	"github.com/stretchr/testify/require"
)

func TestWriteFactsExpandsTimeSeriesRowsToReadableParquet(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "facts.parquet")
	rows := []*pb.TimeSeriesRow{{
		Key: &pb.TimeSeriesKey{
			SpaceId:    "crypto",
			DatasetId:  "kline",
			SubjectId:  "APT-USDT",
			Freq:       "1m",
			Dimensions: map[string]string{"adjust_type": "raw"},
			DataTime:   "2026-06-15T00:00:00Z",
		},
		Columns: []*pb.ColumnValue{
			testutil.DoubleValue("close", 8.1),
			testutil.StringValue("status", "active"),
		},
		Attributes: map[string]string{"source": "acceptance"},
	}}

	manifest, err := deviceparquet.WriteFacts(ctx, path, rows)
	require.NoError(t, err)
	require.Equal(t, uint64(2), manifest.RowCount)
	require.ElementsMatch(t, []string{"close", "status"}, manifest.Columns)
	require.NotEmpty(t, manifest.ContentHash)

	facts, err := parquetgo.ReadFile[deviceparquet.FactRow](path)
	require.NoError(t, err)
	require.Len(t, facts, 2)
	require.Equal(t, "crypto", facts[0].SpaceID)
	require.Equal(t, "kline", facts[0].DatasetID)
	require.Equal(t, "APT-USDT", facts[0].SubjectID)
	require.Equal(t, "1m", facts[0].Freq)
	require.Equal(t, "2026-06-15T00:00:00Z", facts[0].DataTime)
	require.Equal(t, "2026-06-15T00:00:00Z", facts[0].RowID)
	require.JSONEq(t, `{"adjust_type":"raw"}`, facts[0].DimensionsJSON)
	require.JSONEq(t, `{"source":"acceptance"}`, facts[0].AttributesJSON)
}
