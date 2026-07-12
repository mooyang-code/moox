package pebble

import (
	"context"
	"path/filepath"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenRejectsEmptyPath(t *testing.T) {
	_, err := Open(Options{})
	require.Error(t, err)
}

func TestValidateKeyRequiresFields(t *testing.T) {
	assert.Error(t, validateKey(nil))
	assert.Error(t, validateKey(&pb.PrimaryStoreKey{}))
}

func TestVersionRangeContainsHonorsBounds(t *testing.T) {
	rng := &pb.VersionRange{StartVersion: "2026-07-09T08:00:00Z", EndVersion: "2026-07-09T09:00:00Z"}
	assert.True(t, versionRangeContains("2026-07-09T08:30:00Z", rng))
	assert.False(t, versionRangeContains("2026-07-09T07:00:00Z", rng))
}

func TestMergeRowPreservesExistingColumnsWhenPatchIsNull(t *testing.T) {
	base := &pb.PrimaryStoreRow{
		Key:     testPrimaryTimeSeriesKey("2026-07-09T08:10:00.000000000Z"),
		Columns: []*pb.ColumnValue{{ColumnName: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 2}}}},
	}
	patch := &pb.PrimaryStoreRow{
		Key:     testPrimaryTimeSeriesKey("2026-07-09T08:10:00.000000000Z"),
		Columns: []*pb.ColumnValue{{ColumnName: "close"}},
	}
	merged := mergeRow(base, patch)
	require.Len(t, merged.GetColumns(), 1)
	assert.Equal(t, float64(2), merged.GetColumns()[0].GetValue().GetDoubleValue())
}

func TestReadExactRowsReturnsStoredVersions(t *testing.T) {
	ctx := context.Background()
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "primary")})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	row := testPrimaryTimeSeriesRow("2026-07-09T08:10:00.000000000Z")
	require.NoError(t, store.WriteRows(ctx, []*pb.PrimaryStoreRow{row}))

	rows, page, err := store.ReadRows(ctx, []*pb.PrimaryStoreKey{row.GetKey()}, nil, pb.SortOrder_SORT_ORDER_ASC, nil, nil)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.NotNil(t, page)
}

func TestScanRowsRejectsMissingTargetFields(t *testing.T) {
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "primary")})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	_, _, err = store.ScanRows(context.Background(), nil, pb.DataKind_DATA_KIND_TIME_SERIES, nil, pb.SortOrder_SORT_ORDER_ASC, nil, nil)
	require.Error(t, err)
}

func TestPageRowsUsesCursorWindow(t *testing.T) {
	rows := []*pb.PrimaryStoreRow{
		testPrimaryTimeSeriesRow("2026-07-09T08:10:00.000000000Z"),
		testPrimaryTimeSeriesRow("2026-07-09T08:11:00.000000000Z"),
		testPrimaryTimeSeriesRow("2026-07-09T08:12:00.000000000Z"),
	}
	paged, result := pageRows(rows, &pb.Page{Size: 1, Cursor: encodeRowKey(rows[0])}, pb.SortOrder_SORT_ORDER_ASC)
	require.Len(t, paged, 1)
	assert.True(t, result.GetHasMore())
}

func TestEncodedRowVersionParsesVersionSuffix(t *testing.T) {
	key := encodePrimaryStoreKey(testPrimaryTimeSeriesKey("2026-07-09T08:10:00.000000000Z"))
	assert.Equal(t, "2026-07-09T08:10:00.000000000Z", encodedRowVersion(key))
}
