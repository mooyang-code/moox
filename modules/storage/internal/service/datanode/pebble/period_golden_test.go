package pebble

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/seriesset"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// 账本与完成标记的判定金样：完成、降级、迟到提交、重复提交、截止判定，以及写入方上报的标记与 commit_id 查重。
// 金样只经由改键前后都保留的入口（登记快照、周期提交、截止判定、写入方上报、按触发事件查询、带 commit_id 的写入、
// 删除数据集）读写，账本怎么存不影响它。

const (
	goldenSpace    = "crypto"
	goldenDataset  = "bars"
	goldenFreq     = "1m"
	goldenLateness = 15
	goldenSpotTag  = "venue:binance|market:spot|source:spot_http"
	goldenSwapTag  = "venue:binance|market:swap|source:swap_http"
)

// goldenPeriod 是金样的目标周期；截止时间 = 收盘（+1m）+ 允许迟到 15s = 周期起点 + 75s。
var goldenPeriod = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

const goldenDeadline = 75 * time.Second

func goldenDefaultSeries() []seriesset.Series {
	return []seriesset.Series{{SubjectID: "BTC-USDT"}, {SubjectID: "ETH-USDT"}}
}

// goldenUnifiedSeries 是统一数据集的四条序列，规范顺序为 BTC 现货、BTC 永续、ETH 现货、ETH 永续。
func goldenUnifiedSeries() []seriesset.Series {
	return []seriesset.Series{
		{SubjectID: "BTC-USDT", SeriesTag: goldenSpotTag}, {SubjectID: "BTC-USDT", SeriesTag: goldenSwapTag},
		{SubjectID: "ETH-USDT", SeriesTag: goldenSpotTag}, {SubjectID: "ETH-USDT", SeriesTag: goldenSwapTag},
	}
}

// goldenErrorIs 按错误类别判断：invalid、conflict（周期身份冲突）、hash_mismatch、snapshot_missing、deadline_passed。
func goldenErrorIs(t *testing.T, err error, kind string) {
	t.Helper()
	require.Error(t, err)
	var (
		validation ValidationError
		conflict   PeriodConflictError
		mismatch   PeriodHashMismatchError
		missing    SeriesSnapshotMissingError
		late       PeriodDeadlinePassedError
	)
	switch kind {
	case "invalid":
		require.ErrorAs(t, err, &validation)
	case "conflict":
		require.ErrorAs(t, err, &conflict)
	case "hash_mismatch":
		require.ErrorAs(t, err, &mismatch)
	case "snapshot_missing":
		require.ErrorAs(t, err, &missing)
	case "deadline_passed":
		require.ErrorAs(t, err, &late)
	default:
		t.Fatalf("unknown error kind %q", kind)
	}
}

type ledgerStep struct {
	at           time.Duration // 时钟，相对周期起点
	commit       []uint32      // 本步提交的序列序号
	historic     bool          // 行时间取上一周期（历史行）
	value        float64       // 行的 close 值，0 时取 1
	finalize     bool          // 本步跑一次截止判定
	wantStatus   string        // 提交返回的周期状态
	wantAccepted []uint32      // 提交返回的、目标周期已成功的序号
	wantErr      string        // 提交的错误类别，见 goldenErrorIs
}

type ledgerCase struct {
	name               string
	unified            bool // 用统一数据集的四条序列
	steps              []ledgerStep
	wantPeriod         string // 账本里的最终状态，空串表示周期不存在
	wantMarkers        int
	wantMarkerStatus   string
	wantUniverse       []string
	wantFailed         []string
	wantUniverseSeries int
	wantFailedSeries   []string // "标的|series_tag"
	wantStored         map[uint32]float64
}

