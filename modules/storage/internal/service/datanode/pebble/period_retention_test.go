package pebble

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/seriesset"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// 超过保留期后，账本与完成标记随行一起按同一截止时间清理；截止时间及之后的周期保留。
func TestCleanupExpiredBucketsRemovesLedgerAndMarkersBeforeCutoff(t *testing.T) {
	ctx := context.Background()
	oldPeriod := goldenPeriod
	keptPeriod := goldenPeriod.Add(24 * time.Hour)
	clock := newGoldenClock(oldPeriod.Add(61 * time.Second))
	store := newGoldenStore(t, clock)
	series := goldenSeries(t, []seriesset.Series{{SubjectID: "BTC-USDT"}})
	exp := goldenRegister(t, store, clock, goldenSpace, goldenDataset, goldenFreq, series, oldPeriod, goldenLateness)
	for _, period := range []time.Time{oldPeriod, keptPeriod} {
		clock.set(period.Add(61 * time.Second))
		commit := exp
		commit.PeriodTime = period.Unix()
		result, err := store.CommitTimeSeriesBatch(ctx, commit, []TimeSeriesBatchItem{{SeriesIndex: 0, Row: goldenRow(goldenSpace, goldenDataset, goldenFreq, series[0], period, 1)}})
		require.NoError(t, err)
		require.Equal(t, "complete", result.Status)
		raw, _, err := BuildProducerPeriodCompletedMessage(goldenSpace, &pb.DatasetPeriodCompletedMarker{
			DatasetId: goldenDataset, Frequency: goldenFreq, PeriodTime: period.Unix(), Status: "complete",
			UniverseSubjectIds: []string{"BTC-USDT"}, CauseEventId: "source-ready", CompletedAt: timestamppb.New(period.Add(time.Minute)),
		})
		require.NoError(t, err)
		_, err = store.AppendDatasetMarker(ctx, raw)
		require.NoError(t, err)
	}
	// 另一个数据集同一周期的账本不受影响。
	other := goldenRegister(t, store, clock, goldenSpace, "other", goldenFreq, series, keptPeriod.Add(time.Hour), goldenLateness)
	clock.set(keptPeriod.Add(time.Hour + 61*time.Second))
	_, err := store.CommitTimeSeriesBatch(ctx, other, []TimeSeriesBatchItem{{SeriesIndex: 0, Row: goldenRow(goldenSpace, "other", goldenFreq, series[0], keptPeriod.Add(time.Hour), 1)}})
	require.NoError(t, err)

	_, err = store.CleanupExpiredBuckets(ctx, goldenSpace, goldenDataset, keptPeriod)
	require.NoError(t, err)

	require.Equal(t, "", goldenPeriodStatus(t, store, goldenSpace, goldenDataset, goldenFreq, oldPeriod.Unix()))
	require.Equal(t, "complete", goldenPeriodStatus(t, store, goldenSpace, goldenDataset, goldenFreq, keptPeriod.Unix()))
	require.Equal(t, "complete", goldenPeriodStatus(t, store, goldenSpace, "other", goldenFreq, keptPeriod.Add(time.Hour).Unix()))
	for _, item := range []struct {
		period time.Time
		want   bool
	}{{oldPeriod, false}, {keptPeriod, true}} {
		_, found, err := store.GetDatasetPeriodCompletedMarker(ctx, goldenSpace, goldenDataset, "source-ready", item.period.Unix())
		require.NoError(t, err)
		require.Equal(t, item.want, found, "producer marker at %s", item.period)
		_, err = store.readPeriodValue(ledgerMarkerKey(goldenSpace, goldenDataset, goldenFreq, item.period.Unix()))
		require.Equal(t, item.want, err == nil, "ledger marker at %s", item.period)
	}
}

