package pebble_test

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/infra/device/factkey"
	"github.com/mooyang-code/moox/modules/storage/internal/infra/device/pebble"
	"github.com/mooyang-code/moox/modules/storage/internal/testutil"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/require"
)

func TestStoreWritesAndReadsTimeSeriesRange(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)
	key := timeSeriesStoreKey("APT-USDT", "1m", "2026-06-15T00:00:00Z")

	require.NoError(t, store.WriteRows(ctx, []*pb.PrimaryStoreRow{
		{Key: key, Columns: []*pb.ColumnValue{testutil.DoubleValue("close", 8.1)}},
		{Key: timeSeriesStoreKey("APT-USDT", "1m", "2026-06-15T00:01:00Z"), Columns: []*pb.ColumnValue{testutil.DoubleValue("close", 8.2)}},
	}))

	got, page, err := store.ReadRows(ctx, []*pb.PrimaryStoreKey{withoutVersion(key)}, &pb.VersionRange{
		StartVersion: normalizeTime(t, "2026-06-15T00:00:00Z"),
		EndVersion:   normalizeTime(t, "2026-06-15T00:01:00Z"),
	}, pb.SortOrder_SORT_ORDER_ASC, []string{"close"}, nil)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, uint64(2), page.GetTotal())
	require.Equal(t, "close", got[0].GetColumns()[0].GetColumnName())
}

func TestStoreReadsSubsecondRowsAfterWholeSecondLowerBound(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)
	key := timeSeriesStoreKey("APT-USDT", "1m", "2026-01-01T00:00:00.5Z")
	require.NoError(t, store.WriteRows(ctx, []*pb.PrimaryStoreRow{
		{Key: key, Columns: []*pb.ColumnValue{testutil.DoubleValue("close", 8.1)}},
		{Key: timeSeriesStoreKey("APT-USDT", "1m", "2026-01-01T00:00:01Z"), Columns: []*pb.ColumnValue{testutil.DoubleValue("close", 8.2)}},
	}))

	got, _, err := store.ReadRows(ctx, []*pb.PrimaryStoreKey{withoutVersion(key)}, &pb.VersionRange{
		StartVersion: normalizeTime(t, "2026-01-01T00:00:00Z"),
		EndVersion:   normalizeTime(t, "2026-01-01T00:00:01Z"),
	}, pb.SortOrder_SORT_ORDER_ASC, nil, nil)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, normalizeTime(t, "2026-01-01T00:00:00.5Z"), got[0].GetKey().GetVersion())
	require.Equal(t, normalizeTime(t, "2026-01-01T00:00:01Z"), got[1].GetKey().GetVersion())
}

func TestStoreDescCursorPaginationDoesNotRepeatRows(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)
	key := timeSeriesStoreKey("APT-USDT", "1m", "2026-06-15T00:00:00Z")
	require.NoError(t, store.WriteRows(ctx, []*pb.PrimaryStoreRow{
		{Key: key, Columns: []*pb.ColumnValue{testutil.DoubleValue("close", 8.1)}},
		{Key: timeSeriesStoreKey("APT-USDT", "1m", "2026-06-15T00:01:00Z"), Columns: []*pb.ColumnValue{testutil.DoubleValue("close", 8.2)}},
		{Key: timeSeriesStoreKey("APT-USDT", "1m", "2026-06-15T00:02:00Z"), Columns: []*pb.ColumnValue{testutil.DoubleValue("close", 8.3)}},
	}))

	first, page, err := store.ReadRows(ctx, []*pb.PrimaryStoreKey{withoutVersion(key)}, nil, pb.SortOrder_SORT_ORDER_DESC, nil, &pb.Page{Size: 2})
	require.NoError(t, err)
	require.Len(t, first, 2)
	require.True(t, page.GetHasMore())
	require.NotEmpty(t, page.GetNextCursor())
	require.Equal(t, normalizeTime(t, "2026-06-15T00:02:00Z"), first[0].GetKey().GetVersion())
	require.Equal(t, normalizeTime(t, "2026-06-15T00:01:00Z"), first[1].GetKey().GetVersion())

	second, page, err := store.ReadRows(ctx, []*pb.PrimaryStoreKey{withoutVersion(key)}, nil, pb.SortOrder_SORT_ORDER_DESC, nil, &pb.Page{Size: 2, Cursor: page.GetNextCursor()})
	require.NoError(t, err)
	require.Len(t, second, 1)
	require.False(t, page.GetHasMore())
	require.Equal(t, normalizeTime(t, "2026-06-15T00:00:00Z"), second[0].GetKey().GetVersion())
}

func TestStorePatchMergesColumns(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)
	key := timeSeriesStoreKey("APT-USDT", "1m", "2026-06-15T00:00:00Z")
	require.NoError(t, store.WriteRows(ctx, []*pb.PrimaryStoreRow{{
		Key: key, Columns: []*pb.ColumnValue{testutil.DoubleValue("open", 8.0), testutil.DoubleValue("close", 8.1)},
	}}))
	require.NoError(t, store.WriteRows(ctx, []*pb.PrimaryStoreRow{{
		Key: key, Columns: []*pb.ColumnValue{testutil.DoubleValue("close", 8.2)},
	}}))
	require.NoError(t, store.WriteRows(ctx, []*pb.PrimaryStoreRow{{
		Key: key, Columns: []*pb.ColumnValue{{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}},
	}}))

	got, _, err := store.ReadRows(ctx, []*pb.PrimaryStoreKey{key}, nil, pb.SortOrder_SORT_ORDER_ASC, nil, nil)
	require.NoError(t, err)
	require.Len(t, got, 1)
	values := columnDoubles(got[0])
	require.Equal(t, 8.0, values["open"])
	require.Equal(t, 8.2, values["close"])
}

