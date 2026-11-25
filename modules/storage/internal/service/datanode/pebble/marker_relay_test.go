package pebble

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

func appendRelayTestMarker(t *testing.T, store *Store, cause string) string {
	t.Helper()
	raw, _, err := BuildProducerPeriodCompletedMessage("s", &pb.DatasetPeriodCompletedMarker{
		DatasetId: "d", Frequency: "1m", PeriodTime: 1786032000, Status: "complete",
		CauseEventId: cause, CompletedAt: time.Unix(1786032060, 0).UnixMilli(),
	})
	require.NoError(t, err)
	id, err := store.AppendDatasetMarker(context.Background(), raw)
	require.NoError(t, err)
	return id
}

func upsertRelayTestRow(t *testing.T, store *Store, minute int) {
	t.Helper()
	key := &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{
		SubjectId: "BTC-USDT", Freq: "1m", DataTime: fmt.Sprintf("2026-08-16T00:%02d:00Z", minute),
	}}}
	row := &pb.RowFieldUpsert{Key: key, Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1}}}}}
	require.NoError(t, store.UpsertFields(context.Background(), "s", "d", []*pb.RowFieldUpsert{row}))
}

// 扫描只返回完成标记条目，行条目只推进已扫描的位置；按条数分页时从上一页的位置接着读。
func TestReadMarkerChangesReturnsOnlyMarkersAndPages(t *testing.T) {
	ctx := context.Background()
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "db")})
	require.NoError(t, err)
	defer store.Close()

	upsertRelayTestRow(t, store, 0)            // 序号 1
	appendRelayTestMarker(t, store, "cause-1") // 序号 2
	upsertRelayTestRow(t, store, 1)            // 序号 3
	appendRelayTestMarker(t, store, "cause-2") // 序号 4
	upsertRelayTestRow(t, store, 2)            // 序号 5

	page, err := store.ReadMarkerChanges(ctx, 0, 3)
	require.NoError(t, err)
	require.Len(t, page.Markers, 1)
	require.Equal(t, uint64(2), page.Markers[0].Sequence)
	require.False(t, page.Markers[0].CreatedAt.IsZero())
	require.Equal(t, uint64(3), page.Scanned)
	require.True(t, page.HasMore)
	require.False(t, page.Skipped)

	page, err = store.ReadMarkerChanges(ctx, page.Scanned, 3)
	require.NoError(t, err)
	require.Len(t, page.Markers, 1)
	require.Equal(t, uint64(4), page.Markers[0].Sequence)
	require.Equal(t, uint64(5), page.Scanned)
	require.False(t, page.HasMore)

	page, err = store.ReadMarkerChanges(ctx, 5, 3)
	require.NoError(t, err)
	require.Empty(t, page.Markers)
	require.Equal(t, uint64(5), page.Scanned)
	require.False(t, page.HasMore)
}

// 游标之后的条目已被保留期清理删除：报告跳过，从最早可用的条目开始。
func TestReadMarkerChangesSkipsToEarliestAfterTrim(t *testing.T) {
	ctx := context.Background()
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "db")})
	require.NoError(t, err)
	defer store.Close()

	appendRelayTestMarker(t, store, "cause-1") // 序号 1
	appendRelayTestMarker(t, store, "cause-2") // 序号 2
	time.Sleep(20 * time.Millisecond)
	cutoff := time.Now().UTC()
	time.Sleep(20 * time.Millisecond)
	kept := appendRelayTestMarker(t, store, "cause-3") // 序号 3
	deleted, err := store.CleanupChangeLog(ctx, cutoff)
	require.NoError(t, err)
	require.Equal(t, 2, deleted)

	page, err := store.ReadMarkerChanges(ctx, 0, 10)
	require.NoError(t, err)
	require.True(t, page.Skipped)
	require.Len(t, page.Markers, 1)
	require.Equal(t, uint64(3), page.Markers[0].Sequence)
	publishable, err := store.MarkerPublishable(page.Markers[0].Data)
	require.NoError(t, err)
	require.True(t, publishable, "标记 %s 的本体还在", kept)

	// 游标正好在清理位置上不算跳过。
	page, err = store.ReadMarkerChanges(ctx, 2, 10)
	require.NoError(t, err)
	require.False(t, page.Skipped)
	require.Len(t, page.Markers, 1)
}

// 游标持久化在 Pebble 里，重新打开后还在。
func TestMarkerRelayCursorSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	store, err := Open(Options{Path: path})
	require.NoError(t, err)
	cursor, err := store.MarkerRelayCursor()
	require.NoError(t, err)
	require.Zero(t, cursor)
	require.NoError(t, store.SetMarkerRelayCursor(42))
	require.NoError(t, store.Close())

	store, err = Open(Options{Path: path})
	require.NoError(t, err)
	defer store.Close()
	cursor, err = store.MarkerRelayCursor()
	require.NoError(t, err)
	require.Equal(t, uint64(42), cursor)
}
