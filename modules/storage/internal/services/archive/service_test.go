package archive_test

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	deviceparquet "github.com/mooyang-code/moox/modules/storage/internal/infra/device/parquet"
	metasqlite "github.com/mooyang-code/moox/modules/storage/internal/infra/metadata/sqlite"
	"github.com/mooyang-code/moox/modules/storage/internal/services/archive"
	"github.com/mooyang-code/moox/modules/storage/internal/testutil"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	parquetgo "github.com/parquet-go/parquet-go"
	"github.com/stretchr/testify/require"
)

func TestServiceArchivesRowsAndRegistersArchiveFile(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	meta := openArchiveMetadata(t, ctx, root)
	reader := fakeTimeSeriesReader{rows: []*pb.TimeSeriesRow{
		timeSeriesRow("APT-USDT", "2026-06-15T00:00:00Z", []*pb.ColumnValue{testutil.DoubleValue("close", 8.1), testutil.DoubleValue("volume", 10)}),
	}}
	svc := archive.NewService(archive.Options{
		Metadata: meta, Facts: reader, ArchiveRoot: filepath.Join(root, "archive"), DeviceID: "archive-device",
		Now: func() time.Time { return time.Date(2026, 6, 17, 0, 0, 0, 0, time.UTC) },
	})

	file, err := svc.ArchiveDataset(ctx, "crypto", "kline", "date=2026-06-15", &pb.TimeRange{StartTime: "2026-06-15T00:00:00Z"})
	require.NoError(t, err)
	require.Equal(t, "parquet", file.GetFileFormat())
	require.Equal(t, uint64(2), file.GetRowCount())
	require.ElementsMatch(t, []string{"close", "volume"}, file.GetColumns())

	listed, _, err := meta.ListArchiveFiles(ctx, "crypto", "kline", nil)
	require.NoError(t, err)
	require.Len(t, listed, 1)

	rows, err := parquetgo.ReadFile[deviceparquet.FactRow](strings.TrimPrefix(file.GetFileUri(), "file://"))
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, "APT-USDT", rows[0].SubjectID)
}

func TestServiceArchivesWithDefaultActiveParquetDevice(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	meta := openArchiveMetadata(t, ctx, root)
	reader := fakeTimeSeriesReader{rows: []*pb.TimeSeriesRow{
		timeSeriesRow("APT-USDT", "2026-06-15T00:00:00Z", []*pb.ColumnValue{testutil.DoubleValue("close", 8.1)}),
	}}
	svc := archive.NewService(archive.Options{Metadata: meta, Facts: reader, ArchiveRoot: filepath.Join(root, "archive")})

	file, err := svc.ArchiveDataset(ctx, "crypto", "kline", "", nil)
	require.NoError(t, err)
	require.Equal(t, "archive-device", file.GetDeviceId())
}

func TestArchiveDatasetsSkipsRecordDatasets(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	meta := openArchiveMetadata(t, ctx, root)
	_, err := meta.UpsertDataset(ctx, &pb.Dataset{SpaceId: "crypto", DatasetId: "symbols", DataSourceId: "binance", Name: "Symbols", DataKind: pb.DataKind_DATA_KIND_RECORD, Status: "active"})
	require.NoError(t, err)
	reader := fakeTimeSeriesReader{rows: []*pb.TimeSeriesRow{
		timeSeriesRow("APT-USDT", "2026-06-15T00:00:00Z", []*pb.ColumnValue{testutil.DoubleValue("close", 8.1)}),
	}}
	svc := archive.NewService(archive.Options{Metadata: meta, Facts: reader, ArchiveRoot: filepath.Join(root, "archive"), DeviceID: "archive-device"})

	files, err := svc.ArchiveDatasets(ctx, "crypto", "", nil)
	require.NoError(t, err)
	require.Len(t, files, 1)
	require.Equal(t, "kline", files[0].GetDatasetId())
}

// fakeTimeSeriesReader 是归档测试使用的 TimeSeries 读取桩。
type fakeTimeSeriesReader struct {
	rows []*pb.TimeSeriesRow
}

func (f fakeTimeSeriesReader) ReadTimeSeriesRows(ctx context.Context, req *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error) {
	_ = ctx
	return &pb.ReadTimeSeriesRowsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, Rows: f.rows, PageResult: &pb.PageResult{Total: uint64(len(f.rows))}}, nil
}

func openArchiveMetadata(t *testing.T, ctx context.Context, root string) *metasqlite.Store {
	t.Helper()
	meta, err := metasqlite.Open(ctx, metasqlite.Options{Path: filepath.Join(root, "metadata.db"), SchemaPath: schemaPath(t)})
	require.NoError(t, err)
	require.NoError(t, meta.InitSchema(ctx))
	t.Cleanup(func() { require.NoError(t, meta.Close()) })
	_, err = meta.UpsertSpace(ctx, &pb.Space{SpaceId: "crypto", Name: "crypto"})
	require.NoError(t, err)
	_, err = meta.UpsertDataSource(ctx, &pb.DataSource{SpaceId: "crypto", DataSourceId: "binance", Name: "Binance", Kind: "exchange"})
	require.NoError(t, err)
	_, err = meta.UpsertDataset(ctx, &pb.Dataset{SpaceId: "crypto", DatasetId: "kline", DataSourceId: "binance", Name: "K线", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freqs: []string{"1m"}, Status: "active"})
	require.NoError(t, err)
	_, err = meta.UpsertSubject(ctx, &pb.Subject{SpaceId: "crypto", SubjectId: "APT-USDT", SubjectType: "crypto_pair", Name: "APT-USDT", Status: "active"})
	require.NoError(t, err)
	_, err = meta.BindDatasetSubject(ctx, &pb.DatasetSubject{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT", Status: "active"})
	require.NoError(t, err)
	_, err = meta.UpsertPrimaryStoreNode(ctx, &pb.PrimaryStoreNode{NodeId: "node-1", Name: "node-1", Status: "active"})
	require.NoError(t, err)
	_, err = meta.UpsertDevice(ctx, &pb.Device{DeviceId: "archive-device", NodeId: "node-1", Name: "archive", Engine: "parquet_archive", Status: "active"})
	require.NoError(t, err)
	return meta
}

func timeSeriesRow(subjectID string, dataTime string, columns []*pb.ColumnValue) *pb.TimeSeriesRow {
	return &pb.TimeSeriesRow{
		Key:     &pb.TimeSeriesKey{SpaceId: "crypto", DatasetId: "kline", SubjectId: subjectID, Freq: "1m", DataTime: dataTime},
		Columns: columns,
	}
}

func schemaPath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "schema", "metadata.sql")
}