// commit 标记固定保留 7 天：7 天内同一 commit_id 重放识别为重复，超过 7 天当作新提交；清理只删 7 天之前的日桶。
func TestCommitMarkerKeepsSevenDays(t *testing.T) {
	ctx := context.Background()
	day := time.Date(2026, 11, 1, 23, 30, 0, 0, time.UTC)
	clock := newGoldenClock(day)
	store := newGoldenStore(t, clock)
	row := func(value float64) []*pb.RowFieldUpsert {
		return []*pb.RowFieldUpsert{{
			Key:    &pb.RowKey{SpaceId: goldenSpace, DatasetId: "factor_result", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r1", Version: "1"}}},
			Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: value}}}},
		}}
	}
	entries, err := store.UpsertFields(ctx, goldenSpace, "factor_result", "commit-1", row(1))
	require.NoError(t, err)
	require.Len(t, entries, 1)

	for _, offset := range []time.Duration{time.Hour, 3 * 24 * time.Hour, 7 * 24 * time.Hour} {
		clock.set(day.Add(offset))
		entries, err = store.UpsertFields(ctx, goldenSpace, "factor_result", "commit-1", row(1))
		require.NoError(t, err)
		require.Empty(t, entries, "%s 后的同内容重放是重复提交", offset)
		_, err = store.UpsertFields(ctx, goldenSpace, "factor_result", "commit-1", row(2))
		var conflict CommitConflictError
		require.ErrorAs(t, err, &conflict, "%s 后内容不同仍是冲突", offset)
	}

	// 7 天日桶之内的清理不删除这条标记。
	require.NoError(t, store.CleanupExpiredCommits(ctx, day.Add(7*24*time.Hour)))
	_, found, err := store.findCommitMarker(goldenSpace, "factor_result", "commit-1", day.Add(7*24*time.Hour))
	require.NoError(t, err)
	require.True(t, found)

	// 超过 7 天：不再识别，清理也删掉了旧日桶。
	later := day.Add(8 * 24 * time.Hour)
	require.NoError(t, store.CleanupExpiredCommits(ctx, later))
	_, err = store.readPeriodValue(commitMarkerKey(commitDay(day), goldenSpace, "factor_result", "commit-1"))
	require.Error(t, err, "8 天前的日桶已删除")
	clock.set(later)
	entries, err = store.UpsertFields(ctx, goldenSpace, "factor_result", "commit-1", row(2))
	require.NoError(t, err)
	require.Len(t, entries, 1, "超过 7 天的同一 commit_id 当作新提交")
	value, found := goldenStoredClose(t, store, row(0)[0].GetKey())
	require.True(t, found)
	require.Equal(t, 2.0, value)
}

// 每秒的窗口扫描只看各（数据集、频率）最近一段；启动后的全量扫描补上窗口之外的到期周期。
func TestPeriodFinalizerWindowSkipsOldWaitingPeriods(t *testing.T) {
	ctx := context.Background()
	oldPeriod := goldenPeriod
	recentPeriod := goldenPeriod.Add(3 * 24 * time.Hour)
	clock := newGoldenClock(oldPeriod.Add(61 * time.Second))
	store := newGoldenStore(t, clock)
	store.backgroundCancel() // 停掉后台判定，只看扫描本身
	series := goldenSeries(t, goldenDefaultSeries())
	exp := goldenRegister(t, store, clock, goldenSpace, goldenDataset, goldenFreq, series, oldPeriod, goldenLateness)
	for _, period := range []time.Time{oldPeriod, recentPeriod} {
		clock.set(period.Add(61 * time.Second))
		commit := exp
		commit.PeriodTime = period.Unix()
		_, err := store.CommitTimeSeriesBatch(ctx, commit, []TimeSeriesBatchItem{{SeriesIndex: 0, Row: goldenRow(goldenSpace, goldenDataset, goldenFreq, series[0], period, 1)}})
		require.NoError(t, err)
	}
	now := recentPeriod.Add(time.Hour)
	keys, err := store.duePeriodKeys(ctx, now, true)
	require.NoError(t, err)
	require.Equal(t, [][]byte{periodKey(goldenSpace, goldenDataset, goldenFreq, recentPeriod.Unix())}, keys, "窗口扫描跳过三天前的周期")
	keys, err = store.duePeriodKeys(ctx, now, false)
	require.NoError(t, err)
	require.Len(t, keys, 2, "全量扫描找到全部到期周期")
}