func TestGoldenLedgerJudgement(t *testing.T) {
	commit := func(at time.Duration, indexes []uint32, status string, accepted []uint32) ledgerStep {
		return ledgerStep{at: at, commit: indexes, wantStatus: status, wantAccepted: accepted}
	}
	finalize := func(at time.Duration) ledgerStep { return ledgerStep{at: at, finalize: true} }
	both := []string{"BTC-USDT", "ETH-USDT"}
	cases := []ledgerCase{
		{
			name:       "逐个提交全部成功后完成",
			steps:      []ledgerStep{commit(61*time.Second, []uint32{0}, "waiting", []uint32{0}), commit(62*time.Second, []uint32{1}, "complete", []uint32{1})},
			wantPeriod: "complete", wantMarkers: 1, wantMarkerStatus: "complete", wantUniverse: both, wantFailed: nil,
			wantStored: map[uint32]float64{0: 1, 1: 1},
		},
		{
			name:       "一次提交全部序列",
			steps:      []ledgerStep{commit(61*time.Second, []uint32{1, 0}, "complete", []uint32{0, 1})},
			wantPeriod: "complete", wantMarkers: 1, wantMarkerStatus: "complete", wantUniverse: both,
			wantStored: map[uint32]float64{0: 1, 1: 1},
		},
		{
			name: "等待中重复提交同一序列照常写入且只计一次",
			steps: []ledgerStep{
				commit(61*time.Second, []uint32{0}, "waiting", []uint32{0}),
				{at: 62 * time.Second, commit: []uint32{0}, value: 2, wantStatus: "waiting", wantAccepted: []uint32{0}},
				commit(63*time.Second, []uint32{1}, "complete", []uint32{1}),
			},
			wantPeriod: "complete", wantMarkers: 1, wantMarkerStatus: "complete", wantUniverse: both,
			wantStored: map[uint32]float64{0: 2, 1: 1},
		},
		{
			name: "完成后重复提交不再写入也不再发标记",
			steps: []ledgerStep{
				commit(61*time.Second, []uint32{0, 1}, "complete", []uint32{0, 1}),
				{at: 62 * time.Second, commit: []uint32{1}, value: 2, wantStatus: "complete", wantAccepted: []uint32{1}},
			},
			wantPeriod: "complete", wantMarkers: 1, wantMarkerStatus: "complete", wantUniverse: both,
			wantStored: map[uint32]float64{0: 1, 1: 1},
		},
		{
			name:       "截止判定把缺序列的周期降级且只发一次",
			steps:      []ledgerStep{commit(61*time.Second, []uint32{0}, "waiting", []uint32{0}), finalize(goldenDeadline), finalize(90 * time.Second)},
			wantPeriod: "degraded", wantMarkers: 1, wantMarkerStatus: "degraded", wantUniverse: both, wantFailed: []string{"ETH-USDT"},
			wantStored: map[uint32]float64{0: 1},
		},
		{
			name:       "截止前一纳秒仍在等待",
			steps:      []ledgerStep{commit(61*time.Second, []uint32{0}, "waiting", []uint32{0}), finalize(goldenDeadline - time.Nanosecond)},
			wantPeriod: "waiting", wantMarkers: 0,
			wantStored: map[uint32]float64{0: 1},
		},
		{
			name:       "截止后一纳秒降级",
			steps:      []ledgerStep{commit(61*time.Second, []uint32{0}, "waiting", []uint32{0}), finalize(goldenDeadline + time.Nanosecond)},
			wantPeriod: "degraded", wantMarkers: 1, wantMarkerStatus: "degraded", wantUniverse: both, wantFailed: []string{"ETH-USDT"},
			wantStored: map[uint32]float64{0: 1},
		},
		{
			name:       "截止后迟到的提交不写入并降级",
			steps:      []ledgerStep{commit(61*time.Second, []uint32{0}, "waiting", []uint32{0}), commit(80*time.Second, []uint32{1}, "degraded", nil)},
			wantPeriod: "degraded", wantMarkers: 1, wantMarkerStatus: "degraded", wantUniverse: both, wantFailed: []string{"ETH-USDT"},
			wantStored: map[uint32]float64{0: 1},
		},
		{
			name:       "截止时刻的提交算迟到",
			steps:      []ledgerStep{commit(61*time.Second, []uint32{0}, "waiting", []uint32{0}), commit(goldenDeadline, []uint32{1}, "degraded", nil)},
			wantPeriod: "degraded", wantMarkers: 1, wantMarkerStatus: "degraded", wantUniverse: both, wantFailed: []string{"ETH-USDT"},
			wantStored: map[uint32]float64{0: 1},
		},
		{
			name:       "截止前一纳秒的提交照常完成",
			steps:      []ledgerStep{commit(61*time.Second, []uint32{0}, "waiting", []uint32{0}), commit(goldenDeadline-time.Nanosecond, []uint32{1}, "complete", []uint32{1})},
			wantPeriod: "complete", wantMarkers: 1, wantMarkerStatus: "complete", wantUniverse: both,
			wantStored: map[uint32]float64{0: 1, 1: 1},
		},
		{
			name:       "完成后迟到的提交仍返回完成",
			steps:      []ledgerStep{commit(61*time.Second, []uint32{0, 1}, "complete", []uint32{0, 1}), {at: 80 * time.Second, commit: []uint32{0}, value: 3, wantStatus: "complete", wantAccepted: []uint32{0}}},
			wantPeriod: "complete", wantMarkers: 1, wantMarkerStatus: "complete", wantUniverse: both,
			wantStored: map[uint32]float64{0: 1, 1: 1},
		},
		{
			name:       "周期未建立且已过截止时拒绝且不建立周期",
			steps:      []ledgerStep{{at: goldenDeadline, commit: []uint32{0}, wantErr: "deadline_passed"}},
			wantPeriod: "", wantMarkers: 0,
		},
		{
			name:       "从未提交的周期不产生标记",
			steps:      []ledgerStep{finalize(90 * time.Second)},
			wantPeriod: "", wantMarkers: 0,
		},
		{
			name:       "历史行照常写入但不计入目标周期",
			steps:      []ledgerStep{{at: 61 * time.Second, commit: []uint32{0}, historic: true, wantStatus: "waiting"}, finalize(80 * time.Second)},
			wantPeriod: "degraded", wantMarkers: 1, wantMarkerStatus: "degraded", wantUniverse: both, wantFailed: both,
		},
		{
			name:       "统一数据集只把缺失的序列列为失败",
			unified:    true,
			steps:      []ledgerStep{commit(61*time.Second, []uint32{0, 2}, "waiting", []uint32{0, 2}), finalize(goldenDeadline)},
			wantPeriod: "degraded", wantMarkers: 1, wantMarkerStatus: "degraded", wantUniverse: both, wantFailed: both,
			wantUniverseSeries: 4, wantFailedSeries: []string{"BTC-USDT|" + goldenSwapTag, "ETH-USDT|" + goldenSwapTag},
			wantStored: map[uint32]float64{0: 1, 2: 1},
		},
		{
			name:       "统一数据集全部成功时没有失败序列",
			unified:    true,
			steps:      []ledgerStep{commit(61*time.Second, []uint32{3, 2, 1, 0}, "complete", []uint32{0, 1, 2, 3})},
			wantPeriod: "complete", wantMarkers: 1, wantMarkerStatus: "complete", wantUniverse: both, wantUniverseSeries: 4,
			wantStored: map[uint32]float64{0: 1, 1: 1, 2: 1, 3: 1},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			clock := newGoldenClock(goldenPeriod)
			store := newGoldenStore(t, clock)
			input := goldenDefaultSeries()
			if tc.unified {
				input = goldenUnifiedSeries()
			}
			series := goldenSeries(t, input)
			exp := goldenRegister(t, store, clock, goldenSpace, goldenDataset, goldenFreq, series, goldenPeriod, goldenLateness)
			for i, step := range tc.steps {
				clock.set(goldenPeriod.Add(step.at))
				if step.finalize {
					require.NoError(t, goldenFinalize(t, store, clock.now()), "step %d", i)
				}
				if len(step.commit) == 0 {
					continue
				}
				at := goldenPeriod
				if step.historic {
					at = goldenPeriod.Add(-time.Minute)
				}
				value := step.value
				if value == 0 {
					value = 1
				}
				items := make([]TimeSeriesBatchItem, 0, len(step.commit))
				for _, index := range step.commit {
					items = append(items, TimeSeriesBatchItem{SeriesIndex: index, Row: goldenRow(goldenSpace, goldenDataset, goldenFreq, series[index], at, value)})
				}
				result, err := store.CommitTimeSeriesBatch(ctx, exp, items)
				if step.wantErr != "" {
					goldenErrorIs(t, err, step.wantErr)
					continue
				}
				require.NoError(t, err, "step %d", i)
				require.Equal(t, step.wantStatus, result.Status, "step %d", i)
				require.Equal(t, step.wantAccepted, result.AcceptedSeriesIndexes, "step %d", i)
				require.Equal(t, goldenPeriod.Add(goldenDeadline).Unix(), result.DeadlineAt, "step %d", i)
			}

			require.Equal(t, tc.wantPeriod, goldenPeriodStatus(t, store, goldenSpace, goldenDataset, goldenFreq, goldenPeriod.Unix()))
			markers := goldenMarkers(t, store, goldenSpace, goldenDataset)
			require.Len(t, markers, tc.wantMarkers)
			if tc.wantMarkers > 0 {
				marker := markers[0].payload
				require.Equal(t, tc.wantMarkerStatus, marker.GetStatus())
				require.Equal(t, "ledger", marker.GetSource())
				require.Equal(t, goldenDataset, marker.GetDatasetId())
				require.Equal(t, goldenFreq, marker.GetFrequency())
				require.Equal(t, goldenPeriod.Unix(), marker.GetPeriodTime())
				require.Equal(t, exp.SeriesHash, marker.GetSnapshotRef())
				require.Empty(t, marker.GetCauseEventId())
				require.Equal(t, tc.wantUniverse, marker.GetUniverseSubjectIds())
				require.Equal(t, tc.wantFailed, nilIfEmpty(marker.GetFailedSubjects()))
				require.Len(t, marker.GetUniverseSeries(), tc.wantUniverseSeries)
				var failedSeries []string
				for _, ref := range marker.GetFailedSeries() {
					failedSeries = append(failedSeries, ref.GetSubjectId()+"|"+ref.GetSeriesTag())
				}
				require.Equal(t, tc.wantFailedSeries, failedSeries)
				require.NotNil(t, marker.GetCompletedAt())
			}
			for index := range series {
				want, listed := tc.wantStored[uint32(index)]
				got, found := goldenStoredClose(t, store, goldenRow(goldenSpace, goldenDataset, goldenFreq, series[index], goldenPeriod, 0).GetKey())
				require.Equal(t, listed, found, "series %d stored", index)
				if listed {
					require.Equal(t, want, got, "series %d close", index)
				}
			}
		})
	}
}

