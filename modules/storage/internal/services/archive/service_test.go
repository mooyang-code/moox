package archive_test

import (
	"context"
	"path/filepath"
	"runtime"
	"strconv"
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

func TestServiceArchivesPebbleFactsAndRegistersArchiveFile(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	meta, err := metasqlite.Open(ctx, metasqlite.Options{
		Path:       filepath.Join(root, "metadata.db"),
		SchemaPath: schemaPath(t),
	})
	require.NoError(t, err)
	defer meta.Close()
	require.NoError(t, meta.InitSchema(ctx))

	_, err = meta.UpsertSpace(ctx, &pb.Space{SpaceId: "crypto", Name: "crypto"})
	require.NoError(t, err)
	_, err = meta.UpsertDataSource(ctx, &pb.DataSource{SpaceId: "crypto", DataSourceId: "binance", Name: "Binance", Kind: "exchange"})
	require.NoError(t, err)
	_, err = meta.UpsertDataSet(ctx, &pb.DataSet{
		SpaceId:      "crypto",
		DatasetId:    "kline",
		DataSourceId: "binance",
		Name:         "K线",
		DataKind:     pb.DataKind_DATA_KIND_TIME_SERIES,
		Status:       "active",
	})
	require.NoError(t, err)
	_, err = meta.UpsertStorageNode(ctx, &pb.StorageNode{NodeId: "node-1", Name: "node-1", Status: "active"})
	require.NoError(t, err)
	_, err = meta.UpsertDevice(ctx, &pb.Device{DeviceId: "archive-device", NodeId: "node-1", Name: "archive", Engine: "parquet_archive", Status: "active"})
	require.NoError(t, err)

	facts := testutil.OpenPebbleFactStore(t, root)
	require.NoError(t, facts.WriteRows(ctx, []*pb.DataRow{{
		Key: &pb.DataKey{
			Scope:    &pb.DataScope{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT", Freq: "1m"},
			DataTime: "2026-06-15T00:00:00Z",
		},
		Columns: []*pb.ColumnValue{
			testutil.DoubleValue("close", 8.1),
			testutil.DoubleValue("volume", 10),
		},
	}}, pb.WriteMode_WRITE_MODE_UPSERT))

	svc := archive.NewService(archive.Options{
		Metadata:    meta,
		Facts:       facts,
		ArchiveRoot: filepath.Join(root, "archive"),
		DeviceID:    "archive-device",
		Now: func() time.Time {
			return time.Date(2026, 6, 17, 0, 0, 0, 0, time.UTC)
		},
	})
	file, err := svc.ArchiveDataSet(ctx, "crypto", "kline", "date=2026-06-15", &pb.TimeRange{
		StartTime:      "2026-06-15T00:00:00Z",
		StartInclusive: true,
		EndTime:        "2026-06-16T00:00:00Z",
		EndInclusive:   true,
	})
	require.NoError(t, err)
	require.Equal(t, "parquet", file.GetFileFormat())
	require.Equal(t, uint64(2), file.GetRowCount())
	require.ElementsMatch(t, []string{"close", "volume"}, file.GetColumns())
	require.NotEmpty(t, file.GetContentHash())
	require.Equal(t, "active", file.GetStatus())

	listed, _, err := meta.ListArchiveFiles(ctx, "crypto", "kline", nil)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, file.GetArchiveFileId(), listed[0].GetArchiveFileId())

	path := strings.TrimPrefix(file.GetFileUri(), "file://")
	rows, err := parquetgo.ReadFile[deviceparquet.FactRow](path)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, "APT-USDT", rows[0].SubjectID)
}

func TestServiceArchivesAllPagesOfPebbleFacts(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	meta, err := metasqlite.Open(ctx, metasqlite.Options{
		Path:       filepath.Join(root, "metadata.db"),
		SchemaPath: schemaPath(t),
	})
	require.NoError(t, err)
	defer meta.Close()
	require.NoError(t, meta.InitSchema(ctx))

	_, err = meta.UpsertSpace(ctx, &pb.Space{SpaceId: "crypto", Name: "crypto"})
	require.NoError(t, err)
	_, err = meta.UpsertDataSource(ctx, &pb.DataSource{SpaceId: "crypto", DataSourceId: "binance", Name: "Binance", Kind: "exchange"})
	require.NoError(t, err)
	_, err = meta.UpsertDataSet(ctx, &pb.DataSet{
		SpaceId:      "crypto",
		DatasetId:    "kline",
		DataSourceId: "binance",
		Name:         "K线",
		DataKind:     pb.DataKind_DATA_KIND_TIME_SERIES,
		Status:       "active",
	})
	require.NoError(t, err)
	_, err = meta.UpsertStorageNode(ctx, &pb.StorageNode{NodeId: "node-1", Name: "node-1", Status: "active"})
	require.NoError(t, err)
	_, err = meta.UpsertDevice(ctx, &pb.Device{DeviceId: "archive-device", NodeId: "node-1", Name: "archive", Engine: "parquet_archive", Status: "active"})
	require.NoError(t, err)

	facts := testutil.OpenPebbleFactStore(t, root)
	rows := make([]*pb.DataRow, 0, 1001)
	for i := 0; i < 1001; i++ {
		rows = append(rows, &pb.DataRow{
			Key: &pb.DataKey{
				Scope:    &pb.DataScope{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT", Freq: "1m"},
				DataTime: time.Date(2026, 6, 15, 0, i, 0, 0, time.UTC).Format(time.RFC3339),
				RowId:    "row-" + strconv.Itoa(i),
			},
			Columns: []*pb.ColumnValue{testutil.DoubleValue("close", float64(i))},
		})
	}
	require.NoError(t, facts.WriteRows(ctx, rows, pb.WriteMode_WRITE_MODE_UPSERT))

	svc := archive.NewService(archive.Options{
		Metadata:    meta,
		Facts:       facts,
		ArchiveRoot: filepath.Join(root, "archive"),
		DeviceID:    "archive-device",
		Now: func() time.Time {
			return time.Date(2026, 6, 17, 0, 0, 0, 0, time.UTC)
		},
	})
	file, err := svc.ArchiveDataSet(ctx, "crypto", "kline", "date=2026-06-15", nil)
	require.NoError(t, err)
	require.Equal(t, uint64(1001), file.GetRowCount())

	path := strings.TrimPrefix(file.GetFileUri(), "file://")
	archivedRows, err := parquetgo.ReadFile[deviceparquet.FactRow](path)
	require.NoError(t, err)
	require.Len(t, archivedRows, 1001)
}

func schemaPath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../../../schema/storage_metadata.sql"))
}
