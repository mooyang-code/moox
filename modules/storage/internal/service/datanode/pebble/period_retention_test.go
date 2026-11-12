package pebble

import (
	"context"
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