func nilIfEmpty(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	return values
}

// 非法提交整体拒绝：同一请求里合法的行也不写入，账本状态与标记都不变。
func TestGoldenLedgerRejections(t *testing.T) {
	type rejectCase struct {
		name      string
		noSnap    bool   // 不登记快照
		frequency string // 周期频率，空串取 1m
		prepare   string // 先把周期推到的状态：""（不建立）、waiting、complete
		mutate    func(exp *DatasetPeriodExpectation, items []TimeSeriesBatchItem) []TimeSeriesBatchItem
		wantErr   string
	}
	tagged := func(tag string) func(*DatasetPeriodExpectation, []TimeSeriesBatchItem) []TimeSeriesBatchItem {
		return func(_ *DatasetPeriodExpectation, items []TimeSeriesBatchItem) []TimeSeriesBatchItem {
			items[1].Row.Key.GetTimeSeries().SeriesTag = tag
			return items
		}
	}
	cases := []rejectCase{
		{name: "标签与快照不符", prepare: "waiting", mutate: tagged("binance"), wantErr: "invalid"},
		{name: "已完成的周期仍校验标签", prepare: "complete", mutate: tagged("binance"), wantErr: "invalid"},
		{name: "序号越界", mutate: func(_ *DatasetPeriodExpectation, items []TimeSeriesBatchItem) []TimeSeriesBatchItem {
			items[1].SeriesIndex = 2
			return items
		}, wantErr: "invalid"},
		{name: "序号与标的不符", mutate: func(_ *DatasetPeriodExpectation, items []TimeSeriesBatchItem) []TimeSeriesBatchItem {
			items[1].SeriesIndex = 0
			return items
		}, wantErr: "invalid"},
		{name: "标的带空白", prepare: "waiting", mutate: func(_ *DatasetPeriodExpectation, items []TimeSeriesBatchItem) []TimeSeriesBatchItem {
			items[1].Row.Key.GetTimeSeries().SubjectId = " ETH-USDT "
			return items
		}, wantErr: "invalid"},
		{name: "月频不是分钟频", prepare: "waiting", mutate: func(_ *DatasetPeriodExpectation, items []TimeSeriesBatchItem) []TimeSeriesBatchItem {
			items[1].Row.Key.GetTimeSeries().Freq = "1mo"
			return items
		}, wantErr: "invalid"},
		{name: "目标时间带小数秒", prepare: "waiting", mutate: func(_ *DatasetPeriodExpectation, items []TimeSeriesBatchItem) []TimeSeriesBatchItem {
			items[1].Row.Key.GetTimeSeries().DataTime = goldenPeriod.Add(500 * time.Millisecond).Format(time.RFC3339Nano)
			return items
		}, wantErr: "invalid"},
		{name: "快照未登记", noSnap: true, wantErr: "snapshot_missing"},
		{name: "周期未建立时 hash 与生效快照不符", mutate: func(exp *DatasetPeriodExpectation, items []TimeSeriesBatchItem) []TimeSeriesBatchItem {
			exp.SeriesHash = "not-the-effective-hash"
			return items
		}, wantErr: "hash_mismatch"},
		{name: "周期已建立时 hash 不符", prepare: "waiting", mutate: func(exp *DatasetPeriodExpectation, items []TimeSeriesBatchItem) []TimeSeriesBatchItem {
			exp.SeriesHash = "not-the-effective-hash"
			return items
		}, wantErr: "conflict"},
		{name: "周期已建立时序列数不符", prepare: "waiting", mutate: func(exp *DatasetPeriodExpectation, items []TimeSeriesBatchItem) []TimeSeriesBatchItem {
			exp.ExpectedCount = 3
			return items
		}, wantErr: "conflict"},
		{name: "已完成的周期 hash 不符", prepare: "complete", mutate: func(exp *DatasetPeriodExpectation, items []TimeSeriesBatchItem) []TimeSeriesBatchItem {
			exp.SeriesHash = "not-the-effective-hash"
			return items
		}, wantErr: "conflict"},
		{name: "空间带空白", prepare: "waiting", mutate: func(_ *DatasetPeriodExpectation, items []TimeSeriesBatchItem) []TimeSeriesBatchItem {
			items[1].Row.Key.SpaceId = " " + goldenSpace + " "
			return items
		}, wantErr: "invalid"},
		{name: "数据集带空白", prepare: "waiting", mutate: func(_ *DatasetPeriodExpectation, items []TimeSeriesBatchItem) []TimeSeriesBatchItem {
			items[1].Row.Key.DatasetId = " " + goldenDataset + " "
			return items
		}, wantErr: "invalid"},
		{name: "标签带空白", prepare: "waiting", mutate: tagged(" binance "), wantErr: "invalid"},
		{name: "频率带空白", prepare: "waiting", mutate: func(_ *DatasetPeriodExpectation, items []TimeSeriesBatchItem) []TimeSeriesBatchItem {
			items[1].Row.Key.GetTimeSeries().Freq = " 1m "
			return items
		}, wantErr: "invalid"},
		{name: "小时频率的大小写别名", frequency: "1h", prepare: "waiting", mutate: func(_ *DatasetPeriodExpectation, items []TimeSeriesBatchItem) []TimeSeriesBatchItem {
			items[1].Row.Key.GetTimeSeries().Freq = "1H"
			return items
		}, wantErr: "invalid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			clock := newGoldenClock(goldenPeriod.Add(61 * time.Second))
			store := newGoldenStore(t, clock)
			series := goldenSeries(t, goldenDefaultSeries())
			freq := goldenFreq
			if tc.frequency != "" {
				freq = tc.frequency
			}
			exp := DatasetPeriodExpectation{SpaceID: goldenSpace, DatasetID: goldenDataset, Frequency: freq, PeriodTime: goldenPeriod.Unix(), SeriesHash: "unregistered", ExpectedCount: 2, AllowedLatenessSeconds: goldenLateness}
			if !tc.noSnap {
				exp = goldenRegister(t, store, clock, goldenSpace, goldenDataset, freq, series, goldenPeriod, goldenLateness)
			}
			switch tc.prepare {
			case "waiting":
				_, err := store.CommitTimeSeriesBatch(ctx, exp, []TimeSeriesBatchItem{{SeriesIndex: 0, Row: goldenRow(goldenSpace, goldenDataset, freq, series[0], goldenPeriod, 1)}})
				require.NoError(t, err)
			case "complete":
				_, err := store.CommitTimeSeriesBatch(ctx, exp, []TimeSeriesBatchItem{
					{SeriesIndex: 0, Row: goldenRow(goldenSpace, goldenDataset, freq, series[0], goldenPeriod, 1)},
					{SeriesIndex: 1, Row: goldenRow(goldenSpace, goldenDataset, freq, series[1], goldenPeriod, 1)},
				})
				require.NoError(t, err)
			}
			markersBefore := len(goldenMarkers(t, store, goldenSpace, goldenDataset))
			outboxBefore := goldenOutboxCount(t, store)
			clock.set(goldenPeriod.Add(62 * time.Second))
			bad := exp
			items := []TimeSeriesBatchItem{
				{SeriesIndex: 0, Row: goldenRow(goldenSpace, goldenDataset, freq, series[0], goldenPeriod, 9)},
				{SeriesIndex: 1, Row: goldenRow(goldenSpace, goldenDataset, freq, series[1], goldenPeriod, 9)},
			}
			if tc.mutate != nil {
				items = tc.mutate(&bad, items)
			}
			result, err := store.CommitTimeSeriesBatch(ctx, bad, items)
			goldenErrorIs(t, err, tc.wantErr)
			require.Empty(t, result.AcceptedSeriesIndexes)
			require.Equal(t, tc.prepare, goldenPeriodStatus(t, store, goldenSpace, goldenDataset, freq, goldenPeriod.Unix()))
			require.Len(t, goldenMarkers(t, store, goldenSpace, goldenDataset), markersBefore)
			require.Equal(t, outboxBefore, goldenOutboxCount(t, store))
			for index := range series {
				if got, found := goldenStoredClose(t, store, goldenRow(goldenSpace, goldenDataset, freq, series[index], goldenPeriod, 0).GetKey()); found {
					require.Equal(t, 1.0, got, "series %d must keep the prepared value", index)
				}
			}
		})
	}
}