func TestStoreSeparatesRecordAndTimeSeriesKeySpaces(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)
	recordKey := &pb.PrimaryStoreKey{SpaceId: "crypto", DatasetId: "symbols", DataKind: pb.DataKind_DATA_KIND_RECORD, Key: "APT-USDT", Version: "v1"}
	timeKey := timeSeriesStoreKey("APT-USDT", "1m", "2026-06-15T00:00:00Z")
	timeKey.DatasetId = "symbols"
	require.NoError(t, store.WriteRows(ctx, []*pb.PrimaryStoreRow{
		{Key: recordKey, Columns: []*pb.ColumnValue{testutil.StringValue("name", "Aptos")}},
		{Key: timeKey, Columns: []*pb.ColumnValue{testutil.DoubleValue("close", 8.1)}},
	}))

	records, _, err := store.ReadRows(ctx, []*pb.PrimaryStoreKey{recordKey}, nil, pb.SortOrder_SORT_ORDER_ASC, nil, nil)
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, pb.DataKind_DATA_KIND_RECORD, records[0].GetKey().GetDataKind())
}

func TestStoreScansDatasetPrefixByDataKind(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)
	timeKey := timeSeriesStoreKey("APT-USDT", "1m", "2026-06-15T00:00:00Z")
	recordKey := recordStoreKey("APT-USDT", "2026-06-15T00:00:00Z")
	require.NoError(t, store.WriteRows(ctx, []*pb.PrimaryStoreRow{
		{Key: timeKey, Columns: []*pb.ColumnValue{testutil.DoubleValue("close", 8.1)}},
		{Key: timeSeriesStoreKey("APT-USDT", "1m", "2026-06-15T00:01:00Z"), Columns: []*pb.ColumnValue{testutil.DoubleValue("close", 8.2)}},
		{Key: recordKey, Columns: []*pb.ColumnValue{testutil.StringValue("name", "Aptos")}},
	}))

	rows, page, err := store.ScanRows(ctx, &pb.PrimaryStoreTarget{SpaceId: "crypto", DatasetId: "kline"}, pb.DataKind_DATA_KIND_TIME_SERIES, &pb.VersionRange{
		StartVersion: "2026-06-15T00:00:30Z",
		EndVersion:   "2026-06-15T00:01:30Z",
	}, pb.SortOrder_SORT_ORDER_ASC, []string{"close"}, &pb.Page{Size: 1})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, normalizeTime(t, "2026-06-15T00:01:00Z"), rows[0].GetKey().GetVersion())
	require.Equal(t, uint64(1), page.GetTotal())
	require.Equal(t, "close", rows[0].GetColumns()[0].GetColumnName())

	records, _, err := store.ScanRows(ctx, &pb.PrimaryStoreTarget{SpaceId: "crypto", DatasetId: "kline"}, pb.DataKind_DATA_KIND_RECORD, nil, pb.SortOrder_SORT_ORDER_ASC, nil, nil)
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, pb.DataKind_DATA_KIND_RECORD, records[0].GetKey().GetDataKind())
}

func openStore(t *testing.T) *pebble.Store {
	t.Helper()
	store, err := pebble.Open(pebble.Options{Path: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	return store
}

func timeSeriesStoreKey(subjectID string, freq string, dataTime string) *pb.PrimaryStoreKey {
	normalized, err := factkey.NormalizeTimeVersion(dataTime)
	if err != nil {
		panic(err)
	}
	return &pb.PrimaryStoreKey{
		SpaceId:   "crypto",
		DatasetId: "kline",
		DataKind:  pb.DataKind_DATA_KIND_TIME_SERIES,
		Key:       factkey.BuildTimeSeriesDataKey(subjectID, freq, nil),
		Version:   normalized,
	}
}

func recordStoreKey(recordID string, version string) *pb.PrimaryStoreKey {
	return &pb.PrimaryStoreKey{
		SpaceId:   "crypto",
		DatasetId: "kline",
		DataKind:  pb.DataKind_DATA_KIND_RECORD,
		Key:       recordID,
		Version:   factkey.NormalizeVersion(version),
	}
}

func withoutVersion(key *pb.PrimaryStoreKey) *pb.PrimaryStoreKey {
	return &pb.PrimaryStoreKey{SpaceId: key.GetSpaceId(), DatasetId: key.GetDatasetId(), DataKind: key.GetDataKind(), Key: key.GetKey()}
}

func normalizeTime(t *testing.T, value string) string {
	t.Helper()
	normalized, err := factkey.NormalizeTimeVersion(value)
	require.NoError(t, err)
	return normalized
}

func columnDoubles(row *pb.PrimaryStoreRow) map[string]float64 {
	out := make(map[string]float64)
	for _, column := range row.GetColumns() {
		out[column.GetColumnName()] = column.GetValue().GetDoubleValue()
	}
	return out
}
