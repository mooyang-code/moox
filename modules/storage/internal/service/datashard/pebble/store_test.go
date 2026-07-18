package pebble

import (
	"context"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"path/filepath"
	"testing"
	"time"
)

func TestReadRowsDescPageStopsAtRequestedWindow(t *testing.T) {
	ctx := context.Background()
	store, err := Open(Options{ShardID: "test-shard", Path: filepath.Join(t.TempDir(), "primary")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	for _, version := range []string{
		"2026-07-09T08:10:00.000000000Z",
		"2026-07-09T08:11:00.000000000Z",
		"2026-07-09T08:12:00.000000000Z",
		"2026-07-09T08:13:00.000000000Z",
		"2026-07-09T08:14:00.000000000Z",
	} {
		if err := store.WriteRows(ctx, []*pb.ShardRow{testPrimaryTimeSeriesRow(version)}); err != nil {
			t.Fatalf("WriteRows %s: %v", version, err)
		}
	}

	rows, page, err := store.ReadRows(ctx, []*pb.ShardKey{testPrimaryTimeSeriesKey("")}, nil, pb.SortOrder_SORT_ORDER_DESC, nil, &pb.Page{Page: 2, Size: 2})
	if err != nil {
		t.Fatalf("ReadRows: %v", err)
	}
	if len(rows) != 2 ||
		rows[0].GetKey().GetVersion() != "2026-07-09T08:12:00.000000000Z" ||
		rows[1].GetKey().GetVersion() != "2026-07-09T08:11:00.000000000Z" {
		t.Fatalf("versions = %v, want second descending page", primaryVersions(rows))
	}
	if page == nil || !page.GetHasMore() || page.GetTotal() != 0 || page.GetTotalState() != pb.TotalState_SKIPPED {
		t.Fatalf("page = %+v, want skipped total with has_more", page)
	}
}

func TestScanRowsFirstPageUsesBoundedCursor(t *testing.T) {
	ctx := context.Background()
	store, err := Open(Options{ShardID: "test-shard", Path: filepath.Join(t.TempDir(), "primary")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	for i := 0; i < 100; i++ {
		version := time.Date(2026, 7, 9, 8, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Minute).Format(time.RFC3339Nano)
		if err := store.WriteRows(ctx, []*pb.ShardRow{testPrimaryTimeSeriesRow(version)}); err != nil {
			t.Fatalf("WriteRows %s: %v", version, err)
		}
	}

	rows, page, err := store.ScanRows(ctx, &pb.ShardTarget{
		SpaceId: "crypto", DatasetId: "binance_spot_kline",
	}, pb.DataKind_DATA_KIND_TIME_SERIES, nil, pb.SortOrder_SORT_ORDER_ASC, nil, &pb.Page{Size: 2})
	if err != nil {
		t.Fatalf("ScanRows: %v", err)
	}
	if len(rows) != 2 || page == nil || !page.GetHasMore() || page.GetNextCursor() == "" {
		t.Fatalf("rows=%d page=%+v, want bounded first page with cursor", len(rows), page)
	}
	rows, page, err = store.ScanRows(ctx, &pb.ShardTarget{
		SpaceId: "crypto", DatasetId: "binance_spot_kline",
	}, pb.DataKind_DATA_KIND_TIME_SERIES, nil, pb.SortOrder_SORT_ORDER_ASC, nil, &pb.Page{Size: 2, Cursor: page.GetNextCursor()})
	if err != nil {
		t.Fatalf("ScanRows next page: %v", err)
	}
	if len(rows) != 2 || page == nil || page.GetNextCursor() == "" {
		t.Fatalf("next rows=%d page=%+v, want another bounded page", len(rows), page)
	}
}

func TestDeleteRowsRemovesExactPrimaryKeys(t *testing.T) {
	ctx := context.Background()
	store, err := Open(Options{ShardID: "test-shard", Path: filepath.Join(t.TempDir(), "primary")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	old := testPrimaryTimeSeriesRow("2026-07-09T08:10:00.000000000Z")
	newer := testPrimaryTimeSeriesRow("2026-07-09T08:11:00.000000000Z")
	if err := store.WriteRows(ctx, []*pb.ShardRow{old, newer}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteRows(ctx, []*pb.ShardKey{old.GetKey()}); err != nil {
		t.Fatal(err)
	}
	rows, _, err := store.ReadRows(ctx, []*pb.ShardKey{testPrimaryTimeSeriesKey("")}, nil, pb.SortOrder_SORT_ORDER_ASC, nil, &pb.Page{Size: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].GetKey().GetVersion() != newer.GetKey().GetVersion() {
		t.Fatalf("rows after delete=%v", primaryVersions(rows))
	}
}

func TestScanRowsWithPrefixNarrowsSubjectAndFrequency(t *testing.T) {
	ctx := context.Background()
	store, err := Open(Options{ShardID: "test-shard", Path: filepath.Join(t.TempDir(), "primary")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rows := []*pb.ShardRow{
		{Key: &pb.ShardKey{SpaceId: "crypto", DatasetId: "kline", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Key: "agent-1|1m|_", Version: "2026-07-11T00:00:00.000000000Z"}},
		{Key: &pb.ShardKey{SpaceId: "crypto", DatasetId: "kline", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Key: "agent-2|1m|_", Version: "2026-07-11T00:00:00.000000000Z"}},
	}
	if err := store.WriteRows(ctx, rows); err != nil {
		t.Fatal(err)
	}
	prefix := escape("agent-1") + "%7C" + escape("1m") + "%7C"
	got, _, err := store.ScanRowsWithPrefix(ctx, &pb.ShardTarget{SpaceId: "crypto", DatasetId: "kline"}, pb.DataKind_DATA_KIND_TIME_SERIES, nil, pb.SortOrder_SORT_ORDER_ASC, nil, &pb.Page{Size: 10}, prefix)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].GetKey().GetKey() != "agent-1|1m|_" {
		t.Fatalf("rows=%v, want only agent-1", got)
	}
}

func testPrimaryTimeSeriesRow(version string) *pb.ShardRow {
	return &pb.ShardRow{Key: testPrimaryTimeSeriesKey(version)}
}

func testPrimaryTimeSeriesKey(version string) *pb.ShardKey {
	return &pb.ShardKey{
		SpaceId:   "crypto",
		DatasetId: "binance_spot_kline",
		DataKind:  pb.DataKind_DATA_KIND_TIME_SERIES,
		Key:       "BTC-USDT|1m|_",
		Version:   version,
	}
}

func primaryVersions(rows []*pb.ShardRow) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.GetKey().GetVersion())
	}
	return out
}

func TestOpenRejectsEmptyPath(t *testing.T) {
	_, err := Open(Options{})
	require.Error(t, err)
}

func TestValidateKeyRequiresFields(t *testing.T) {
	assert.Error(t, validateKey(nil))
	assert.Error(t, validateKey(&pb.ShardKey{}))
}

func TestVersionRangeContainsHonorsBounds(t *testing.T) {
	rng := &pb.VersionRange{StartVersion: "2026-07-09T08:00:00Z", EndVersion: "2026-07-09T09:00:00Z"}
	assert.True(t, versionRangeContains("2026-07-09T08:30:00Z", rng))
	assert.False(t, versionRangeContains("2026-07-09T07:00:00Z", rng))
}

func TestMergeRowRemovesExistingColumnWhenPatchIsNull(t *testing.T) {
	base := &pb.ShardRow{
		Key:     testPrimaryTimeSeriesKey("2026-07-09T08:10:00.000000000Z"),
		Columns: []*pb.ColumnValue{{ColumnName: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 2}}}},
	}
	patch := &pb.ShardRow{
		Key: testPrimaryTimeSeriesKey("2026-07-09T08:10:00.000000000Z"),
		Columns: []*pb.ColumnValue{{ColumnName: "close", Value: &pb.TypedValue{
			Value: &pb.TypedValue_NullValue{NullValue: pb.NullValue_NULL_VALUE},
		}}},
	}
	merged := mergeRow(base, patch)
	require.Len(t, merged.GetColumns(), 1)
	_, ok := merged.GetColumns()[0].GetValue().GetValue().(*pb.TypedValue_NullValue)
	assert.True(t, ok)
}

func TestMergeRowRemovesRequestedAttribute(t *testing.T) {
	base := &pb.ShardRow{
		Key:        testPrimaryTimeSeriesKey("2026-07-09T08:10:00.000000000Z"),
		Attributes: map[string]string{"source": "feed", "keep": "yes"},
	}
	patch := &pb.ShardRow{
		Key:                testPrimaryTimeSeriesKey("2026-07-09T08:10:00.000000000Z"),
		AttributesToDelete: []string{"source"},
	}
	merged := mergeRow(base, patch)
	assert.NotContains(t, merged.GetAttributes(), "source")
	assert.Equal(t, "yes", merged.GetAttributes()["keep"])
}

func TestReadExactRowsReturnsStoredVersions(t *testing.T) {
	ctx := context.Background()
	store, err := Open(Options{ShardID: "test-shard", Path: filepath.Join(t.TempDir(), "primary")})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	row := testPrimaryTimeSeriesRow("2026-07-09T08:10:00.000000000Z")
	require.NoError(t, store.WriteRows(ctx, []*pb.ShardRow{row}))

	rows, page, err := store.ReadRows(ctx, []*pb.ShardKey{row.GetKey()}, nil, pb.SortOrder_SORT_ORDER_ASC, nil, nil)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.NotNil(t, page)
}

func TestScanRowsRejectsMissingTargetFields(t *testing.T) {
	store, err := Open(Options{ShardID: "test-shard", Path: filepath.Join(t.TempDir(), "primary")})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	_, _, err = store.ScanRows(context.Background(), nil, pb.DataKind_DATA_KIND_TIME_SERIES, nil, pb.SortOrder_SORT_ORDER_ASC, nil, nil)
	require.Error(t, err)
}

func TestPageRowsUsesCursorWindow(t *testing.T) {
	rows := []*pb.ShardRow{
		testPrimaryTimeSeriesRow("2026-07-09T08:10:00.000000000Z"),
		testPrimaryTimeSeriesRow("2026-07-09T08:11:00.000000000Z"),
		testPrimaryTimeSeriesRow("2026-07-09T08:12:00.000000000Z"),
	}
	paged, result := pageRows(rows, &pb.Page{Size: 1, Cursor: encodeRowKey(rows[0])}, pb.SortOrder_SORT_ORDER_ASC)
	require.Len(t, paged, 1)
	assert.True(t, result.GetHasMore())
}

func TestEncodedRowVersionParsesVersionSuffix(t *testing.T) {
	key := encodeShardKey(testPrimaryTimeSeriesKey("2026-07-09T08:10:00.000000000Z"))
	assert.Equal(t, "2026-07-09T08:10:00.000000000Z", encodedRowVersion(key))
}

func TestWriteRowsMergesExistingRowColumns(t *testing.T) {
	ctx := context.Background()
	store, err := Open(Options{ShardID: "test-shard", Path: filepath.Join(t.TempDir(), "primary")})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	base := testPrimaryTimeSeriesRow("2026-07-09T08:10:00.000000000Z")
	base.Columns = []*pb.ColumnValue{
		{ColumnName: "open", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1}}},
		{ColumnName: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 2}}},
	}
	require.NoError(t, store.WriteRows(ctx, []*pb.ShardRow{base}))

	patch := testPrimaryTimeSeriesRow("2026-07-09T08:10:00.000000000Z")
	patch.Columns = []*pb.ColumnValue{
		{ColumnName: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 3}}},
		{ColumnName: "volume", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 100}}},
	}
	require.NoError(t, store.WriteRows(ctx, []*pb.ShardRow{patch}))

	rows, _, err := store.ReadRows(ctx, []*pb.ShardKey{testPrimaryTimeSeriesKey("2026-07-09T08:10:00.000000000Z")}, nil, pb.SortOrder_SORT_ORDER_ASC, nil, nil)
	require.NoError(t, err)
	require.Len(t, rows, 1)

	columns := map[string]float64{}
	for _, col := range rows[0].GetColumns() {
		if num := col.GetValue().GetDoubleValue(); num != 0 {
			columns[col.GetColumnName()] = num
		}
	}
	require.Equal(t, float64(1), columns["open"])
	require.Equal(t, float64(3), columns["close"])
	require.Equal(t, float64(100), columns["volume"])
}