// 并发提交不丢位：每个序列各提交一次，最后完成且只发一个标记。
func TestGoldenLedgerConcurrentCommitsCompleteOnce(t *testing.T) {
	ctx := context.Background()
	clock := newGoldenClock(goldenPeriod.Add(61 * time.Second))
	store := newGoldenStore(t, clock)
	input := make([]seriesset.Series, 32)
	for i := range input {
		input[i] = seriesset.Series{SubjectID: fmt.Sprintf("S%02d-USDT", i)}
	}
	series := goldenSeries(t, input)
	exp := goldenRegister(t, store, clock, goldenSpace, goldenDataset, goldenFreq, series, goldenPeriod, goldenLateness)
	var wg sync.WaitGroup
	errs := make([]error, len(series))
	for i := range series {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			_, errs[index] = store.CommitTimeSeriesBatch(ctx, exp, []TimeSeriesBatchItem{{SeriesIndex: uint32(index), Row: goldenRow(goldenSpace, goldenDataset, goldenFreq, series[index], goldenPeriod, 1)}})
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, "complete", goldenPeriodStatus(t, store, goldenSpace, goldenDataset, goldenFreq, goldenPeriod.Unix()))
	markers := goldenMarkers(t, store, goldenSpace, goldenDataset)
	require.Len(t, markers, 1)
	require.Equal(t, "complete", markers[0].payload.GetStatus())
	require.Empty(t, markers[0].payload.GetFailedSubjects())
}