// 行与位图已提交、判定之前进程退出：记录位图已满但仍在等待。重开后截止扫描（窗口与全量都一样）找到它并判定为完成，只发一个标记。
func TestPeriodFinalizerRecoversFullBitmapLeftWaiting(t *testing.T) {
	ctx := context.Background()
	clock := newGoldenClock(goldenPeriod.Add(61 * time.Second))
	store := newGoldenStore(t, clock)
	store.backgroundCancel() // 停掉后台判定，只看扫描本身
	series := goldenSeries(t, goldenDefaultSeries())
	exp := goldenRegister(t, store, clock, goldenSpace, goldenDataset, goldenFreq, series, goldenPeriod, goldenLateness)
	raw, err := json.Marshal(periodRecord{Status: periodWaiting, SeriesHash: exp.SeriesHash, ExpectedCount: 2, DeadlineAt: goldenPeriod.Add(goldenDeadline).Unix(), Success: []byte{0b11}})
	require.NoError(t, err)
	key := periodKey(goldenSpace, goldenDataset, goldenFreq, goldenPeriod.Unix())
	require.NoError(t, store.db.Set(key, raw, store.writeOptions))
	// 一条损坏的记录不挡住其他周期。
	require.NoError(t, store.db.Set(periodKey(goldenSpace, "broken", goldenFreq, goldenPeriod.Unix()), []byte("{"), store.writeOptions))

	now := clock.now()
	for _, windowed := range []bool{true, false} {
		keys, err := store.duePeriodKeys(ctx, now, windowed)
		require.Error(t, err)
		require.Equal(t, [][]byte{key}, keys, "windowed=%t", windowed)
	}
	finalized, err := store.FinalizeWaitingDatasetPeriods(ctx, now)
	require.ErrorContains(t, err, "已损坏")
	require.Equal(t, 1, finalized)
	require.Equal(t, "complete", goldenPeriodStatus(t, store, goldenSpace, goldenDataset, goldenFreq, goldenPeriod.Unix()))
	markers := goldenMarkers(t, store, goldenSpace, goldenDataset)
	require.Len(t, markers, 1)
	require.Equal(t, "complete", markers[0].payload.GetStatus())
	_, err = store.FinalizeWaitingDatasetPeriods(ctx, now.Add(time.Minute))
	require.ErrorContains(t, err, "已损坏")
	require.Len(t, goldenMarkers(t, store, goldenSpace, goldenDataset), 1)
}

// 删除数据集也删掉序列快照与生效表：同名重建后可以从更早的生效点重新登记，新快照照常提交。
func TestDeleteDatasetRowsClearsSeriesSnapshots(t *testing.T) {
	ctx := context.Background()
	clock := newGoldenClock(goldenPeriod.Add(61 * time.Second))
	store := newGoldenStore(t, clock)
	later := goldenPeriod.Add(time.Hour)
	old := goldenSeries(t, goldenDefaultSeries())
	oldExp := goldenRegister(t, store, clock, goldenSpace, goldenDataset, goldenFreq, old, later, goldenLateness)
	_, err := store.DeleteDatasetRows(ctx, goldenSpace, goldenDataset)
	require.NoError(t, err)
	_, err = store.loadSeriesSnapshot(goldenSpace, goldenDataset, goldenFreq, oldExp.SeriesHash)
	var missing SeriesSnapshotMissingError
	require.ErrorAs(t, err, &missing, "旧快照已删除")

	// 生效表已清空：比旧生效点更早的 goldenPeriod 可以重新登记。
	fresh := goldenSeries(t, []seriesset.Series{{SubjectID: "SOL-USDT"}})
	exp := goldenRegister(t, store, clock, goldenSpace, goldenDataset, goldenFreq, fresh, goldenPeriod, goldenLateness)
	result, err := store.CommitTimeSeriesBatch(ctx, exp, []TimeSeriesBatchItem{{SeriesIndex: 0, Row: goldenRow(goldenSpace, goldenDataset, goldenFreq, fresh[0], goldenPeriod, 1)}})
	require.NoError(t, err)
	require.Equal(t, "complete", result.Status)
	markers := goldenMarkers(t, store, goldenSpace, goldenDataset)
	require.Len(t, markers, 1)
	require.Equal(t, []string{"SOL-USDT"}, markers[0].payload.GetUniverseSubjectIds())
}
