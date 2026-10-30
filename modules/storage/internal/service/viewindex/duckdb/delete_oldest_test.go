//go:build cgo

package duckdb

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

// deleteOldestTestIndex 建一个三个序列的 1m 索引：A 有 15 根、B 有 12 根、C 有 6 根，时间都从 base 起逐分钟递增。
func deleteOldestTestIndex(t *testing.T) (*IndexManager, time.Time) {
	t.Helper()
	ctx := context.Background()
	m, err := OpenIndexManager(IndexManagerOptions{Root: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.Close() })
	require.NoError(t, m.Prepare(ctx, "idx", viewindex.ViewIndexSchema{
		SpaceID: "s", ViewID: "v", PrimaryDatasetID: "prices", ViewVersion: 1, Engine: "duckdb", SchemaHash: "hash",
		Columns: []*pb.ViewColumn{{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}},
	}))
	base := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	var writes []viewindex.RowWrite
	for subject, bars := range map[string]int{"A": 15, "B": 12, "C": 6} {
		for i := 0; i < bars; i++ {
			writes = append(writes, viewindex.RowWrite{
				Key: viewindex.RowKey{Key: &pb.RowKey{SpaceId: "s", DatasetId: "prices", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{
					SubjectId: subject, Freq: "1m", DataTime: base.Add(time.Duration(i) * time.Minute).Format(time.RFC3339), SeriesTag: "venue:x",
				}}}},
				Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: float64(i)}}}},
			})
		}
	}
	require.NoError(t, m.Write(ctx, "idx", viewindex.ViewIndexWriteBatch{RowWrites: writes, ViewRevision: 1, ViewSchemaHash: "hash", WriteMode: viewindex.LiveWrite}))
	return m, base
}

// seriesState 返回某个序列计数表里的根数、实际行数与最早一根的时间。
func seriesState(t *testing.T, m *IndexManager, subject string) (counted, actual int, oldest time.Time) {
	t.Helper()
	db, _, _, _, err := m.getIndex(context.Background(), "idx")
	require.NoError(t, err)
	require.NoError(t, db.QueryRow(`SELECT row_count FROM view_series_counts WHERE subject_id = ?`, subject).Scan(&counted))
	require.NoError(t, db.QueryRow(`SELECT count(*), min(data_time) FROM view_rows WHERE subject_id = ?`, subject).Scan(&actual, &oldest))
	return counted, actual, oldest
}

func TestDeleteOldestRowsKeepsNewestBarsPerSeries(t *testing.T) {
	ctx := context.Background()
	m, base := deleteOldestTestIndex(t)

	cuts, err := m.PlanOldestRows(ctx, "idx", 10, 64, 100)
	require.NoError(t, err)
	require.Len(t, cuts, 2, "只有超出 10 根的 A、B 需要删除")
	require.Equal(t, "A", cuts[0].SubjectID, "超出最多的序列排在前面")
	require.Equal(t, uint64(5), cuts[0].Rows)
	require.Equal(t, base.Add(5*time.Minute), cuts[0].Before.UTC(), "A 的分界是第 6 旧的那根")
	require.Equal(t, uint64(2), cuts[1].Rows)

	deleted, err := m.DeleteOldestRows(ctx, "idx", cuts)
	require.NoError(t, err)
	require.Equal(t, uint64(7), deleted)
	for subject, wantOldest := range map[string]time.Time{"A": base.Add(5 * time.Minute), "B": base.Add(2 * time.Minute)} {
		counted, actual, oldest := seriesState(t, m, subject)
		require.Equal(t, 10, counted, "%s 的计数应减到保留根数", subject)
		require.Equal(t, 10, actual, "%s 只剩最新的 10 根", subject)
		require.Equal(t, wantOldest, oldest.UTC(), "%s 删掉的必须是最旧的数据", subject)
	}
	counted, actual, _ := seriesState(t, m, "C")
	require.Equal(t, 6, counted)
	require.Equal(t, 6, actual, "没超出保留根数的序列不动")

	from, err := m.RefreshCoverageFrom(ctx, "idx")
	require.NoError(t, err)
	require.Equal(t, canonicalCoverageTime(base), from, "C 仍从 base 开始，覆盖起点不变")
	stats, err := m.StatMetadata(ctx, "idx")
	require.NoError(t, err)
	require.Equal(t, from, stats.IndexedFrom)

	again, err := m.PlanOldestRows(ctx, "idx", 10, 64, 100)
	require.NoError(t, err)
	require.Empty(t, again, "删到保留根数后不再有要删的")
}

func TestPlanOldestRowsHonoursRowBudgetAndSeriesLimit(t *testing.T) {
	ctx := context.Background()
	m, base := deleteOldestTestIndex(t)

	cuts, err := m.PlanOldestRows(ctx, "idx", 10, 64, 3)
	require.NoError(t, err)
	require.Len(t, cuts, 1, "额度 3 行只够 A 的一部分")
	require.Equal(t, uint64(3), cuts[0].Rows)
	require.Equal(t, base.Add(3*time.Minute), cuts[0].Before.UTC())
	deleted, err := m.DeleteOldestRows(ctx, "idx", cuts)
	require.NoError(t, err)
	require.Equal(t, uint64(3), deleted)
	counted, actual, oldest := seriesState(t, m, "A")
	require.Equal(t, 12, counted)
	require.Equal(t, 12, actual)
	require.Equal(t, base.Add(3*time.Minute), oldest.UTC())

	cuts, err = m.PlanOldestRows(ctx, "idx", 10, 1, 100)
	require.NoError(t, err)
	require.Len(t, cuts, 1, "每批最多 1 个序列")
}

func TestRefreshCoverageFromMovesForwardAfterDeletingOldest(t *testing.T) {
	ctx := context.Background()
	m, base := deleteOldestTestIndex(t)
	db, _, _, _, err := m.getIndex(ctx, "idx")
	require.NoError(t, err)
	// 去掉 C，让最早的数据只在 A、B 上，删除后覆盖起点应前移。
	_, err = db.Exec(`DELETE FROM view_rows WHERE subject_id = 'C'`)
	require.NoError(t, err)
	_, err = db.Exec(`DELETE FROM view_series_counts WHERE subject_id = 'C'`)
	require.NoError(t, err)

	cuts, err := m.PlanOldestRows(ctx, "idx", 10, 64, 100)
	require.NoError(t, err)
	_, err = m.DeleteOldestRows(ctx, "idx", cuts)
	require.NoError(t, err)
	from, err := m.RefreshCoverageFrom(ctx, "idx")
	require.NoError(t, err)
	require.Equal(t, canonicalCoverageTime(base.Add(2*time.Minute)), from, "B 剩下的最早一根是第 3 分钟")
	stats, err := m.StatMetadata(ctx, "idx")
	require.NoError(t, err)
	require.Equal(t, from, stats.IndexedFrom)
}