// 账本跨重启保留：重开后截止判定照常把未齐的周期降级。
func TestGoldenLedgerSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "pebble")
	clock := newGoldenClock(goldenPeriod.Add(61 * time.Second))
	store := openGoldenStore(t, path, clock, 0)
	series := goldenSeries(t, goldenDefaultSeries())
	exp := goldenRegister(t, store, clock, goldenSpace, goldenDataset, goldenFreq, series, goldenPeriod, goldenLateness)
	_, err := store.CommitTimeSeriesBatch(ctx, exp, []TimeSeriesBatchItem{{SeriesIndex: 0, Row: goldenRow(goldenSpace, goldenDataset, goldenFreq, series[0], goldenPeriod, 1)}})
	require.NoError(t, err)
	require.NoError(t, store.Close())

	store = openGoldenStore(t, path, clock, 0)
	defer store.Close()
	require.Equal(t, "waiting", goldenPeriodStatus(t, store, goldenSpace, goldenDataset, goldenFreq, goldenPeriod.Unix()))
	clock.set(goldenPeriod.Add(goldenDeadline))
	require.NoError(t, goldenFinalize(t, store, clock.now()))
	require.Equal(t, "degraded", goldenPeriodStatus(t, store, goldenSpace, goldenDataset, goldenFreq, goldenPeriod.Unix()))
	markers := goldenMarkers(t, store, goldenSpace, goldenDataset)
	require.Len(t, markers, 1)
	require.Equal(t, []string{"ETH-USDT"}, markers[0].payload.GetFailedSubjects())
}

// 判定事件超过事件上限：建立周期时就拒绝；建立后才调低上限的周期停在等待、报错，不挡住其他周期。
func TestGoldenLedgerMarkerSizeLimit(t *testing.T) {
	ctx := context.Background()
	t.Run("建立周期时预检", func(t *testing.T) {
		clock := newGoldenClock(goldenPeriod.Add(61 * time.Second))
		store := openGoldenStore(t, filepath.Join(t.TempDir(), "pebble"), clock, 1)
		defer store.Close()
		series := goldenSeries(t, goldenDefaultSeries())
		restore := clock.now()
		clock.set(goldenPeriod.Add(-time.Minute))
		_, err := store.RegisterSeriesSnapshot(ctx, goldenSpace, goldenDataset, goldenFreq, series, goldenPeriod.Unix())
		clock.set(restore)
		require.ErrorContains(t, err, "exceeds limit", "登记快照时按最坏情况预检判定事件大小")
	})
	t.Run("调低上限后的周期不挡住其他周期", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "pebble")
		clock := newGoldenClock(goldenPeriod.Add(61 * time.Second))
		store := openGoldenStore(t, path, clock, 0)
		long := make([]seriesset.Series, 30)
		for i := range long {
			long[i] = seriesset.Series{SubjectID: fmt.Sprintf("LONG-ASSET-SYMBOL-%02d-ABCDEFGHIJKLMNOPQRSTUVWXYZ", i)}
		}
		largeSeries := goldenSeries(t, long)
		smallSeries := goldenSeries(t, []seriesset.Series{{SubjectID: "BTC-USDT"}})
		large := goldenRegister(t, store, clock, goldenSpace, "large", goldenFreq, largeSeries, goldenPeriod, goldenLateness)
		small := goldenRegister(t, store, clock, goldenSpace, "small", goldenFreq, smallSeries, goldenPeriod, goldenLateness)
		_, err := store.CommitTimeSeriesBatch(ctx, large, []TimeSeriesBatchItem{{SeriesIndex: 0, Row: goldenRow(goldenSpace, "large", goldenFreq, largeSeries[0], goldenPeriod, 1)}})
		require.NoError(t, err)
		_, err = store.CommitTimeSeriesBatch(ctx, small, []TimeSeriesBatchItem{{SeriesIndex: 0, Row: goldenRow(goldenSpace, "small", goldenFreq, smallSeries[0], goldenPeriod.Add(-time.Minute), 1)}})
		require.NoError(t, err)
		require.NoError(t, store.Close())

		now := goldenPeriod.Add(goldenDeadline)
		smallRaw, _, err := BuildLedgerPeriodCompletedMessage(goldenSpace, &LedgerPeriodCompleted{
			DatasetID: "small", Frequency: goldenFreq, PeriodTime: goldenPeriod.Unix(), Status: "degraded", SnapshotRef: small.SeriesHash,
			UniverseSubjectIDs: []string{"BTC-USDT"}, FailedSubjects: []string{"BTC-USDT"}, CompletedAt: timestamppb.New(now),
		})
		require.NoError(t, err)
		clock.set(now)
		store = openGoldenStore(t, path, clock, len(smallRaw))
		defer store.Close()
		require.ErrorContains(t, goldenFinalize(t, store, now), "exceeds limit")
		require.Equal(t, "waiting", goldenPeriodStatus(t, store, goldenSpace, "large", goldenFreq, goldenPeriod.Unix()))
		require.Equal(t, "degraded", goldenPeriodStatus(t, store, goldenSpace, "small", goldenFreq, goldenPeriod.Unix()))
		require.Empty(t, goldenMarkers(t, store, goldenSpace, "large"))
		require.Len(t, goldenMarkers(t, store, goldenSpace, "small"), 1)
	})
}

// 同一时刻的分钟周期与月周期是两期，各自判定。
func TestGoldenLedgerFrequencyIsPartOfPeriodIdentity(t *testing.T) {
	ctx := context.Background()
	period := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	clock := newGoldenClock(period.Add(61 * time.Second))
	store := newGoldenStore(t, clock)
	series := goldenSeries(t, []seriesset.Series{{SubjectID: "BTC-USDT"}})
	for _, frequency := range []string{"1m", "1mo"} {
		exp := goldenRegister(t, store, clock, goldenSpace, goldenDataset, frequency, series, period, goldenLateness)
		result, err := store.CommitTimeSeriesBatch(ctx, exp, []TimeSeriesBatchItem{{SeriesIndex: 0, Row: goldenRow(goldenSpace, goldenDataset, frequency, series[0], period, 1)}})
		require.NoError(t, err)
		require.Equal(t, "complete", result.Status)
		require.Equal(t, []uint32{0}, result.AcceptedSeriesIndexes)
	}
	markers := goldenMarkers(t, store, goldenSpace, goldenDataset)
	require.Len(t, markers, 2)
	require.NotEqual(t, markers[0].message.GetEventId(), markers[1].message.GetEventId())
}

// 目标时间的等价写法（带时区）先规范化，再与周期起点精确比较。
func TestGoldenLedgerEquivalentTimestampNotation(t *testing.T) {
	ctx := context.Background()
	clock := newGoldenClock(goldenPeriod.Add(61 * time.Second))
	store := newGoldenStore(t, clock)
	series := goldenSeries(t, []seriesset.Series{{SubjectID: "BTC-USDT"}})
	exp := goldenRegister(t, store, clock, goldenSpace, goldenDataset, goldenFreq, series, goldenPeriod, goldenLateness)
	row := goldenRow(goldenSpace, goldenDataset, goldenFreq, series[0], goldenPeriod, 1)
	row.Key.GetTimeSeries().DataTime = goldenPeriod.In(time.FixedZone("UTC+8", 8*60*60)).Format(time.RFC3339Nano)
	result, err := store.CommitTimeSeriesBatch(ctx, exp, []TimeSeriesBatchItem{{SeriesIndex: 0, Row: row}})
	require.NoError(t, err)
	require.Equal(t, "complete", result.Status)
	require.Equal(t, []uint32{0}, result.AcceptedSeriesIndexes)
}

func goldenProducerMarker(cause string, periodTime int64, status string) *pb.DatasetPeriodCompletedMarker {
	return &pb.DatasetPeriodCompletedMarker{
		DatasetId: "factor_result", Frequency: goldenFreq, PeriodTime: periodTime, Status: status,
		UniverseSubjectIds: []string{"BTC-USDT", "ETH-USDT"}, CauseEventId: cause,
		CompletedAt: timestamppb.New(time.Unix(periodTime+30, 0)), DetailType: "factor.detail", Detail: []byte("detail-" + cause),
	}
}

// 写入方上报的完成标记：按“触发事件 + 周期”幂等，内容变了是冲突；查询只按触发事件与周期点查。
func TestGoldenProducerMarkers(t *testing.T) {
	period := goldenPeriod.Unix()
	type report struct {
		marker   *pb.DatasetPeriodCompletedMarker
		wantErr  bool // 期望 ConflictError
		wantSame int  // 与第几次上报（从 0 起）同一个事件 ID；-1 表示新事件
	}
	type lookup struct {
		cause      string
		periodTime int64
		wantReport int // 期望查到第几次上报的内容；-1 表示查不到
	}
	monthly := goldenProducerMarker("source-ready", period, "complete")
	monthly.Frequency = "1mo"
	cases := []struct {
		name       string
		reports    []report
		lookups    []lookup
		wantOutbox int
	}{
		{
			name:       "首次上报可按触发事件与周期查到",
			reports:    []report{{marker: goldenProducerMarker("source-ready", period, "complete"), wantSame: -1}},
			lookups:    []lookup{{"source-ready", period, 0}, {"other-cause", period, -1}, {"source-ready", period + 60, -1}},
			wantOutbox: 1,
		},
		{
			name:       "相同内容重报幂等",
			reports:    []report{{marker: goldenProducerMarker("source-ready", period, "complete"), wantSame: -1}, {marker: goldenProducerMarker("source-ready", period, "complete"), wantSame: 0}},
			lookups:    []lookup{{"source-ready", period, 0}},
			wantOutbox: 1,
		},
		{
			name:       "同一触发事件与周期内容变了是冲突",
			reports:    []report{{marker: goldenProducerMarker("source-ready", period, "complete"), wantSame: -1}, {marker: goldenProducerMarker("source-ready", period, "degraded"), wantErr: true}},
			lookups:    []lookup{{"source-ready", period, 0}},
			wantOutbox: 1,
		},
		{
			name:       "同一周期不同触发事件各存一条",
			reports:    []report{{marker: goldenProducerMarker("source-ready", period, "complete"), wantSame: -1}, {marker: goldenProducerMarker("recalc-1", period, "degraded"), wantSame: -1}},
			lookups:    []lookup{{"source-ready", period, 0}, {"recalc-1", period, 1}},
			wantOutbox: 2,
		},
		{
			name:       "同一触发事件不同周期各存一条",
			reports:    []report{{marker: goldenProducerMarker("source-ready", period, "complete"), wantSame: -1}, {marker: goldenProducerMarker("source-ready", period+60, "complete"), wantSame: -1}},
			lookups:    []lookup{{"source-ready", period, 0}, {"source-ready", period + 60, 1}},
			wantOutbox: 2,
		},
		{
			name:       "同一触发事件与周期换了频率仍是同一个事件",
			reports:    []report{{marker: goldenProducerMarker("source-ready", period, "complete"), wantSame: -1}, {marker: monthly, wantErr: true}},
			lookups:    []lookup{{"source-ready", period, 0}},
			wantOutbox: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store := newGoldenStore(t, newGoldenClock(goldenPeriod))
			eventIDs := make([]string, len(tc.reports))
			for i, item := range tc.reports {
				raw, wantID, err := BuildProducerPeriodCompletedMessage(goldenSpace, item.marker)
				require.NoError(t, err)
				eventID, err := store.AppendDatasetMarker(ctx, raw)
				if item.wantErr {
					var conflict ConflictError
					require.ErrorAs(t, err, &conflict, "report %d", i)
					continue
				}
				require.NoError(t, err, "report %d", i)
				require.Equal(t, wantID, eventID)
				eventIDs[i] = eventID
				if item.wantSame >= 0 {
					require.Equal(t, eventIDs[item.wantSame], eventID)
				}
			}
			for _, item := range tc.lookups {
				message, found, err := store.GetDatasetPeriodCompletedMarker(ctx, goldenSpace, "factor_result", item.cause, item.periodTime)
				require.NoError(t, err)
				if item.wantReport < 0 {
					require.False(t, found, "lookup %s/%d", item.cause, item.periodTime)
					continue
				}
				require.True(t, found, "lookup %s/%d", item.cause, item.periodTime)
				want, _, err := BuildProducerPeriodCompletedMessage(goldenSpace, tc.reports[item.wantReport].marker)
				require.NoError(t, err)
				wantMessage := &eventpb.EventMessage{}
				require.NoError(t, proto.Unmarshal(want, wantMessage))
				require.True(t, proto.Equal(wantMessage, message), "lookup %s/%d", item.cause, item.periodTime)
			}
			require.Len(t, goldenMarkers(t, store, goldenSpace, "factor_result"), tc.wantOutbox)
		})
	}
}

// commit_id 查重：同内容重放不写入，内容不同是冲突，与行的顺序无关，跨重启有效，不同数据集互不影响。
func TestGoldenCommitIDReplay(t *testing.T) {
	key := func(dataset, record string) *pb.RowKey {
		return &pb.RowKey{SpaceId: goldenSpace, DatasetId: dataset, Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: record, Version: "1"}}}
	}
	rows := func(dataset string, value float64, records ...string) []*pb.RowFieldUpsert {
		out := make([]*pb.RowFieldUpsert, 0, len(records))
		for _, record := range records {
			out = append(out, &pb.RowFieldUpsert{Key: key(dataset, record), Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: value}}}}})
		}
		return out
	}
	type write struct {
		dataset   string
		commitID  string
		rows      []*pb.RowFieldUpsert
		reopen    bool // 写之前重开库
		wantWrite bool // 期望真正写入（返回一条 outbox 条目）
		wantErr   bool // 期望 CommitConflictError
	}
	cases := []struct {
		name   string
		writes []write
		want   map[string]float64 // 数据集/记录 → 最终 close
	}{
		{
			name:   "同内容重放不再写入",
			writes: []write{{dataset: "a", commitID: "c1", rows: rows("a", 1, "r1"), wantWrite: true}, {dataset: "a", commitID: "c1", rows: rows("a", 1, "r1")}},
			want:   map[string]float64{"a/r1": 1},
		},
		{
			name:   "行顺序不同仍是同一次提交",
			writes: []write{{dataset: "a", commitID: "c1", rows: rows("a", 1, "r1", "r2"), wantWrite: true}, {dataset: "a", commitID: "c1", rows: rows("a", 1, "r2", "r1")}},
			want:   map[string]float64{"a/r1": 1, "a/r2": 1},
		},
		{
			name:   "同一 commit_id 内容不同是冲突",
			writes: []write{{dataset: "a", commitID: "c1", rows: rows("a", 1, "r1"), wantWrite: true}, {dataset: "a", commitID: "c1", rows: rows("a", 2, "r1"), wantErr: true}},
			want:   map[string]float64{"a/r1": 1},
		},
		{
			name:   "重开库后仍识别重放",
			writes: []write{{dataset: "a", commitID: "c1", rows: rows("a", 1, "r1"), wantWrite: true}, {dataset: "a", commitID: "c1", rows: rows("a", 1, "r1"), reopen: true}, {dataset: "a", commitID: "c1", rows: rows("a", 2, "r1"), wantErr: true}},
			want:   map[string]float64{"a/r1": 1},
		},
		{
			name:   "不同数据集的同名 commit_id 互不影响",
			writes: []write{{dataset: "a", commitID: "c1", rows: rows("a", 1, "r1"), wantWrite: true}, {dataset: "b", commitID: "c1", rows: rows("b", 2, "r1"), wantWrite: true}},
			want:   map[string]float64{"a/r1": 1, "b/r1": 2},
		},
		{
			name:   "不带 commit_id 的写入后写覆盖先写",
			writes: []write{{dataset: "a", rows: rows("a", 1, "r1"), wantWrite: true}, {dataset: "a", rows: rows("a", 2, "r1"), wantWrite: true}},
			want:   map[string]float64{"a/r1": 2},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "pebble")
			clock := newGoldenClock(goldenPeriod)
			store := openGoldenStore(t, path, clock, 0)
			defer func() { _ = store.Close() }()
			for i, item := range tc.writes {
				if item.reopen {
					require.NoError(t, store.Close())
					store = openGoldenStore(t, path, clock, 0)
				}
				entries, err := store.UpsertFields(ctx, goldenSpace, item.dataset, item.commitID, item.rows)
				if item.wantErr {
					var conflict CommitConflictError
					require.ErrorAs(t, err, &conflict, "write %d", i)
					continue
				}
				require.NoError(t, err, "write %d", i)
				if item.wantWrite {
					require.Len(t, entries, 1, "write %d", i)
				} else {
					require.Empty(t, entries, "write %d", i)
				}
			}
			for name, want := range tc.want {
				var dataset, record string
				_, err := fmt.Sscanf(name, "%1s/%s", &dataset, &record)
				require.NoError(t, err)
				got, found := goldenStoredClose(t, store, key(dataset, record))
				require.True(t, found, name)
				require.Equal(t, want, got, name)
			}
		})
	}
}

// 删除数据集清掉它的账本、完成标记与 commit 标记，其他数据集不受影响；重建后从头开始。
func TestGoldenDeleteDatasetClearsLedgerMarkersAndCommits(t *testing.T) {
	ctx := context.Background()
	clock := newGoldenClock(goldenPeriod.Add(61 * time.Second))
	store := newGoldenStore(t, clock)
	series := goldenSeries(t, goldenDefaultSeries())
	exps := map[string]DatasetPeriodExpectation{}
	for _, dataset := range []string{"delete-me", "keep-me"} {
		exps[dataset] = goldenRegister(t, store, clock, goldenSpace, dataset, goldenFreq, series, goldenPeriod, goldenLateness)
		_, err := store.CommitTimeSeriesBatch(ctx, exps[dataset], []TimeSeriesBatchItem{{SeriesIndex: 0, Row: goldenRow(goldenSpace, dataset, goldenFreq, series[0], goldenPeriod, 1)}})
		require.NoError(t, err)
		marker := goldenProducerMarker("source-ready", goldenPeriod.Unix(), "complete")
		marker.DatasetId = dataset
		raw, _, err := BuildProducerPeriodCompletedMessage(goldenSpace, marker)
		require.NoError(t, err)
		_, err = store.AppendDatasetMarker(ctx, raw)
		require.NoError(t, err)
		record := &pb.RowFieldUpsert{Key: &pb.RowKey{SpaceId: goldenSpace, DatasetId: dataset, Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Freq: goldenFreq, DataTime: goldenPeriod.Add(-time.Hour).Format(time.RFC3339Nano)}}}, Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1}}}}}
		entries, err := store.UpsertFields(ctx, goldenSpace, dataset, "commit-1", []*pb.RowFieldUpsert{record})
		require.NoError(t, err)
		require.Len(t, entries, 1)
	}

	_, err := store.DeleteDatasetRows(ctx, goldenSpace, "delete-me")
	require.NoError(t, err)
	require.Equal(t, "", goldenPeriodStatus(t, store, goldenSpace, "delete-me", goldenFreq, goldenPeriod.Unix()))
	require.Equal(t, "waiting", goldenPeriodStatus(t, store, goldenSpace, "keep-me", goldenFreq, goldenPeriod.Unix()))
	_, found, err := store.GetDatasetPeriodCompletedMarker(ctx, goldenSpace, "delete-me", "source-ready", goldenPeriod.Unix())
	require.NoError(t, err)
	require.False(t, found)
	_, found, err = store.GetDatasetPeriodCompletedMarker(ctx, goldenSpace, "keep-me", "source-ready", goldenPeriod.Unix())
	require.NoError(t, err)
	require.True(t, found)
	require.Empty(t, goldenMarkers(t, store, goldenSpace, "delete-me"))

	// 截止后只有保留的数据集被判定。
	clock.set(goldenPeriod.Add(goldenDeadline))
	require.NoError(t, goldenFinalize(t, store, clock.now()))
	require.Equal(t, "", goldenPeriodStatus(t, store, goldenSpace, "delete-me", goldenFreq, goldenPeriod.Unix()))
	require.Equal(t, "degraded", goldenPeriodStatus(t, store, goldenSpace, "keep-me", goldenFreq, goldenPeriod.Unix()))
	require.Empty(t, goldenMarkers(t, store, goldenSpace, "delete-me"))

	// 重建后同一 commit_id 作为新提交写入，同一触发事件可以重新上报。
	record := &pb.RowFieldUpsert{Key: &pb.RowKey{SpaceId: goldenSpace, DatasetId: "delete-me", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Freq: goldenFreq, DataTime: goldenPeriod.Add(-time.Hour).Format(time.RFC3339Nano)}}}, Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 2}}}}}
	entries, err := store.UpsertFields(ctx, goldenSpace, "delete-me", "commit-1", []*pb.RowFieldUpsert{record})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	marker := goldenProducerMarker("source-ready", goldenPeriod.Unix(), "degraded")
	marker.DatasetId = "delete-me"
	raw, _, err := BuildProducerPeriodCompletedMessage(goldenSpace, marker)
	require.NoError(t, err)
	_, err = store.AppendDatasetMarker(ctx, raw)
	require.NoError(t, err)
}

// 截止判定与迟到提交并发：两边都看到降级，只发一个标记，迟到的行不写入。
func TestGoldenLedgerDeadlineRacesLateCommit(t *testing.T) {
	for round := 0; round < 20; round++ {
		ctx := context.Background()
		clock := newGoldenClock(goldenPeriod.Add(61 * time.Second))
		store := newGoldenStore(t, clock)
		series := goldenSeries(t, goldenDefaultSeries())
		exp := goldenRegister(t, store, clock, goldenSpace, goldenDataset, goldenFreq, series, goldenPeriod, goldenLateness)
		_, err := store.CommitTimeSeriesBatch(ctx, exp, []TimeSeriesBatchItem{{SeriesIndex: 0, Row: goldenRow(goldenSpace, goldenDataset, goldenFreq, series[0], goldenPeriod, 1)}})
		require.NoError(t, err)
		clock.set(goldenPeriod.Add(goldenDeadline + time.Second))

		var wg sync.WaitGroup
		var commitResult DatasetPeriodResult
		var commitErr, finalizeErr error
		start := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			commitResult, commitErr = store.CommitTimeSeriesBatch(ctx, exp, []TimeSeriesBatchItem{{SeriesIndex: 1, Row: goldenRow(goldenSpace, goldenDataset, goldenFreq, series[1], goldenPeriod, 1)}})
		}()
		go func() {
			defer wg.Done()
			<-start
			finalizeErr = goldenFinalize(t, store, clock.now())
		}()
		close(start)
		wg.Wait()
		require.NoError(t, commitErr)
		require.NoError(t, finalizeErr)
		require.Equal(t, "degraded", commitResult.Status)
		require.Empty(t, commitResult.AcceptedSeriesIndexes)
		require.Equal(t, "degraded", goldenPeriodStatus(t, store, goldenSpace, goldenDataset, goldenFreq, goldenPeriod.Unix()))
		markers := goldenMarkers(t, store, goldenSpace, goldenDataset)
		require.Len(t, markers, 1)
		require.Equal(t, []string{"ETH-USDT"}, markers[0].payload.GetFailedSubjects())
		_, found := goldenStoredClose(t, store, goldenRow(goldenSpace, goldenDataset, goldenFreq, series[1], goldenPeriod, 0).GetKey())
		require.False(t, found, "迟到的行不写入")
	}
}
